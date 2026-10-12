package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

type fakeMediaWorker struct {
	spec    mediaworker.ProductionSpec
	blocker bool
}

type retryOnceMediaWorker struct {
	base  fakeMediaWorker
	calls int
}

func (w *retryOnceMediaWorker) Validate(ctx context.Context, spec mediaworker.ProductionSpec, outputDir string) (mediaworker.Result, error) {
	return w.base.Validate(ctx, spec, outputDir)
}

func (w *retryOnceMediaWorker) Run(ctx context.Context, spec mediaworker.ProductionSpec, outputDir string) (mediaworker.Result, error) {
	w.calls++
	if w.calls == 1 {
		blocker := mediaworker.CapabilityBlocker{Capability: "tts", Message: "temporary voice provider failure"}
		return mediaworker.Result{Blockers: []mediaworker.CapabilityBlocker{blocker}}, &mediaworker.CapabilityBlockerError{Blocker: blocker}
	}
	return w.base.Run(ctx, spec, outputDir)
}

func (f *fakeMediaWorker) Validate(context.Context, mediaworker.ProductionSpec, string) (mediaworker.Result, error) {
	return mediaworker.Result{Valid: true}, nil
}

func (f *fakeMediaWorker) Run(_ context.Context, spec mediaworker.ProductionSpec, outputDir string) (mediaworker.Result, error) {
	f.spec = spec
	if f.blocker {
		blocker := mediaworker.CapabilityBlocker{Capability: "tts", Message: "voice provider is not configured"}
		return mediaworker.Result{Blockers: []mediaworker.CapabilityBlocker{blocker}}, &mediaworker.CapabilityBlockerError{Blocker: blocker}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return mediaworker.Result{}, err
	}
	path := filepath.Join(outputDir, "voiceover.wav")
	content := []byte("fake-media")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return mediaworker.Result{}, err
	}
	manifestPath := filepath.Join(outputDir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(`{"schemaVersion":"1.0","kind":"artifact-manifest"}`), 0o644); err != nil {
		return mediaworker.Result{}, err
	}
	digest := sha256.Sum256(content)
	return mediaworker.Result{
		OK: true, Valid: true, JobID: spec.JobID, OutputDir: outputDir,
		Manifest: &mediaworker.ArtifactManifest{
			SchemaVersion: mediaworker.ArtifactManifestVersion, Kind: "media", JobID: spec.JobID,
			ProductionSpecVersion: mediaworker.ProductionSpecVersion,
			Artifacts:             []mediaworker.Artifact{{ID: "media_voiceover", Kind: "voiceover-audio", Path: path, RelativePath: "voiceover.wav", Bytes: int64(len(content)), SHA256: fmt.Sprintf("%x", digest), MediaType: "audio/wav"}},
		},
		ArtifactManifestPath: manifestPath,
	}, nil
}

func testStore(t *testing.T) *runstore.Store {
	t.Helper()
	store, err := runstore.Open(t.TempDir() + "/runs.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func offeringRequest(key string) domain.DistributionRequest {
	return domain.DistributionRequest{
		IdempotencyKey: key,
		Platform:       domain.PlatformXiaohongshu,
		Offering: &domain.OfferingInput{
			Kind:  domain.OfferingKindProduct,
			Title: "桌面空气净化器",
			Name:  "桌面空气净化器",
			ValuePoints: []string{
				"低噪",
				"可拆洗",
			},
		},
	}
}

func researchAtom() *domain.ContentAtom {
	return &domain.ContentAtom{
		SchemaVersion: domain.ContentAtomSchemaVersion,
		Revision:      1,
		ID:            "atom_research_test",
		Kind:          domain.ContentAtomResearch,
		SourceType:    "research_topic",
		SourceID:      "topic_test",
		ThesisID:      "thesis_test",
		Audience:      "工程师",
		Hook:          "这个结论为什么值得现在看？",
		Claim:         "证据支持一个可复核的判断。",
		EvidenceIDs:   []string{"evidence_test"},
		Title:         "研究问题",
	}
}

func waitForRun(t *testing.T, service *Service, id string, want ...domain.RunStatus) domain.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(context.Background(), id)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		for _, status := range want {
			if run.Status == status {
				return run
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, _ := service.GetRun(context.Background(), id)
	t.Fatalf("run %s did not reach %v; last=%s checkpoint=%s error=%+v block=%+v", id, want, run.Status, run.Checkpoint, run.Error, run.CapabilityBlock)
	return domain.Run{}
}

func TestOfferingRunReachesPackageReadyWithArtifact(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })

	run, created, err := service.StartRun(context.Background(), offeringRequest("offering"))
	if err != nil || !created {
		t.Fatalf("start run: created=%t err=%v", created, err)
	}
	if run.Request.OriginalInputType != "legacy_offering" || run.Request.Atom == nil {
		t.Fatalf("legacy request was not normalized: %+v", run.Request)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
	if len(got.Artifacts) != 1 || got.Artifacts[0].Kind != "package" {
		t.Fatalf("package artifact missing: %+v", got.Artifacts)
	}
	if got.Artifacts[0].Path == "" || filepath.Base(got.Artifacts[0].Path) != "publish_package.json" {
		t.Fatalf("legacy exporter package missing: %+v", got.Artifacts[0])
	}
	b, err := os.ReadFile(got.Artifacts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	var project domain.VideoProject
	if err := json.Unmarshal(b, &project); err != nil {
		t.Fatalf("exported package is not a legacy project JSON: %v", err)
	}
	if len(project.Variants) != 10 {
		t.Fatalf("variant_count=0 did not preserve legacy default: got %d", len(project.Variants))
	}
}

func TestMediaEnabledRunReachesMediaReadyAndKeepsVideoTruthful(t *testing.T) {
	store := testStore(t)
	fake := &fakeMediaWorker{}
	service := NewServiceWithOptions(store, t.TempDir(), Options{MediaWorker: fake, MediaPaths: MediaRuntimePaths{Python: "python3"}})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("media-success")
	request.Media = &domain.MediaRequest{Enabled: true, Voice: "test-voice"}
	run, created, err := service.StartRun(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("start media run: created=%t err=%v", created, err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusMediaReady)
	wantArtifacts := map[string]string{run.ID + ":publish_package": "package", run.ID + ":production_spec": "production_spec", run.ID + ":media_voiceover": "voiceover-audio"}
	if len(got.Artifacts) != len(wantArtifacts) {
		t.Fatalf("want package, production spec, and audio artifacts, got %+v", got.Artifacts)
	}
	for _, artifact := range got.Artifacts {
		kind, exists := wantArtifacts[artifact.ID]
		if !exists || artifact.Kind != kind {
			t.Fatalf("unexpected artifact identity: %+v", artifact)
		}
		delete(wantArtifacts, artifact.ID)
	}
	if len(wantArtifacts) != 0 {
		t.Fatalf("missing artifacts: %+v", wantArtifacts)
	}
	if fake.spec.Timeline == nil || fake.spec.Timeline.PlaybackRate != 2 || fake.spec.Timeline.InputPlaybackRate != 1 {
		t.Fatalf("unexpected playback spec: %+v", fake.spec.Timeline)
	}
	if fake.spec.Paths == nil || fake.spec.Paths.Python != "python3" {
		t.Fatalf("python path was not passed to worker: %+v", fake.spec.Paths)
	}
	if fake.spec.Voice == nil || fake.spec.Voice.Voice != "test-voice" || fake.spec.Voice.Default["rate"] != "+0%" || fake.spec.Voice.Default["pitch"] != "+0Hz" || fake.spec.Voice.Default["volume"] != "+0%" {
		t.Fatalf("unexpected voice defaults: %+v", fake.spec.Voice)
	}
	if len(fake.spec.Segments) == 0 || fake.spec.Segments[0].Profile != "hook" {
		t.Fatalf("first segment must use hook profile: %+v", fake.spec.Segments)
	}
	if fake.spec.Voice.Profiles["hook"]["rate"] != "+0%" || fake.spec.Voice.Profiles["hook"]["pitch"] != "+4Hz" || fake.spec.Voice.Profiles["hook"]["volume"] != "+8%" {
		t.Fatalf("unexpected hook profile: %+v", fake.spec.Voice.Profiles["hook"])
	}
	if fake.spec.Voice.Profiles["normal"]["rate"] != "+0%" {
		t.Fatalf("normal profile must not apply timeline speed: %+v", fake.spec.Voice.Profiles["normal"])
	}
	if len(fake.spec.Segments) == 0 {
		t.Fatalf("media spec must contain a segment: %+v", fake.spec)
	}
	for _, artifact := range got.Artifacts {
		if artifact.Kind == "voiceover-audio" && artifact.Metadata["video_ready"] != false {
			t.Fatal("audio-only manifest must explicitly report video_ready=false")
		}
	}
	events, err := service.Events(context.Background(), run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.Type] = true
	}
	for _, eventType := range []string{"media.validating", "media.started", "media.artifact", "media.ready"} {
		if !seen[eventType] {
			t.Fatalf("missing %s event: %+v", eventType, events)
		}
	}
}

func TestMediaManifestIDsAreScopedAcrossRuns(t *testing.T) {
	store := testStore(t)
	fake := &fakeMediaWorker{}
	service := NewServiceWithOptions(store, t.TempDir(), Options{MediaWorker: fake, MediaPaths: MediaRuntimePaths{Python: "python3"}})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	var runs []domain.Run
	for _, key := range []string{"media-run-one", "media-run-two"} {
		request := offeringRequest(key)
		request.Media = &domain.MediaRequest{Enabled: true, Voice: "test-voice"}
		run, created, err := service.StartRun(context.Background(), request)
		if err != nil || !created {
			t.Fatalf("start %s: created=%t err=%v", key, created, err)
		}
		runs = append(runs, waitForRun(t, service, run.ID, domain.RunStatusMediaReady))
	}
	for _, run := range runs {
		found := false
		for _, artifact := range run.Artifacts {
			if artifact.Metadata["manifest_artifact_id"] == "media_voiceover" {
				found = true
				if artifact.ID != run.ID+":media_voiceover" {
					t.Fatalf("ledger artifact ID is not run scoped: %+v", artifact)
				}
			}
		}
		if !found {
			t.Fatalf("run %s lost its media manifest artifact: %+v", run.ID, run.Artifacts)
		}
	}
}

func TestRetryReplacesRunPackageArtifactWithCurrentDiskSHA(t *testing.T) {
	store := testStore(t)
	worker := &retryOnceMediaWorker{}
	service := NewServiceWithOptions(store, t.TempDir(), Options{MediaWorker: worker, MediaPaths: MediaRuntimePaths{Python: "python3"}})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("package-retry-current-hash")
	request.Media = &domain.MediaRequest{Enabled: true, Voice: "test-voice"}
	run, created, err := service.StartRun(context.Background(), request)
	if err != nil || !created {
		t.Fatalf("start run: created=%t err=%v", created, err)
	}
	first := waitForRun(t, service, run.ID, domain.RunStatusBlocked)
	var firstPackage domain.Artifact
	for _, artifact := range first.Artifacts {
		if artifact.Kind == "package" {
			firstPackage = artifact
		}
	}
	if firstPackage.ID != run.ID+":publish_package" {
		t.Fatalf("package ledger ID is not stable/run scoped: %+v", firstPackage)
	}
	if attempt, ok := firstPackage.Metadata["attempt"].(float64); !ok || attempt != 0 {
		t.Fatalf("first package attempt metadata = %#v, want 0", firstPackage.Metadata["attempt"])
	}
	if _, err := service.Retry(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	second := waitForRun(t, service, run.ID, domain.RunStatusMediaReady)
	var packages []domain.Artifact
	for _, artifact := range second.Artifacts {
		if artifact.Kind == "package" {
			packages = append(packages, artifact)
		}
	}
	if len(packages) != 1 {
		t.Fatalf("retry must leave one current package artifact, got %+v", packages)
	}
	packageBytes, err := os.ReadFile(packages[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	currentSHA := sha256.Sum256(packageBytes)
	if fmt.Sprintf("%x", currentSHA) != packages[0].SHA256 {
		t.Fatalf("package ledger SHA %s does not match current disk content %x", packages[0].SHA256, currentSHA)
	}
	if packages[0].ID != run.ID+":publish_package" {
		t.Fatalf("retried package ID changed: %+v", packages[0])
	}
	if attempt, ok := packages[0].Metadata["attempt"].(float64); !ok || attempt != 1 {
		t.Fatalf("retried package attempt metadata = %#v, want 1", packages[0].Metadata["attempt"])
	}
	for _, artifact := range second.Artifacts {
		if artifact.Metadata["manifest_artifact_id"] != "media_voiceover" {
			continue
		}
		if attempt, ok := artifact.Metadata["attempt"].(float64); !ok || attempt != 1 {
			t.Fatalf("retried media artifact attempt metadata = %#v, want 1", artifact.Metadata["attempt"])
		}
		return
	}
	t.Fatal("retried manifest artifact missing")
}

func TestRenderRequiresRemotionCWD(t *testing.T) {
	store := testStore(t)
	fake := &fakeMediaWorker{}
	service := NewServiceWithOptions(store, t.TempDir(), Options{MediaWorker: fake})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("render-missing-cwd")
	request.Media = &domain.MediaRequest{Enabled: true, Render: true}
	run, _, err := service.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusBlocked)
	if got.CapabilityBlock == nil || got.CapabilityBlock.Capability != "remotion" {
		t.Fatalf("unexpected render blocker: %+v", got.CapabilityBlock)
	}
}

func TestBuildProductionSpecUsesTimelineFor2xOnly(t *testing.T) {
	project := domain.VideoProject{
		Name:     "demo",
		Offering: domain.OfferingInput{Name: "demo", Description: "fallback"},
		Brief:    &domain.PublishingBrief{Storyboard: domain.Storyboard{Shots: []domain.Shot{{Voiceover: "hook"}, {Voiceover: "normal"}}}},
	}
	spec := buildProductionSpec("run", project, domain.MediaRequest{Render: true}, t.TempDir(), MediaRuntimePaths{Python: "python3", Remotion: "node.exe", RemotionCLI: "remotion-cli.js", RemotionCWD: "remotion"})
	if spec.Timeline == nil || spec.Timeline.PlaybackRate != 2 || spec.Timeline.InputPlaybackRate != 1 {
		t.Fatalf("unexpected 2x timeline: %+v", spec.Timeline)
	}
	openMontageSpec := buildProductionSpec("run-local-render", project, domain.MediaRequest{Render: true, OpenMontageRegistry: true}, t.TempDir(), MediaRuntimePaths{})
	if openMontageSpec.Timeline.PlaybackRate != 2 {
		t.Fatalf("registry render must preserve requested default 2x playback rate: %+v", openMontageSpec.Timeline)
	}
	if spec.Voice == nil || spec.Voice.Default["rate"] != "+0%" || spec.Voice.Profiles["hook"]["rate"] != "+0%" || spec.Voice.Profiles["normal"]["rate"] != "+0%" {
		t.Fatalf("voice rate must stay neutral: %+v", spec.Voice)
	}
	if len(spec.Segments) != 2 || spec.Segments[0].Profile != "hook" || spec.Segments[1].Profile != "normal" {
		t.Fatalf("unexpected segment profiles: %+v", spec.Segments)
	}
	if spec.Render == nil || !spec.Render.Enabled || spec.Render.CWD != "remotion" {
		t.Fatalf("remotion cwd was not passed to render spec: %+v", spec.Render)
	}
	if spec.Render.Entry != "src/index.tsx" || spec.Render.Composition != "Explainer" {
		t.Fatalf("unexpected default remotion entry/composition: %+v", spec.Render)
	}
	if spec.Paths == nil || len(spec.Paths.RemotionArgsPrefix) != 1 || spec.Paths.RemotionArgsPrefix[0] != "remotion-cli.js" {
		t.Fatalf("remotion CLI prefix was not passed: %+v", spec.Paths)
	}
	withoutRender := buildProductionSpec("run-no-render", project, domain.MediaRequest{}, t.TempDir(), MediaRuntimePaths{})
	if withoutRender.Render == nil || withoutRender.Render.Entry != "" || withoutRender.Render.Composition != "" {
		t.Fatalf("render defaults must not apply when render is disabled: %+v", withoutRender.Render)
	}
	if withoutRender.Paths == nil || len(withoutRender.Paths.RemotionArgsPrefix) != 0 {
		t.Fatalf("empty remotion CLI must not produce a prefix: %+v", withoutRender.Paths)
	}
	overridden := buildProductionSpec("run-custom-render", project, domain.MediaRequest{Render: true}, t.TempDir(), MediaRuntimePaths{RemotionCWD: "remotion", RemotionEntry: "custom/index.tsx", RemotionComposition: "CustomExplainer"})
	if overridden.Render.Entry != "custom/index.tsx" || overridden.Render.Composition != "CustomExplainer" {
		t.Fatalf("custom remotion entry/composition were not passed: %+v", overridden.Render)
	}
}

func TestMediaEnabledWithoutWorkerIsBlocked(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("media-missing-worker")
	request.Media = &domain.MediaRequest{Enabled: true}
	run, _, err := service.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusBlocked)
	if got.CapabilityBlock == nil || got.CapabilityBlock.Capability != "media_worker" {
		t.Fatalf("unexpected block: %+v", got.CapabilityBlock)
	}
}

func TestMediaWorkerBlockerIsPassedThrough(t *testing.T) {
	store := testStore(t)
	fake := &fakeMediaWorker{blocker: true}
	service := NewServiceWithOptions(store, t.TempDir(), Options{MediaWorker: fake})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("media-blocker")
	request.Media = &domain.MediaRequest{Enabled: true}
	run, _, err := service.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusBlocked)
	if got.CapabilityBlock == nil || got.CapabilityBlock.Capability != "tts" || got.CapabilityBlock.Message != "voice provider is not configured" {
		t.Fatalf("worker blocker was not passed through: %+v", got.CapabilityBlock)
	}
}

func TestContentAtomValidationRejectsNearlyEmptyAtom(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	_, _, err := service.StartRun(context.Background(), domain.DistributionRequest{
		IdempotencyKey: "invalid-atom",
		Platform:       domain.PlatformXiaohongshu,
		Atom:           &domain.ContentAtom{Kind: domain.ContentAtomOffering, Title: "标题"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid atom error=%v, want ErrInvalidRequest", err)
	}
}

func TestResearchAndJudgmentAtomsRequireThesisEvidence(t *testing.T) {
	for _, kind := range []domain.ContentAtomKind{domain.ContentAtomResearch, domain.ContentAtomJudgment} {
		atom := *researchAtom()
		atom.Kind = kind
		atom.ThesisID = ""
		if err := atom.Validate(); err == nil {
			t.Fatalf("%s atom without thesis was accepted", kind)
		}
	}
}

func TestResearchAndJudgmentAtomsReachPackageReady(t *testing.T) {
	for _, platform := range []domain.Platform{domain.PlatformDouyin, domain.PlatformXiaohongshu, domain.PlatformBilibili} {
		for _, kind := range []domain.ContentAtomKind{domain.ContentAtomResearch, domain.ContentAtomJudgment} {
			t.Run(string(platform)+"/"+string(kind), func(t *testing.T) {
				store := testStore(t)
				service := NewService(store, t.TempDir())
				if err := service.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = service.Stop(context.Background()) })
				atom := *researchAtom()
				atom.Kind = kind
				atom.ID = "atom_" + string(platform) + "_" + string(kind)
				run, created, err := service.StartRun(context.Background(), domain.DistributionRequest{
					IdempotencyKey: atom.ID,
					Platform:       platform,
					Atom:           &atom,
				})
				if err != nil || !created {
					t.Fatalf("start content atom run: created=%t err=%v", created, err)
				}
				got := waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
				if got.CapabilityBlock != nil {
					t.Fatalf("content atom was blocked: %+v", got.CapabilityBlock)
				}
				if len(got.Artifacts) != 1 {
					t.Fatalf("package artifact missing: %+v", got.Artifacts)
				}
				b, err := os.ReadFile(got.Artifacts[0].Path)
				if err != nil {
					t.Fatal(err)
				}
				var project domain.VideoProject
				if err := json.Unmarshal(b, &project); err != nil {
					t.Fatal(err)
				}
				if project.Offering.Kind != domain.OfferingKindCapability || len(project.Offering.SellingPoints) != 0 {
					t.Fatalf("atom was treated as a product: %+v", project.Offering)
				}
				if len(project.Offering.ProofPoints) != len(atom.EvidenceIDs) || project.Template != "bilibili_knowledge_story" && platform == domain.PlatformBilibili {
					t.Fatalf("atom mapping or Bilibili template was not preserved: offering=%+v template=%q", project.Offering, project.Template)
				}
				if platform == domain.PlatformBilibili {
					if project.Brief == nil || len(project.Brief.TitleOptions) < 5 || len(project.Brief.Tags) == 0 || len(project.Brief.Tags) > 5 {
						t.Fatalf("Bilibili brief is incomplete: %+v", project.Brief)
					}
					for _, title := range project.Brief.TitleOptions {
						if len([]rune(title)) > 80 {
							t.Fatalf("Bilibili title exceeds 80 runes: %q", title)
						}
					}
					if len([]rune(project.Brief.Caption)) > 2000 {
						t.Fatalf("Bilibili caption exceeds 2000 runes")
					}
					for _, tag := range project.Brief.Tags {
						if len([]rune(tag)) > 20 || tag == "" {
							t.Fatalf("Bilibili tag is outside the compact constraint: %q", tag)
						}
					}
				}
			})
		}
	}
}

func TestServiceConcurrentIdempotencyCreatesOneRun(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	const workers = 12
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
			run, created, err := service.StartRun(context.Background(), offeringRequest("service-same-key"))
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
			t.Fatalf("concurrent service create: %v", result.err)
		}
		if result.created {
			createdCount++
		}
		if runID == "" {
			runID = result.run.ID
		} else if result.run.ID != runID {
			t.Fatalf("multiple run ids: %q and %q", runID, result.run.ID)
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
}

func TestBilibiliTemplateAndWechatNewRunIsDisabled(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	bili, created, err := service.StartRun(context.Background(), domain.DistributionRequest{
		IdempotencyKey: "bili",
		Platform:       domain.PlatformBilibili,
		Offering:       &domain.OfferingInput{Title: "视频"},
	})
	if err != nil || !created {
		t.Fatalf("start Bili run: created=%t err=%v", created, err)
	}
	got := waitForRun(t, service, bili.ID, domain.RunStatusPackageReady)
	if got.CapabilityBlock != nil || got.Request.Platform != domain.PlatformBilibili {
		t.Fatalf("unexpected Bili result: status=%s block=%+v", got.Status, got.CapabilityBlock)
	}
	_, _, err = service.StartRun(context.Background(), domain.DistributionRequest{
		IdempotencyKey: "wechat",
		Platform:       domain.PlatformWechatChannels,
		Offering:       &domain.OfferingInput{Title: "视频号内容"},
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Wechat error=%v, want ErrInvalidRequest", err)
	}
}

func TestRenderedVideoArtifactRecognitionIsExplicit(t *testing.T) {
	tests := []struct {
		name string
		item mediaworker.Artifact
		want bool
	}{
		{name: "worker manifest shape", item: mediaworker.Artifact{ID: "rendered-video", Kind: "video"}, want: true},
		{name: "legacy explicit kind", item: mediaworker.Artifact{ID: "rendered-video", Kind: "rendered-video"}, want: true},
		{name: "generic video is not render", item: mediaworker.Artifact{ID: "preview", Kind: "video"}, want: false},
		{name: "wrong kind", item: mediaworker.Artifact{ID: "rendered-video", Kind: "image"}, want: false},
		{name: "audio artifact", item: mediaworker.Artifact{ID: "voiceover", Kind: "voiceover-audio"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isRenderedVideoArtifact(test.item); got != test.want {
				t.Fatalf("isRenderedVideoArtifact(%+v)=%t, want %t", test.item, got, test.want)
			}
		})
	}
}

func TestStartStopRestartLifecycle(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	run, _, err := service.StartRun(context.Background(), offeringRequest("lifecycle"))
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseReaperRecoversRunAfterStartupLeaseExpires(t *testing.T) {
	store := testStore(t)
	run, _, err := store.CreateRun(context.Background(), offeringRequest("delayed-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := store.AcquireLease(context.Background(), run.ID, "crashed-worker", 35*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunFenced(context.Background(), run.ID, oldLease, domain.RunStatusRunning, "started", nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	service := NewServiceWithLeaseReaperInterval(store, t.TempDir(), 5*time.Millisecond)
	service.executeRun = func(ctx context.Context, run domain.Run, lease runstore.Lease) error {
		return store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusPackageReady, "recovered", nil, nil, map[string]any{"recovered": true})
	}
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	got := waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
	if got.Checkpoint != "recovered" || got.Attempt != 0 {
		t.Fatalf("expired startup lease was not recovered correctly: %+v", got)
	}
}

func TestCancelResumeRetryTransitions(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	run, _, err := store.CreateRun(context.Background(), domain.DistributionRequest{
		IdempotencyKey: "cancel-flow",
		Platform:       domain.PlatformXiaohongshu,
		Atom: func() *domain.ContentAtom {
			atom := researchAtom()
			atom.ID = "atom_research_cancel_flow"
			atom.Priority = 1.5
			return atom
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(context.Background(), run.ID)
	if err != nil || cancelled.Status != domain.RunStatusCancelled {
		t.Fatalf("cancel: run=%+v err=%v", cancelled, err)
	}
	resumed, err := service.Resume(context.Background(), run.ID)
	if err != nil || resumed.Status != domain.RunStatusQueued {
		t.Fatalf("resume: run=%+v err=%v", resumed, err)
	}
	ready := waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
	if ready.Status != domain.RunStatusPackageReady || ready.CapabilityBlock != nil {
		t.Fatalf("resumed content atom did not reach package ready: %+v", ready)
	}
	retried, err := service.Retry(context.Background(), run.ID)
	if err != nil || retried.ID != run.ID || retried.Attempt != 0 || retried.Status != domain.RunStatusPackageReady {
		t.Fatalf("retry terminal run: run=%+v err=%v", retried, err)
	}
	final := waitForRun(t, service, run.ID, domain.RunStatusPackageReady)
	if final.Attempt != 0 || final.CapabilityBlock != nil {
		t.Fatalf("retry did not preserve incremented attempt: %+v", final)
	}
}

func TestShutdownRequeuesNonUserCancelledRun(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	service.executeRun = func(ctx context.Context, _ domain.Run, _ runstore.Lease) error {
		<-ctx.Done()
		return ctx.Err()
	}
	run, _, err := store.CreateRun(context.Background(), offeringRequest("shutdown"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.process(ctx, run.ID)
		close(done)
	}()
	waitForRun(t, service, run.ID, domain.RunStatusRunning)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not stop")
	}
	got, err := service.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RunStatusQueued {
		t.Fatalf("shutdown changed run to %s, want queued", got.Status)
	}
}

func TestUserCancelIsNotRequeuedAfterWorkerStops(t *testing.T) {
	store := testStore(t)
	service := NewService(store, t.TempDir())
	service.executeRun = func(ctx context.Context, _ domain.Run, _ runstore.Lease) error {
		<-ctx.Done()
		return ctx.Err()
	}
	run, _, err := store.CreateRun(context.Background(), offeringRequest("user-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.process(ctx, run.ID)
		close(done)
	}()
	waitForRun(t, service, run.ID, domain.RunStatusRunning)
	if _, err := service.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not stop")
	}
	got, err := service.GetRun(context.Background(), run.ID)
	if err != nil || got.Status != domain.RunStatusCancelled {
		t.Fatalf("user cancelled run became %s err=%v", got.Status, err)
	}
}
