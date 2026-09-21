// Package orchestration coordinates Research, Judgment, and ContentAtom
// compilation. It deliberately has no dependency on distribution or platform
// execution packages.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/research"
)

type Researcher interface {
	Research(context.Context, domain.ResearchTopic) (research.EvidenceBundle, error)
}

type Judge interface {
	Evaluate(context.Context, research.EvidenceBundle) (judgment.Judgment, judgment.ContentThesis, error)
}

type Compiler interface {
	Compile(context.Context, domain.ResearchTopic, research.EvidenceBundle, judgment.Judgment, judgment.ContentThesis) ([]domain.ContentAtom, error)
}

type Observer interface {
	Observe(context.Context, Event) error
}

type EventType string

const (
	EventWorkflowStarted   EventType = "workflow.started"
	EventResearchStarted   EventType = "research.started"
	EventResearchCompleted EventType = "research.completed"
	EventJudgmentStarted   EventType = "judgment.started"
	EventJudgmentCompleted EventType = "judgment.completed"
	EventCompileStarted    EventType = "content_atom.compile_started"
	EventCompileCompleted  EventType = "content_atom.compile_completed"
	EventWorkflowCompleted EventType = "workflow.completed"
	EventWorkflowBlocked   EventType = "workflow.blocked"
	EventWorkflowFailed    EventType = "workflow.failed"
)

type Event struct {
	Sequence int64          `json:"sequence"`
	Type     EventType      `json:"type"`
	Stage    string         `json:"stage"`
	Payload  map[string]any `json:"payload,omitempty"`
	At       time.Time      `json:"at"`
}

// CapabilityBlocker is returned when a required workflow dependency is not
// configured. It is a capability boundary, not a fabricated result.
type CapabilityBlocker struct {
	Capability string
	Reason     string
}

func (e *CapabilityBlocker) Error() string {
	if e == nil {
		return "orchestration: capability blocked"
	}
	return fmt.Sprintf("orchestration: capability %q blocked: %s", e.Capability, e.Reason)
}

type Result struct {
	Topic    domain.ResearchTopic
	Evidence research.EvidenceBundle
	Judgment judgment.Judgment
	Thesis   judgment.ContentThesis
	Atoms    []domain.ContentAtom
	Events   []Event
}

type JudgmentWorkflow struct {
	Researcher Researcher
	Judge      Judge
	Compiler   Compiler
	Observer   Observer
}

func NewJudgmentWorkflow(researcher Researcher, judge Judge, compiler Compiler, observer Observer) *JudgmentWorkflow {
	return &JudgmentWorkflow{Researcher: researcher, Judge: judge, Compiler: compiler, Observer: observer}
}

func (w *JudgmentWorkflow) Run(ctx context.Context, topic domain.ResearchTopic) (Result, error) {
	result := Result{Topic: topic}
	if err := topic.Validate(); err != nil {
		result.record(EventWorkflowFailed, "topic", map[string]any{"error": err.Error()})
		return result, fmt.Errorf("orchestration: invalid research topic: %w", err)
	}
	observer := wObserver(w)
	if err := result.emit(ctx, observer, EventWorkflowStarted, "workflow", map[string]any{"topic": strings.TrimSpace(topic.Topic)}); err != nil {
		return result, err
	}
	if w == nil || w.Researcher == nil {
		return w.blocked(ctx, result, "research_execution", "researcher is not configured")
	}
	if w.Judge == nil {
		return w.blocked(ctx, result, "judgment_model_generation", "judge is not configured")
	}
	if w.Compiler == nil {
		return w.blocked(ctx, result, "content_atom_compilation", "content atom compiler is not configured")
	}

	if err := result.emit(ctx, w.Observer, EventResearchStarted, "research", map[string]any{"source_count": len(topic.SourceURLs)}); err != nil {
		return result, err
	}
	bundle, err := w.Researcher.Research(ctx, topic)
	if err != nil {
		return w.failed(ctx, result, EventResearchFailedStage(err), "research", err)
	}
	if err := bundle.Validate(bundle.Sources); err != nil {
		return w.failed(ctx, result, EventWorkflowFailed, "research", fmt.Errorf("invalid evidence bundle: %w", err))
	}
	result.Evidence = bundle
	if err := result.emit(ctx, w.Observer, EventResearchCompleted, "research", map[string]any{"evidence_count": len(bundle.Evidence), "source_count": len(bundle.Sources)}); err != nil {
		return result, err
	}

	if err := result.emit(ctx, w.Observer, EventJudgmentStarted, "judgment", nil); err != nil {
		return result, err
	}
	judgmentResult, thesis, err := w.Judge.Evaluate(ctx, bundle)
	if err != nil {
		return w.failed(ctx, result, EventJudgmentFailedStage(err), "judgment", err)
	}
	result.Judgment, result.Thesis = judgmentResult, thesis
	if err := result.emit(ctx, w.Observer, EventJudgmentCompleted, "judgment", map[string]any{"confidence": judgmentResult.Confidence}); err != nil {
		return result, err
	}

	if err := result.emit(ctx, w.Observer, EventCompileStarted, "content_atom", nil); err != nil {
		return result, err
	}
	atoms, err := w.Compiler.Compile(ctx, topic, bundle, judgmentResult, thesis)
	if err != nil {
		return w.failed(ctx, result, EventWorkflowFailed, "content_atom", err)
	}
	if len(atoms) == 0 || len(atoms) > domain.MaxResearchTopicAtoms {
		return w.failed(ctx, result, EventWorkflowFailed, "content_atom", fmt.Errorf("compiler returned %d atoms; expected 1..%d", len(atoms), domain.MaxResearchTopicAtoms))
	}
	for index := range atoms {
		if err := atoms[index].Validate(); err != nil {
			return w.failed(ctx, result, EventWorkflowFailed, "content_atom", fmt.Errorf("atom %d is invalid: %w", index, err))
		}
	}
	result.Atoms = atoms
	if err := result.emit(ctx, w.Observer, EventCompileCompleted, "content_atom", map[string]any{"atom_count": len(atoms)}); err != nil {
		return result, err
	}
	if err := result.emit(ctx, w.Observer, EventWorkflowCompleted, "workflow", map[string]any{"atom_count": len(atoms)}); err != nil {
		return result, err
	}
	return result, nil
}

func (r *Result) emit(ctx context.Context, observer Observer, eventType EventType, stage string, payload map[string]any) error {
	r.Events = append(r.Events, Event{Sequence: int64(len(r.Events) + 1), Type: eventType, Stage: stage, Payload: payload, At: time.Now().UTC()})
	if observer == nil {
		return nil
	}
	return observer.Observe(ctx, r.Events[len(r.Events)-1])
}

func (r *Result) record(eventType EventType, stage string, payload map[string]any) {
	r.Events = append(r.Events, Event{Sequence: int64(len(r.Events) + 1), Type: eventType, Stage: stage, Payload: payload, At: time.Now().UTC()})
}

func (w *JudgmentWorkflow) blocked(ctx context.Context, result Result, capability, reason string) (Result, error) {
	blocker := &CapabilityBlocker{Capability: capability, Reason: reason}
	_ = result.emit(ctx, wObserver(w), EventWorkflowBlocked, "workflow", map[string]any{"capability": capability, "reason": reason})
	return result, blocker
}

func (w *JudgmentWorkflow) failed(ctx context.Context, result Result, eventType EventType, stage string, err error) (Result, error) {
	if errors.As(err, new(*CapabilityBlocker)) || errors.As(err, new(*judgment.CapabilityBlocker)) {
		_ = result.emit(ctx, wObserver(w), EventWorkflowBlocked, stage, map[string]any{"error": err.Error()})
		return result, err
	}
	_ = result.emit(ctx, wObserver(w), eventType, stage, map[string]any{"error": err.Error()})
	return result, err
}

func wObserver(w *JudgmentWorkflow) Observer {
	if w == nil {
		return nil
	}
	return w.Observer
}

func EventResearchFailedStage(err error) EventType {
	if isCapability(err) {
		return EventWorkflowBlocked
	}
	return EventWorkflowFailed
}

func EventJudgmentFailedStage(err error) EventType {
	if isCapability(err) {
		return EventWorkflowBlocked
	}
	return EventWorkflowFailed
}

func isCapability(err error) bool {
	var workflowBlocker *CapabilityBlocker
	var judgmentBlocker *judgment.CapabilityBlocker
	return errors.As(err, &workflowBlocker) || errors.As(err, &judgmentBlocker)
}
