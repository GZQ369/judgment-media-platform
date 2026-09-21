package judgment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/research"
)

func TestServiceReturnsCapabilityBlockerWithoutModel(t *testing.T) {
	bundle := testBundle()
	_, _, err := NewService(nil).Evaluate(context.Background(), bundle)
	var blocker *CapabilityBlocker
	if !errors.As(err, &blocker) {
		t.Fatalf("error = %v, want CapabilityBlocker", err)
	}
	if !errors.Is(err, ErrModelNotConfigured) || blocker.Capability != "judgment_model_generation" {
		t.Fatalf("blocker = %#v, error = %v", blocker, err)
	}
}

func TestServiceProducesOnlyJudgmentAndThesisFromEvidenceBundle(t *testing.T) {
	bundle := testBundle()
	citation := research.Citation{SourceURL: bundle.Sources[0].SourceURL, ContentHash: bundle.Sources[0].ContentHash, Quote: "A sourced fact."}
	response, err := json.Marshal(map[string]any{
		"judgment": map[string]any{"statement": "The evidence supports a narrow conclusion.", "citations": []research.Citation{citation}, "confidence": 0.8},
		"thesis":   map[string]any{"statement": "Make the narrow conclusion useful to builders.", "audience": "builders", "angle": "evidence first", "desired_action": "inspect the source", "cta": "read the evidence", "citations": []research.Citation{citation}, "confidence": 0.75},
	})
	if err != nil {
		t.Fatal(err)
	}
	model := &recordingExecutor{response: string(response)}
	gotJudgment, gotThesis, err := NewService(model).Evaluate(context.Background(), bundle)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if gotJudgment.Statement == "" || gotThesis.Statement == "" {
		t.Fatalf("output = %#v %#v", gotJudgment, gotThesis)
	}
	for _, forbidden := range []string{"atom_drafts", "media_render", "publish_request"} {
		if strings.Contains(model.system+model.user, forbidden) {
			t.Fatalf("judgment prompt contains an output contract from distribution: %q", forbidden)
		}
	}
}

func TestServiceRejectsUncitedModelOutput(t *testing.T) {
	bundle := testBundle()
	model := &recordingExecutor{response: `{"judgment":{"statement":"unsupported","confidence":0.8},"thesis":{"statement":"also unsupported","confidence":0.8}}`}
	_, _, err := NewService(model).Evaluate(context.Background(), bundle)
	if !errors.Is(err, research.ErrNoCitations) {
		t.Fatalf("error = %v, want missing citation", err)
	}
}

type recordingExecutor struct {
	system   string
	user     string
	response string
}

func (e *recordingExecutor) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	e.system = systemPrompt
	e.user = userPrompt
	return e.response, nil
}

func testBundle() research.EvidenceBundle {
	snapshot := research.NewSourceSnapshot("https://source.example/article", "text/plain", []byte("A sourced fact."), time.Now().UTC())
	return research.EvidenceBundle{
		Topic:   "A research topic",
		Sources: []research.SourceSnapshot{snapshot},
		Evidence: []research.Evidence{{Statement: "A sourced fact is available.", Citations: []research.Citation{{
			SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: "A sourced fact.",
		}}}},
	}
}
