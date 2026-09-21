package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

type coordinationRunReader struct{ run domain.Run }

func (r coordinationRunReader) GetRun(context.Context, string) (domain.Run, error) {
	return r.run, nil
}

func TestCoordinationHappyPathRebuildsCandidateAndKeepsPrepareReadOnly(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "rendered.mp4")
	coverPath := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(videoPath, []byte("verified video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coverPath, []byte("verified cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := domain.VideoProject{Platform: domain.PlatformXiaohongshu, Variants: []domain.Variant{{ID: "v1", Title: "标题", Caption: "完整正文"}}, Brief: &domain.PublishingBrief{Caption: "默认正文", Tags: []string{"tag"}}}
	packagePath := filepath.Join(dir, "publish_package.json")
	raw, _ := json.Marshal(project)
	if err := os.WriteFile(packagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "run-1", Status: domain.RunStatusMediaReady, Request: domain.DistributionRequest{Platform: domain.PlatformXiaohongshu, Targets: []domain.DistributionTarget{{Platform: domain.PlatformXiaohongshu, AccountID: "acct"}}, Atom: &domain.ContentAtom{Revision: 3}}, Artifacts: []domain.Artifact{{ID: "package", Kind: "package", Path: packagePath}, {ID: "rendered-video", Kind: "video", Path: videoPath}, {ID: "cover", Kind: "cover", Path: coverPath}}}
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	adapter := &fakeAdapter{platform: PlatformXiaohongshu, configured: true}
	orch, _ := newTestOrchestrator(t, now, adapter)
	service, err := NewService(coordinationRunReader{run: run}, orch, CoordinationOptions{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.Candidate(context.Background(), CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: PlatformXiaohongshu, VariantID: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Package.VideoSHA256 == "" || candidate.Package.CoverSHA256 == "" || candidate.Package.CopySHA256 == "" || candidate.Package.CopySummary != "" {
		t.Fatalf("candidate did not rebuild concrete identity: %+v", candidate.Package)
	}
	if adapter.commitCalls.Load() != 0 {
		t.Fatal("candidate called adapter")
	}
	auth, err := service.Authorize(context.Background(), AuthorizeCandidateRequest{CandidateRequest: candidate.Request, Confirm: true, PolicyVersion: "v1", Selected: []Platform{PlatformXiaohongshu}, ExpiresAt: now.Add(5 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(context.Background(), PrepareCandidateRequest{CandidateRequest: candidate.Request, AuthorizationID: auth.ID, Fingerprint: candidate.Fingerprint, PolicyVersion: "v1", Selected: []Platform{PlatformXiaohongshu}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != StatusDraftReady || adapter.commitCalls.Load() != 0 {
		t.Fatalf("prepare was not read-only: status=%s calls=%d", prepared.Status, adapter.commitCalls.Load())
	}
	committed, err := service.Commit(context.Background(), CommitRequest{PublicationID: prepared.ID, Fingerprint: candidate.Fingerprint, Confirm: true})
	if err != nil || committed.Status != StatusSubmitted || adapter.commitCalls.Load() != 1 {
		t.Fatalf("commit = status %s err %v calls %d", committed.Status, err, adapter.commitCalls.Load())
	}
	if _, err := service.Commit(context.Background(), CommitRequest{PublicationID: prepared.ID, Fingerprint: candidate.Fingerprint, Confirm: true}); !errors.Is(err, ErrNeedsReconcile) {
		t.Fatalf("second commit err = %v, want reconcile gate", err)
	}
}

func TestCoordinationRejectsChangedCopyBeforePrepare(t *testing.T) {
	// The candidate fingerprint is derived from full copy content, so a changed
	// package invalidates Prepare even when all paths and summaries remain the same.
	dir := t.TempDir()
	videoPath, coverPath, packagePath := filepath.Join(dir, "v.mp4"), filepath.Join(dir, "c.jpg"), filepath.Join(dir, "publish_package.json")
	_ = os.WriteFile(videoPath, []byte("v"), 0o600)
	_ = os.WriteFile(coverPath, []byte("c"), 0o600)
	project := domain.VideoProject{Variants: []domain.Variant{{ID: "v1", Title: "before title", Caption: "before"}}}
	raw, _ := json.Marshal(project)
	_ = os.WriteFile(packagePath, raw, 0o600)
	run := domain.Run{ID: "run-2", Status: domain.RunStatusMediaReady, Request: domain.DistributionRequest{Platform: domain.PlatformDouyin, Atom: &domain.ContentAtom{Revision: 1}}, Artifacts: []domain.Artifact{{Kind: "package", Path: packagePath}, {ID: "rendered-video", Kind: "video", Path: videoPath}, {ID: "cover", Kind: "cover", Path: coverPath}}}
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	orch, _ := newTestOrchestrator(t, now, &fakeAdapter{platform: PlatformDouyin, configured: true})
	service, _ := NewService(coordinationRunReader{run: run}, orch, CoordinationOptions{Clock: func() time.Time { return now }})
	candidate, err := service.Candidate(context.Background(), CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: PlatformDouyin})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(packagePath, []byte(`{"variants":[{"id":"v1","title":"after title","caption":"after"} ]}`), 0o600)
	_, err = service.Prepare(context.Background(), PrepareCandidateRequest{CandidateRequest: candidate.Request, AuthorizationID: "unknown", Fingerprint: candidate.Fingerprint, PolicyVersion: "v1", Selected: []Platform{PlatformDouyin}})
	if !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("changed copy error = %v, want ErrCandidateChanged", err)
	}
}
