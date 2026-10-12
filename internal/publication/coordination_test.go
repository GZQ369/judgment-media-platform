package publication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestCandidateArtifactsPreferCurrentLedgerBatchRegardlessOfOrder(t *testing.T) {
	oldVideo := domain.Artifact{ID: "old-render", Kind: "video", Path: "old.mp4", Metadata: map[string]any{"manifest_artifact_id": "rendered-video"}}
	oldCover := domain.Artifact{ID: "old-cover", Kind: "cover", Path: "old-cover.png", Metadata: map[string]any{"manifest_artifact_id": "cover-portrait-3x4"}}
	currentVideo := acceptedManifestArtifact("run-retry", "rendered-video", "video", "current.mp4")
	currentCover := acceptedManifestArtifact("run-retry", "cover-portrait-3x4", "cover", "current-cover.png")

	for _, artifacts := range [][]domain.Artifact{
		{oldVideo, oldCover, currentVideo, currentCover},
		{currentCover, currentVideo, oldCover, oldVideo},
	} {
		run := domain.Run{ID: "run-retry", Artifacts: artifacts}
		_, video, covers, err := candidateArtifacts(run)
		if err != nil {
			t.Fatalf("candidateArtifacts() error = %v", err)
		}
		if video.Path != currentVideo.Path || covers["portrait_3x4"].Path != currentCover.Path {
			t.Fatalf("selected video=%q cover=%q, want current batch", video.Path, covers["portrait_3x4"].Path)
		}
	}
}

func TestCandidateArtifactsNeverMixCurrentVideoWithLegacyCover(t *testing.T) {
	run := domain.Run{ID: "run-retry", Artifacts: []domain.Artifact{
		acceptedManifestArtifact("run-retry", "rendered-video", "video", "current.mp4"),
		{ID: "old-cover", Kind: "cover", Path: "old-cover.png", Metadata: map[string]any{"manifest_artifact_id": "cover-portrait-3x4"}},
	}}
	if _, _, _, err := candidateArtifacts(run); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("candidateArtifacts() error = %v, want missing current-batch cover", err)
	}
}

func TestCandidateArtifactsRetryDropsCoverRolesAbsentFromCurrentAttempt(t *testing.T) {
	run := domain.Run{ID: "run-retry", Attempt: 2, Artifacts: []domain.Artifact{
		acceptedManifestArtifactAtAttempt("run-retry", "rendered-video", "video", "attempt-1.mp4", 1),
		acceptedManifestArtifactAtAttempt("run-retry", "cover-portrait-3x4", "cover", "attempt-1-portrait.png", 1),
		acceptedManifestArtifactAtAttempt("run-retry", "cover-landscape-4x3", "cover", "attempt-1-landscape.png", 1),
		acceptedManifestArtifactAtAttempt("run-retry", "rendered-video", "video", "attempt-2.mp4", 2),
		acceptedManifestArtifactAtAttempt("run-retry", "cover-portrait-3x4", "cover", "attempt-2-portrait.png", 2),
	}}
	_, video, covers, err := candidateArtifacts(run)
	if err != nil {
		t.Fatalf("candidateArtifacts() error = %v", err)
	}
	if video.Path != "attempt-2.mp4" || len(covers) != 1 || covers["portrait_3x4"].Path != "attempt-2-portrait.png" {
		t.Fatalf("selected attempt 2 video=%q covers=%+v", video.Path, covers)
	}
}

func TestCandidateArtifactsRejectMissingOrAmbiguousCurrentLedgerEntries(t *testing.T) {
	oldVideo := domain.Artifact{ID: "old-render", Kind: "video", Path: "old.mp4", Metadata: map[string]any{"manifest_artifact_id": "rendered-video"}}
	currentCover := acceptedManifestArtifact("run-retry", "cover-portrait-3x4", "cover", "current-cover.png")
	if _, _, _, err := candidateArtifacts(domain.Run{ID: "run-retry", Artifacts: []domain.Artifact{oldVideo, currentCover}}); !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("missing current video error = %v, want ErrArtifactMissing", err)
	}

	currentVideo := acceptedManifestArtifact("run-retry", "rendered-video", "video", "current.mp4")
	duplicate := currentVideo
	duplicate.Path = "other-current.mp4"
	if _, _, _, err := candidateArtifacts(domain.Run{ID: "run-retry", Artifacts: []domain.Artifact{currentVideo, duplicate, currentCover}}); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("duplicate current video error = %v, want ErrCandidateChanged", err)
	}
}

func TestCandidateArtifactsKeepsUnambiguousLegacyPath(t *testing.T) {
	legacyVideo := domain.Artifact{ID: "rendered-video", Kind: "video", Path: "legacy.mp4"}
	legacyCover := domain.Artifact{ID: "cover", Kind: "cover", Path: "legacy-cover.png"}
	_, video, covers, err := candidateArtifacts(domain.Run{ID: "legacy-run", Artifacts: []domain.Artifact{legacyVideo, legacyCover}})
	if err != nil {
		t.Fatalf("candidateArtifacts() error = %v", err)
	}
	if video.Path != legacyVideo.Path || covers["general"].Path != legacyCover.Path {
		t.Fatalf("legacy selection video=%q covers=%+v", video.Path, covers)
	}

	duplicateVideo := legacyVideo
	duplicateVideo.ID = "another-render"
	duplicateVideo.Metadata = map[string]any{"manifest_artifact_id": "rendered-video"}
	if _, _, _, err := candidateArtifacts(domain.Run{ID: "legacy-run", Artifacts: []domain.Artifact{legacyVideo, duplicateVideo, legacyCover}}); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("ambiguous legacy video error = %v, want ErrCandidateChanged", err)
	}
}

func acceptedManifestArtifact(runID, manifestID, kind, path string) domain.Artifact {
	return acceptedManifestArtifactAtAttempt(runID, manifestID, kind, path, 0)
}

func acceptedManifestArtifactAtAttempt(runID, manifestID, kind, path string, attempt int) domain.Artifact {
	sum := sha256.Sum256([]byte(path))
	return domain.Artifact{ID: runID + ":" + manifestID, Kind: kind, Path: path, SHA256: hex.EncodeToString(sum[:]), Metadata: map[string]any{"manifest_artifact_id": manifestID, "attempt": attempt}}
}

func TestCandidateRejectsTamperedAcceptedPackageBeforeAndAfterAuthorization(t *testing.T) {
	run, packagePath, original := acceptedPackageRun(t)
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	orch, _ := newTestOrchestrator(t, now, &fakeAdapter{platform: PlatformDouyin, configured: true})
	service, err := NewService(coordinationRunReader{run: run}, orch, CoordinationOptions{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	req := CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: PlatformDouyin}
	candidate, err := service.Candidate(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	writePackage := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(packagePath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePackage(append(append([]byte(nil), original...), '\n'))
	if _, err := service.Candidate(context.Background(), req); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("tampered candidate error = %v, want ErrCandidateChanged", err)
	}
	authorize := AuthorizeCandidateRequest{CandidateRequest: req, Confirm: true, PolicyVersion: "v1", Selected: []Platform{PlatformDouyin}, ExpiresAt: now.Add(5 * time.Minute)}
	if _, err := service.Authorize(context.Background(), authorize); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("tampered authorize error = %v, want ErrCandidateChanged", err)
	}
	writePackage(original)
	auth, err := service.Authorize(context.Background(), authorize)
	if err != nil {
		t.Fatal(err)
	}
	writePackage(append(append([]byte(nil), original...), ' '))
	_, err = service.Prepare(context.Background(), PrepareCandidateRequest{CandidateRequest: req, AuthorizationID: auth.ID, Fingerprint: candidate.Fingerprint, PolicyVersion: "v1", Selected: []Platform{PlatformDouyin}})
	if !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("tampered prepare error = %v, want ErrCandidateChanged", err)
	}
}

func TestCandidateUsesCurrentAttemptPackageWithOlderPackagePresent(t *testing.T) {
	run, currentPath, _ := acceptedPackageRun(t)
	run.Attempt = 1
	oldPath := filepath.Join(t.TempDir(), "old_publish_package.json")
	oldRaw := []byte(`{"variants":[{"id":"v1","title":"old title","caption":"old copy"}]}`)
	if err := os.WriteFile(oldPath, oldRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256(oldRaw)
	oldPackage := domain.Artifact{ID: "old:publish_package", Kind: "package", Path: oldPath, SHA256: hex.EncodeToString(oldDigest[:]), Metadata: map[string]any{"manifest_artifact_id": "publish_package", "attempt": 0}}
	currentPackage := run.Artifacts[0]
	currentPackage.Metadata["attempt"] = 1
	run.Artifacts[1].Metadata["attempt"] = 1
	run.Artifacts[2].Metadata["attempt"] = 1
	mediaArtifacts := append([]domain.Artifact(nil), run.Artifacts[1:]...)
	now := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	for _, packages := range [][]domain.Artifact{{oldPackage, currentPackage}, {currentPackage, oldPackage}} {
		run.Artifacts = append(append([]domain.Artifact{}, packages...), mediaArtifacts...)
		selected, _, _, err := candidateArtifacts(run)
		if err != nil || selected.Path != currentPath {
			t.Fatalf("selected package = %q, error = %v, want %q", selected.Path, err, currentPath)
		}
		orch, _ := newTestOrchestrator(t, now, &fakeAdapter{platform: PlatformDouyin, configured: true})
		service, err := NewService(coordinationRunReader{run: run}, orch, CoordinationOptions{Clock: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		candidate, err := service.Candidate(context.Background(), CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: PlatformDouyin})
		if err != nil || candidate.Package.Title != "current title" {
			t.Fatalf("candidate title = %q, error = %v, want current title", candidate.Package.Title, err)
		}
	}
}

func TestSelectCandidatePackageDigestRequirements(t *testing.T) {
	canonical := domain.Artifact{ID: "run-1:publish_package", Kind: "package", Path: "package.json", Metadata: map[string]any{"manifest_artifact_id": "publish_package", "attempt": 0}}
	if _, err := selectCandidatePackage(domain.Run{ID: "run-1"}, []domain.Artifact{canonical}); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("canonical package without digest error = %v, want ErrCandidateChanged", err)
	}
	delete(canonical.Metadata, "attempt")
	if _, err := selectCandidatePackage(domain.Run{ID: "run-1"}, []domain.Artifact{canonical}); !errors.Is(err, ErrCandidateChanged) {
		t.Fatalf("canonical package without attempt or digest error = %v, want ErrCandidateChanged", err)
	}
	legacy := domain.Artifact{ID: "package", Kind: "package", Path: "package.json"}
	selected, err := selectCandidatePackage(domain.Run{ID: "run-1"}, []domain.Artifact{legacy})
	if err != nil || selected.Path != legacy.Path {
		t.Fatalf("unique legacy package = %+v, error = %v", selected, err)
	}
}

func acceptedPackageRun(t *testing.T) (domain.Run, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	packagePath := filepath.Join(dir, "publish_package.json")
	raw := []byte(`{"variants":[{"id":"v1","title":"current title","caption":"current copy"}]}`)
	if err := os.WriteFile(packagePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	videoPath := filepath.Join(dir, "rendered.mp4")
	coverPath := filepath.Join(dir, "cover.png")
	for path, content := range map[string]string{videoPath: "video", coverPath: "cover"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	packageDigest := sha256.Sum256(raw)
	videoDigest, _ := fileSHA(videoPath)
	coverDigest, _ := fileSHA(coverPath)
	run := domain.Run{ID: "run-package", Status: domain.RunStatusMediaReady, Request: domain.DistributionRequest{Platform: domain.PlatformDouyin}, Artifacts: []domain.Artifact{
		{ID: "run-package:publish_package", Kind: "package", Path: packagePath, SHA256: hex.EncodeToString(packageDigest[:]), Metadata: map[string]any{"manifest_artifact_id": "publish_package", "attempt": 0}},
		{ID: "run-package:rendered-video", Kind: "video", Path: videoPath, SHA256: videoDigest, Metadata: map[string]any{"manifest_artifact_id": "rendered-video", "attempt": 0}},
		{ID: "run-package:cover", Kind: "cover", Path: coverPath, SHA256: coverDigest, Metadata: map[string]any{"manifest_artifact_id": "cover", "attempt": 0}},
	}}
	return run, packagePath, raw
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
	videoDigest, _ := fileSHA(videoPath)
	coverDigest, _ := fileSHA(coverPath)
	run := domain.Run{ID: "run-1", Status: domain.RunStatusMediaReady, Request: domain.DistributionRequest{Platform: domain.PlatformXiaohongshu, Targets: []domain.DistributionTarget{{Platform: domain.PlatformXiaohongshu, AccountID: "acct"}}, Atom: &domain.ContentAtom{Revision: 3}}, Artifacts: []domain.Artifact{{ID: "package", Kind: "package", Path: packagePath}, {ID: "run-1:rendered-video", Kind: "video", Path: videoPath, SHA256: videoDigest, Metadata: map[string]any{"manifest_artifact_id": "rendered-video"}}, {ID: "run-1:cover", Kind: "cover", Path: coverPath, SHA256: coverDigest, Metadata: map[string]any{"manifest_artifact_id": "cover"}}}}
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
