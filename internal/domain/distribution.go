package domain

import (
	"fmt"
	"strings"
	"time"
)

// PlatformBilibili is introduced here so the new run contract can name Bilibili
// without changing legacy platform business code in this vertical slice.
const PlatformBilibili Platform = "bilibili"

// ContentAtomKind identifies the source semantics of a content atom.
type ContentAtomKind string

const (
	ContentAtomOffering ContentAtomKind = "offering"
	ContentAtomResearch ContentAtomKind = "research"
	ContentAtomJudgment ContentAtomKind = "judgment"
)

const ContentAtomSchemaVersion = "content_atom.v1"

// ResearchContent carries research-oriented input without pretending it is an offering.
type ResearchContent struct {
	Question    string   `json:"question,omitempty"`
	Findings    []string `json:"findings,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Limitations []string `json:"limitations,omitempty"`
}

// ContentAtom is the normalized research/content contract for distribution.
type ContentAtom struct {
	SchemaVersion string          `json:"schema_version"`
	Revision      int             `json:"revision"`
	ID            string          `json:"id,omitempty"`
	Kind          ContentAtomKind `json:"kind"`
	SourceType    string          `json:"source_type"`
	SourceID      string          `json:"source_id"`
	ThesisID      string          `json:"thesis_id"`
	Audience      string          `json:"audience"`
	Angle         string          `json:"angle"`
	Hook          string          `json:"hook"`
	Claim         string          `json:"claim"`
	EvidenceIDs   []string        `json:"evidence_ids"`
	CounterPoint  string          `json:"counter_point"`
	Insight       string          `json:"insight"`
	DesiredAction string          `json:"desired_action"`
	CTA           string          `json:"cta"`
	Priority      float64         `json:"priority"`

	// Legacy migration fields remain accepted but do not replace the contract above.
	Title    string           `json:"title,omitempty"`
	Summary  string           `json:"summary,omitempty"`
	Body     string           `json:"body,omitempty"`
	Sources  []string         `json:"sources,omitempty"`
	Offering *OfferingInput   `json:"offering,omitempty"`
	Research *ResearchContent `json:"research,omitempty"`
}

// DistributionTarget identifies one future platform/account delivery target.
type DistributionTarget struct {
	Platform  Platform `json:"platform"`
	AccountID string   `json:"account_id,omitempty"`
	Format    string   `json:"format,omitempty"`
}

// MediaRequest opts a distribution run into the repository media worker.
// Keeping this pointer optional preserves the legacy package-only behavior.
type MediaRequest struct {
	Enabled      bool    `json:"enabled"`
	Voice        string  `json:"voice,omitempty"`
	PlaybackRate float64 `json:"playback_rate,omitempty"`
	Render       bool    `json:"render,omitempty"`
}

// DistributionRequest is the public request contract for an asynchronous run.
// Callers supply exactly one of Atom and Offering. Normalized legacy requests
// may retain Offering as a compatibility echo alongside the canonical Atom.
type DistributionRequest struct {
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Atom           *ContentAtom   `json:"atom,omitempty"`
	Offering       *OfferingInput `json:"offering,omitempty"`
	// OriginalInputType records the compatibility input used before normalization.
	OriginalInputType string `json:"original_input_type,omitempty"`
	// Platform remains for legacy single-platform clients. New clients may send
	// targets, but this vertical slice accepts at most one target.
	Platform     Platform             `json:"platform,omitempty"`
	Targets      []DistributionTarget `json:"targets,omitempty"`
	Template     string               `json:"template,omitempty"`
	VariantCount int                  `json:"variant_count,omitempty"`
	Media        *MediaRequest        `json:"media,omitempty"`
}

// Validate checks the fields required at the distribution contract boundary.
// Research and judgment atoms must carry traceable thesis evidence; offering
// atoms may additionally carry the normalized offering payload.
func (a ContentAtom) Validate() error {
	missing := func(field string) error {
		return fmt.Errorf("content atom %s is required", field)
	}
	if strings.TrimSpace(a.SchemaVersion) != ContentAtomSchemaVersion {
		return fmt.Errorf("content atom schema_version must be %q", ContentAtomSchemaVersion)
	}
	if a.Revision < 1 {
		return fmt.Errorf("content atom revision must be positive")
	}
	if strings.TrimSpace(a.ID) == "" {
		return missing("id")
	}
	if a.Kind == "" {
		return missing("kind")
	}
	if strings.TrimSpace(a.SourceType) == "" {
		return missing("source_type")
	}
	if strings.TrimSpace(a.SourceID) == "" {
		return missing("source_id")
	}
	if strings.TrimSpace(a.Title) == "" {
		return missing("title")
	}
	if strings.TrimSpace(a.Audience) == "" {
		return missing("audience")
	}
	if strings.TrimSpace(a.Hook) == "" {
		return missing("hook")
	}
	if strings.TrimSpace(a.Claim) == "" {
		return missing("claim")
	}
	switch a.Kind {
	case ContentAtomOffering:
	case ContentAtomResearch, ContentAtomJudgment:
		if strings.TrimSpace(a.ThesisID) == "" {
			return missing("thesis_id")
		}
		if len(a.EvidenceIDs) == 0 {
			return missing("evidence_ids")
		}
	default:
		return fmt.Errorf("content atom kind %q is unsupported", a.Kind)
	}
	return nil
}

// RunStatus is the durable state of a distribution run.
type RunStatus string

const (
	RunStatusQueued       RunStatus = "queued"
	RunStatusRunning      RunStatus = "running"
	RunStatusBlocked      RunStatus = "blocked"
	RunStatusPackageReady RunStatus = "package_ready"
	RunStatusMediaReady   RunStatus = "media_ready"
	RunStatusFailed       RunStatus = "failed"
	RunStatusCancelled    RunStatus = "cancelled"
)

// CapabilityBlock describes a truthful capability boundary reached by a run.
type CapabilityBlock struct {
	Code       string `json:"code"`
	Capability string `json:"capability"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable"`
}

// RunError is a structured failure reported by the asynchronous runner.
type RunError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Artifact is a durable output ledger entry. A path may point to a local file.
type Artifact struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Kind      string         `json:"kind"`
	Path      string         `json:"path,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// DistributionRun is the durable public view consumed by API clients and dashboards.
type DistributionRun struct {
	ID              string              `json:"id"`
	Status          RunStatus           `json:"status"`
	Request         DistributionRequest `json:"request"`
	Checkpoint      string              `json:"checkpoint,omitempty"`
	Attempt         int                 `json:"attempt"`
	CapabilityBlock *CapabilityBlock    `json:"capability_block,omitempty"`
	Error           *RunError           `json:"error,omitempty"`
	Artifacts       []Artifact          `json:"artifacts,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	StartedAt       *time.Time          `json:"started_at,omitempty"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
}

// Run is retained as a concise source-compatible alias for DistributionRun.
type Run = DistributionRun

// RunEvent is an append-only event in the run ledger.
type RunEvent struct {
	ID         int64          `json:"id"`
	RunID      string         `json:"run_id"`
	Type       string         `json:"type"`
	Checkpoint string         `json:"checkpoint,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}
