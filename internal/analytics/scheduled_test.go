package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
)

type scheduledGateway struct {
	mu             sync.Mutex
	publications   []platforminsights.Publication
	byID           map[string][]platforminsights.Publication
	getCalls       map[string]int
	ordinaryGets   int
	cursors        []string
	reconcileCalls int
}

func (g *scheduledGateway) ListForAnalytics(_ context.Context, cursor string, limit int) ([]platforminsights.Publication, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cursors = append(g.cursors, cursor)
	start := 0
	if cursor != "" {
		for i, publication := range g.publications {
			if publication.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	if start >= len(g.publications) {
		return nil, nil
	}
	end := start + limit
	if end > len(g.publications) {
		end = len(g.publications)
	}
	return append([]platforminsights.Publication(nil), g.publications[start:end]...), nil
}

func (g *scheduledGateway) Get(_ context.Context, id string) (platforminsights.Publication, error) {
	g.mu.Lock()
	g.ordinaryGets++
	g.mu.Unlock()
	return platforminsights.Publication{}, errors.New("scheduled polling must not call Get")
}

func (g *scheduledGateway) GetForAnalytics(_ context.Context, id string) (platforminsights.Publication, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.getCalls == nil {
		g.getCalls = make(map[string]int)
	}
	sequence := g.byID[id]
	if len(sequence) == 0 {
		return platforminsights.Publication{}, errors.New("publication not found")
	}
	index := g.getCalls[id]
	if index >= len(sequence) {
		index = len(sequence) - 1
	}
	g.getCalls[id]++
	return sequence[index], nil
}

func (g *scheduledGateway) Reconcile(context.Context, string) (platforminsights.Publication, error) {
	g.mu.Lock()
	g.reconcileCalls++
	g.mu.Unlock()
	return platforminsights.Publication{}, errors.New("scheduled polling must not reconcile")
}

func scheduledPublication(id string, status platforminsights.PublicationStatus, updatedAt time.Time) platforminsights.Publication {
	return platforminsights.Publication{
		ID: id, Platform: "douyin", Account: "acct", ExternalID: "item-" + id, ExternalURL: "https://example/" + id,
		Status: status, UpdatedAt: updatedAt,
		Evidence: platforminsights.Evidence{Status: status, ExternalID: "item-" + id, URL: "https://example/" + id, Verified: status == platforminsights.StatusPublished, Proof: "verified"},
	}
}

func seedFinishedJob(t *testing.T, store *Store, id, status string, next time.Time) SyncJob {
	t.Helper()
	ctx := context.Background()
	if err := store.Enqueue(ctx, id); err != nil {
		t.Fatal(err)
	}
	job, lease, err := store.ClaimDue(ctx, "seed-"+id, time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Finish(ctx, id, lease, status, next, nil); err != nil {
		t.Fatal(err)
	}
	job, err = store.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestScheduledDiscoveryReactivatesEligibleManualDoneJobs(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	window := time.Hour
	cases := []struct {
		name      string
		status    platforminsights.PublicationStatus
		updatedAt time.Time
		verified  bool
		want      string
	}{
		{name: "submitted", status: platforminsights.StatusSubmitted, updatedAt: now, want: JobQueued},
		{name: "reviewing", status: platforminsights.StatusReviewing, updatedAt: now, want: JobQueued},
		{name: "unknown", status: platforminsights.StatusUnknown, updatedAt: now, want: JobQueued},
		{name: "published within window", status: platforminsights.StatusPublished, updatedAt: now.Add(-window / 2), verified: true, want: JobQueued},
		{name: "published outside window", status: platforminsights.StatusPublished, updatedAt: now.Add(-2 * window), verified: true, want: JobDone},
		{name: "unverified published", status: platforminsights.StatusPublished, updatedAt: now, want: JobDone},
		{name: "rejected", status: platforminsights.StatusRejected, updatedAt: now, want: JobDone},
		{name: "failed", status: platforminsights.StatusFailed, updatedAt: now, want: JobDone},
		{name: "expired", status: platforminsights.PublicationStatus("expired"), updatedAt: now, want: JobDone},
	}
	publications := make([]platforminsights.Publication, 0, len(cases))
	store := testStore(t)
	for _, tc := range cases {
		id := "done-" + strings.ReplaceAll(tc.name, " ", "-")
		seedFinishedJob(t, store, id, JobDone, now)
		publication := scheduledPublication(id, tc.status, tc.updatedAt)
		publication.Evidence.Verified = tc.verified
		publications = append(publications, publication)
	}
	gateway := &scheduledGateway{publications: publications}
	service := NewServiceWithOptions(store, gateway, nil, Options{PollEnabled: true, Clock: func() time.Time { return now }, MetricsWindow: window})

	if err := service.discover(ctx, gateway); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		id := "done-" + strings.ReplaceAll(tc.name, " ", "-")
		job, err := store.GetJob(ctx, id)
		if err != nil || job.Status != tc.want {
			t.Fatalf("%s job = %+v, %v; want %q", tc.name, job, err, tc.want)
		}
	}
}

func TestScheduledDiscoveryDoesNotChurnTerminalOrExistingJobs(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := testStore(t)
	terminal := seedFinishedJob(t, store, "terminal", JobDone, now.Add(-time.Hour))
	waiting := seedFinishedJob(t, store, "waiting", JobWaiting, now.Add(time.Hour))
	retry := seedFinishedJob(t, store, "retry", JobRetry, now.Add(time.Hour))
	if err := store.Enqueue(ctx, "active"); err != nil {
		t.Fatal(err)
	}
	active, lease, err := store.ClaimDue(ctx, "active-owner", time.Hour, time.Now().UTC().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, "queued"); err != nil {
		t.Fatal(err)
	}
	queued, err := store.GetJob(ctx, "queued")
	if err != nil {
		t.Fatal(err)
	}
	publications := []platforminsights.Publication{
		scheduledPublication("terminal", platforminsights.StatusRejected, now),
		scheduledPublication("queued", platforminsights.StatusSubmitted, now),
		scheduledPublication("waiting", platforminsights.StatusReviewing, now),
		scheduledPublication("retry", platforminsights.StatusUnknown, now),
		scheduledPublication("active", platforminsights.StatusSubmitted, now),
	}
	gateway := &scheduledGateway{publications: publications}
	service := NewServiceWithOptions(store, gateway, nil, Options{PollEnabled: true, Clock: func() time.Time { return now }, MetricsWindow: time.Hour})

	for i := 0; i < 3; i++ {
		if err := service.discover(ctx, gateway); err != nil {
			t.Fatal(err)
		}
	}

	gotTerminal, err := store.GetJob(ctx, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	if gotTerminal.Status != JobDone || !gotTerminal.UpdatedAt.Equal(terminal.UpdatedAt) {
		t.Fatalf("terminal job changed after repeated discovery: before=%+v after=%+v", terminal, gotTerminal)
	}
	for _, want := range []SyncJob{waiting, retry} {
		got, err := store.GetJob(ctx, want.PublicationID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != want.Status || got.Attempts != want.Attempts || !got.NextAttemptAt.Equal(want.NextAttemptAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
			t.Fatalf("existing %s job changed: before=%+v after=%+v", want.Status, want, got)
		}
	}
	gotQueued, err := store.GetJob(ctx, queued.PublicationID)
	if err != nil {
		t.Fatal(err)
	}
	if gotQueued.Status != queued.Status || gotQueued.Attempts != queued.Attempts || !gotQueued.NextAttemptAt.Equal(queued.NextAttemptAt) || !gotQueued.UpdatedAt.Equal(queued.UpdatedAt) {
		t.Fatalf("existing queued job changed: before=%+v after=%+v", queued, gotQueued)
	}
	gotActive, err := store.GetJob(ctx, active.PublicationID)
	if err != nil {
		t.Fatal(err)
	}
	if gotActive.Status != JobRunning || gotActive.LeaseOwner != lease.Owner || gotActive.LeaseToken != lease.Token || !gotActive.LeaseUntil.Equal(lease.Until) {
		t.Fatalf("active lease changed after discovery: %+v", gotActive)
	}
}

func TestReactivateDoneIsAtomicAndLeaseFenced(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	now := time.Now().UTC()
	seedFinishedJob(t, store, "concurrent", JobDone, now)
	results := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		go func() {
			reactivated, err := store.ReactivateDone(ctx, "concurrent", now)
			if err != nil {
				t.Errorf("reactivate done: %v", err)
			}
			results <- reactivated
		}()
	}
	if first, second := <-results, <-results; first == second {
		t.Fatalf("concurrent reactivation results = %v, %v; want exactly one success", first, second)
	}
	job, err := store.GetJob(ctx, "concurrent")
	if err != nil || job.Status != JobQueued {
		t.Fatalf("concurrent reactivation job = %+v, %v", job, err)
	}

	active, lease, err := store.ClaimDue(ctx, "lease-owner", time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if reactivated, err := store.ReactivateDone(ctx, active.PublicationID, now); err != nil || reactivated {
		t.Fatalf("active lease reactivation = %v, %v", reactivated, err)
	}
	current, err := store.GetJob(ctx, active.PublicationID)
	if err != nil || current.Status != JobRunning || current.LeaseToken != lease.Token {
		t.Fatalf("active lease job changed = %+v, %v", current, err)
	}
}

func TestScheduledCursorPersistsAcrossRestartAndCoversBeyondPage(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/analytics.db"
	publications := make([]platforminsights.Publication, 0, 205)
	byID := make(map[string][]platforminsights.Publication)
	for i := 1; i <= 205; i++ {
		id := fmt.Sprintf("pub-%03d", i)
		publication := scheduledPublication(id, platforminsights.StatusSubmitted, time.Now().UTC())
		publications = append(publications, publication)
		byID[id] = []platforminsights.Publication{publication}
	}
	gateway := &scheduledGateway{publications: publications, byID: byID}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithOptions(store, gateway, nil, Options{Owner: "cursor-a", PollEnabled: true, DiscoveryPageSize: 200, MaxJobs: 1, ReviewInterval: time.Hour})
	if err := service.RunScheduledOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.DiscoveryCursor(ctx)
	if err != nil || cursor != "pub-200" {
		t.Fatalf("cursor after first page = %q, %v", cursor, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	service = NewServiceWithOptions(store, gateway, nil, Options{Owner: "cursor-b", PollEnabled: true, DiscoveryPageSize: 200, MaxJobs: 1, ReviewInterval: time.Hour})
	if err := service.RunScheduledOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cursor, err = store.DiscoveryCursor(ctx)
	if err != nil || cursor != "pub-205" {
		t.Fatalf("cursor after restart = %q, %v", cursor, err)
	}
	if _, err := store.GetJob(ctx, "pub-205"); err != nil {
		t.Fatalf("publication beyond first page was not scheduled: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service = NewServiceWithOptions(store, gateway, nil, Options{Owner: "cursor-c", PollEnabled: true, DiscoveryPageSize: 200, MaxJobs: 1, ReviewInterval: time.Hour})
	if err := service.RunScheduledOnce(ctx); err != nil {
		t.Fatal(err)
	}
	cursor, err = store.DiscoveryCursor(ctx)
	if err != nil || cursor != "" {
		t.Fatalf("cursor did not reset after final page: %q, %v", cursor, err)
	}
}

func TestScheduledTransitionsAndDoesNotReconcile(t *testing.T) {
	store := testStore(t)
	id := "transition"
	now := time.Now().UTC()
	submitted := scheduledPublication(id, platforminsights.StatusSubmitted, now)
	reviewing := scheduledPublication(id, platforminsights.StatusReviewing, now)
	published := scheduledPublication(id, platforminsights.StatusPublished, now)
	gateway := &scheduledGateway{
		publications: []platforminsights.Publication{submitted},
		byID:         map[string][]platforminsights.Publication{id: {submitted, reviewing, published}},
	}
	provider := &fakeMetricsProvider{}
	service := NewServiceWithOptions(store, gateway, []platforminsights.MetricsProvider{provider}, Options{Owner: "transition", PollEnabled: true, DiscoveryPageSize: 1, MaxJobs: 1, ReviewInterval: time.Millisecond, MetricsInterval: time.Millisecond, MetricsWindow: time.Hour})
	for i := 0; i < 8; i++ {
		if err := service.RunScheduledOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	reviews, err := store.ListReviewSnapshots(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 3 || reviews[0].Status != platforminsights.StatusSubmitted || reviews[1].Status != platforminsights.StatusReviewing || reviews[2].Status != platforminsights.StatusPublished {
		t.Fatalf("unexpected scheduled review history: %+v", reviews)
	}
	if provider.calls == 0 {
		t.Fatal("published follow-up did not fetch metrics")
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil || job.Status != JobWaiting {
		t.Fatalf("scheduled published job = %+v, %v", job, err)
	}
	if gateway.reconcileCalls != 0 {
		t.Fatalf("scheduled polling called reconcile %d times", gateway.reconcileCalls)
	}
	if gateway.ordinaryGets != 0 {
		t.Fatalf("scheduled polling called mutating Get %d times", gateway.ordinaryGets)
	}
}

func TestScheduledMetricsWindowAndDedupe(t *testing.T) {
	store := testStore(t)
	id := "expired"
	publication := scheduledPublication(id, platforminsights.StatusPublished, time.Now().UTC().Add(-2*time.Hour))
	gateway := &scheduledGateway{
		publications: []platforminsights.Publication{publication, publication, publication},
		byID:         map[string][]platforminsights.Publication{id: {publication}},
	}
	provider := &fakeMetricsProvider{}
	service := NewServiceWithOptions(store, gateway, []platforminsights.MetricsProvider{provider}, Options{Owner: "window", PollEnabled: true, DiscoveryPageSize: 10, MaxJobs: 10, MetricsWindow: time.Hour})
	if err := service.RunScheduledOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil || job.Status != JobDone {
		t.Fatalf("expired metrics job = %+v, %v", job, err)
	}
	if provider.calls != 0 {
		t.Fatalf("expired metrics window fetched %d times", provider.calls)
	}
	if count, err := store.CountReviewSnapshots(context.Background(), id); err != nil || count != 1 {
		t.Fatalf("duplicate discovery created %d review snapshots, %v", count, err)
	}
}

func TestScheduledMissingProviderBlocks(t *testing.T) {
	store := testStore(t)
	id := "blocked"
	publication := scheduledPublication(id, platforminsights.StatusPublished, time.Now().UTC())
	gateway := &scheduledGateway{publications: []platforminsights.Publication{publication}, byID: map[string][]platforminsights.Publication{id: {publication}}}
	service := NewServiceWithOptions(store, gateway, nil, Options{Owner: "blocked", PollEnabled: true, DiscoveryPageSize: 1, MaxJobs: 1, MetricsWindow: time.Hour})
	if err := service.RunScheduledOnce(context.Background()); !errors.Is(err, platforminsights.ErrCapabilityBlocked) {
		t.Fatalf("missing provider error = %v", err)
	}
	job, err := store.GetJob(context.Background(), id)
	if err != nil || job.Status != JobBlocked {
		t.Fatalf("missing provider job = %+v, %v", job, err)
	}
}

type discoveryWithoutSnapshot struct {
	platforminsights.PublicationGateway
	getCalls int
}

type unusedRunReader struct{}

func (unusedRunReader) GetRun(context.Context, string) (domain.Run, error) {
	return domain.Run{}, errors.New("analytics must not read runs")
}

func (g *discoveryWithoutSnapshot) Get(context.Context, string) (platforminsights.Publication, error) {
	g.getCalls++
	return platforminsights.Publication{}, errors.New("Get must not be called")
}

func (g *discoveryWithoutSnapshot) ListForAnalytics(context.Context, string, int) ([]platforminsights.Publication, error) {
	return nil, nil
}

func TestScheduledRequiresDirectSnapshotReader(t *testing.T) {
	store := testStore(t)
	gateway := &discoveryWithoutSnapshot{}
	service := NewServiceWithOptions(store, gateway, nil, Options{PollEnabled: true})
	if err := service.RunScheduledOnce(context.Background()); !errors.Is(err, ErrNoSnapshotReader) {
		t.Fatalf("missing snapshot reader error = %v", err)
	}
	if gateway.getCalls != 0 {
		t.Fatalf("ordinary Get called %d times", gateway.getCalls)
	}
}

func TestScheduledPassPreservesExpiredCommittingPublication(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	publicationDB, err := sql.Open("sqlite", t.TempDir()+"/publications.db")
	if err != nil {
		t.Fatal(err)
	}
	defer publicationDB.Close()
	orchestrator, err := publication.NewWithClock(publicationDB, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	const id = "expired-committing"
	pkg := publication.Package{
		Creator: "creator", Account: "account", Platform: publication.PlatformDouyin,
		Run: "run", AtomRevision: "1", VariantID: "variant", Video: "video.mp4",
		VideoSHA256: strings.Repeat("a", 64), Cover: "cover.png", CoverSHA256: strings.Repeat("b", 64),
		Copy: "complete copy",
	}
	fingerprint, err := pkg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	updated := now.Add(-3 * time.Hour).Format(time.RFC3339Nano)
	_, err = publicationDB.ExecContext(ctx, `INSERT INTO pub_publications(
		id,creator,run_id,account,platform,variant_id,package_json,fingerprint,policy_version,business_day,authorization_id,status,
		reservation_token,operation_token,lease_until,external_id,external_url,evidence,last_error,reserved_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, pkg.Creator, pkg.Run, pkg.Account, pkg.Platform, pkg.VariantID, string(raw), fingerprint, "v1", "2026-09-21", "auth-committing", "committing",
		"reservation", "operation-token", now.Add(-time.Hour).UnixNano(), "", "", "", "original-error", updated, updated, updated)
	if err != nil {
		t.Fatal(err)
	}
	readRawState := func() [5]string {
		t.Helper()
		var state [5]string
		if err := publicationDB.QueryRowContext(ctx, `SELECT status,operation_token,CAST(lease_until AS TEXT),last_error,updated_at FROM pub_publications WHERE id=?`, id).
			Scan(&state[0], &state[1], &state[2], &state[3], &state[4]); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := readRawState()
	store := testStore(t)
	coordination, err := publication.NewService(unusedRunReader{}, orchestrator, publication.CoordinationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gateway := platforminsights.NewPublicationServiceGateway(coordination)
	service := NewServiceWithOptions(store, gateway, nil, Options{PollEnabled: true, MaxJobs: 1, Clock: func() time.Time { return now.Add(time.Second) }})
	if err := service.RunScheduledOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if after := readRawState(); after != before {
		t.Fatalf("scheduled pass mutated publication row: before=%q after=%q", before, after)
	}
	job, err := store.GetJob(ctx, id)
	if err != nil || job.Status != JobWaiting {
		t.Fatalf("committing analytics job = %+v, %v", job, err)
	}
}

type capabilityProvider struct {
	state      platforminsights.CapabilityState
	capCalls   int
	fetchCalls int
}

func (p *capabilityProvider) Name() string     { return "capability-test" }
func (p *capabilityProvider) Platform() string { return "douyin" }
func (p *capabilityProvider) Capability(context.Context) platforminsights.Capability {
	p.capCalls++
	return platforminsights.Capability{Provider: p.Name(), Platform: p.Platform(), State: p.state, Reason: "credentials unavailable"}
}
func (p *capabilityProvider) Fetch(_ context.Context, req platforminsights.MetricsRequest) (platforminsights.MetricsObservation, error) {
	p.fetchCalls++
	return platforminsights.MetricsObservation{
		Provider: p.Name(), Platform: p.Platform(), ExternalID: req.ExternalID,
		ObservedAt: time.Now().UTC(), Metrics: map[string]float64{"views": 1},
	}, nil
}

func TestScheduledBlockedJobRecoversAfterRestart(t *testing.T) {
	for _, firstState := range []platforminsights.CapabilityState{"missing", platforminsights.CapabilityUnavailable} {
		t.Run(string(firstState), func(t *testing.T) {
			ctx := context.Background()
			path := t.TempDir() + "/analytics.db"
			now := time.Now().UTC().Add(time.Second)
			id := "blocked-recovery"
			item := scheduledPublication(id, platforminsights.StatusPublished, now.Add(-time.Minute))
			gateway := &scheduledGateway{publications: []platforminsights.Publication{item}, byID: map[string][]platforminsights.Publication{id: {item}}}
			store, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			var before *capabilityProvider
			var providers []platforminsights.MetricsProvider
			if firstState != "missing" {
				before = &capabilityProvider{state: firstState}
				providers = []platforminsights.MetricsProvider{before}
			}
			options := Options{Owner: "first", PollEnabled: true, MaxJobs: 1, RetryDelay: time.Second, MetricsWindow: time.Hour, Clock: func() time.Time { return now }}
			service := NewServiceWithOptions(store, gateway, providers, options)
			if err := service.RunScheduledOnce(ctx); !errors.Is(err, platforminsights.ErrCapabilityBlocked) {
				t.Fatalf("initial capability error = %v", err)
			}
			blocked, err := store.GetJob(ctx, id)
			if err != nil || blocked.Status != JobBlocked || !blocked.NextAttemptAt.After(now) || blocked.NextAttemptAt.After(now.Add(maxRetryDelay)) {
				t.Fatalf("blocked schedule = %+v, %v", blocked, err)
			}
			if before != nil && before.fetchCalls != 0 {
				t.Fatalf("unavailable provider fetched %d times", before.fetchCalls)
			}
			if err := service.RunScheduledOnce(ctx); err != nil {
				t.Fatalf("blocked job retried before due: %v", err)
			}
			stillBlocked, err := store.GetJob(ctx, id)
			if err != nil || stillBlocked.Status != JobBlocked || stillBlocked.Attempts != blocked.Attempts {
				t.Fatalf("blocked status changed before due: %+v, %v", stillBlocked, err)
			}
			now = blocked.NextAttemptAt.Add(time.Millisecond)
			if err := service.RunScheduledOnce(ctx); !errors.Is(err, platforminsights.ErrCapabilityBlocked) {
				t.Fatalf("second capability error = %v", err)
			}
			blockedAgain, err := store.GetJob(ctx, id)
			if err != nil || blockedAgain.Status != JobBlocked || blockedAgain.Attempts != blocked.Attempts+1 || !blockedAgain.NextAttemptAt.After(now.Add(options.RetryDelay)) || blockedAgain.NextAttemptAt.After(now.Add(maxRetryDelay)) {
				t.Fatalf("second blocked schedule = %+v, %v", blockedAgain, err)
			}
			if before != nil && (before.capCalls != 2 || before.fetchCalls != 0) {
				t.Fatalf("unavailable provider checks/fetches = %d/%d", before.capCalls, before.fetchCalls)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			now = blockedAgain.NextAttemptAt.Add(time.Millisecond)
			ready := &capabilityProvider{state: platforminsights.CapabilityReady}
			options.Owner = "after-restart"
			service = NewServiceWithOptions(store, gateway, []platforminsights.MetricsProvider{ready}, options)
			if err := service.RunScheduledOnce(ctx); err != nil {
				t.Fatal(err)
			}
			job, err := store.GetJob(ctx, id)
			if err != nil || job.Status != JobWaiting {
				t.Fatalf("ready job = %+v, %v", job, err)
			}
			if ready.capCalls != 1 || ready.fetchCalls != 1 {
				t.Fatalf("ready provider checks/fetches = %d/%d", ready.capCalls, ready.fetchCalls)
			}
		})
	}
}

func TestManualSyncCanRequeueBlockedJobWithPollingDisabled(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	provider := &capabilityProvider{state: platforminsights.CapabilityUnavailable}
	gateway := &sequenceGateway{publications: []platforminsights.Publication{testPublication(platforminsights.StatusPublished)}}
	service := NewServiceWithOptions(store, gateway, []platforminsights.MetricsProvider{provider}, Options{PollEnabled: false})
	if err := service.Sync(ctx, "pub-1"); !errors.Is(err, platforminsights.ErrCapabilityBlocked) {
		t.Fatalf("initial manual sync error = %v", err)
	}
	job, err := store.GetJob(ctx, "pub-1")
	if err != nil || job.Status != JobBlocked || provider.fetchCalls != 0 {
		t.Fatalf("blocked manual job = %+v, fetches=%d, err=%v", job, provider.fetchCalls, err)
	}
	provider.state = platforminsights.CapabilityReady
	if err := service.Sync(ctx, "pub-1"); err != nil {
		t.Fatalf("manual sync after capability recovery: %v", err)
	}
	job, err = store.GetJob(ctx, "pub-1")
	if err != nil || job.Status != JobDone || provider.fetchCalls != 1 {
		t.Fatalf("recovered manual job = %+v, fetches=%d, err=%v", job, provider.fetchCalls, err)
	}
}
