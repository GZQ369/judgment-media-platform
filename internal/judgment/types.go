package judgment

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/research"
)

var (
	// ErrModelNotConfigured is returned instead of fabricating a judgment when
	// the runtime has no model executor.
	ErrModelNotConfigured = errors.New("judgment: model executor is not configured")
	ErrInvalidOutput      = errors.New("judgment: model output does not meet the minimum schema")
)

// CapabilityBlocker identifies a missing runtime capability without implying
// that a judgment was generated.
type CapabilityBlocker struct {
	Capability string
	Reason     string
}

func (e *CapabilityBlocker) Error() string {
	if e == nil {
		return "judgment: capability blocked"
	}
	return fmt.Sprintf("judgment: capability %q blocked: %s", e.Capability, e.Reason)
}

func (e *CapabilityBlocker) Unwrap() error { return ErrModelNotConfigured }

type Judgment struct {
	Statement  string              `json:"statement"`
	Citations  []research.Citation `json:"citations"`
	Confidence float64             `json:"confidence"`
}

type ContentThesis struct {
	Statement     string              `json:"statement"`
	Audience      string              `json:"audience"`
	Angle         string              `json:"angle"`
	DesiredAction string              `json:"desired_action"`
	CTA           string              `json:"cta"`
	Citations     []research.Citation `json:"citations"`
	Confidence    float64             `json:"confidence"`
}

// ModelExecutor is intentionally smaller than any provider SDK. The service
// only needs a text completion capability and never knows how publishing works.
type ModelExecutor interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func validateOutput(judgment Judgment, thesis ContentThesis, bundle research.EvidenceBundle) error {
	if strings.TrimSpace(judgment.Statement) == "" || strings.TrimSpace(thesis.Statement) == "" {
		return ErrInvalidOutput
	}
	if !validConfidence(judgment.Confidence) || !validConfidence(thesis.Confidence) {
		return ErrInvalidOutput
	}
	if err := validateCitations(judgment.Citations, bundle); err != nil {
		return fmt.Errorf("judgment citations: %w", err)
	}
	if err := validateCitations(thesis.Citations, bundle); err != nil {
		return fmt.Errorf("thesis citations: %w", err)
	}
	return nil
}

func validateCitations(citations []research.Citation, bundle research.EvidenceBundle) error {
	if len(citations) == 0 {
		return research.ErrNoCitations
	}
	for _, citation := range citations {
		if err := bundle.ValidateCitation(citation); err != nil {
			return err
		}
	}
	return nil
}

func validConfidence(value float64) bool { return value >= 0 && value <= 1 }
