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
}

func (f fakeJudgment) Run(context.Context, domain.ResearchTopic) (orchestration.Result, error) {
	return f.result, f.err
}

type fakeDistribution struct {
	mu         sync.Mutex
	starts     []domain.DistributionRequest
	runs       map[string]domain.Run
	statuses   []domain.RunStatus
	startErrAt int
	startErr   error
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

func TestStoreLeaseRecoveryAndCancel(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), PipelineRequest{Topic: testTopic(), Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}}})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "test", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, PipelineStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
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
