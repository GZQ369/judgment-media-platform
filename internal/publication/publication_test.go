package publication

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type fakeAdapter struct {
	platform       Platform
	configured     bool
	commitCalls    atomic.Int32
	reconcileCalls atomic.Int32
	commit         func(AdapterRequest) (Evidence, error)
	reconcile      func(AdapterRequest) (Evidence, error)
}

func (f *fakeAdapter) Platform() Platform { return f.platform }
func (f *fakeAdapter) Capability(context.Context) Capability {
	return Capability{Platform: f.platform, Configured: f.configured, Reason: "fake adapter configuration"}
}
func (f *fakeAdapter) Commit(_ context.Context, req AdapterRequest) (Evidence, error) {
	f.commitCalls.Add(1)
	if f.commit != nil {
		return f.commit(req)
	}
	return Evidence{Status: StatusSubmitted, ExternalID: req.PublicationID, Verified: true, Proof: "fake"}, nil
}
func (f *fakeAdapter) Reconcile(_ context.Context, req AdapterRequest) (Evidence, error) {
	f.reconcileCalls.Add(1)
	if f.reconcile != nil {
		return f.reconcile(req)
	}
	return Evidence{}, nil
}

func newTestOrchestrator(t *testing.T, now time.Time, adapters ...Adapter) (*Orchestrator, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:publication-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	o, err := NewWithClock(db, func() time.Time { return now }, adapters...)
	if err != nil {
		t.Fatal(err)
	}
	return o, db
}

func authorize(t *testing.T, o *Orchestrator, pkg Package, expiry time.Time) Authorization {
	t.Helper()
	fingerprint, err := pkg.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	a, err := o.Authorize(context.Background(), AuthorizeRequest{
		Fingerprint:   fingerprint,
		PolicyVersion: "v1",
		Creator:       pkg.Creator,
		Account:       pkg.Account,
		Platform:      pkg.Platform,
		Run:           pkg.Run,
		VariantID:     pkg.VariantID,
		ExpiresAt:     expiry,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func packageFor(t *testing.T, account string, platform Platform, run string) Package {
	t.Helper()
	dir := t.TempDir()
	video := []byte("video:" + account + ":" + string(platform) + ":" + run)
	cover := []byte("cover:" + account + ":" + string(platform) + ":" + run)
	videoPath := filepath.Join(dir, "video.mp4")
	coverPath := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(videoPath, video, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coverPath, cover, 0o600); err != nil {
		t.Fatal(err)
	}
	return Package{
		Creator:      "creator-1",
		Account:      account,
		Platform:     platform,
		Run:          run,
		AtomRevision: "atom-1",
		VariantID:    "variant-1",
		Video:        videoPath,
		VideoSHA256:  digest(video),
		Cover:        coverPath,
		CoverSHA256:  digest(cover),
		Copy:         "complete copy for " + account + ":" + string(platform) + ":" + run,
		CopySummary:  "summary",
	}
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func prepare(t *testing.T, o *Orchestrator, pkg Package, auth Authorization) Publication {
	t.Helper()
	p, err := o.Prepare(context.Background(), PrepareRequest{
		Package:         pkg,
		Policy:          DefaultPolicy("v1"),
		AuthorizationID: auth.ID,
		Fingerprint:     auth.Fingerprint,
		BusinessDay:     o.now().In(o.location).Format("2006-01-02"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConcurrentCommitUsesOneReservation(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-1")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	p := prepare(t, o, pkg, a)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := o.Commit(context.Background(), CommitRequest{PublicationID: p.ID, Fingerprint: p.Fingerprint, Confirm: true})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes int
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrNeedsReconcile) {
			t.Fatalf("unexpected concurrent commit error: %v", err)
		}
	}
	if successes != 1 || fake.commitCalls.Load() != 1 {
		t.Fatalf("reservation was not exclusive: successes=%d commit_calls=%d", successes, fake.commitCalls.Load())
	}
}

func TestDailyAdmissionIsRunLevelButThreePlatformsShareRun(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fakes := []*fakeAdapter{
		{platform: PlatformDouyin, configured: true},
		{platform: PlatformXiaohongshu, configured: true},
		{platform: PlatformBilibili, configured: true},
	}
	o, _ := newTestOrchestrator(t, now, fakes[0], fakes[1], fakes[2])
	for _, platform := range []Platform{PlatformDouyin, PlatformXiaohongshu, PlatformBilibili} {
		pkg := packageFor(t, "acct", platform, "run-1")
		a := authorize(t, o, pkg, now.Add(time.Hour))
		p := prepare(t, o, pkg, a)
		if p.Status != StatusDraftReady {
			t.Fatalf("unexpected status for %s: %s", platform, p.Status)
		}
	}

	secondPackage := packageFor(t, "acct", PlatformDouyin, "run-2")
	second := authorize(t, o, secondPackage, now.Add(time.Hour))
	_, err := o.Prepare(context.Background(), PrepareRequest{Package: secondPackage, Policy: DefaultPolicy("v1"), AuthorizationID: second.ID, Fingerprint: second.Fingerprint, BusinessDay: "2026-09-16"})
	var dailyErr *DailyContentError
	if !errors.As(err, &dailyErr) {
		t.Fatalf("expected run-level daily admission error, got %v", err)
	}
}

func TestExpiredAndChangedFingerprintAuthorizationAreRejected(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true}
	clock := now
	db, err := sql.Open("sqlite", "file:publication-expiry-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	o, err := NewWithClock(db, func() time.Time { return clock }, fake)
	if err != nil {
		t.Fatal(err)
	}
	expiredPackage := packageFor(t, "acct", PlatformDouyin, "run-expired")
	expired := authorize(t, o, expiredPackage, now.Add(time.Minute))
	clock = now.Add(2 * time.Minute)
	_, err = o.Prepare(context.Background(), PrepareRequest{Package: expiredPackage, Policy: DefaultPolicy("v1"), AuthorizationID: expired.ID, Fingerprint: expired.Fingerprint, BusinessDay: "2026-09-16"})
	if !errors.Is(err, ErrAuthorizationExpired) {
		t.Fatalf("expected expired authorization, got %v", err)
	}

	fingerprintPackage := packageFor(t, "acct", PlatformDouyin, "run-fp")
	a := authorize(t, o, fingerprintPackage, now.Add(time.Hour))
	p := prepare(t, o, fingerprintPackage, a)
	_, err = o.Commit(context.Background(), CommitRequest{PublicationID: p.ID, Fingerprint: digest([]byte("changed")), Confirm: true})
	if !errors.Is(err, ErrAuthorizationMismatch) {
		t.Fatalf("expected fingerprint binding mismatch, got %v", err)
	}
}

func TestUnknownRequiresReconcileAndNeverResubmits(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	var commitReservation, reconcileReservation string
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true, commit: func(req AdapterRequest) (Evidence, error) {
		commitReservation = req.Reservation
		return Evidence{}, context.DeadlineExceeded
	}, reconcile: func(req AdapterRequest) (Evidence, error) {
		reconcileReservation = req.Reservation
		return Evidence{Status: StatusSubmitted, ExternalID: "external-1", URL: "https://platform.example/item-1", Verified: true, Proof: "query evidence"}, nil
	}}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-unknown")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	p := prepare(t, o, pkg, a)
	_, err := o.Commit(context.Background(), CommitRequest{PublicationID: p.ID, Fingerprint: p.Fingerprint, Confirm: true})
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("expected unknown outcome, got %v", err)
	}
	if fake.commitCalls.Load() != 1 {
		t.Fatalf("expected one commit call, got %d", fake.commitCalls.Load())
	}
	_, err = o.Commit(context.Background(), CommitRequest{PublicationID: p.ID, Fingerprint: p.Fingerprint, Confirm: true})
	if !errors.Is(err, ErrNeedsReconcile) {
		t.Fatalf("expected reconcile gate, got %v", err)
	}
	resolved, err := o.Reconcile(context.Background(), ReconcileRequest{PublicationID: p.ID})
	if err != nil || resolved.Status != StatusSubmitted || fake.reconcileCalls.Load() != 1 {
		t.Fatalf("unexpected reconciliation: publication=%+v err=%v calls=%d", resolved, err, fake.reconcileCalls.Load())
	}
	if commitReservation == "" || reconcileReservation != commitReservation {
		t.Fatalf("reconcile replaced stable adapter reservation: commit=%q reconcile=%q", commitReservation, reconcileReservation)
	}
}

func TestCapabilityBlockerIsExplicitAndUnconfigured(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: false}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-blocked")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	_, err := o.Prepare(context.Background(), PrepareRequest{Package: pkg, Policy: DefaultPolicy("v1"), AuthorizationID: a.ID, Fingerprint: a.Fingerprint, BusinessDay: "2026-09-16"})
	var blocker *CapabilityBlocker
	if !errors.As(err, &blocker) || blocker.Platform != PlatformDouyin {
		t.Fatalf("expected explicit capability blocker, got %v", err)
	}
}

func TestUnverifiedReadyLikeEvidenceCannotSucceed(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true, commit: func(AdapterRequest) (Evidence, error) {
		return Evidence{Message: "READY"}, nil
	}}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-ready")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	p := prepare(t, o, pkg, a)
	resolved, err := o.Commit(context.Background(), CommitRequest{PublicationID: p.ID, Fingerprint: p.Fingerprint, Confirm: true})
	if !errors.Is(err, ErrInvalidEvidence) || resolved.Status != StatusUnknown {
		t.Fatalf("READY-like evidence must be unknown: publication=%+v err=%v", resolved, err)
	}
}

func TestPublicationRoundTripsAfterDatabaseReopen(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	dbPath := filepath.Join(t.TempDir(), "publication.db")
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	o, err := NewWithClock(db, func() time.Time { return now }, fake)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	pkg := packageFor(t, "acct", PlatformDouyin, "run-roundtrip")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	prepared := prepare(t, o, pkg, a)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	o2, err := NewWithClock(reopened, func() time.Time { return now }, fake)
	if err != nil {
		t.Fatal(err)
	}
	got, err := o2.Get(context.Background(), prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != prepared.ID || got.Fingerprint != prepared.Fingerprint || got.Status != StatusDraftReady {
		t.Fatalf("database roundtrip changed publication: got=%+v want=%+v", got, prepared)
	}
}

func TestPrepareAndCommitRejectChangedLocalMediaBytes(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-media-change")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	prepared := prepare(t, o, pkg, a)
	if err := os.WriteFile(pkg.Video, []byte("changed video bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := o.Commit(context.Background(), CommitRequest{PublicationID: prepared.ID, Fingerprint: prepared.Fingerprint, Confirm: true})
	if !errors.Is(err, ErrAuthorizationMismatch) {
		t.Fatalf("expected changed media mismatch, got %v", err)
	}
	if resolved.Status != StatusDraftReady || fake.commitCalls.Load() != 0 {
		t.Fatalf("changed media must not reserve or call adapter: publication=%+v calls=%d", resolved, fake.commitCalls.Load())
	}
}

func TestReconcileOperationTokenRejectsConcurrentAndStaleResults(t *testing.T) {
	now := time.Date(2026, 9, 16, 1, 0, 0, 0, time.UTC)
	entered := make(chan struct{})
	release := make(chan struct{})
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true,
		commit: func(AdapterRequest) (Evidence, error) { return Evidence{}, context.DeadlineExceeded },
		reconcile: func(ctx AdapterRequest) (Evidence, error) {
			close(entered)
			<-release
			return Evidence{Status: StatusSubmitted, ExternalID: "external-race", Verified: true, Proof: "query"}, nil
		},
	}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-reconcile-race")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	prepared := prepare(t, o, pkg, a)
	if _, err := o.Commit(context.Background(), CommitRequest{PublicationID: prepared.ID, Fingerprint: prepared.Fingerprint, Confirm: true}); !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("expected unknown initial outcome, got %v", err)
	}

	type reconcileResult struct {
		publication Publication
		err         error
	}
	first := make(chan reconcileResult, 1)
	go func() {
		publication, err := o.Reconcile(context.Background(), ReconcileRequest{PublicationID: prepared.ID})
		first <- reconcileResult{publication: publication, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reconcile adapter was not called")
	}
	_, err := o.Reconcile(context.Background(), ReconcileRequest{PublicationID: prepared.ID})
	if !errors.Is(err, ErrOperationActive) {
		t.Fatalf("expected active reconcile lease, got %v", err)
	}
	close(release)
	result := <-first
	if result.err != nil || result.publication.Status != StatusSubmitted {
		t.Fatalf("reconcile did not resolve after concurrent attempt: publication=%+v err=%v", result.publication, result.err)
	}
	if fake.commitCalls.Load() != 1 || fake.reconcileCalls.Load() != 1 {
		t.Fatalf("unexpected adapter calls: commit=%d reconcile=%d", fake.commitCalls.Load(), fake.reconcileCalls.Load())
	}
}

func TestBusinessDayUsesAsiaShanghaiAndCallerCannotChooseAnotherDay(t *testing.T) {
	now := time.Date(2026, 9, 15, 16, 0, 0, 0, time.UTC)
	fake := &fakeAdapter{platform: PlatformDouyin, configured: true}
	o, _ := newTestOrchestrator(t, now, fake)
	pkg := packageFor(t, "acct", PlatformDouyin, "run-shanghai-boundary")
	a := authorize(t, o, pkg, now.Add(time.Hour))
	fingerprint := a.Fingerprint
	prepared, err := o.Prepare(context.Background(), PrepareRequest{
		Package:         pkg,
		Policy:          DefaultPolicy("v1"),
		AuthorizationID: a.ID,
		Fingerprint:     fingerprint,
		BusinessDay:     "2026-09-16",
	})
	if err != nil || prepared.BusinessDay != "2026-09-16" {
		t.Fatalf("expected Asia/Shanghai boundary day, publication=%+v err=%v", prepared, err)
	}

	wrongPackage := packageFor(t, "acct-2", PlatformDouyin, "run-wrong-day")
	wrong := authorize(t, o, wrongPackage, now.Add(time.Hour))
	_, err = o.Prepare(context.Background(), PrepareRequest{
		Package:         wrongPackage,
		Policy:          DefaultPolicy("v1"),
		AuthorizationID: wrong.ID,
		Fingerprint:     wrong.Fingerprint,
		BusinessDay:     "2026-09-15",
	})
	if !errors.Is(err, ErrBusinessDayMismatch) {
		t.Fatalf("expected caller-selected business day to be rejected, got %v", err)
	}
}
