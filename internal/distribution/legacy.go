package distribution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// AdaptLegacyOffering turns the public legacy Offering input into the durable
// ContentAtom contract. The stable digest keeps retries idempotent while the
// original request field remains available for compatibility clients.
func AdaptLegacyOffering(offering domain.OfferingInput) (domain.ContentAtom, error) {
	name := firstNonEmpty(offering.Name, offering.Title)
	if name == "" {
		return domain.ContentAtom{}, fmt.Errorf("legacy offering name or title is required")
	}
	b, err := json.Marshal(offering)
	if err != nil {
		return domain.ContentAtom{}, fmt.Errorf("marshal legacy offering: %w", err)
	}
	digest := sha256.Sum256(b)
	stableID := hex.EncodeToString(digest[:])[:16]
	description := firstNonEmpty(offering.Description, firstSlice(offering.ValuePoints), firstSlice(offering.SellingPoints))
	claim := firstNonEmpty(description, name+"提供可验证的解决方案")
	audience := firstNonEmpty(offering.TargetAudience, "待确认目标受众")
	hook := name + "到底解决什么问题？"
	if len(offering.Problems) > 0 && strings.TrimSpace(offering.Problems[0]) != "" {
		hook = "如果你正在面对" + strings.TrimSpace(offering.Problems[0]) + "，这项方案值得先看清楚。"
	}
	atom := domain.ContentAtom{
		SchemaVersion: domain.ContentAtomSchemaVersion,
		Revision:      1,
		ID:            "atom_legacy_offering_" + stableID,
		Kind:          domain.ContentAtomOffering,
		SourceType:    "legacy_offering",
		SourceID:      "offering_" + stableID,
		Audience:      audience,
		Angle:         "problem_solution",
		Hook:          hook,
		Claim:         claim,
		Insight:       description,
		DesiredAction: offering.CallToAction,
		CTA:           offering.CallToAction,
		Title:         name,
		Summary:       description,
		Body:          description,
		Offering:      cloneOffering(offering),
	}
	if err := atom.Validate(); err != nil {
		return domain.ContentAtom{}, err
	}
	return atom, nil
}

func cloneOffering(offering domain.OfferingInput) *domain.OfferingInput {
	copy := offering
	copy.ValuePoints = append([]string(nil), offering.ValuePoints...)
	copy.SellingPoints = append([]string(nil), offering.SellingPoints...)
	copy.Problems = append([]string(nil), offering.Problems...)
	copy.PainPoints = append([]string(nil), offering.PainPoints...)
	copy.UsageScenarios = append([]string(nil), offering.UsageScenarios...)
	copy.Assets = append([]string(nil), offering.Assets...)
	copy.ProofPoints = append([]string(nil), offering.ProofPoints...)
	return &copy
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstNonEmpty(values ...string) string { return first(values...) }
