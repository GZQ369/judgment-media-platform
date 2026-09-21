package runstore

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	_ "modernc.org/sqlite"
)

func TestLeaseTimestampUsesNanosecondOrdering(t *testing.T) {
	base := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	earlier := base.Add(100 * time.Millisecond)
	later := earlier.Add(time.Nanosecond)
	if earlier.Format(time.RFC3339Nano) <= later.Format(time.RFC3339Nano) {
		t.Fatalf("test data must demonstrate the legacy lexical ordering bug: earlier=%q later=%q", earlier.Format(time.RFC3339Nano), later.Format(time.RFC3339Nano))
	}
	_, earlierNS := leaseTimestamp(earlier)
	_, laterNS := leaseTimestamp(later)
	if laterNS <= earlierNS {
		t.Fatalf("numeric lease ordering reversed: earlier=%d later=%d", earlierNS, laterNS)
	}
}

func TestLegacyLeaseTimestampMigrationPreservesActiveLease(t *testing.T) {
	path := t.TempDir() + "/legacy.sqlite"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE runs (id TEXT PRIMARY KEY);
CREATE TABLE run_leases (
  run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  token TEXT NOT NULL,
  lease_until TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO runs(id) VALUES ('legacy-run');`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	legacyUntil := time.Date(2030, time.January, 2, 3, 4, 5, 100000000, time.UTC)
	legacyStamp := legacyUntil.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO run_leases(run_id,owner,token,lease_until,updated_at) VALUES(?,?,?,?,?)`, "legacy-run", "legacy-worker", "legacy-token", legacyStamp, legacyStamp); err != nil {
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
	if err := store.db.QueryRow(`SELECT lease_until_ns FROM run_leases WHERE run_id=?`, "legacy-run").Scan(&migrated); err != nil {
		t.Fatal(err)
	}
	if _, want := leaseTimestamp(legacyUntil); migrated != want {
		t.Fatalf("migrated lease_until_ns=%d, want %d", migrated, want)
	}
	if _, err := store.AcquireLease(context.Background(), "legacy-run", "new-worker", time.Minute); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("active legacy lease was not preserved: %v", err)
	}
}

func TestStoreSmokeCreateReadArtifact(t *testing.T) {
	store, err := Open(t.TempDir() + "/runs.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	request := domain.DistributionRequest{
		IdempotencyKey: "smoke-1",
		Platform:       domain.PlatformXiaohongshu,
		Offering:       &domain.OfferingInput{Title: "Smoke offering"},
	}
	run, created, err := store.CreateRun(context.Background(), request)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if !created || run.Status != domain.RunStatusQueued {
		t.Fatalf("unexpected created run: created=%t run=%+v", created, run)
	}

	if err := store.AddArtifact(context.Background(), domain.Artifact{
		RunID:    run.ID,
		Kind:     "package",
		Path:     "package.json",
		Metadata: map[string]any{"smoke": true},
	}); err != nil {
		t.Fatalf("add artifact: %v", err)
	}
	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Metadata["smoke"] != true {
		t.Fatalf("artifact was not hydrated: %+v", got.Artifacts)
	}
}

func TestMediaReadySetsFinishedAt(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("media-ready-finished"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRun(context.Background(), run.ID, domain.RunStatusMediaReady, "media_ready", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RunStatusMediaReady || got.FinishedAt == nil {
		t.Fatalf("media-ready run did not finish: status=%s finished_at=%v", got.Status, got.FinishedAt)
	}
}

func TestCreateRunRejectsInvalidContentAtom(t *testing.T) {
	store := openTestStore(t)
	_, _, err := store.CreateRun(context.Background(), domain.DistributionRequest{
		Platform: domain.PlatformXiaohongshu,
		Atom:     &domain.ContentAtom{Kind: domain.ContentAtomOffering, Title: "标题"},
	})
	if err == nil {
		t.Fatal("invalid content atom was persisted")
	}
}

func smokeRequest(key string) domain.DistributionRequest {
	return domain.DistributionRequest{
		IdempotencyKey: key,
		Platform:       domain.PlatformXiaohongshu,
		Offering:       &domain.OfferingInput{Title: "Smoke offering", Description: "same payload"},
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir() + "/runs.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateRunIdempotencyIsConcurrentAndPayloadBound(t *testing.T) {
	store := openTestStore(t)
	const workers = 16
	results := make(chan struct {
		run     domain.Run
		created bool
		err     error
	}, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, created, err := store.CreateRun(context.Background(), smokeRequest("same-key"))
			results <- struct {
				run     domain.Run
				created bool
				err     error
			}{run, created, err}
		}()
	}
	wg.Wait()
	close(results)
	var runID string
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent create: %v", result.err)
		}
		if result.created {
			createdCount++
		}
		if runID == "" {
			runID = result.run.ID
		} else if result.run.ID != runID {
			t.Fatalf("idempotency returned multiple run ids: %q and %q", runID, result.run.ID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
	if _, _, err := store.CreateRun(context.Background(), domain.DistributionRequest{
		IdempotencyKey: "same-key",
		Platform:       domain.PlatformXiaohongshu,
		Offering:       &domain.OfferingInput{Title: "different"},
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("different payload error=%v, want ErrIdempotencyConflict", err)
	}
}

func TestRecoverRunningKeepsLiveLeaseAndRequeuesExpiredRun(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("recovery"))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(context.Background(), run.ID, "live-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, domain.RunStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil || got.Status != domain.RunStatusRunning {
		t.Fatalf("live lease was recovered: status=%s err=%v", got.Status, err)
	}

	if err := store.ReleaseLeaseToken(context.Background(), run.ID, lease); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverRunning(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = store.GetRun(context.Background(), run.ID)
	if err != nil || got.Status != domain.RunStatusQueued {
		t.Fatalf("orphaned running run was not requeued: status=%s err=%v", got.Status, err)
	}
}

func TestOpenReopenPreservesRunAndArtifact(t *testing.T) {
	path := t.TempDir() + "/runs.sqlite"
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := first.CreateRun(context.Background(), smokeRequest("reopen"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AddArtifact(context.Background(), domain.Artifact{RunID: run.ID, Kind: "package", Metadata: map[string]any{"persisted": true}}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got, err := second.GetRun(context.Background(), run.ID)
	if err != nil || got.ID != run.ID || len(got.Artifacts) != 1 || got.Artifacts[0].Metadata["persisted"] != true {
		t.Fatalf("reopened run=%+v err=%v", got, err)
	}
}

func TestOldLeaseCannotOverwriteNewLeaseEpoch(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("fencing"))
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := store.AcquireLease(context.Background(), run.ID, "old-worker", time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(8 * time.Millisecond)
	newLease, err := store.AcquireLease(context.Background(), run.ID, "new-worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, oldLease, domain.RunStatusRunning, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale update error=%v, want ErrLeaseLost", err)
	}
	if err := store.AppendEventFenced(context.Background(), run.ID, oldLease, "stale", "stale", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale event error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, newLease, domain.RunStatusRunning, "current", nil, nil, nil); err != nil {
		t.Fatalf("new lease update: %v", err)
	}
	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil || got.Checkpoint != "current" {
		t.Fatalf("new lease was overwritten: run=%+v err=%v", got, err)
	}
}

func TestExpiredLeaseCannotWriteBeforeReaper(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("expired-before-reaper"))
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
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, domain.RunStatusRunning, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired update error=%v, want ErrLeaseLost", err)
	}
	if err := store.AppendEventFenced(context.Background(), run.ID, lease, "stale", "stale", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired event error=%v, want ErrLeaseLost", err)
	}
	if err := store.AddArtifactFenced(context.Background(), domain.Artifact{RunID: run.ID, ID: "stale-artifact", Kind: "package"}, lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired artifact error=%v, want ErrLeaseLost", err)
	}

	got, err := store.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RunStatusQueued || got.Checkpoint != "" || len(got.Artifacts) != 0 {
		t.Fatalf("expired lease changed run: status=%s checkpoint=%q artifacts=%d", got.Status, got.Checkpoint, len(got.Artifacts))
	}
}

func TestReclaimedLeaseRejectsOldTokenWrites(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("reclaimed-old-token"))
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
	if err := store.UpdateRunFenced(context.Background(), run.ID, oldLease, domain.RunStatusRunning, "stale", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed update error=%v, want ErrLeaseLost", err)
	}
	if err := store.AppendEventFenced(context.Background(), run.ID, oldLease, "stale", "stale", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed event error=%v, want ErrLeaseLost", err)
	}
	if err := store.AddArtifactFenced(context.Background(), domain.Artifact{RunID: run.ID, ID: "stale-reclaimed-artifact", Kind: "package"}, oldLease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("reclaimed artifact error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, newLease, domain.RunStatusRunning, "current", nil, nil, nil); err != nil {
		t.Fatalf("new lease update: %v", err)
	}
}

func TestValidLeaseWritesAndClockBoundary(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("valid-and-boundary"))
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
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, domain.RunStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatalf("valid update: %v", err)
	}
	if err := store.AppendEventFenced(context.Background(), run.ID, lease, "checkpoint", "checkpoint", nil); err != nil {
		t.Fatalf("valid event: %v", err)
	}
	if err := store.AddArtifactFenced(context.Background(), domain.Artifact{RunID: run.ID, ID: "valid-artifact", Kind: "package"}, lease); err != nil {
		t.Fatalf("valid artifact: %v", err)
	}

	boundary, boundaryNS := leaseTimestamp(time.Now())
	if _, err := store.db.Exec(`UPDATE run_leases SET lease_until=?,lease_until_ns=? WHERE run_id=?`, boundary, boundaryNS, run.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLease(context.Background(), run.ID, lease, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("boundary renew error=%v, want ErrLeaseLost", err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, lease, domain.RunStatusFailed, "boundary", nil, nil, nil); !errors.Is(err, ErrLeaseLost) {
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

func TestReleaseLeaseTokenRejectsExpiredLeaseWithoutDeletingIt(t *testing.T) {
	store := openTestStore(t)
	run, _, err := store.CreateRun(context.Background(), smokeRequest("release-expired"))
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
	if err := store.db.QueryRow(`SELECT owner,token FROM run_leases WHERE run_id=?`, run.ID).Scan(&owner, &token); err != nil {
		t.Fatalf("expired lease was deleted: %v", err)
	}
	if owner != lease.Owner || token != lease.Token {
		t.Fatalf("lease changed after rejected release: owner=%q token=%q", owner, token)
	}
}

func TestListRunsDoesNotHoldRowsWhileHydratingArtifacts(t *testing.T) {
	store := openTestStore(t)
	for i := 0; i < 8; i++ {
		run, _, err := store.CreateRun(context.Background(), smokeRequest("list-"+string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddArtifact(context.Background(), domain.Artifact{RunID: run.ID, Kind: "package", Metadata: map[string]any{"index": i}}); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.ListRuns(context.Background(), 50)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("list runs: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListRuns appears to be deadlocked while hydrating artifacts")
	}
}
