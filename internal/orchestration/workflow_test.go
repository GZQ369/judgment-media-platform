package orchestration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/contentatom"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/research"
)

func TestJudgmentWorkflowRunsResearchJudgmentAndCompilationInOrder(t *testing.T) {
	topic, bundle, judgmentResult, thesis := workflowFixtures()
	var order []string
	researcher := fakeResearcher{run: func(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error) {
		order = append(order, "research")
		return bundle, nil
	}}
	judge := fakeJudge{run: func(context.Context, research.EvidenceBundle) (judgment.Judgment, judgment.ContentThesis, error) {
		order = append(order, "judgment")
		return judgmentResult, thesis, nil
	}}
	compiler := fakeCompiler{run: func(context.Context, domain.ResearchTopic, research.EvidenceBundle, judgment.Judgment, judgment.ContentThesis) ([]domain.ContentAtom, error) {
		order = append(order, "compile")
		return contentatom.NewCompiler(contentatom.Options{}).Compile(context.Background(), topic, bundle, judgmentResult, thesis)
	}}
	observer := &recordingObserver{}
	result, err := NewJudgmentWorkflow(researcher, judge, compiler, observer).Run(context.Background(), topic)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Atoms) != 1 || len(result.Events) != 8 {
		t.Fatalf("result = %#v", result)
	}
	if want := []string{"research", "judgment", "compile"}; !equalStrings(order, want) {
		t.Fatalf("order = %#v, want %#v", order, want)
	}
	if len(observer.events) != len(result.Events) || result.Events[len(result.Events)-1].Type != EventWorkflowCompleted {
		t.Fatalf("events = %#v", result.Events)
	}
	for index, event := range result.Events {
		if event.Sequence != int64(index+1) || event.Type == "" || event.Stage == "" {
			t.Fatalf("event %d = %#v", index, event)
		}
	}
}

func TestJudgmentWorkflowReturnsResearchCapabilityBlocker(t *testing.T) {
	topic, _, _, _ := workflowFixtures()
	blocker := &CapabilityBlocker{Capability: "research_execution", Reason: "gateway is not configured"}
	result, err := NewJudgmentWorkflow(fakeResearcher{run: func(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error) {
		return research.EvidenceBundle{}, blocker
	}}, fakeJudge{}, fakeCompiler{}, nil).Run(context.Background(), topic)
	if !errors.Is(err, blocker) {
		t.Fatalf("error = %v, want blocker", err)
	}
	if len(result.Events) != 3 || result.Events[2].Type != EventWorkflowBlocked {
		t.Fatalf("events = %#v", result.Events)
	}
}

func TestJudgmentWorkflowReturnsJudgmentCapabilityBlocker(t *testing.T) {
	topic, bundle, _, _ := workflowFixtures()
	blocker := &judgment.CapabilityBlocker{Capability: "judgment_model_generation", Reason: "model is not configured"}
	result, err := NewJudgmentWorkflow(
		fakeResearcher{run: func(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error) { return bundle, nil }},
		fakeJudge{run: func(context.Context, research.EvidenceBundle) (judgment.Judgment, judgment.ContentThesis, error) {
			return judgment.Judgment{}, judgment.ContentThesis{}, blocker
		}},
		fakeCompiler{}, nil,
	).Run(context.Background(), topic)
	if !errors.Is(err, blocker) {
		t.Fatalf("error = %v, want judgment blocker", err)
	}
	if len(result.Events) != 5 || result.Events[4].Type != EventWorkflowBlocked {
		t.Fatalf("events = %#v", result.Events)
	}
}

func TestJudgmentWorkflowRejectsInvalidTopicBeforeDependencies(t *testing.T) {
	called := false
	result, err := NewJudgmentWorkflow(fakeResearcher{run: func(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error) {
		called = true
		return research.EvidenceBundle{}, nil
	}}, fakeJudge{}, fakeCompiler{}, nil).Run(context.Background(), domain.ResearchTopic{Topic: "missing source", DesiredAction: "act"})
	if err == nil || called || len(result.Events) != 1 || result.Events[0].Type != EventWorkflowFailed {
		t.Fatalf("result=%#v err=%v called=%v", result, err, called)
	}
}

type fakeResearcher struct {
	run func(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error)
}

func (f fakeResearcher) Research(ctx context.Context, topic domain.ResearchTopic) (research.EvidenceBundle, error) {
	if f.run == nil {
		return research.EvidenceBundle{}, nil
	}
	return f.run(ctx, topic)
}

type fakeJudge struct {
	run func(context.Context, research.EvidenceBundle) (judgment.Judgment, judgment.ContentThesis, error)
}

func (f fakeJudge) Evaluate(ctx context.Context, bundle research.EvidenceBundle) (judgment.Judgment, judgment.ContentThesis, error) {
	if f.run == nil {
		return judgment.Judgment{}, judgment.ContentThesis{}, nil
	}
	return f.run(ctx, bundle)
}

type fakeCompiler struct {
	run func(context.Context, domain.ResearchTopic, research.EvidenceBundle, judgment.Judgment, judgment.ContentThesis) ([]domain.ContentAtom, error)
}

func (f fakeCompiler) Compile(ctx context.Context, topic domain.ResearchTopic, bundle research.EvidenceBundle, result judgment.Judgment, thesis judgment.ContentThesis) ([]domain.ContentAtom, error) {
	if f.run == nil {
		return nil, nil
	}
	return f.run(ctx, topic, bundle, result, thesis)
}

type recordingObserver struct{ events []Event }

func (r *recordingObserver) Observe(_ context.Context, event Event) error {
	r.events = append(r.events, event)
	return nil
}

func workflowFixtures() (domain.ResearchTopic, research.EvidenceBundle, judgment.Judgment, judgment.ContentThesis) {
	snapshot := research.NewSourceSnapshot("https://example.com/workflow", "text/plain", []byte("Workflow fact."), time.Unix(1700000000, 0).UTC())
	citation := research.Citation{SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: "Workflow fact."}
	bundle := research.EvidenceBundle{
		Topic: "workflow topic", Sources: []research.SourceSnapshot{snapshot},
		Evidence: []research.Evidence{{Statement: "The workflow fact is sourced.", Citations: []research.Citation{citation}}},
	}
	return domain.ResearchTopic{Topic: "workflow topic", SourceURLs: []string{snapshot.SourceURL}, DesiredAction: "inspect it"}, bundle,
		judgment.Judgment{Statement: "The fact supports the judgment.", Citations: []research.Citation{citation}, Confidence: 0.8},
		judgment.ContentThesis{Statement: "Use the fact to guide action.", Audience: "builders", Angle: "evidence", DesiredAction: "inspect it", CTA: "read more", Citations: []research.Citation{citation}, Confidence: 0.8}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
