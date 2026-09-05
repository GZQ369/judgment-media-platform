package workflow

import (
	"context"
	"fmt"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/tools"
)

// QAReviewerNode 检查平台风格、素材完整度和风险表达。
type QAReviewerNode struct{}

func (QAReviewerNode) Name() string { return "QAReviewer" }
func (QAReviewerNode) Inputs() []string {
	return []string{"TitleOptions", "Script[]", "Caption", "MaterialPlan"}
}
func (QAReviewerNode) Outputs() []string { return []string{"QAReport"} }

// Run 生成确定性 QA 结果。
func (QAReviewerNode) Run(_ context.Context, state *WorkflowState) error {
	state.QA = tools.CheckPlatformStyle(state.Project.Platform, state.Offering.Kind, state.TitleOptions, state.Scripts, state.Caption, state.MaterialPlan)
	return nil
}

// VariantGeneratorNode 生成 A/B 测试变体。
type VariantGeneratorNode struct{}

func (VariantGeneratorNode) Name() string      { return "VariantGenerator" }
func (VariantGeneratorNode) Inputs() []string  { return []string{"PublishingBrief"} }
func (VariantGeneratorNode) Outputs() []string { return []string{"Variant[]"} }

// Run 生成标题、封面、hook、CTA 等维度的变体。
func (VariantGeneratorNode) Run(_ context.Context, state *WorkflowState) error {
	count := state.VariantCount
	if count <= 0 {
		count = 10
	}
	if count > 50 {
		count = 50
	}
	state.Variants = buildVariants(state, count)
	return nil
}

func buildVariants(state *WorkflowState, count int) []domain.Variant {
	xhsHooks := []string{
		fmt.Sprintf("买%s前，先看这3点。", state.Product.Title),
		fmt.Sprintf("我为什么不建议你盲买%s？", state.Product.Title),
		fmt.Sprintf("%s适合谁？不适合谁？", state.Product.Title),
		fmt.Sprintf("同样是%s，差别主要在这里。", state.Product.Title),
		fmt.Sprintf("%s真实体验：优点和小缺点都说。", state.Product.Title),
	}
	douyinHooks := []string{
		fmt.Sprintf("别再乱买%s，先看这3秒。", state.Product.Title),
		fmt.Sprintf("%s买前，马上看这个。", state.Product.Title),
		fmt.Sprintf("3秒判断%s适不适合你。", state.Product.Title),
		fmt.Sprintf("%s不是越贵越好，关键看这点。", state.Product.Title),
		fmt.Sprintf("想省钱买%s，先避开这个坑。", state.Product.Title),
	}
	diffs := []string{"标题角度", "痛点 hook", "封面文案", "脚本结构", "CTA 话术", "人群定位", "卖点顺序", "避坑口吻", "测评口吻", "收藏引导"}
	ctas := []string{"先收藏再慢慢看", "评论区告诉我你最关心哪点", "需要清单可以留言", "适合再买，别盲冲"}
	hooks := xhsHooks
	if state.Offering.Kind != domain.OfferingKindProduct {
		hooks = []string{
			fmt.Sprintf("%s到底能解决什么问题？", state.Offering.Title),
			fmt.Sprintf("用%s前，先确认这3点。", state.Offering.Title),
			fmt.Sprintf("%s适合谁？不适合谁？", state.Offering.Title),
			fmt.Sprintf("同样的问题，用%s前后差在哪？", state.Offering.Title),
			fmt.Sprintf("%s真实流程：结果和边界都说清楚。", state.Offering.Title),
		}
		ctas = []string{state.Offering.CallToAction, "说说你要解决的问题", "需要案例和流程可以留言", "先确认需求，再决定是否使用"}
		diffs = []string{"问题角度", "结果演示", "流程说明", "适用边界", "CTA 话术", "人群定位", "价值顺序", "案例口吻", "效率对比", "咨询引导"}
	}
	if state.Project.Platform == domain.PlatformDouyin {
		if state.Offering.Kind == domain.OfferingKindProduct {
			hooks = douyinHooks
			ctas = []string{"现在对照你的场景选", "评论区说你的预算", "买前先收藏", "需要清单直接留言"}
		}
		diffs = []string{"3秒 hook", "强 CTA", "痛点快切", "错误选择对比", "人群筛选", "卖点顺序", "封面大字", "购买前判断", "评论引导", "短句脚本"}
		if state.Offering.Kind != domain.OfferingKindProduct {
			diffs = []string{"3秒 hook", "结果快切", "问题对比", "执行流程", "人群筛选", "价值顺序", "封面大字", "适用判断", "咨询引导", "短句脚本"}
		}
	}
	variants := make([]domain.Variant, 0, count)
	for i := 0; i < count; i++ {
		script := state.Scripts[i%len(state.Scripts)]
		title := state.TitleOptions[i%len(state.TitleOptions)]
		if i >= len(state.TitleOptions) {
			title = fmt.Sprintf("%s｜版本%d：%s", state.Product.Title, i+1, diffs[i%len(diffs)])
		}
		variants = append(variants, domain.Variant{
			ID:             fmt.Sprintf("var_%02d", i+1),
			ProjectID:      state.Project.ID,
			Title:          title,
			CoverText:      state.CoverTexts[i%len(state.CoverTexts)],
			Hook:           hooks[i%len(hooks)],
			ScriptStyle:    script.Style,
			CallToAction:   ctas[i%len(ctas)],
			Caption:        state.Caption,
			Differentiator: diffs[i%len(diffs)],
			Metrics:        domain.VariantMetric{},
			CreatedAt:      state.Project.UpdatedAt,
		})
	}
	return variants
}
