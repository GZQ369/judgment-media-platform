package research

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	DefaultMaxSourceBytes  int64 = 2 << 20
	DefaultMaxGatewayBytes int64 = 1 << 20
	DefaultSourceTimeout         = 15 * time.Second
	DefaultGatewayTimeout        = 45 * time.Second
	DefaultMaxRedirects          = 4
)

var (
	ErrNoSources               = errors.New("research: at least one source is required")
	ErrNoTextCapability        = errors.New("research: gateway response has no text capability")
	ErrNoCitations             = errors.New("research: claim has no verifiable source citation")
	ErrInvalidCitation         = errors.New("research: citation does not match a fetched source")
	ErrInvalidResult           = errors.New("research: result does not meet the minimum schema")
	ErrResearchInputTooLarge   = errors.New("research: combined source text exceeds the input budget")
	ErrUnsupportedFeed         = errors.New("research: response is not a supported RSS or Atom feed")
	ErrSourceTooLarge          = errors.New("research: source exceeds the configured size limit")
	ErrGatewayResponseTooLarge = errors.New("research: gateway response exceeds the configured size limit")
)

// SourceSnapshot is the exact fetched source plus conservative text derived
// from it. Original is intentionally retained for audit and quote checking.
type SourceSnapshot struct {
	SourceURL    string    `json:"source_url"`
	FetchedAt    time.Time `json:"fetched_at"`
	ContentHash  string    `json:"content_hash"`
	OriginalSize int64     `json:"original_size"`
	ContentType  string    `json:"content_type,omitempty"`
	Original     []byte    `json:"-"`
	Text         string    `json:"text,omitempty"`
}

// NewSourceSnapshot makes a snapshot from already fetched bytes.
func NewSourceSnapshot(sourceURL, contentType string, original []byte, fetchedAt time.Time) SourceSnapshot {
	digest := sha256.Sum256(original)
	copyOfOriginal := append([]byte(nil), original...)
	return SourceSnapshot{
		SourceURL:    strings.TrimSpace(sourceURL),
		FetchedAt:    fetchedAt,
		ContentHash:  hex.EncodeToString(digest[:]),
		OriginalSize: int64(len(original)),
		ContentType:  contentType,
		Original:     copyOfOriginal,
		Text:         ExtractText(contentType, original),
	}
}

// Citation identifies a quote in one of the fetched snapshots.
type Citation struct {
	SourceURL   string `json:"source_url"`
	ContentHash string `json:"content_hash"`
	Quote       string `json:"quote"`
	Locator     string `json:"locator,omitempty"`
}

type Evidence struct {
	Statement string     `json:"statement"`
	Citations []Citation `json:"citations"`
}

type HistoricalContext struct {
	Statement string     `json:"statement"`
	Citations []Citation `json:"citations"`
}

// Contradiction records a material disagreement found in the source set.
// Research records the disagreement and its provenance; it does not resolve it.
type Contradiction struct {
	Statement           string     `json:"statement"`
	SupportingCitations []Citation `json:"supporting_citations"`
	OpposingCitations   []Citation `json:"opposing_citations"`
}

// EvidenceBundle is the contract emitted by Research. It contains source
// snapshots and sourced observations only. Judgment and content planning are
// intentionally separate packages and are not fields of this result.
type EvidenceBundle struct {
	Topic             string              `json:"topic"`
	Sources           []SourceSnapshot    `json:"sources"`
	Evidence          []Evidence          `json:"evidence"`
	CounterEvidence   []Evidence          `json:"counter_evidence"`
	HistoricalContext []HistoricalContext `json:"historical_context"`
	Contradictions    []Contradiction     `json:"contradictions"`
}

// ResearchResult is retained as a source-compatible name while callers
// migrate to the explicit EvidenceBundle contract.
// Deprecated: use EvidenceBundle.
type ResearchResult = EvidenceBundle

// Validate ensures that every non-empty claim points back to a fetched source.
func (r EvidenceBundle) Validate(sources []SourceSnapshot) error {
	if len(sources) == 0 {
		return ErrNoSources
	}
	if strings.TrimSpace(r.Topic) == "" || len(r.Evidence) == 0 {
		return ErrInvalidResult
	}
	for _, claim := range r.Evidence {
		if err := validateClaim(claim.Statement, claim.Citations, sources); err != nil {
			return fmt.Errorf("evidence: %w", err)
		}
	}
	for _, claim := range r.CounterEvidence {
		if err := validateClaim(claim.Statement, claim.Citations, sources); err != nil {
			return fmt.Errorf("counter evidence: %w", err)
		}
	}
	for _, claim := range r.HistoricalContext {
		if err := validateClaim(claim.Statement, claim.Citations, sources); err != nil {
			return fmt.Errorf("historical context: %w", err)
		}
	}
	for _, contradiction := range r.Contradictions {
		if strings.TrimSpace(contradiction.Statement) == "" {
			return ErrInvalidResult
		}
		if err := validateCitations(contradiction.SupportingCitations, sources); err != nil {
			return fmt.Errorf("contradiction supporting evidence: %w", err)
		}
		if err := validateCitations(contradiction.OpposingCitations, sources); err != nil {
			return fmt.Errorf("contradiction opposing evidence: %w", err)
		}
	}
	return nil
}

// ValidateCitation allows downstream bounded consumers, such as Judgment, to
// verify that their sourced output still points to this bundle.
func (r EvidenceBundle) ValidateCitation(citation Citation) error {
	return validateCitation(citation, r.Sources)
}

func validateClaim(statement string, citations []Citation, sources []SourceSnapshot) error {
	if strings.TrimSpace(statement) == "" {
		return ErrInvalidResult
	}
	if len(citations) == 0 {
		return ErrNoCitations
	}
	return validateCitations(citations, sources)
}

func validateCitations(citations []Citation, sources []SourceSnapshot) error {
	if len(citations) == 0 {
		return ErrNoCitations
	}
	for _, citation := range citations {
		if err := validateCitation(citation, sources); err != nil {
			return err
		}
	}
	return nil
}

func validateCitation(citation Citation, sources []SourceSnapshot) error {
	if strings.TrimSpace(citation.SourceURL) == "" || strings.TrimSpace(citation.ContentHash) == "" || strings.TrimSpace(citation.Quote) == "" {
		return ErrInvalidCitation
	}
	for _, source := range sources {
		if citation.SourceURL == source.SourceURL && citation.ContentHash == source.ContentHash {
			if bytes.Contains(source.Original, []byte(citation.Quote)) || strings.Contains(source.Text, citation.Quote) {
				return nil
			}
			return ErrInvalidCitation
		}
	}
	return ErrInvalidCitation
}
