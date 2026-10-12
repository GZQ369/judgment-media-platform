package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/agentkit"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/exporter"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
	"github.com/GZQ369/judgment-media-platform/internal/tools"
	"github.com/GZQ369/judgment-media-platform/internal/workflow"
	"github.com/google/uuid"
)

var (
	ErrInvalidRequest        = errors.New("invalid distribution request")
	ErrServiceStopped        = errors.New("distribution service is stopped")
	ErrServiceStarting       = errors.New("distribution service is starting")
	ErrServiceStopping       = errors.New("distribution service is stopping")
	ErrTargetsNotImplemented = errors.New("multi-target distribution is not implemented")
)

const (
	leaseTTL                   = 30 * time.Second
	heartbeatInterval          = 10 * time.Second
	defaultLeaseReaperInterval = 1 * time.Second
	defaultRemotionEntry       = "src/index.tsx"
	defaultRemotionComposition = "Explainer"
)

// Service owns asynchronous run execution independently of HTTP request contexts.
type Service struct {
	store               *runstore.Store
	dataDir             string
	mediaWorker         mediaworker.Worker
	openMontageRegistry mediaworker.Worker
	mediaPaths          MediaRuntimePaths
	queue               chan string
	owner               string
	executeRun          func(context.Context, domain.Run, runstore.Lease) error
	reaperInterval      time.Duration

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
	starting bool
}

// MediaRuntimePaths are copied into ProductionSpec without making the
// distribution service responsible for launching external processes.
type MediaRuntimePaths struct {
	Python              string
	FFmpeg              string
	FFprobe             string
	Remotion            string
	RemotionCLI         string
	RemotionCWD         string
	RemotionEntry       string
	RemotionComposition string
	EdgeTTSRoot         string
}

// Options supplies optional media execution dependencies.
type Options struct {
	MediaWorker         mediaworker.Worker
	OpenMontageRegistry mediaworker.Worker
	MediaPaths          MediaRuntimePaths
	ReaperInterval      time.Duration
}

// NewService creates a durable distribution facade. It does not start a worker.
func NewService(store *runstore.Store, dataDir string) *Service {
	return NewServiceWithOptions(store, dataDir, Options{})
}

// NewServiceWithLeaseReaperInterval is useful for hosts that need to tune
// recovery latency, and for deterministic tests of lease expiry.
func NewServiceWithLeaseReaperInterval(store *runstore.Store, dataDir string, interval time.Duration) *Service {
	return NewServiceWithOptions(store, dataDir, Options{ReaperInterval: interval})
}

// NewServiceWithLeaseReaperIntervalAndOptions combines test tuning with
// injected capabilities.
func NewServiceWithLeaseReaperIntervalAndOptions(store *runstore.Store, dataDir string, interval time.Duration, options Options) *Service {
	options.ReaperInterval = interval
	return NewServiceWithOptions(store, dataDir, options)
}

// NewServiceWithOptions creates a service with optional media worker support.
func NewServiceWithOptions(store *runstore.Store, dataDir string, options Options) *Service {
	if dataDir == "" {
		dataDir = "data"
	}
	interval := options.ReaperInterval
	if interval <= 0 {
		interval = defaultLeaseReaperInterval
	}
	service := &Service{
		store:               store,
		dataDir:             filepath.Clean(dataDir),
		mediaWorker:         options.MediaWorker,
		openMontageRegistry: options.OpenMontageRegistry,
		mediaPaths:          options.MediaPaths,
		queue:               make(chan string, 256),
		owner:               fmt.Sprintf("distribution-%d-%s", os.Getpid(), uuid.NewString()),
		cancels:             map[string]context.CancelFunc{},
		reaperInterval:      interval,
	}
	service.executeRun = service.execute
	return service
}

// Start starts the worker and requeues runs left behind by a previous process.
func (s *Service) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	if s.starting {
		s.mu.Unlock()
		return ErrServiceStarting
	}
	if s.done != nil {
		select {
		case <-s.done:
		default:
			s.mu.Unlock()
			return ErrServiceStopping
		}
	}
	s.starting = true
	s.mu.Unlock()

	if err := s.store.RecoverRunning(context.Background()); err != nil {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return err
	}
	ids, err := s.store.RunnableIDs(context.Background())
	if err != nil {
		s.mu.Lock()
		s.starting = false
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.done = make(chan struct{})
	ctx, done := s.ctx, s.done
	s.started = true
	s.starting = false
	s.mu.Unlock()
	go func() {
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			s.worker(ctx)
		}()
		go func() {
			defer workers.Done()
			s.leaseReaper(ctx)
		}()
		workers.Wait()
		close(done)
	}()
	for _, id := range ids {
		if err := enqueueWith(ctx, s.queue, id); err != nil {
			break
		}
	}
	return nil
}

// Stop cancels the worker and waits for the current run to stop.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.starting {
		s.mu.Unlock()
		return ErrServiceStarting
	}
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel := s.cancel
	done := s.done
	s.started = false
	s.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartRun validates and persists a run before enqueueing it. The caller context
// is used only for the durable create operation; execution is detached from it.
func (s *Service) StartRun(ctx context.Context, request domain.DistributionRequest) (domain.Run, bool, error) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return domain.Run{}, false, ErrServiceStopped
	}
	request, err := normalizeRequest(request)
	if err != nil {
		return domain.Run{}, false, err
	}
	run, created, err := s.store.CreateRun(ctx, request)
	if err != nil {
		return domain.Run{}, false, err
	}
	if created {
		if err := s.enqueue(run.ID); err != nil && !errors.Is(err, ErrServiceStopped) {
			return run, created, err
		}
	}
	return run, created, nil
}

// ListRuns returns durable runs newest first.
func (s *Service) ListRuns(ctx context.Context, limit int) ([]domain.Run, error) {
	return s.store.ListRuns(ctx, limit)
}

// GetRun returns a durable run detail.
func (s *Service) GetRun(ctx context.Context, id string) (domain.Run, error) {
	return s.store.GetRun(ctx, id)
}

// Events returns append-only events after eventID.
func (s *Service) Events(ctx context.Context, id string, eventID int64, limit int) ([]domain.RunEvent, error) {
	if _, err := s.store.GetRun(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListEvents(ctx, id, eventID, limit)
}

// Cancel requests cancellation. Repeating the request is idempotent.
func (s *Service) Cancel(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return domain.Run{}, err
	}
	if terminal(run.Status) {
		return run, nil
	}
	if err := s.store.CancelRun(ctx, id); err != nil {
		return domain.Run{}, err
	}
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.store.GetRun(ctx, id)
}

// Resume requeues a blocked, failed, or cancelled run without increasing attempt.
func (s *Service) Resume(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusRunning || run.Status == domain.RunStatusPackageReady || run.Status == domain.RunStatusMediaReady {
		return run, nil
	}
	if err := s.store.UpdateRun(ctx, id, domain.RunStatusQueued, "resume_requested", nil, nil, map[string]any{"operation": "resume"}); err != nil {
		return domain.Run{}, err
	}
	if err := s.enqueue(id); err != nil && !errors.Is(err, ErrServiceStopped) {
		return domain.Run{}, err
	}
	return s.store.GetRun(ctx, id)
}

// Retry requeues a run and increments its durable attempt counter.
func (s *Service) Retry(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunStatusQueued || run.Status == domain.RunStatusRunning {
		return run, nil
	}
	if run.Status == domain.RunStatusPackageReady || run.Status == domain.RunStatusMediaReady {
		return run, nil
	}
	if err := s.store.SetAttempt(ctx, id, run.Attempt+1, "retry_requested", "retry_requested", map[string]any{"attempt": run.Attempt + 1}); err != nil {
		return domain.Run{}, err
	}
	if err := s.store.UpdateRun(ctx, id, domain.RunStatusQueued, "retry_queued", nil, nil, map[string]any{"operation": "retry"}); err != nil {
		return domain.Run{}, err
	}
	if err := s.enqueue(id); err != nil && !errors.Is(err, ErrServiceStopped) {
		return domain.Run{}, err
	}
	return s.store.GetRun(ctx, id)
}

func (s *Service) enqueue(id string) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrServiceStopped
	}
	ctx := s.ctx
	s.mu.Unlock()
	return enqueueWith(ctx, s.queue, id)
}

func enqueueWith(ctx context.Context, queue chan string, id string) error {
	select {
	case queue <- id:
		return nil
	case <-ctx.Done():
		return ErrServiceStopped
	}
}

func (s *Service) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.queue:
			s.process(ctx, id)
		}
	}
}

// leaseReaper periodically requeues runs whose previous worker lease has
// expired. A ticker avoids busy polling and also handles a restart where the
// old lease is still live at startup.
func (s *Service) leaseReaper(ctx context.Context) {
	ticker := time.NewTicker(s.reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.RecoverRunning(ctx); err != nil {
				continue
			}
			ids, err := s.store.RunnableIDs(ctx)
			if err != nil {
				continue
			}
			for _, id := range ids {
				if err := enqueueWith(ctx, s.queue, id); err != nil {
					return
				}
			}
		}
	}
}

func (s *Service) heartbeat(ctx context.Context, runID string, lease runstore.Lease, cancel context.CancelFunc, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.RenewLease(context.Background(), runID, lease, leaseTTL); err != nil {
				cancel()
				return
			}
		}
	}
}

func (s *Service) process(ctx context.Context, id string) {
	lease, err := s.store.AcquireLease(ctx, id, s.owner, leaseTTL)
	if err != nil {
		return
	}
	defer s.store.ReleaseLeaseToken(context.Background(), id, lease)
	run, err := s.store.GetRun(ctx, id)
	if err != nil || terminal(run.Status) || run.Status != domain.RunStatusQueued {
		return
	}
	if err := s.store.UpdateRunFenced(ctx, id, lease, domain.RunStatusRunning, "started", nil, nil, map[string]any{"attempt": run.Attempt}); err != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go s.heartbeat(runCtx, id, lease, cancel, heartbeatDone)
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()
	err = s.executeRun(runCtx, run, lease)
	cancel()
	<-heartbeatDone
	s.mu.Lock()
	delete(s.cancels, id)
	s.mu.Unlock()
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		current, getErr := s.store.GetRun(context.Background(), id)
		if getErr == nil && current.Status == domain.RunStatusCancelled {
			return
		}
		if errors.Is(ctx.Err(), context.Canceled) && getErr == nil && !terminal(current.Status) {
			_ = s.store.UpdateRunFenced(context.Background(), id, lease, domain.RunStatusQueued, "shutdown_requeued", nil, nil, map[string]any{"reason": "service_shutdown"})
		}
		return
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		current, getErr := s.store.GetRun(context.Background(), id)
		if getErr == nil && !terminal(current.Status) {
			_ = s.store.UpdateRunFenced(context.Background(), id, lease, domain.RunStatusQueued, "shutdown_requeued", nil, nil, map[string]any{"reason": "service_shutdown"})
		}
		return
	}
	if err != nil {
		_ = s.store.UpdateRunFenced(context.Background(), id, lease, domain.RunStatusFailed, "failed", nil, &domain.RunError{Code: "RUN_FAILED", Message: err.Error(), Retryable: true}, map[string]any{"error": err.Error()})
	}
}

func (s *Service) execute(ctx context.Context, run domain.Run, lease runstore.Lease) error {
	request := run.Request
	if request.Atom == nil {
		return fmt.Errorf("%w: persisted request has no content atom", ErrInvalidRequest)
	}
	offering, block := offeringFromAtom(*request.Atom)
	if block != nil {
		return s.store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
	}
	platform := tools.NormalizePlatform(request.Platform)
	offering = tools.NormalizeOffering(offering, platform)
	project := domain.VideoProject{
		ID:              "run-project_" + run.ID,
		Name:            offering.Name + "发布包",
		Platform:        platform,
		Template:        tools.NormalizeTemplate(platform, request.Template),
		DurationSeconds: tools.ProfileForPlatform(platform).DefaultDuration,
		Status:          "draft",
		Offering:        offering,
		Product:         offering,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
	}
	if err := s.store.AppendEventFenced(ctx, run.ID, lease, "checkpoint", "workflow_started", map[string]any{"project_id": project.ID}); err != nil {
		return err
	}
	variantCount := request.VariantCount
	if variantCount == 0 {
		variantCount = 10
	}
	state, err := workflow.NewProductSeedRunner(agentkit.NewDeterministicRunner()).Run(ctx, project, workflow.RunOptions{VariantCount: variantCount})
	if err != nil {
		return err
	}
	project = state.ProjectWithOutputs()
	if err := s.store.AppendEventFenced(ctx, run.ID, lease, "checkpoint", "package_building", map[string]any{"project_id": project.ID}); err != nil {
		return err
	}
	path, digest, err := s.writePackage(run.ID, project)
	if err != nil {
		return err
	}
	artifact := domain.Artifact{ID: run.ID + ":publish_package", RunID: run.ID, Kind: "package", Path: path, SHA256: digest, Metadata: map[string]any{"manifest_artifact_id": "publish_package", "attempt": run.Attempt, "media": "not_connected", "publish": "not_connected", "project_id": project.ID}}
	if err := s.store.AddArtifactFenced(ctx, artifact, lease); err != nil {
		return err
	}
	if request.Media == nil || !request.Media.Enabled {
		return s.store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusPackageReady, "package_ready", nil, nil, map[string]any{"media": "not_connected", "publish": "not_connected", "artifact_path": path})
	}
	worker := s.mediaWorker
	if request.Media.OpenMontageRegistry {
		worker = s.openMontageRegistry
	}
	if worker == nil {
		block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: "media_worker", Message: "media generation was requested but no media worker is configured", Retryable: true}
		return s.store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusBlocked, "capability_block", block, nil, map[string]any{"capability": block.Capability})
	}
	if request.Media.HasTaskAssets() {
		if err := mediaworker.RequireTaskAssetWorker(worker); err != nil {
			return s.handleMediaError(ctx, run.ID, lease, err)
		}
	}
	if request.Media.OpenMontageRegistry && request.Media.Voice != "" {
		return s.handleMediaError(ctx, run.ID, lease, &mediaworker.CapabilityBlockerError{Blocker: mediaworker.CapabilityBlocker{Capability: "voice_selection", Message: "OpenMontage uses the configured Piper model and cannot execute media.voice."}})
	}
	if request.Media.Render && !request.Media.OpenMontageRegistry && strings.TrimSpace(s.mediaPaths.RemotionCWD) == "" {
		block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: "remotion", Message: "rendering was requested but MEDIA_WORKER_REMOTION_CWD is not configured", Retryable: true}
		return s.store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusBlocked, "capability_block", block, nil, map[string]any{"capability": block.Capability})
	}

	mediaDir := filepath.Join(s.dataDir, "runs", run.ID, "media")
	spec := buildProductionSpec(run.ID, project, *request.Media, mediaDir, s.mediaPaths)
	if err := bindProductionSpecAssets(s.dataDir, filepath.Dir(mediaDir), *request.Media, &spec); err != nil {
		return fmt.Errorf("bind media assets: %w", err)
	}
	specPath, err := mediaworker.PersistProductionSpec(filepath.Dir(mediaDir), spec)
	if err != nil {
		return err
	}
	inputArtifacts := []struct{ id, kind, path string }{{"production_spec", "production_spec", specPath}}
	if request.Media.HasTaskAssets() {
		inputArtifacts = append(inputArtifacts, struct{ id, kind, path string }{"input_assets", "input_asset_manifest", mediaworker.AssetSnapshotManifestPath(filepath.Dir(mediaDir))})
	}
	for _, input := range inputArtifacts {
		digest, err := fileSHA256(input.path)
		if err != nil {
			return err
		}
		info, err := os.Stat(input.path)
		if err != nil {
			return err
		}
		if err := s.store.AddArtifactFenced(ctx, domain.Artifact{ID: run.ID + ":" + input.id, RunID: run.ID, Kind: input.kind, Path: input.path, SHA256: digest, Metadata: map[string]any{"attempt": run.Attempt, "bytes": info.Size(), "media_type": "application/json"}}, lease); err != nil {
			return err
		}
	}
	if err := s.store.AppendEventFenced(ctx, run.ID, lease, "media.validating", "media_validating", map[string]any{"output_dir": mediaDir}); err != nil {
		return err
	}
	validation, err := worker.Validate(ctx, spec, mediaDir)
	if err != nil || len(validation.Blockers) > 0 {
		if err == nil && len(validation.Blockers) > 0 {
			err = &mediaworker.CapabilityBlockerError{Blocker: validation.Blockers[0], ExitCode: 0}
		}
		return s.handleMediaError(ctx, run.ID, lease, err)
	}
	if err := s.store.AppendEventFenced(ctx, run.ID, lease, "media.started", "media_started", map[string]any{"job_id": spec.JobID, "output_dir": mediaDir}); err != nil {
		return err
	}
	result, err := worker.Run(ctx, spec, mediaDir)
	if err != nil || len(result.Blockers) > 0 {
		if err == nil && len(result.Blockers) > 0 {
			err = &mediaworker.CapabilityBlockerError{Blocker: result.Blockers[0], ExitCode: 0}
		}
		return s.handleMediaError(ctx, run.ID, lease, err)
	}
	if result.Manifest == nil {
		return fmt.Errorf("media worker returned no artifact manifest")
	}
	if request.Media.OpenMontageRegistry && request.Media.Render {
		if err := validateProductionRender(ctx, result.Manifest, result.ArtifactManifestPath, mediaDir, s.mediaPaths.FFprobe); err != nil {
			return fmt.Errorf("production render acceptance failed: %w", err)
		}
	}
	videoReady := false
	for _, item := range result.Manifest.Artifacts {
		artifactPath := item.Path
		if !filepath.IsAbs(artifactPath) {
			artifactPath = filepath.Join(mediaDir, artifactPath)
		}
		info, err := os.Stat(artifactPath)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxProductionArtifactBytes {
			return fmt.Errorf("media artifact %s is not a non-empty regular file within the 2 GiB limit", item.ID)
		}
		actualSHA, err := fileSHA256(artifactPath)
		if err != nil {
			return err
		}
		videoArtifact := isRenderedVideoArtifact(item)
		if videoArtifact {
			videoReady = true
		}
		ledgerID := run.ID + ":" + item.ID
		artifact := domain.Artifact{
			ID: ledgerID, RunID: run.ID, Kind: item.Kind, Path: artifactPath, SHA256: actualSHA,
			Metadata: map[string]any{"manifest_artifact_id": item.ID, "attempt": run.Attempt, "relative_path": item.RelativePath, "bytes": info.Size(), "media_type": item.MediaType, "manifest_sha256": item.SHA256, "video_ready": videoArtifact, "duration_seconds": item.DurationSeconds, "width": item.Width, "height": item.Height, "sample_rate": item.SampleRate, "channels": item.Channels},
		}
		if err := s.store.AddArtifactFenced(ctx, artifact, lease); err != nil {
			return err
		}
		if err := s.store.AppendEventFenced(ctx, run.ID, lease, "media.artifact", "media_artifact", map[string]any{"artifact_id": ledgerID, "manifest_artifact_id": item.ID, "kind": item.Kind, "path": artifactPath, "sha256": actualSHA}); err != nil {
			return err
		}
	}
	if request.Media.OpenMontageRegistry && request.Media.Render && !videoReady {
		return fmt.Errorf("production render acceptance failed: manifest has no rendered-video artifact")
	}
	metadata := map[string]any{"media": "ready", "video_ready": videoReady, "artifact_manifest": result.ArtifactManifestPath}
	readyData := map[string]any{"artifact_count": len(result.Manifest.Artifacts), "video_ready": videoReady}
	if request.Media.OpenMontageRegistry {
		readyData["narration_generated"] = true
		readyData["renderer"] = "openmontage_registry"
		metadata["renderer"] = "openmontage_registry"
		metadata["production_mode"] = "asset_driven"
		metadata["narration_generated"] = true
	}
	if err := s.store.AppendEventFenced(ctx, run.ID, lease, "media.ready", "media_ready", readyData); err != nil {
		return err
	}
	return s.store.UpdateRunFenced(ctx, run.ID, lease, domain.RunStatusMediaReady, "media_ready", nil, nil, metadata)
}

// isRenderedVideoArtifact recognizes only the manifest's reserved rendered
// video entry. A generic kind=video is intentionally insufficient because
// preview clips and other video inputs are not the completed render output.
func isRenderedVideoArtifact(item mediaworker.Artifact) bool {
	return item.ID == "rendered-video" && (item.Kind == "video" || item.Kind == "rendered-video")
}

func (s *Service) handleMediaError(ctx context.Context, runID string, lease runstore.Lease, err error) error {
	var blocker *mediaworker.CapabilityBlockerError
	if errors.As(err, &blocker) {
		block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: blocker.Blocker.Capability, Message: blocker.Blocker.Message, Retryable: true}
		return s.store.UpdateRunFenced(ctx, runID, lease, domain.RunStatusBlocked, "capability_block", block, nil, map[string]any{"capability": block.Capability, "remediation": blocker.Blocker.Remediation})
	}
	return err
}

func buildProductionSpec(runID string, project domain.VideoProject, request domain.MediaRequest, mediaDir string, paths MediaRuntimePaths) mediaworker.ProductionSpec {
	rate := request.PlaybackRate
	if rate <= 0 {
		rate = 2
	}
	brief := project.Brief
	var remotionArgsPrefix []string
	if paths.RemotionCLI != "" {
		remotionArgsPrefix = []string{paths.RemotionCLI}
	}
	spec := mediaworker.ProductionSpec{
		ProductionSpecVersion: mediaworker.ProductionSpecVersion,
		JobID:                 runID,
		Segments:              []mediaworker.SegmentSpec{},
		Timeline:              &mediaworker.TimelineSpec{PlaybackRate: rate, InputPlaybackRate: 1},
		Outputs:               &mediaworker.OutputsSpec{AudioFormat: "wav"},
		Paths:                 &mediaworker.PathsSpec{Python: paths.Python, FFmpeg: paths.FFmpeg, FFprobe: paths.FFprobe, Remotion: paths.Remotion, RemotionArgsPrefix: remotionArgsPrefix, EdgeTTSRoot: paths.EdgeTTSRoot},
		Voice:                 &mediaworker.VoiceSpec{Voice: firstText(request.Voice, "zh-CN-XiaoxiaoNeural"), Default: map[string]string{"rate": "+0%", "pitch": "+0Hz", "volume": "+0%"}, Profiles: map[string]map[string]string{"hook": {"rate": "+0%", "pitch": "+4Hz", "volume": "+8%"}, "normal": {"rate": "+0%", "pitch": "+0Hz", "volume": "+0%"}}},
		Render:                &mediaworker.RenderSpec{Enabled: request.Render, CWD: paths.RemotionCWD, Output: filepath.Join(mediaDir, "rendered-video.mp4")},
	}
	if request.Render {
		spec.Render.Entry = firstText(paths.RemotionEntry, defaultRemotionEntry)
		spec.Render.Composition = firstText(paths.RemotionComposition, defaultRemotionComposition)
	}
	if brief != nil {
		for index, shot := range brief.Storyboard.Shots {
			text := firstText(shot.Voiceover, brief.VoiceoverText, firstScriptVoiceover(brief.Scripts))
			caption := firstText(shot.Subtitle, brief.SubtitleText, text)
			id := fmt.Sprintf("segment-%d", index+1)
			profile := "normal"
			if index == 0 {
				profile = "hook"
			}
			spec.Segments = append(spec.Segments, mediaworker.SegmentSpec{ID: id, Text: text, Profile: profile, CaptionLines: []string{caption}})
			spec.Scenes = append(spec.Scenes, mediaworker.SceneSpec{ID: fmt.Sprintf("scene-%d", index+1), SegmentIDs: []string{id}, Props: map[string]any{"visual": shot.Visual, "asset_type": shot.AssetType, "purpose": shot.Purpose, "transition": shot.Transition}})
		}
		if len(spec.Segments) == 0 {
			text := firstText(brief.VoiceoverText, firstScriptVoiceover(brief.Scripts), project.Offering.Description, project.Name)
			spec.Segments = append(spec.Segments, mediaworker.SegmentSpec{ID: "segment-1", Text: text, Profile: "hook", CaptionLines: []string{firstText(brief.SubtitleText, text)}})
		}
	}
	if len(spec.Segments) == 0 {
		text := firstText(project.Offering.Description, project.Offering.Name, project.Name)
		spec.Segments = append(spec.Segments, mediaworker.SegmentSpec{ID: "segment-1", Text: text, Profile: "hook", CaptionLines: []string{text}})
	}
	if request.HasTaskAssets() {
		spec.Assets, _ = productionAssetRequest(request)
		if len(spec.Scenes) == 0 {
			spec.Scenes = []mediaworker.SceneSpec{{ID: "scene-1", SegmentIDs: []string{spec.Segments[0].ID}}}
		}
	}
	return spec
}

func productionAssetRequest(request domain.MediaRequest) ([]mediaworker.AssetSpec, []mediaworker.SceneAssetBinding) {
	assets := make([]mediaworker.AssetSpec, 0, len(request.Assets))
	for _, a := range request.Assets {
		assets = append(assets, mediaworker.AssetSpec{ID: a.ID, Ref: a.Ref, Kind: a.Kind, SHA256: a.SHA256})
	}
	bindings := make([]mediaworker.SceneAssetBinding, 0, len(request.SceneAssets))
	for _, b := range request.SceneAssets {
		bindings = append(bindings, mediaworker.SceneAssetBinding{SceneID: b.SceneID, AssetID: b.AssetID})
	}
	return assets, bindings
}

func bindProductionSpecAssets(dataDir, runDir string, request domain.MediaRequest, spec *mediaworker.ProductionSpec) error {
	if !request.HasTaskAssets() {
		return nil
	}
	assets, bindings := productionAssetRequest(request)
	if !request.Enabled || !request.Render {
		return fmt.Errorf("task assets require media.enabled and media.render")
	}
	if err := mediaworker.ValidateAssetRequest(assets, bindings, request.DefaultAssetID); err != nil {
		return err
	}
	spec.Assets = assets
	if err := mediaworker.BindSceneAssets(spec, bindings, request.DefaultAssetID); err != nil {
		return err
	}
	return mediaworker.SnapshotAssets(dataDir, runDir, spec)
}

func firstScriptVoiceover(scripts []domain.Script) string {
	if len(scripts) == 0 {
		return ""
	}
	return scripts[0].VoiceoverText
}

func firstText(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Service) writePackage(runID string, project domain.VideoProject) (string, string, error) {
	dir := filepath.Join(s.dataDir, "runs", runID)
	paths, err := exporter.ExportProjectFiles(project, dir, []string{"json"})
	if err != nil {
		return "", "", err
	}
	if len(paths) != 1 {
		return "", "", fmt.Errorf("exporter returned %d package files, want one json file", len(paths))
	}
	path := paths[0]
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(b)
	return path, hex.EncodeToString(digest[:]), nil
}

func normalizeRequest(request domain.DistributionRequest) (domain.DistributionRequest, error) {
	if request.Media != nil && request.Media.HasTaskAssets() {
		if !request.Media.Enabled || !request.Media.Render {
			return domain.DistributionRequest{}, fmt.Errorf("%w: task assets require media.enabled and media.render", ErrInvalidRequest)
		}
		assets, bindings := productionAssetRequest(*request.Media)
		if err := mediaworker.ValidateAssetRequest(assets, bindings, request.Media.DefaultAssetID); err != nil {
			return domain.DistributionRequest{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	if request.Atom != nil && request.Offering != nil {
		return domain.DistributionRequest{}, fmt.Errorf("%w: exactly one of atom or offering is required", ErrInvalidRequest)
	}
	if request.Atom == nil && request.Offering == nil {
		return domain.DistributionRequest{}, fmt.Errorf("%w: exactly one of atom or offering is required", ErrInvalidRequest)
	}
	if request.Offering != nil {
		atom, err := AdaptLegacyOffering(*request.Offering)
		if err != nil {
			return domain.DistributionRequest{}, fmt.Errorf("%w: legacy offering: %v", ErrInvalidRequest, err)
		}
		request.Atom = &atom
		request.OriginalInputType = "legacy_offering"
	}
	if len(request.Targets) > 1 {
		return domain.DistributionRequest{}, fmt.Errorf("%w: targets currently accepts at most one target", ErrTargetsNotImplemented)
	}
	if len(request.Targets) == 1 {
		targetPlatform := request.Targets[0].Platform
		if request.Platform != "" && request.Platform != targetPlatform {
			return domain.DistributionRequest{}, fmt.Errorf("%w: platform and targets[0].platform disagree", ErrInvalidRequest)
		}
		request.Platform = targetPlatform
	}
	request.Platform = tools.NormalizePlatform(request.Platform)
	if request.Platform == domain.PlatformWechatChannels {
		return domain.DistributionRequest{}, fmt.Errorf("%w: WeChat Channels is disabled for new asynchronous runs", ErrInvalidRequest)
	}
	if len(request.Targets) == 1 {
		request.Targets[0].Platform = request.Platform
	}
	if request.Platform != domain.PlatformXiaohongshu && request.Platform != domain.PlatformDouyin && request.Platform != domain.PlatformBilibili && request.Platform != domain.PlatformWechatChannels {
		return domain.DistributionRequest{}, fmt.Errorf("%w: unsupported platform %q", ErrInvalidRequest, request.Platform)
	}
	if request.VariantCount < 0 || request.VariantCount > 50 {
		return domain.DistributionRequest{}, fmt.Errorf("%w: variant_count must be between 0 and 50", ErrInvalidRequest)
	}
	if request.Atom != nil {
		if err := request.Atom.Validate(); err != nil {
			return domain.DistributionRequest{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
		}
	}
	return request, nil
}

func offeringFromAtom(atom domain.ContentAtom) (domain.OfferingInput, *domain.CapabilityBlock) {
	if atom.Kind != domain.ContentAtomOffering {
		if atom.Kind == domain.ContentAtomResearch || atom.Kind == domain.ContentAtomJudgment {
			return contentAtomOffering(atom), nil
		}
		return domain.OfferingInput{}, &domain.CapabilityBlock{Code: "INVALID_ATOM_KIND", Capability: "content_atom", Message: "unsupported content atom kind", Retryable: false}
	}
	if atom.Offering != nil {
		return *atom.Offering, nil
	}
	return domain.OfferingInput{Title: atom.Title, Name: atom.Title, Description: atom.Summary + "\n" + atom.Body, ProofPoints: append([]string(nil), atom.Sources...)}, nil
}

func terminal(status domain.RunStatus) bool {
	return status == domain.RunStatusPackageReady || status == domain.RunStatusMediaReady || status == domain.RunStatusFailed || status == domain.RunStatusCancelled || status == domain.RunStatusBlocked
}
