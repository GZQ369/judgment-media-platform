package domain

import "testing"

func TestResearchTopicValidate(t *testing.T) {
	valid := ResearchTopic{
		Topic:         "source grounded topic",
		SourceURLs:    []string{"https://example.com/article"},
		Audiences:     []string{"builders"},
		Angles:        []string{"evidence first"},
		DesiredAction: "inspect the evidence",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid topic rejected: %v", err)
	}

	for name, topic := range map[string]ResearchTopic{
		"missing topic":   {SourceURLs: valid.SourceURLs, DesiredAction: valid.DesiredAction},
		"missing sources": {Topic: valid.Topic, DesiredAction: valid.DesiredAction},
		"insecure source": {Topic: valid.Topic, SourceURLs: []string{"http://example.com"}, DesiredAction: valid.DesiredAction},
		"empty audience":  {Topic: valid.Topic, SourceURLs: valid.SourceURLs, Audiences: []string{" "}, DesiredAction: valid.DesiredAction},
		"duplicate angle": {Topic: valid.Topic, SourceURLs: valid.SourceURLs, Angles: []string{"same", "same"}, DesiredAction: valid.DesiredAction},
		"too many atoms":  {Topic: valid.Topic, SourceURLs: valid.SourceURLs, DesiredAction: valid.DesiredAction, MaxAtoms: MaxResearchTopicAtoms + 1},
		"missing action":  {Topic: valid.Topic, SourceURLs: valid.SourceURLs},
	} {
		t.Run(name, func(t *testing.T) {
			if err := topic.Validate(); err == nil {
				t.Fatal("Validate() succeeded for invalid topic")
			}
		})
	}
}
