package workflow

import (
	"context"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// ProductSeedRunner 执行商品短视频发布包 workflow。
type ProductSeedRunner struct {
	executor AgentExecutor
}

// NewProductSeedRunner 创建商品短视频 workflow runner。
func NewProductSeedRunner(executor AgentExecutor) *ProductSeedRunner {
	return &ProductSeedRunner{executor: executor}
}

// Run 按固定节点顺序生成 brief，并可选生成 A/B 变体。
func (r *ProductSeedRunner) Run(ctx context.Context, project domain.VideoProject, opts RunOptions) (*WorkflowState, error) {
	state := NewWorkflowState(project, opts.VariantCount)
	nodes := []WorkflowNode{
		ProductAnalyzerNode{},
		AnglePlannerNode{},
		ScriptWriterNode{},
		DirectorNode{},
		MaterialPlannerNode{},
		VoicePlannerNode{},
		SubtitlePlannerNode{},
		QAReviewerNode{},
	}
	if opts.VariantCount > 0 {
		nodes = append(nodes, VariantGeneratorNode{})
	}
	nodes = append(nodes, ExportPackagerNode{})
	for _, node := range nodes {
		if err := runNode(ctx, r.executor, node, state); err != nil {
			return state, err
		}
	}
	return state, nil
}
