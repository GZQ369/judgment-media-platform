package analytics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
)

type sequenceGateway struct {
	publications []platforminsights.Publication
	index        int
}

func (g *sequenceGateway) Get(context.Context, string) (platforminsights.Publication, error) {
	return g.publications[g.index], nil
}

func (g *sequenceGateway) Reconcile(context.Context, string) (platforminsights.Publication, error) {
	publication := g.publications[g.index]
	if g.index < len(g.publications)-1 {
		g.index++
	}
	return publication, nil
}

type lateResultGateway struct {
	publication platforminsights.Publication
	started     chan struct{}
	release     chan struct{}
	returned    chan struct{}
}

func (g *lateResultGateway) Get(context.Context, string) (platforminsights.Publication, error) {
	return g.publication, nil
}

func (g *lateResultGateway) Reconcile(context.Context, string) (platforminsights.Publication, error) {
	close(g.started)
	<-g.release
	close(g.returned)
	return g.publication, errors.New("reconcile returned partial publication")
}

type fakeMetricsProvider struct {
	calls int
}

func (p *fakeMetricsProvider) Name() string     { return "fake" }
func (p *fakeMetricsProvider) Platform() string { return "douyin" }
func (p *fakeMetricsProvider) Capability(context.Context) platforminsights.Capability {
	return platforminsights.Capability{Provider: p.Name(), Platform: p.Platform(), State: platforminsights.CapabilityReady}
}
func (p *fakeMetricsProvider) Fetch(_ context.Context, req platforminsights.MetricsRequest) (platforminsights.MetricsObservation, error) {
	p.calls++
	return platforminsights.MetricsObservation{
		Provider: p.Name(), Platform: p.Platform(), ExternalID: req.ExternalID,
		ObservedAt: time.Now().UTC(), Metrics: map[string]float64{"views": 12},
	}, nil
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(t.TempDir() + "/analytics.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testPublication(status platforminsights.PublicationStatus) platforminsights.Publication {
	return platforminsights.Publication{
		ID: "pub-1", Platform: "douyin", Account: "acct", ExternalID: "item-1", ExternalURL: "https://example/item-1",
		Status: status, Evidence: platforminsights.Evidence{Status: status, ExternalID: "item-1", URL: "https://example/item-1", Verified: true, Proof: "fake-proof"},
		UpdatedAt: time.Now().UTC(),
	}
}

func TestServiceStoresReviewTransitionsAndIdempotentMetrics(t *testing.T) {
	store := testStore(t)
	gateway := &sequenceGateway{publications: []platforminsights.Publication{
		testPublication(platforminsights.StatusSubmitted),
		testPublication(platforminsights.StatusReviewing),
		testPublication(platforminsights.StatusPublished),
	}}
	provider := &fakeMetricsProvider{}
	service := NewServiceWithOptions(store, gateway, []platforminsights.MetricsProvider{provider}, Options{Owner: "worker-a", LeaseTTL: time.Second, RetryDelay: time.Millisecond})
	for i := 0; i < 3; i++ {
		if err := service.Sync(context.Background(), "pub-1"); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
	}
	// The gateway repeats its final observation. Both the review and metric
	// snapshots must remain single rows for the duplicate observation.
	if err := service.Sync(context.Background(), "pub-1"); err != nil {
		t.Fatal(err)
	}
	reviews, err := store.ListReviewSnapshots(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 3 || reviews[0].Status != platforminsights.StatusSubmitted || reviews[1].Status != platforminsights.StatusReviewing || reviews[2].Status != platforminsights.StatusPublished {
		t.Fatalf("unexpected review history: %+v", reviews)
	}
	metrics, err := store.ListMetricSnapshots(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(metrics) != 1 || provider.calls != 2 {
		t.Fatalf("unexpected metric history/calls: %d/%d", len(metrics), provider.calls)
	}
}

func TestServiceNeverPersistsUnverifiedPublished(t *testing.T) {
	store := testStore(t)
	unverified := testPublication(platforminsights.StatusPublished)
	unverified.Evidence.Verified = false
	service := NewService(store, &sequenceGateway{publications: []platforminsights.Publication{unverified}})
	if err := service.Sync(context.Background(), "pub-1"); err != nil {
		t.Fatal(err)
	}
	reviews, err := store.ListReviewSnapshots(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].Status != platforminsights.StatusUnknown || reviews[0].Verified {
		t.Fatalf("unverified published result was not downgraded: %+v", reviews)
	}
}

func TestServicePersistsCompletedPartialReconcileBeforeRetry(t *testing.T) {
	store := testStore(t)
	publication := testPublication(platforminsights.StatusReviewing)
	reconcileErr := errors.New("provider returned partial publication")
	gateway := platforminsights.PublicationGatewayFunc{
		GetFunc: func(context.Context, string) (platforminsights.Publication, error) {
			return publication, nil
		},
		ReconcileFunc: func(context.Context, string) (platforminsights.Publication, error) {
			return publication, reconcileErr
		},
	}
	service := NewServiceWithOptions(store, gateway, nil, Options{Owner: "worker-a", RetryDelay: time.Millisecond})

	if err := service.Sync(context.Background(), "pub-1"); !errors.Is(err, reconcileErr) {
		t.Fatalf("partial reconcile error = %v", err)
	}
	reviews, err := store.ListReviewSnapshots(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 || reviews[0].Status != platforminsights.StatusReviewing {
		t.Fatalf("completed partial reconcile was not snapshotted: %+v", reviews)
	}
	job, err := store.GetJob(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobRetry || job.LastError != reconcileErr.Error() {
		t.Fatalf("partial reconcile job state = %+v", job)
	}
}

func TestLeaseExpiryFencesStaleWorker(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Enqueue(ctx, "pub-1"); err != nil {
		t.Fatal(err)
	}
	firstAt := time.Now().UTC()
	_, first, err := store.ClaimDue(ctx, "worker-a", time.Second, firstAt)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.ClaimDue(ctx, "worker-b", time.Second, firstAt.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == first.Token {
		t.Fatal("expired lease was not fenced with a new token")
	}
	_, _, err = store.SaveReviewSnapshotFenced(ctx, ReviewSnapshot{PublicationID: "pub-1", Provider: "publication", Platform: "douyin", Status: platforminsights.StatusReviewing}, first)
	if !errors.Is(err, platforminsights.ErrLeaseLost) {
		t.Fatalf("stale fenced write error = %v", err)
	}
	if err := store.Finish(ctx, "pub-1", first, JobDone, time.Now().UTC(), nil); !errors.Is(err, platforminsights.ErrLeaseLost) {
		t.Fatalf("stale finish error = %v", err)
	}
	if _, _, err := store.SaveReviewSnapshotFenced(ctx, ReviewSnapshot{PublicationID: "pub-1", Provider: "publication", Platform: "douyin", Status: platforminsights.StatusReviewing}, second); err != nil {
		t.Fatalf("current fenced write: %v", err)
	}
}

func TestSyncClaimsOnlyRequestedPublication(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := store.Enqueue(ctx, "unrelated-publication"); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(store, &sequenceGateway{publications: []platforminsights.Publication{testPublication(platforminsights.StatusSubmitted)}}, nil, Options{Owner: "targeted-worker", LeaseTTL: time.Second})
	if err := service.Sync(ctx, "pub-1"); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetJob(ctx, "unrelated-publication")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobQueued {
		t.Fatalf("unrelated job status=%q, want queued", job.Status)
	}
}

func TestSyncCancellationDoesNotReadLateReconcileResult(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := store.Enqueue(context.Background(), "pub-1"); err != nil {
		t.Fatal(err)
	}
	gateway := &lateResultGateway{
		publication: testPublication(platforminsights.StatusReviewing),
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		returned:    make(chan struct{}),
	}
	service := NewServiceWithOptions(store, gateway, nil, Options{Owner: "worker-a", LeaseTTL: time.Second})

	done := make(chan error, 1)
	go func() { done <- service.RunOnce(ctx) }()
	<-gateway.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sync error = %v", err)
	}

	close(gateway.release)
	select {
	case <-gateway.returned:
	case <-time.After(time.Second):
		t.Fatal("late reconcile did not return")
	}
	reviews, err := store.ListReviewSnapshots(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 0 {
		t.Fatalf("late reconcile persisted review snapshot: %+v", reviews)
	}
	job, err := store.GetJob(context.Background(), "pub-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobRunning || job.LeaseOwner != "worker-a" {
		t.Fatalf("canceled worker unexpectedly finished job: %+v", job)
	}

	_, staleLease, err := store.ClaimDue(context.Background(), "worker-b", time.Second, job.LeaseUntil.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveReviewSnapshotFenced(context.Background(), ReviewSnapshot{
		PublicationID: "pub-1", Provider: publicationProvider, Platform: "douyin", Status: platforminsights.StatusReviewing,
	}, Lease{Owner: "worker-a", Token: job.LeaseToken, Until: job.LeaseUntil}); !errors.Is(err, platforminsights.ErrLeaseLost) {
		t.Fatalf("stale canceled worker write error = %v", err)
	}
	if _, _, err := store.SaveReviewSnapshotFenced(context.Background(), ReviewSnapshot{
		PublicationID: "pub-1", Provider: publicationProvider, Platform: "douyin", Status: platforminsights.StatusReviewing,
	}, staleLease); err != nil {
		t.Fatalf("current worker fenced write: %v", err)
	}
}
