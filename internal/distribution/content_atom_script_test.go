package distribution

import (
	"context"
	"strings"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/agentkit"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/workflow"
)

func TestContentAtomBilibiliScriptKeepsAtomHookAndAvoidsDuplicatedSections(t *testing.T) {
	atom := domain.ContentAtom{
		Title:        "FDE如何评估AI演示",
		Hook:         "AI演示看起来能跑，怎样确认它真的解决了现场问题？",
		Claim:        "先把现场任务拆成可复现的步骤",
		Insight:      "逐步记录输入、输出和失败条件",
		CounterPoint: "演示成功不能替代真实环境验证",
		EvidenceIDs:  []string{"evidence-1"},
		Audience:     "一线工程师",
		CTA:          "欢迎补充你的验证方法",
	}
	offering := contentAtomOffering(atom)
	if strings.Join(offering.ValuePoints, "|") != strings.Join([]string{atom.Claim, atom.Insight, atom.CounterPoint}, "|") {
		t.Fatalf("content atom points should remain plain text: %#v", offering.ValuePoints)
	}
	state := runBilibiliOffering(t, offering)
	script := state.Scripts[0]
	if script.Hook != atom.Hook {
		t.Fatalf("script hook=%q, want atom hook %q", script.Hook, atom.Hook)
	}
	if strings.Contains(script.Hook, "还在被") || strings.Contains(script.Hook, "困扰？") {
		t.Fatalf("atom hook was replaced by a sales-style template: %q", script.Hook)
	}
	if strings.Contains(script.FullScript, "先拆开核心观点：观点：") || strings.Contains(script.FullScript, "再看证据和推理：洞察：") || strings.Contains(script.FullScript, "最后说明适用边界：边界：") {
		t.Fatalf("section labels are duplicated in script: %s", script.FullScript)
	}
	if len(state.Storyboard.Shots) < 5 {
		t.Fatalf("expected Bilibili storyboard, got %+v", state.Storyboard)
	}
	takeaway := state.Storyboard.Shots[4].Voiceover
	if takeaway == script.Body || len([]rune(takeaway)) > 50 || strings.Contains(takeaway, atom.Claim) || strings.Contains(takeaway, atom.Insight) || strings.Contains(takeaway, atom.CounterPoint) {
		t.Fatalf("shot 5 should be a concise synthesis, got %q", takeaway)
	}
}

func TestLegacyBilibiliProductServiceAndCapabilityTemplatesRemainUnchanged(t *testing.T) {
	tests := []struct {
		name     string
		kind     domain.OfferingKind
		title    string
		wantHook string
	}{
		{name: "product", kind: domain.OfferingKindProduct, title: "旧商品", wantHook: "你是不是也遇到过：旧问题？"},
		{name: "service", kind: domain.OfferingKindService, title: "旧服务", wantHook: "还在被旧问题困扰？旧服务怎么解决。"},
		{name: "capability", kind: domain.OfferingKindCapability, title: "旧能力", wantHook: "还在被旧问题困扰？旧能力怎么解决。"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			offering := domain.OfferingInput{Kind: test.kind, Name: test.title, Title: test.title, Problems: []string{"旧问题"}, ValuePoints: []string{"旧要点一", "旧要点二", "旧要点三"}}
			state := runBilibiliOffering(t, offering)
			if state.Scripts[0].Hook != test.wantHook {
				t.Fatalf("legacy %s hook=%q, want %q", test.name, state.Scripts[0].Hook, test.wantHook)
			}
			if got := state.Storyboard.Shots[4].Voiceover; got != state.Scripts[0].Body {
				t.Fatalf("legacy %s storyboard takeaway changed: got %q, want script body", test.name, got)
			}
		})
	}
}

func runBilibiliOffering(t *testing.T, offering domain.OfferingInput) *workflow.WorkflowState {
	t.Helper()
	project := domain.VideoProject{ID: "bili-content-test", Name: offering.Name, Platform: domain.PlatformBilibili, DurationSeconds: 90, Offering: offering, Product: offering}
	state, err := workflow.NewProductSeedRunner(agentkit.NewDeterministicRunner()).Run(context.Background(), project, workflow.RunOptions{})
	if err != nil {
		t.Fatalf("run Bilibili workflow: %v", err)
	}
	return state
}
