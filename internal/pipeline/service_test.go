package pipeline

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
	_ "modernc.org/sqlite"
)

func TestPipelineLegacyLeaseTimestampMigrationPreservesActiveLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-pipeline.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE pipeline_runs (id TEXT PRIMARY KEY);
CREATE TABLE pipeline_leases (
  run_id TEXT PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  token TEXT NOT NULL,
  lease_until TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO pipeline_runs(id) VALUES ('legacy-pipeline');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	legacyUntil := time.Date(2030, time.January, 2, 3, 4, 5, 100000000, time.UTC)
	legacyStamp := legacyUntil.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO pipeline_leases(run_id,owner,token,lease_until,updated_at) VALUES(?,?,?,?,?)`, "legacy-pipeline", "legacy-worker", "legacy-token", legacyStamp, legacyStamp); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var migrated int64
	if err := store.db.QueryRow(`SELECT lease_until_ns FROM pipeline_leases WHERE run_id=?`, "legacy-pipeline").Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if _, want := leaseTimestamp(legacyUntil); migrated != want {
		t.Fatalf("migrated lease_until_ns=%d, want %d", migrated, want)
	}
	if _, err := store.AcquireLease(context.Background(), "legacy-pipeline", "new-worker", time.Minute); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("active legacy lease was not preserved: %v", err)
	}
}

type fakeJudgment struct {
	result orchestration.Result
	err    error
	calls  *int
}

func (f fakeJudgment) Run(context.Context, domain.ResearchTopic) (orchestration.Result, error) {
	if f.calls != nil {
		*f.calls++
	}
	return f.result, f.err
}

type fakeDistribution struct {
	mu             sync.Mutex
	starts         []domain.DistributionRequest
	runs           map[string]domain.Run
	statuses       []domain.RunStatus
	startErrAt     int
	startErr       error
	retries        int
	retryErrBefore error
	retryErrAfter  error
	receipts       map[fakeRetryKey]bool
}

type fakeRetryKey struct {
	parentID      string
	parentAttempt int
	childID       string
}

func (f *fakeDistribution) RetryOnce(_ context.Context, parentID string, parentAttempt int, id string) (domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	operation := fakeRetryKey{parentID, parentAttempt, id}
	if f.receipts == nil {
		f.receipts = make(map[fakeRetryKey]bool)
	}
	if f.receipts[operation] {
		for _, run := range f.runs {
			if run.ID == id {
				return run, nil
			}
		}
	}
	f.retries++
	if f.retryErrBefore != nil {
		err := f.retryErrBefore
		f.retryErrBefore = nil
		return domain.Run{}, err
	}
	for key, run := range f.runs {
		if run.ID == id {
			if run.Status == domain.RunStatusBlocked || run.Status == domain.RunStatusFailed || run.Status == domain.RunStatusCancelled {
				run.Status = domain.RunStatusPackageReady
			}
			run.UpdatedAt = time.Now().UTC()
			f.runs[key] = run
			f.receipts[operation] = true
			if f.retryErrAfter != nil {
				err := f.retryErrAfter
				f.retryErrAfter = nil
				return domain.Run{}, err
			}
			return run, nil
		}
	}
	return domain.Run{}, errors.New("child not found")
}

type starterOnly struct{ DistributionStarter }

func (starterOnly) Retry(context.Context, string) (domain.Run, error) {
	return domain.Run{}, errors.New("ordinary Retry must not be used")
}

func newFakeDistribution(status domain.RunStatus) *fakeDistribution {
	return &fakeDistribution{runs: map[string]domain.Run{}, statuses: []domain.RunStatus{status}}
}

func (f *fakeDistribution) StartRun(_ context.Context, request domain.DistributionRequest) (domain.Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.runs[request.IdempotencyKey]; ok {
		return existing, false, nil
	}
	if f.startErrAt > 0 && len(f.starts)+1 == f.startErrAt {
		return domain.Run{}, false, f.startErr
	}
	run := domain.Run{ID: "child_" + request.IdempotencyKey, Status: f.statuses[0], Request: request, UpdatedAt: time.Now().UTC()}
	f.starts = append(f.starts, request)
	f.runs[request.IdempotencyKey] = run
	return run, true, nil
}

func (f *fakeDistribution) GetRun(_ context.Context, id string) (domain.Run, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key, run := range f.runs {
		if run.ID == id {
			return run, nil
		}
		_ = key
	}
	return domain.Run{}, errors.New("child not found")
}

func testTopic() domain.ResearchTopic {
	return domain.ResearchTopic{Topic: "topic", SourceURLs: []string{"https://example.com/source"}, DesiredAction: "inspect", Audiences: []string{"builders"}, Angles: []string{"evidence"}}
}

func testAtom(id string) domain.ContentAtom {
	return domain.ContentAtom{SchemaVersion: domain.ContentAtomSchemaVersion, Revision: 1, ID: id, Kind: domain.ContentAtomJudgment, SourceType: "research", SourceID: "source", ThesisID: "thesis", Audience: "builders", Angle: "evidence", Hook: "hook", Claim: "claim", EvidenceIDs: []string{"evidence"}, Title: "title"}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "pipeline.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func waitPipeline(t *testing.T, service *Service, id string, want PipelineStatus) PipelineRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == want {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := service.GetRun(context.Background(), id)
	t.Fatalf("pipeline status = %s, want %s", run.Status, want)
	return PipelineRun{}
}

func TestServiceFansOutAtomsAndTargetsAndWaitsReady(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusPackageReady)
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1"), testAtom("a2")}}}, children, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, created, err := service.StartRun(context.Background(), PipelineRequest{IdempotencyKey: "parent-1", Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin, AccountID: "one"}, {Platform: domain.PlatformXiaohongshu, AccountID: "two"}}})
	if err != nil || !created {
		t.Fatalf("start = %#v, %v, created=%v", run, err, created)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusDistributionReady)
	if len(run.ChildRunIDs) != 4 || len(children.starts) != 4 {
		t.Fatalf("children = %d/%d", len(run.ChildRunIDs), len(children.starts))
	}
	for _, request := range children.starts {
		if request.Atom == nil || len(request.Targets) != 1 {
			t.Fatalf("child request = %#v", request)
		}
	}
}

func TestServicePersistsWorkflowCapabilityBlocker(t *testing.T) {
	store := openTestStore(t)
	service := NewService(store, nil, newFakeDistribution(domain.RunStatusPackageReady), Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if run.CapabilityBlock == nil || run.CapabilityBlock.Capability != "judgment_workflow" {
		t.Fatalf("block = %#v", run.CapabilityBlock)
	}
}

func TestServicePersistsDistributionCapabilityBlocker(t *testing.T) {
	store := openTestStore(t)
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, nil, Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if run.CapabilityBlock == nil || run.CapabilityBlock.Capability != "distribution_starter" {
		t.Fatalf("block = %#v", run.CapabilityBlock)
	}
}

func TestServicePersistsPartialFanoutBeforeLaterBlocker(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusPackageReady)
	children.startErrAt = 2
	children.startErr = &orchestration.CapabilityBlocker{Capability: "distribution_api", Reason: "connector unavailable"}
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1"), testAtom("a2")}}}, children, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if len(run.ChildRunIDs) != 1 || len(run.Children) != 1 {
		t.Fatalf("partial children = %#v / %#v", run.ChildRunIDs, run.Children)
	}
	if run.Children[0].ID == "" || run.Children[0].Status != domain.RunStatusPackageReady {
		t.Fatalf("persisted child = %#v", run.Children[0])
	}
	if run.CapabilityBlock == nil || run.CapabilityBlock.Capability != "distribution_api" {
		t.Fatalf("parent blocker = %#v", run.CapabilityBlock)
	}
}

func TestServiceChildBlockedAndIdempotencyReplay(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusBlocked)
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, children, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := PipelineRequest{IdempotencyKey: "same", Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}}
	first, created, err := service.StartRun(context.Background(), request)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	second, created, err := service.StartRun(context.Background(), request)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("replay = %s created=%v err=%v", second.ID, created, err)
	}
	first = waitPipeline(t, service, first.ID, PipelineStatusBlocked)
	if len(first.Children) != 1 || first.Children[0].Status != domain.RunStatusBlocked {
		t.Fatalf("children = %#v", first.Children)
	}
	if len(children.starts) != 1 {
		t.Fatalf("child starts = %d", len(children.starts))
	}
	conflict := request
	conflict.Targets = []domain.DistributionTarget{{Platform: domain.PlatformXiaohongshu}}
	if _, _, err := service.StartRun(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
}

func TestRetryReusesSavedAtomsAndRetriesExistingChild(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusBlocked)
	calls := 0
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("stable-atom")}}, calls: &calls}, children, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	firstID, firstKey := run.Children[0].ID, run.Children[0].IdempotencyKey
	if _, err := service.Retry(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusDistributionReady)
	if calls != 1 {
		t.Fatalf("judgment calls=%d, want 1", calls)
	}
	if children.retries != 1 {
		t.Fatalf("child retries=%d, want 1", children.retries)
	}
	if len(children.starts) != 1 || run.Children[0].ID != firstID || run.Children[0].IdempotencyKey != firstKey {
		t.Fatalf("child duplicated or changed: starts=%d children=%#v", len(children.starts), run.Children)
	}
}

func TestRetryWithoutChildRetryCapabilityBlocks(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusBlocked)
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, starterOnly{children}, Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if _, err := service.Retry(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if run.CapabilityBlock == nil || run.CapabilityBlock.Capability != "distribution_retry_once" {
		t.Fatalf("expected distribution_retry_once capability block, got %#v", run.CapabilityBlock)
	}
	if len(children.starts) != 1 {
		t.Fatalf("child starts=%d, want 1", len(children.starts))
	}
}

func TestRetryRecoversAcrossUnpersistedChildRetry(t *testing.T) {
	for _, tc := range []struct {
		name        string
		before      bool
		wantRetries int
	}{
		{name: "before child retry", before: true, wantRetries: 2},
		{name: "after child retry", wantRetries: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := openTestStore(t)
			children := newFakeDistribution(domain.RunStatusBlocked)
			service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, children, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
			if err := service.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Stop(context.Background()) })
			run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
			if err != nil {
				t.Fatal(err)
			}
			run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
			childID := run.Children[0].ID
			children.mu.Lock()
			if tc.before {
				children.retryErrBefore = context.Canceled
			} else {
				children.retryErrAfter = context.Canceled
			}
			children.mu.Unlock()
			if _, err := service.Retry(context.Background(), run.ID); err != nil {
				t.Fatal(err)
			}
			run = waitPipeline(t, service, run.ID, PipelineStatusDistributionReady)
			if children.retries != tc.wantRetries || len(children.starts) != 1 || run.Children[0].ID != childID {
				t.Fatalf("retries=%d starts=%d children=%#v", children.retries, len(children.starts), run.Children)
			}
		})
	}
}

func TestRetryUsesCurrentChildStatus(t *testing.T) {
	store := openTestStore(t)
	children := newFakeDistribution(domain.RunStatusBlocked)
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, children, Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := service.StartRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusBlocked)
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "stale-observation", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	stale := append([]ChildRun(nil), run.Children...)
	stale[0].Status = domain.RunStatusRunning
	if err := store.SetChildrenFenced(context.Background(), run.ID, lease, stale, "stale_child_status"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLeaseToken(context.Background(), run.ID, lease); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Retry(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	run = waitPipeline(t, service, run.ID, PipelineStatusDistributionReady)
	if children.retries != 1 || len(children.starts) != 1 || run.Children[0].ID != stale[0].ID {
		t.Fatalf("retries=%d starts=%d children=%#v", children.retries, len(children.starts), run.Children)
	}
}

func TestRetryRunAtomicRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry-pipeline.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.CreateRun(ctx, PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	block := &domain.CapabilityBlock{Code: "CAPABILITY_BLOCKED", Capability: "test", Message: "temporary", Retryable: true}
	if err := store.UpdateRun(ctx, run.ID, PipelineStatusBlocked, "capability_blocked", block, nil, nil); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(ctx, run.ID, "old-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER fail_retry_event BEFORE INSERT ON pipeline_events WHEN NEW.checkpoint='retry_queued' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.RetryRun(ctx, run.ID); err == nil || changed {
		t.Fatalf("injected failure: changed=%v err=%v", changed, err)
	}
	stillBlocked, err := store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillBlocked.Status != PipelineStatusBlocked || stillBlocked.Attempt != 0 || stillBlocked.Checkpoint != "capability_blocked" || stillBlocked.CapabilityBlock == nil || stillBlocked.FinishedAt == nil {
		t.Fatalf("retry left partial state: %#v", stillBlocked)
	}
	if err := store.RenewLease(ctx, run.ID, lease, time.Minute); err != nil {
		t.Fatalf("failed transaction revoked lease: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_retry_event`); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, newFakeDistribution(domain.RunStatusPackageReady), Options{PollInterval: time.Millisecond})
	queued, err := service.Retry(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != PipelineStatusQueued || queued.Attempt != 1 || queued.Checkpoint != "retry_queued" || queued.CapabilityBlock != nil || queued.Error != nil || queued.FinishedAt != nil {
		t.Fatalf("atomic retry state: %#v", queued)
	}
	if err := store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old lease was not fenced: %v", err)
	}
	if again, err := service.Retry(ctx, run.ID); err != nil || again.Attempt != 1 {
		t.Fatalf("duplicate retry: attempt=%d err=%v", again.Attempt, err)
	}
	events, err := store.ListEvents(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	retryEvents := 0
	for _, event := range events {
		if event.Checkpoint == "retry_queued" {
			retryEvents++
			if event.Payload["attempt"] != float64(1) {
				t.Fatalf("retry event payload: %#v", event.Payload)
			}
		}
	}
	if retryEvents != 1 {
		t.Fatalf("retry events=%d, want 1", retryEvents)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service = NewService(reopened, fakeJudgment{result: orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}}, newFakeDistribution(domain.RunStatusPackageReady), Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	completed := waitPipeline(t, service, run.ID, PipelineStatusDistributionReady)
	if completed.Attempt != 1 || len(completed.Children) != 1 {
		t.Fatalf("recovered retry: %#v", completed)
	}
}

type receiptDistribution struct {
	store        *runstore.Store
	afterReceipt func(string)
	creates      int
	callCount    int
}

func (d *receiptDistribution) StartRun(ctx context.Context, request domain.DistributionRequest) (domain.Run, bool, error) {
	run, created, err := d.store.CreateRun(ctx, request)
	if created {
		d.creates++
	}
	return run, created, err
}

func (d *receiptDistribution) GetRun(ctx context.Context, id string) (domain.Run, error) {
	return d.store.GetRun(ctx, id)
}

func (d *receiptDistribution) RetryOnce(ctx context.Context, parent string, attempt int, child string) (domain.Run, error) {
	d.callCount++
	run, err := d.store.RetryOnce(ctx, parent, attempt, child)
	if err == nil && d.afterReceipt != nil {
		d.afterReceipt(child)
	}
	return run, err
}

func TestRetryOnceSurvivesParentWriteFailureAndChildFailsAgain(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new child association"
		if existing {
			name = "existing blocked child"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			parentStore := openTestStore(t)
			childPath := filepath.Join(t.TempDir(), "children.db")
			childStore, err := runstore.Open(childPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = childStore.Close() })
			distribution := &receiptDistribution{store: childStore}
			request := PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}}
			parent, _, err := parentStore.CreateRun(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := parentStore.AcquireLease(ctx, parent.ID, "setup", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			atom := testAtom("stable")
			if err := parentStore.SaveWorkflowFenced(ctx, parent.ID, lease, orchestration.Result{Atoms: []domain.ContentAtom{atom}}); err != nil {
				t.Fatal(err)
			}
			key := ChildIdempotencyKey(parent.ID, atom.ID, request.Targets[0])
			if existing {
				child, _, err := distribution.StartRun(ctx, domain.DistributionRequest{IdempotencyKey: key, Atom: cloneAtom(atom), Targets: request.Targets})
				if err != nil {
					t.Fatal(err)
				}
				if err := childStore.UpdateRun(ctx, child.ID, domain.RunStatusBlocked, "blocked", nil, nil, nil); err != nil {
					t.Fatal(err)
				}
				if err := parentStore.SetChildrenFenced(ctx, parent.ID, lease, []ChildRun{{ID: child.ID, IdempotencyKey: key, AtomID: atom.ID, Target: request.Targets[0], Status: domain.RunStatusBlocked}}, "child_created"); err != nil {
					t.Fatal(err)
				}
			}
			if err := parentStore.UpdateRunFenced(ctx, parent.ID, lease, PipelineStatusFailed, "failed", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			service := NewService(parentStore, nil, distribution, Options{PollInterval: time.Millisecond, PollTimeout: time.Second})
			parent, err = service.Retry(ctx, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			lease, err = parentStore.AcquireLease(ctx, parent.ID, "interrupted", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := parentStore.UpdateRunFenced(ctx, parent.ID, lease, PipelineStatusRunning, "started", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			interrupted, cancel := context.WithCancel(ctx)
			defer cancel()
			var childID string
			distribution.afterReceipt = func(id string) {
				childID = id
				if err := childStore.UpdateRun(ctx, id, domain.RunStatusFailed, "failed_again", nil, nil, nil); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			if err := service.execute(interrupted, parent, lease); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected interrupted parent write: %v", err)
			}
			before, err := parentStore.GetRun(ctx, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(before.Children) > 0 && before.Children[0].RetryAttempt != 0 {
				t.Fatal("interrupted parent persisted retry marker")
			}
			if !existing && len(before.Children) != 0 {
				t.Fatal("new association unexpectedly persisted")
			}
			if err := parentStore.ReleaseLeaseToken(ctx, parent.ID, lease); err != nil {
				t.Fatal(err)
			}
			if err := childStore.Close(); err != nil {
				t.Fatal(err)
			}
			childStore, err = runstore.Open(childPath)
			if err != nil {
				t.Fatal(err)
			}
			distribution.store, distribution.afterReceipt = childStore, nil
			if err := parentStore.RecoverRunning(ctx); err != nil {
				t.Fatal(err)
			}
			service.process(ctx, parent.ID)
			after, err := parentStore.GetRun(ctx, parent.ID)
			if err != nil {
				t.Fatal(err)
			}
			child, err := childStore.GetRun(ctx, childID)
			if err != nil {
				t.Fatal(err)
			}
			wantAttempt := 0
			if existing {
				wantAttempt = 1
			}
			if after.Status != PipelineStatusFailed || len(after.Children) != 1 || after.Children[0].RetryAttempt != 1 || after.Children[0].ID != childID || child.Attempt != wantAttempt || child.Status != domain.RunStatusFailed || distribution.creates != 1 || distribution.callCount != 2 {
				t.Fatalf("replay: parent=%#v child=%#v creates=%d calls=%d", after, child, distribution.creates, distribution.callCount)
			}
		})
	}
}

func TestStoreLeaseRecoveryAndCancel(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "test", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, PipelineStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	leaseUntil, leaseUntilNS := leaseTimestamp(time.Now().Add(-time.Minute))
	if _, err := store.db.Exec(`UPDATE pipeline_leases SET lease_until=?,lease_until_ns=? WHERE run_id=?`, leaseUntil, leaseUntilNS, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err = store.GetRun(context.Background(), run.ID)
	if err != nil || run.Status != PipelineStatusQueued {
		t.Fatalf("recovered = %s, %v", run.Status, err)
	}
	if err := store.CancelRun(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = store.GetRun(context.Background(), run.ID)
	if err != nil || run.Status != PipelineStatusCancelled {
		t.Fatalf("cancelled = %s, %v", run.Status, err)
	}
}

func TestExpiredPipelineLeaseCannotWriteBeforeReaper(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "old-worker", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	if err := store.RenewLease(context.Background(), run.ID, lease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired renew error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, PipelineStatusRunning, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired update error=%v, want ErrLeaseLost", err)
	}
	if err := store.SaveWorkflowFenced(context.Background(), run.ID, lease, orchestration.Result{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired workflow save error=%v, want ErrLeaseLost", err)
	}
	if err := store.SetChildrenFenced(context.Background(), run.ID, lease, []ChildRun{{ID: "stale-child"}}, "stale-children"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired children set error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateChildrenFenced(context.Background(), run.ID, lease, []ChildRun{{ID: "stale-child"}}, "stale-observation"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired children update error=%v, want ErrLeaseLost", err)
	}

	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != PipelineStatusQueued || got.Checkpoint != "" {
		t.Fatalf("expired lease changed pipeline: status=%s checkpoint=%q", got.Status, got.Checkpoint)
	}
}

func TestReclaimedPipelineLeaseRejectsOldToken(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := store.AcquireLease(context.Background(), run.ID, "old-worker", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	newLease, err := store.AcquireLease(context.Background(), run.ID, "new-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RenewLease(context.Background(), run.ID, oldLease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed renew error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, oldLease, PipelineStatusFailed, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed update error=%v, want ErrLeaseLost", err)
	}
	if err := store.SaveWorkflowFenced(context.Background(), run.ID, oldLease, orchestration.Result{}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed workflow save error=%v, want ErrLeaseLost", err)
	}
	if err := store.SetChildrenFenced(context.Background(), run.ID, oldLease, []ChildRun{{ID: "stale-child"}}, "stale-children"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed children set error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateChildrenFenced(context.Background(), run.ID, oldLease, []ChildRun{{ID: "stale-child"}}, "stale-observation"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed children update error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, newLease, PipelineStatusRunning, "current", nil, nil, nil); err != nil {
		t.Fatalf("new lease update: %v", err)
	}
}

func TestValidPipelineLeaseAndClockBoundary(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(context.Background(), run.ID, lease, time.Minute); err != nil {
		t.Fatalf("valid renew: %v", err)
	}
	if err := store.SaveWorkflowFenced(context.Background(), run.ID, lease, orchestration.Result{Atoms: []domain.ContentAtom{testAtom("a1")}}); err != nil {
		t.Fatalf("valid workflow save: %v", err)
	}
	if err := store.SetChildrenFenced(context.Background(), run.ID, lease, []ChildRun{{ID: "child"}}, "children"); err != nil {
		t.Fatalf("valid children set: %v", err)
	}
	if err := store.UpdateChildrenFenced(context.Background(), run.ID, lease, []ChildRun{{ID: "child", Status: domain.RunStatusPackageReady}}, "observed"); err != nil {
		t.Fatalf("valid children update: %v", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, PipelineStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatalf("valid update: %v", err)
	}

	boundary, boundaryNS := leaseTimestamp(time.Now())
	if _, err := store.db.Exec(`UPDATE pipeline_leases SET lease_until=?,lease_until_ns=? WHERE run_id=?`, boundary, boundaryNS, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(context.Background(), run.ID, lease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("boundary renew error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, PipelineStatusFailed, "boundary", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("boundary update error=%v, want ErrLeaseLost", err)
	}
	if err := store.ReleaseLeaseToken(context.Background(), run.ID, lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("boundary release error=%v, want ErrLeaseLost", err)
	}
	newLease, err := store.AcquireLease(context.Background(), run.ID, "new-worker", time.Minute)
	if err != nil {
		t.Fatalf("boundary lease was not reclaimable: %v", err)
	}
	if err := store.ReleaseLeaseToken(context.Background(), run.ID, newLease); err != nil {
		t.Fatalf("valid release: %v", err)
	}
}

func TestReleasePipelineLeaseTokenRejectsExpiredLeaseWithoutDeletingIt(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "worker", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := store.ReleaseLeaseToken(context.Background(), run.ID, lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired release error=%v, want ErrLeaseLost", err)
	}
	var owner, token string
	if err := store.db.QueryRow(`SELECT owner,token FROM pipeline_leases WHERE run_id=?`, run.ID).Scan(&owner, &token); err != nil {
		t.Fatalf("expired lease was deleted: %v", err)
	}
	if owner != lease.Owner || token != lease.Token {
		t.Fatalf("lease changed after rejected release: owner=%q token=%q", owner, token)
	}
}
