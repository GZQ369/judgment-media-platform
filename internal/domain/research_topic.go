package domain

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	// DefaultResearchTopicMaxAtoms keeps an unconfigured topic intentionally
	// small. A caller must opt into more content atoms explicitly.
	DefaultResearchTopicMaxAtoms = 1
	MaxResearchTopicAtoms        = 16
)

// ResearchTopic is the input contract for source-grounded judgment.
// It contains editorial constraints only; platform, media, and publishing
// concerns belong to the distribution layer.
type ResearchTopic struct {
	Topic         string   `json:"topic"`
	SourceURLs    []string `json:"source_urls"`
	Audiences     []string `json:"audiences,omitempty"`
	Angles        []string `json:"angles,omitempty"`
	DesiredAction string   `json:"desired_action"`
	MaxAtoms      int      `json:"max_atoms,omitempty"`
}

// Validate checks the topic before any model or source work begins.
func (t ResearchTopic) Validate() error {
	if strings.TrimSpace(t.Topic) == "" {
		return fmt.Errorf("research topic topic is required")
	}
	if len(t.SourceURLs) == 0 {
		return fmt.Errorf("research topic source_urls must contain at least one URL")
	}
	seenURLs := make(map[string]struct{}, len(t.SourceURLs))
	for _, raw := range t.SourceURLs {
		raw = strings.TrimSpace(raw)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("research topic source URL must be an absolute HTTPS URL: %q", raw)
		}
		if _, exists := seenURLs[raw]; exists {
			return fmt.Errorf("research topic source URL is duplicated: %q", raw)
		}
		seenURLs[raw] = struct{}{}
	}
	if strings.TrimSpace(t.DesiredAction) == "" {
		return fmt.Errorf("research topic desired_action is required")
	}
	if t.MaxAtoms < 0 || t.MaxAtoms > MaxResearchTopicAtoms {
		return fmt.Errorf("research topic max_atoms must be between 0 and %d", MaxResearchTopicAtoms)
	}
	if err := validateNonEmptyUnique("audience", t.Audiences); err != nil {
		return err
	}
	if err := validateNonEmptyUnique("angle", t.Angles); err != nil {
		return err
	}
	return nil
}

func validateNonEmptyUnique(kind string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("research topic %ss must not contain empty values", kind)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("research topic %ss must be unique: %q", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
