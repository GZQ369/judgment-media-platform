package workflow

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/agentkit"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

func TestWorkflowNodesGenerateBriefState(t *testing.T) {
	project := domain.VideoProject{
		ID:              "proj_test",
		Name:            "宠物自动饮水机发布包",
		Platform:        domain.PlatformXiaohongshu,
		Template:        "xiaohongshu_product_seed",
		DurationSeconds: 45,
		Status:          "draft",
		Product: domain.ProductInput{
			Title:          "宠物自动饮水机",
			Price:          "89",
			Category:       "宠物用品",
			SellingPoints:  []string{"静音", "过滤", "可拆洗"},
			PainPoints:     []string{"猫咪不爱喝水"},
			TargetAudience: "养猫新手",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	state, err := NewProductSeedRunner(agentkit.NewDeterministicRunner()).Run(context.Background(), project, RunOptions{VariantCount: 10})
	if err != nil {
		t.Fatalf("workflow failed: %v", err)
	}
	gotOrder := make([]string, 0, len(state.NodeLog))
	for _, node := range state.NodeLog {
		gotOrder = append(gotOrder, node.Name)
		if node.Status != "completed" {
			t.Fatalf("node not completed: %+v", node)
		}
	}
	wantOrder := []string{"ProductAnalyzer", "AnglePlanner", "ScriptWriter", "Director", "MaterialPlanner", "VoicePlanner", "SubtitlePlanner", "QAReviewer", "VariantGenerator", "ExportPackager"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("unexpected node order:\n got: %v\nwant: %v", gotOrder, wantOrder)
	}
	brief := state.Brief()
	if brief.ProjectID != project.ID || len(brief.TitleOptions) < 5 || len(brief.Scripts) < 3 {
		t.Fatalf("brief is incomplete: %+v", brief)
	}
	if len(state.Variants) != 10 {
		t.Fatalf("expected 10 variants, got %d", len(state.Variants))
	}
	if state.Artifacts["publish_package_markdown"] != "publish_package.md" {
		t.Fatalf("export artifact not recorded: %+v", state.Artifacts)
	}
}
