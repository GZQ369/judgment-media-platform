package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/GZQ369/judgment-media-platform/internal/research"
)

// PipelineStatus is the durable state of a pipeline run.
type PipelineStatus string

const (
	PipelineStatusQueued            PipelineStatus = "queued"
	PipelineStatusRunning           PipelineStatus = "running"
	PipelineStatusBlocked           PipelineStatus = "blocked"
	PipelineStatusFailed            PipelineStatus = "failed"
	PipelineStatusDistributionReady PipelineStatus = "distribution_ready"
	PipelineStatusCancelled         PipelineStatus = "cancelled"
)

// Short status aliases are provided for callers that prefer status names
// without the PipelineStatus prefix.
const (
	StatusQueued            = PipelineStatusQueued
	StatusRunning           = PipelineStatusRunning
	StatusBlocked           = PipelineStatusBlocked
	StatusFailed            = PipelineStatusFailed
	StatusDistributionReady = PipelineStatusDistributionReady
	StatusCancelled         = PipelineStatusCancelled
)

// PipelineRequest is the pipeline boundary. ResearchTopic intentionally ends
// at this boundary; child distribution requests contain only a ContentAtom.
type PipelineRequest struct {
	IdempotencyKey string                      `json:"idempotency_key,omitempty"`
	Topic          domain.ResearchTopic        `json:"topic"`
	Targets        []domain.DistributionTarget `json:"targets"`
	Template       string                      `json:"template,omitempty"`
	VariantCount   int                         `json:"variant_count,omitempty"`
	Media          *domain.MediaRequest        `json:"media,omitempty"`
}

// Validate checks the fields needed before a run is persisted.
func (r PipelineRequest) Validate() error {
	if err := r.Topic.Validate(); err != nil {
		return err
	}
	if len(r.Targets) == 0 {
		return invalidRequest("at least one distribution target is required")
	}
	seen := make(map[string]struct{}, len(r.Targets))
	for _, target := range r.Targets {
		if target.Platform == "" {
			return invalidRequest("distribution target platform is required")
		}
		key := targetKey(target)
		if _, ok := seen[key]; ok {
			return invalidRequest("distribution targets must be unique")
		}
		seen[key] = struct{}{}
	}
	if r.VariantCount < 0 {
		return invalidRequest("variant_count must not be negative")
	}
	return nil
}

// ChildRun records the durable relationship between one atom/target pair and
// its distribution run, including the latest observed child outcome.
type ChildRun struct {
	ID              string                    `json:"id"`
	IdempotencyKey  string                    `json:"idempotency_key"`
	AtomID          string                    `json:"atom_id"`
	Target          domain.DistributionTarget `json:"target"`
	Status          domain.RunStatus          `json:"status"`
	CapabilityBlock *domain.CapabilityBlock   `json:"capability_block,omitempty"`
	Error           *domain.RunError          `json:"error,omitempty"`
	UpdatedAt       time.Time                 `json:"updated_at"`
	RetryAttempt    int                       `json:"retry_attempt,omitempty"`
}

// PipelineRun is the durable parent view. DistributionReady means every
// child is package_ready or media_ready; it does not mean that anything was
// published.
type PipelineRun struct {
	ID              string                  `json:"id"`
	Status          PipelineStatus          `json:"status"`
	Request         PipelineRequest         `json:"request"`
	Research        research.EvidenceBundle `json:"research,omitempty"`
	Judgment        judgment.Judgment       `json:"judgment,omitempty"`
	Thesis          judgment.ContentThesis  `json:"thesis,omitempty"`
	Atoms           []domain.ContentAtom    `json:"atoms,omitempty"`
	ChildRunIDs     []string                `json:"child_run_ids,omitempty"`
	Children        []ChildRun              `json:"children,omitempty"`
	Checkpoint      string                  `json:"checkpoint,omitempty"`
	Attempt         int                     `json:"attempt"`
	CapabilityBlock *domain.CapabilityBlock `json:"capability_block,omitempty"`
	Error           *domain.RunError        `json:"error,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
	StartedAt       *time.Time              `json:"started_at,omitempty"`
	FinishedAt      *time.Time              `json:"finished_at,omitempty"`
}

// JudgmentRunner is the only judgment workflow dependency required by the
// coordinator. orchestration.JudgmentWorkflow satisfies this interface.
type JudgmentRunner interface {
	Run(context.Context, domain.ResearchTopic) (JudgmentResult, error)
}

// JudgmentResult is the structured output expected from a judgment workflow.
// It aliases orchestration.Result so the existing workflow implementation can
// be injected without an adapter.
type JudgmentResult = orchestration.Result

// DistributionStarter is the small distribution dependency used for fan-out.
// The bool reports whether StartRun created a new child.
type DistributionStarter interface {
	StartRun(context.Context, domain.DistributionRequest) (domain.Run, bool, error)
	GetRun(context.Context, string) (domain.Run, error)
}

// DistributionRetryOnce consumes each parent-attempt/child operation durably.
// A dependency with only ordinary Retry cannot provide this guarantee.
type DistributionRetryOnce interface {
	RetryOnce(context.Context, string, int, string) (domain.Run, error)
}

// Options controls worker recovery, lease renewal, and child polling.
type Options struct {
	LeaseTTL       time.Duration
	ReaperInterval time.Duration
	PollInterval   time.Duration
	PollTimeout    time.Duration
	// ChildPollInterval and ChildPollTimeout are descriptive aliases for the
	// corresponding Poll fields.
	ChildPollInterval time.Duration
	ChildPollTimeout  time.Duration
	QueueSize         int
	Owner             string
}

// Config configures an independently opened pipeline SQLite ledger.
type Config struct {
	Path string
}

// ErrInvalidRequest is returned when a pipeline request cannot be persisted.
var ErrInvalidRequest = errors.New("invalid pipeline request")

func invalidRequest(message string) error { return fmt.Errorf("%w: %s", ErrInvalidRequest, message) }

func targetKey(target domain.DistributionTarget) string {
	return string(target.Platform) + "\x00" + target.AccountID + "\x00" + target.Format
}

func terminal(status PipelineStatus) bool {
	return status == PipelineStatusBlocked || status == PipelineStatusFailed || status == PipelineStatusDistributionReady || status == PipelineStatusCancelled
}
