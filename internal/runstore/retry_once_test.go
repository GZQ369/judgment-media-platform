package runstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

func openRetryStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func retryChild(t *testing.T, s *Store, status domain.RunStatus) domain.Run {
	t.Helper()
	r, _, err := s.CreateRun(context.Background(), domain.DistributionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRun(context.Background(), r.ID, status, "original", &domain.CapabilityBlock{Capability: "test"}, &domain.RunError{Code: "test"}, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRetryOnceConcurrentAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry.db")
	a, b := openRetryStore(t, path), openRetryStore(t, path)
	child := retryChild(t, a, domain.RunStatusBlocked)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			_, err := s.RetryOnce(ctx, "parent", 1, child.ID)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err := a.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Attempt != 1 || run.Status != domain.RunStatusQueued || run.Error != nil || run.CapabilityBlock != nil || run.FinishedAt != nil {
		t.Fatalf("retry state: %#v", run)
	}
	var receipts, events int
	if err := a.db.QueryRow(`SELECT count(*) FROM distribution_retry_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM run_events WHERE type='retry_once'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || events != 1 {
		t.Fatalf("receipts=%d events=%d", receipts, events)
	}
	if err := a.UpdateRun(ctx, child.ID, domain.RunStatusFailed, "failed_again", nil, &domain.RunError{Code: "again"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	s := openRetryStore(t, path)
	replayed, err := s.RetryOnce(ctx, "parent", 1, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Status != domain.RunStatusFailed || replayed.Attempt != 1 || replayed.Error == nil || replayed.Error.Code != "again" {
		t.Fatalf("replay requeued failed child: %#v", replayed)
	}
	next, err := s.RetryOnce(ctx, "parent", 2, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != domain.RunStatusQueued || next.Attempt != 2 {
		t.Fatalf("next attempt: %#v", next)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM run_events WHERE type='retry_once'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("events=%d, want 2", events)
	}
}

func TestRetryOnceNoopConsumesKeyAndKeepsLease(t *testing.T) {
	for _, status := range []domain.RunStatus{domain.RunStatusQueued, domain.RunStatusRunning, domain.RunStatusPackageReady, domain.RunStatusMediaReady, domain.RunStatusFailed, domain.RunStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			s := openRetryStore(t, filepath.Join(t.TempDir(), "retry.db"))
			child := retryChild(t, s, status)
			lease, err := s.AcquireLease(ctx, child.ID, "worker", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.RetryOnce(ctx, "parent", 1, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantAttempt, wantOutcome := 0, "noop"
			if status == domain.RunStatusFailed || status == domain.RunStatusCancelled {
				wantAttempt, wantOutcome = 1, "queued"
			}
			if r.Attempt != wantAttempt {
				t.Fatalf("attempt=%d", r.Attempt)
			}
			if err := s.RenewLease(ctx, child.ID, lease, time.Minute); err != nil {
				t.Fatalf("lease lost: %v", err)
			}
			var outcome string
			var recordedAttempt int
			if err := s.db.QueryRow(`SELECT outcome,child_attempt FROM distribution_retry_receipts`).Scan(&outcome, &recordedAttempt); err != nil {
				t.Fatal(err)
			}
			if outcome != wantOutcome || recordedAttempt != wantAttempt {
				t.Fatalf("receipt=%s/%d", outcome, recordedAttempt)
			}
			if err := s.UpdateRun(ctx, child.ID, domain.RunStatusFailed, "later_failure", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			r, err = s.RetryOnce(ctx, "parent", 1, child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != domain.RunStatusFailed || r.Attempt != wantAttempt {
				t.Fatalf("consumed operation replayed: %#v", r)
			}
		})
	}
}

func TestRetryOnceEventFailureRollsBackReceiptAndRun(t *testing.T) {
	ctx := context.Background()
	s := openRetryStore(t, filepath.Join(t.TempDir(), "retry.db"))
	child := retryChild(t, s, domain.RunStatusBlocked)
	if _, err := s.db.Exec(`CREATE TEMP TRIGGER reject_retry BEFORE INSERT ON run_events WHEN NEW.type='retry_once' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryOnce(ctx, "parent", 1, child.ID); err == nil {
		t.Fatal("expected transaction failure")
	}
	r, err := s.GetRun(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err := s.db.QueryRow(`SELECT count(*) FROM distribution_retry_receipts`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || r.Status != domain.RunStatusBlocked || r.Attempt != 0 || r.Error == nil {
		t.Fatalf("partial transaction: receipts=%d run=%#v", receipts, r)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_retry`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryOnce(ctx, "parent", 1, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryOnce(ctx, "parent", 0, child.ID); err == nil {
		t.Fatal("accepted zero parent attempt")
	}
	if _, err := s.RetryOnce(ctx, "parent", 1, "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing child: %v", err)
	}
}

func TestRetryOperationKeyHasStructuredBoundaries(t *testing.T) {
	a := RetryOperationKey("a\x00b", 1, "c")
	if len(a) != 64 || a != RetryOperationKey("a\x00b", 1, "c") || a == RetryOperationKey("a", 1, "b\x00c") || a == RetryOperationKey("a\x00b", 2, "c") {
		t.Fatal("operation key is not stable and unambiguous")
	}
}

func TestRetryOnceRejectsConflictingReceipts(t *testing.T) {
	for _, wrongKey := range []bool{false, true} {
		t.Run(fmt.Sprint(wrongKey), func(t *testing.T) {
			s := openRetryStore(t, filepath.Join(t.TempDir(), "retry.db"))
			child := retryChild(t, s, domain.RunStatusBlocked)
			key, parent := RetryOperationKey("parent", 1, child.ID), "different-parent"
			if wrongKey {
				key, parent = "incorrect-operation-key", "parent"
			}
			if _, err := s.db.Exec(`INSERT INTO distribution_retry_receipts VALUES(?,?,?,?,?,'noop',?)`, key, parent, 1, child.ID, 0, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RetryOnce(context.Background(), "parent", 1, child.ID); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("conflict accepted: %v", err)
			}
			r, err := s.GetRun(context.Background(), child.ID)
			if err != nil {
				t.Fatal(err)
			}
			if r.Attempt != 0 || r.Status != domain.RunStatusBlocked {
				t.Fatalf("conflict mutated child: %#v", r)
			}
		})
	}
}

func TestRetryOnceBusyRetriesAreBoundedAndLeaveNoReceipt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry.db")
	a, b := openRetryStore(t, path), openRetryStore(t, path)
	child := retryChild(t, a, domain.RunStatusBlocked)
	if _, err := b.db.Exec(`PRAGMA busy_timeout=1`); err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE runs SET checkpoint='locked' WHERE id=?`, child.ID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := b.RetryOnce(ctx, "parent", 1, child.ID); err == nil {
		t.Fatal("expected busy error")
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("bounded retry duration=%v", elapsed)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := b.db.QueryRow(`SELECT count(*) FROM distribution_retry_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("busy retry left %d receipts", count)
	}
	r, err := b.RetryOnce(ctx, "parent", 1, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Attempt != 1 {
		t.Fatalf("attempt=%d", r.Attempt)
	}
}
