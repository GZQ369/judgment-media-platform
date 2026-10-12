package distribution

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

func TestRetryOnceReplayQueuedWakesQueueWithoutNewAttempt(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	child, _, err := store.CreateRun(ctx, offeringRequest("retry-once"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRun(ctx, child.ID, domain.RunStatusFailed, "failed", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, t.TempDir())
	// Exercise enqueue without letting a worker change queued state.
	service.started, service.ctx = true, ctx
	for i := 0; i < 2; i++ {
		r, err := service.RetryOnce(ctx, "parent", 1, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Attempt != 1 || r.Status != domain.RunStatusQueued {
			t.Fatalf("replay: %#v", r)
		}
		select {
		case id := <-service.queue:
			if id != child.ID {
				t.Fatalf("queued %s", id)
			}
		default:
			t.Fatal("queued replay did not wake worker")
		}
	}
	if err := store.UpdateRun(ctx, child.ID, domain.RunStatusFailed, "failed_again", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	r, err := service.RetryOnce(ctx, "parent", 1, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Attempt != 1 || r.Status != domain.RunStatusFailed || len(service.queue) != 0 {
		t.Fatalf("failed replay: %#v", r)
	}
}

func TestRetryOnceCommitBeforeStartupIsRecovered(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "distribution.db")
	store, err := runstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	child, _, err := store.CreateRun(ctx, offeringRequest("retry-once-restart"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRun(ctx, child.ID, domain.RunStatusBlocked, "blocked", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	stopped := NewService(store, t.TempDir())
	if _, err := stopped.RetryOnce(ctx, "parent", 1, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service := NewService(reopened, t.TempDir())
	service.executeRun = func(ctx context.Context, r domain.Run, lease runstore.Lease) error {
		return reopened.UpdateRunFenced(ctx, r.ID, lease, domain.RunStatusPackageReady, "ready", nil, nil, nil)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	r := waitForRun(t, service, child.ID, domain.RunStatusPackageReady)
	if r.Attempt != 1 {
		t.Fatalf("attempt=%d", r.Attempt)
	}
}
