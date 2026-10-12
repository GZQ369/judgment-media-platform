package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/GZQ369/judgment-media-platform/internal/research"
	"github.com/google/uuid"
)

var (
	// ErrServiceStopped indicates that the asynchronous worker is not running.
	ErrServiceStopped = errors.New("pipeline service is stopped")
	// ErrServiceStarting indicates a concurrent Start call.
	ErrServiceStarting = errors.New("pipeline service is starting")
	// ErrServiceStopping indicates that a previous Stop is still draining.
	ErrServiceStopping = errors.New("pipeline service is stopping")
)

const (
	defaultLeaseTTL       = 30 * time.Second
	defaultReaperInterval = time.Second
	defaultPollInterval   = 250 * time.Millisecond
	defaultPollTimeout    = 30 * time.Minute
)

// Service owns one durable pipeline worker and its recovery reaper.
type Service struct {
	store        *Store
	judgment     JudgmentRunner
	distribution DistributionStarter
	queue        chan string
	owner        string
	leaseTTL     time.Duration
	reaper       time.Duration
	poll         time.Duration
	pollTimeout  time.Duration

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
	starting bool
}

// NewService creates a pipeline facade without starting its worker. An options
// value may be supplied; omitting it selects the durable defaults.
func NewService(store *Store, runner JudgmentRunner, starter DistributionStarter, optionValues ...Options) *Service {
	var options Options
	if len(optionValues) > 0 {
		options = optionValues[0]
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = defaultLeaseTTL
	}
	if options.ReaperInterval <= 0 {
		options.ReaperInterval = defaultReaperInterval
	}
	if options.PollInterval <= 0 {
		options.PollInterval = options.ChildPollInterval
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.PollTimeout <= 0 {
		options.PollTimeout = options.ChildPollTimeout
	}
	if options.PollTimeout <= 0 {
		options.PollTimeout = defaultPollTimeout
	}
	if options.QueueSize <= 0 {
		options.QueueSize = 256
	}
	owner := options.Owner
	if owner == "" {
		owner = fmt.Sprintf("pipeline-%d-%s", os.Getpid(), uuid.NewString())
	}
	return &Service{
		store: store, judgment: runner, distribution: starter,
		queue: make(chan string, options.QueueSize), owner: owner,
		leaseTTL: options.LeaseTTL, reaper: options.ReaperInterval,
		poll: options.PollInterval, pollTimeout: options.PollTimeout,
		cancels: map[string]context.CancelFunc{},
	}
}

// NewServiceWithOptions is the explicit constructor form for callers that
// prefer not to use the variadic convenience form.
func NewServiceWithOptions(store *Store, runner JudgmentRunner, starter DistributionStarter, options Options) *Service {
	return NewService(store, runner, starter, options)
}

// Start recovers expired work and starts the pipeline worker and reaper.
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
	s.started, s.starting = true, false
	s.mu.Unlock()
	go func() {
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); s.worker(ctx) }()
		go func() { defer workers.Done(); s.leaseReaper(ctx) }()
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

// Stop cancels the worker and waits for its current operation to drain.
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
	cancel, done := s.cancel, s.done
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

// StartRun validates, durably creates, and enqueues a pipeline.
func (s *Service) StartRun(ctx context.Context, request PipelineRequest) (PipelineRun, bool, error) {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return PipelineRun{}, false, ErrServiceStopped
	}
	run, created, err := s.store.CreateRun(ctx, request)
	if err != nil {
		return PipelineRun{}, false, err
	}
	if created {
		if err := s.enqueue(run.ID); err != nil && !errors.Is(err, ErrServiceStopped) {
			return run, created, err
		}
	}
	return run, created, nil
}

// GetRun returns durable pipeline details.
func (s *Service) GetRun(ctx context.Context, id string) (PipelineRun, error) {
	return s.store.GetRun(ctx, id)
}

// Get is a concise alias for GetRun.
func (s *Service) Get(ctx context.Context, id string) (PipelineRun, error) {
	return s.GetRun(ctx, id)
}

// ListRuns returns newest pipeline runs first.
func (s *Service) ListRuns(ctx context.Context, limit int) ([]PipelineRun, error) {
	return s.store.ListRuns(ctx, limit)
}

// List is a concise alias for ListRuns.
func (s *Service) List(ctx context.Context, limit int) ([]PipelineRun, error) {
	return s.ListRuns(ctx, limit)
}

// Events returns append-only pipeline events after eventID.
func (s *Service) Events(ctx context.Context, id string, eventID int64, limit int) ([]domain.RunEvent, error) {
	if _, err := s.store.GetRun(ctx, id); err != nil {
		return nil, err
	}
	return s.store.ListEvents(ctx, id, eventID, limit)
}

// Cancel requests cancellation. Repeating it is idempotent.
func (s *Service) Cancel(ctx context.Context, id string) (PipelineRun, error) {
	run, err := s.store.GetRun(ctx, id)
	if err != nil {
		return PipelineRun{}, err
	}
	if terminal(run.Status) {
		return run, nil
	}
	if err := s.store.CancelRun(ctx, id); err != nil {
		return PipelineRun{}, err
	}
	s.mu.Lock()
	cancel := s.cancels[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return s.store.GetRun(ctx, id)
}

// Retry requeues a failed, blocked, or cancelled pipeline. Child idempotency
// keys remain stable, so already-created child runs are reused.
func (s *Service) Retry(ctx context.Context, id string) (PipelineRun, error) {
	queued, err := s.store.RetryRun(ctx, id)
	if err != nil {
		return PipelineRun{}, err
	}
	if queued {
		if err := s.enqueue(id); err != nil && !errors.Is(err, ErrServiceStopped) {
			return PipelineRun{}, err
		}
	}
	return s.store.GetRun(ctx, id)
}

// ChildIdempotencyKey returns the stable key used for an atom/target child.
func ChildIdempotencyKey(pipelineID, atomID string, target domain.DistributionTarget) string {
	raw := pipelineID + "\x00" + atomID + "\x00" + targetKey(target)
	digest := sha256.Sum256([]byte(raw))
	return "pipeline-child-" + hex.EncodeToString(digest[:])
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

func (s *Service) leaseReaper(ctx context.Context) {
	ticker := time.NewTicker(s.reaper)
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
				if enqueueWith(ctx, s.queue, id) != nil {
					return
				}
			}
		}
	}
}

func (s *Service) heartbeat(ctx context.Context, id string, lease Lease, cancel context.CancelFunc, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(s.leaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.store.RenewLease(context.Background(), id, lease, s.leaseTTL); err != nil {
				cancel()
				return
			}
		}
	}
}

func (s *Service) process(ctx context.Context, id string) {
	lease, err := s.store.AcquireLease(ctx, id, s.owner, s.leaseTTL)
	if err != nil {
		return
	}
	defer s.store.ReleaseLeaseToken(context.Background(), id, lease)
	run, err := s.store.GetRun(ctx, id)
	if err != nil || run.Status != PipelineStatusQueued {
		return
	}
	if err := s.store.UpdateRunFenced(ctx, id, lease, PipelineStatusRunning, "started", nil, nil, map[string]any{"attempt": run.Attempt}); err != nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go s.heartbeat(runCtx, id, lease, cancel, heartbeatDone)
	s.mu.Lock()
	s.cancels[id] = cancel
	s.mu.Unlock()
	err = s.execute(runCtx, run, lease)
	cancel()
	<-heartbeatDone
	s.mu.Lock()
	delete(s.cancels, id)
	s.mu.Unlock()
	if errors.Is(err, context.Canceled) {
		current, getErr := s.store.GetRun(context.Background(), id)
		if getErr == nil && current.Status == PipelineStatusCancelled {
			return
		}
		if getErr == nil && !terminal(current.Status) {
			_ = s.store.UpdateRunFenced(context.Background(), id, lease, PipelineStatusQueued, "shutdown_requeued", nil, nil, map[string]any{"reason": "service_shutdown"})
		}
		return
	}
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		_ = s.store.UpdateRunFenced(context.Background(), id, lease, PipelineStatusFailed, "failed", nil, &domain.RunError{Code: "PIPELINE_FAILED", Message: err.Error(), Retryable: true}, map[string]any{"error": err.Error()})
	}
}

func (s *Service) execute(ctx context.Context, run PipelineRun, lease Lease) error {
	result := orchestration.Result{Evidence: run.Research, Judgment: run.Judgment, Thesis: run.Thesis, Atoms: run.Atoms}
	if len(run.Atoms) == 0 {
		if s.judgment == nil {
			block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: "judgment_workflow", Message: "judgment workflow is not configured", Retryable: true}
			return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
		}
		var err error
		result, err = s.judgment.Run(ctx, run.Request.Topic)
		if err != nil {
			if block := capabilityBlock(err); block != nil {
				return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
			}
			return err
		}
	}
	if len(result.Atoms) == 0 {
		return fmt.Errorf("judgment workflow returned no content atoms")
	}
	for _, atom := range result.Atoms {
		if err := atom.Validate(); err != nil {
			return fmt.Errorf("judgment workflow returned invalid atom: %w", err)
		}
	}
	if len(run.Atoms) == 0 {
		if err := s.store.SaveWorkflowFenced(ctx, run.ID, lease, result); err != nil {
			return err
		}
	}
	if s.distribution == nil {
		block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: "distribution_starter", Message: "distribution starter is not configured", Retryable: true}
		return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
	}
	children := make([]ChildRun, 0, len(result.Atoms)*len(run.Request.Targets))
	for _, atom := range result.Atoms {
		for _, target := range run.Request.Targets {
			key := ChildIdempotencyKey(run.ID, atom.ID, target)
			var prior *ChildRun
			for index := range run.Children {
				if run.Children[index].IdempotencyKey == key {
					prior = &run.Children[index]
					break
				}
			}
			child := ChildRun{IdempotencyKey: key, AtomID: atom.ID, Target: target}
			if prior != nil {
				child = *prior
				current, err := s.distribution.GetRun(ctx, child.ID)
				if err != nil {
					return err
				}
				child.Status = current.Status
				child.CapabilityBlock = current.CapabilityBlock
				child.Error = current.Error
				child.UpdatedAt = current.UpdatedAt
			} else {
				request := domain.DistributionRequest{IdempotencyKey: key, Atom: cloneAtom(atom), Targets: []domain.DistributionTarget{target}, Template: run.Request.Template, VariantCount: run.Request.VariantCount, Media: run.Request.Media}
				created, _, err := s.distribution.StartRun(ctx, request)
				if err != nil {
					if block := capabilityBlock(err); block != nil {
						return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
					}
					return err
				}
				child.ID = created.ID
				child.Status = created.Status
			}
			if run.Attempt > 0 && child.RetryAttempt < run.Attempt {
				retrier, ok := s.distribution.(DistributionRetryOnce)
				if !ok {
					block := &domain.CapabilityBlock{Code: "CAPABILITY_UNAVAILABLE", Capability: "distribution_retry_once", Message: "distribution dependency does not support durable RetryOnce", Retryable: true}
					return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability, "child_run_id": child.ID})
				}
				retried, err := retrier.RetryOnce(ctx, run.ID, run.Attempt, child.ID)
				if err != nil {
					if block := capabilityBlock(err); block != nil {
						return s.store.UpdateRunFenced(ctx, run.ID, lease, PipelineStatusBlocked, "capability_blocked", block, nil, map[string]any{"capability": block.Capability})
					}
					return err
				}
				child.Status = retried.Status
				child.CapabilityBlock = retried.CapabilityBlock
				child.Error = retried.Error
				child.UpdatedAt = retried.UpdatedAt
				child.RetryAttempt = run.Attempt
			}
			children = append(children, child)
			// Persist every successful child immediately. If a later fan-out
			// call blocks or fails, the parent still retains all known child
			// IDs; a crash between calls can safely replay the same key.
			if err := s.store.SetChildrenFenced(ctx, run.ID, lease, children, "child_created"); err != nil {
				return err
			}
		}
	}
	return s.waitForChildren(ctx, run.ID, lease, children)
}

func (s *Service) waitForChildren(ctx context.Context, id string, lease Lease, children []ChildRun) error {
	deadline := time.NewTimer(s.pollTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		if done, err := s.observeChildren(ctx, id, lease, children); done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("child distribution readiness timeout")
		case <-ticker.C:
		}
	}
}

func (s *Service) observeChildren(ctx context.Context, id string, lease Lease, children []ChildRun) (bool, error) {
	allReady := true
	for index := range children {
		run, err := s.distribution.GetRun(ctx, children[index].ID)
		if err != nil {
			return false, err
		}
		children[index].Status = run.Status
		children[index].CapabilityBlock = run.CapabilityBlock
		children[index].Error = run.Error
		children[index].UpdatedAt = run.UpdatedAt
		// Persist observations incrementally as well. If a later child lookup
		// fails, statuses already fetched are not lost with the failed poll.
		if err := s.store.UpdateChildrenFenced(ctx, id, lease, children, "child_observed"); err != nil {
			return false, err
		}
		if run.Status == domain.RunStatusBlocked {
			block := run.CapabilityBlock
			if block == nil {
				block = &domain.CapabilityBlock{Code: "CHILD_BLOCKED", Capability: "distribution", Message: "a child distribution run is blocked", Retryable: true}
			}
			return true, s.store.UpdateRunFenced(ctx, id, lease, PipelineStatusBlocked, "child_blocked", block, nil, map[string]any{"child_run_id": run.ID})
		}
		if run.Status == domain.RunStatusFailed || run.Status == domain.RunStatusCancelled {
			return true, s.store.UpdateRunFenced(ctx, id, lease, PipelineStatusFailed, "child_failed", nil, &domain.RunError{Code: "CHILD_DISTRIBUTION_FAILED", Message: fmt.Sprintf("child %s ended %s", run.ID, run.Status), Retryable: true}, map[string]any{"child_run_id": run.ID})
		}
		if run.Status != domain.RunStatusPackageReady && run.Status != domain.RunStatusMediaReady {
			allReady = false
		}
	}
	if err := s.store.UpdateChildrenFenced(ctx, id, lease, children, "children_observed"); err != nil {
		return false, err
	}
	if allReady {
		return true, s.store.UpdateRunFenced(ctx, id, lease, PipelineStatusDistributionReady, "distribution_ready", nil, nil, map[string]any{"child_count": len(children)})
	}
	return false, nil
}

func capabilityBlock(err error) *domain.CapabilityBlock {
	var workflowBlocker *orchestration.CapabilityBlocker
	if errors.As(err, &workflowBlocker) {
		return &domain.CapabilityBlock{Code: "CAPABILITY_BLOCKED", Capability: firstNonEmpty(workflowBlocker.Capability, "orchestration"), Message: workflowBlocker.Reason, Retryable: true}
	}
	var judgmentBlocker *judgment.CapabilityBlocker
	if errors.As(err, &judgmentBlocker) {
		return &domain.CapabilityBlock{Code: "CAPABILITY_BLOCKED", Capability: firstNonEmpty(judgmentBlocker.Capability, "judgment"), Message: judgmentBlocker.Reason, Retryable: true}
	}
	if errors.Is(err, research.ErrNoTextCapability) {
		return &domain.CapabilityBlock{Code: "CAPABILITY_BLOCKED", Capability: "research_execution", Message: err.Error(), Retryable: true}
	}
	return nil
}

func cloneAtom(atom domain.ContentAtom) *domain.ContentAtom {
	copy := atom
	copy.EvidenceIDs = append([]string(nil), atom.EvidenceIDs...)
	copy.Sources = append([]string(nil), atom.Sources...)
	return &copy
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown"
}
