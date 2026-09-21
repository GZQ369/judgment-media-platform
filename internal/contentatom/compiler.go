// Package contentatom compiles sourced judgment output into the stable
// ContentAtom contract. It has no knowledge of media, platforms, or publishing.
package contentatom

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/research"
)

const (
	DefaultMaxAtoms = domain.DefaultResearchTopicMaxAtoms
	MaxAtoms        = domain.MaxResearchTopicAtoms
)

type CombinationStrategy string

const (
	StrategyPairwise  CombinationStrategy = "pairwise"
	StrategyCartesian CombinationStrategy = "cartesian"
)

// Options controls explicit fan-out. The default is one thesis atom.
type Options struct {
	MaxAtoms int
	Strategy CombinationStrategy
}

type Compiler struct {
	options Options
}

func NewCompiler(options Options) *Compiler {
	if options.MaxAtoms <= 0 {
		options.MaxAtoms = DefaultMaxAtoms
	}
	if options.Strategy == "" {
		options.Strategy = StrategyPairwise
	}
	return &Compiler{options: options}
}

// Compile creates deterministic atoms from validated, sourced judgment
// output. Topic is included to carry audience/angle constraints and to make
// the compiler's fan-out policy explicit at the boundary.
func (c *Compiler) Compile(ctx context.Context, topic domain.ResearchTopic, bundle research.EvidenceBundle, result judgment.Judgment, thesis judgment.ContentThesis) ([]domain.ContentAtom, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := topic.Validate(); err != nil {
		return nil, fmt.Errorf("content atom: invalid research topic: %w", err)
	}
	if err := bundle.Validate(bundle.Sources); err != nil {
		return nil, fmt.Errorf("content atom: invalid evidence bundle: %w", err)
	}
	if strings.TrimSpace(result.Statement) == "" || strings.TrimSpace(thesis.Statement) == "" {
		return nil, fmt.Errorf("content atom: judgment and thesis statements are required")
	}

	evidenceIDs, err := citationEvidenceIDs(bundle, result.Citations, thesis.Citations)
	if err != nil {
		return nil, err
	}
	if len(evidenceIDs) == 0 {
		return nil, fmt.Errorf("content atom: at least one sourced citation is required")
	}

	maxAtoms := c.maxAtoms(topic)
	pairs, err := combinations(topic, thesis, c.strategy(), maxAtoms)
	if err != nil {
		return nil, err
	}
	sourceID := stableID("research", struct {
		Topic   string
		Sources []string
	}{Topic: strings.TrimSpace(bundle.Topic), Sources: sourceKeys(bundle.Sources)})
	thesisID := stableID("thesis", struct {
		Statement     string
		DesiredAction string
		CTA           string
		EvidenceIDs   []string
	}{
		Statement: strings.TrimSpace(thesis.Statement), DesiredAction: strings.TrimSpace(thesis.DesiredAction),
		CTA: strings.TrimSpace(thesis.CTA), EvidenceIDs: evidenceIDs,
	})

	atoms := make([]domain.ContentAtom, 0, len(pairs))
	for _, pair := range pairs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		audience := pair.audience
		angle := pair.angle
		claim := strings.TrimSpace(result.Statement)
		insight := strings.TrimSpace(thesis.Statement)
		action := firstNonEmpty(thesis.DesiredAction, topic.DesiredAction)
		cta := firstNonEmpty(thesis.CTA, action)
		hook := buildHook(angle, claim)
		title := buildTitle(angle, insight)
		counterpoint := firstCounterpoint(bundle)
		priority := clampConfidence((result.Confidence + thesis.Confidence) / 2)
		atom := domain.ContentAtom{
			SchemaVersion: domain.ContentAtomSchemaVersion,
			Revision:      1,
			Kind:          domain.ContentAtomJudgment,
			SourceType:    "research",
			SourceID:      sourceID,
			ThesisID:      thesisID,
			Audience:      audience,
			Angle:         angle,
			Hook:          hook,
			Claim:         claim,
			EvidenceIDs:   append([]string(nil), evidenceIDs...),
			CounterPoint:  counterpoint,
			Insight:       insight,
			DesiredAction: action,
			CTA:           cta,
			Priority:      priority,
			Title:         title,
		}
		atom.ID = stableID("atom", struct {
			SourceID    string
			ThesisID    string
			Revision    int
			Audience    string
			Angle       string
			Hook        string
			Claim       string
			EvidenceIDs []string
		}{sourceID, thesisID, atom.Revision, audience, angle, hook, claim, evidenceIDs})
		if err := atom.Validate(); err != nil {
			return nil, fmt.Errorf("content atom: compiled atom %q is invalid: %w", atom.ID, err)
		}
		atoms = append(atoms, atom)
	}
	return atoms, nil
}

type pair struct{ audience, angle string }

func combinations(topic domain.ResearchTopic, thesis judgment.ContentThesis, strategy CombinationStrategy, limit int) ([]pair, error) {
	audiences := append([]string(nil), topic.Audiences...)
	angles := append([]string(nil), topic.Angles...)
	if len(audiences) == 0 {
		audiences = []string{firstNonEmpty(thesis.Audience, "general audience")}
	}
	if len(angles) == 0 {
		angles = []string{firstNonEmpty(thesis.Angle, "core evidence")}
	}
	if limit <= 0 || limit > MaxAtoms {
		return nil, fmt.Errorf("content atom max_atoms must be between 1 and %d", MaxAtoms)
	}
	if strategy != StrategyPairwise && strategy != StrategyCartesian {
		return nil, fmt.Errorf("content atom combination strategy %q is unsupported", strategy)
	}
	pairs := make([]pair, 0, limit)
	if strategy == StrategyCartesian {
		for _, audience := range audiences {
			for _, angle := range angles {
				pairs = append(pairs, pair{audience: strings.TrimSpace(audience), angle: strings.TrimSpace(angle)})
				if len(pairs) == limit {
					return pairs, nil
				}
			}
		}
		return pairs, nil
	}
	count := len(audiences)
	if len(angles) > count {
		count = len(angles)
	}
	if count > limit {
		count = limit
	}
	for i := 0; i < count; i++ {
		pairs = append(pairs, pair{audience: strings.TrimSpace(audiences[i%len(audiences)]), angle: strings.TrimSpace(angles[i%len(angles)])})
	}
	return pairs, nil
}

func (c *Compiler) maxAtoms(topic domain.ResearchTopic) int {
	limit := c.options.MaxAtoms
	if limit <= 0 {
		limit = DefaultMaxAtoms
	}
	if topic.MaxAtoms > 0 && topic.MaxAtoms < limit {
		limit = topic.MaxAtoms
	}
	if limit > MaxAtoms {
		limit = MaxAtoms
	}
	return limit
}

func (c *Compiler) strategy() CombinationStrategy {
	if c == nil || c.options.Strategy == "" {
		return StrategyPairwise
	}
	return c.options.Strategy
}

func citationEvidenceIDs(bundle research.EvidenceBundle, citations ...[]research.Citation) ([]string, error) {
	ids := make(map[string]struct{})
	for _, group := range citations {
		for _, citation := range group {
			if err := bundle.ValidateCitation(citation); err != nil {
				return nil, fmt.Errorf("content atom: citation is not in evidence bundle: %w", err)
			}
			ids[stableID("evidence", citation)] = struct{}{}
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func sourceKeys(sources []research.SourceSnapshot) []string {
	keys := make([]string, 0, len(sources))
	for _, source := range sources {
		keys = append(keys, strings.TrimSpace(source.SourceURL)+"#"+strings.TrimSpace(source.ContentHash))
	}
	sort.Strings(keys)
	return keys
}

func stableID(prefix string, value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return prefix + "_" + hex.EncodeToString(digest[:])[:24]
}

func buildHook(angle, claim string) string {
	angle = strings.TrimSpace(angle)
	claim = firstSentence(claim)
	if angle == "" {
		return claim
	}
	return angle + "：" + claim
}

func buildTitle(angle, insight string) string {
	if strings.TrimSpace(angle) == "" {
		return firstSentence(insight)
	}
	return strings.TrimSpace(angle) + "：" + firstSentence(insight)
}

func firstCounterpoint(bundle research.EvidenceBundle) string {
	if len(bundle.CounterEvidence) == 0 {
		return ""
	}
	return strings.TrimSpace(bundle.CounterEvidence[0].Statement)
}

func firstSentence(value string) string {
	value = strings.TrimSpace(value)
	for _, delimiter := range []string{"。", ".", "!", "！", "?", "？", "\n"} {
		if index := strings.Index(value, delimiter); index >= 0 {
			return strings.TrimSpace(value[:index+len(delimiter)])
		}
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func clampConfidence(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
