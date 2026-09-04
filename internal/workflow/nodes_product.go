package workflow

import (
	"context"
	"fmt"

	"vertical-intel-video-agent/internal/domain"
	"vertical-intel-video-agent/internal/tools"
)

// ProductAnalyzerNode 分析商品输入并补齐 fallback 信息。
type ProductAnalyzerNode struct{}

func (ProductAnalyzerNode) Name() string      { return "ProductAnalyzer" }
func (ProductAnalyzerNode) Inputs() []string  { return []string{"VideoProject.Product"} }
func (ProductAnalyzerNode) Outputs() []string { return []string{"ProductAnalysis"} }

// Run 生成商品卖点、痛点、信任点和风险表达清单。
func (ProductAnalyzerNode) Run(_ context.Context, state *WorkflowState) error {
	platform := tools.NormalizePlatform(state.Project.Platform)
	state.Project.Platform = platform
	state.Project.Template = tools.NormalizeTemplate(platform, state.Project.Template)
	state.Offering = tools.NormalizeOffering(tools.ResolveOffering(state.Project.Offering, state.Project.Product), platform)
	state.Product = state.Offering
	state.Project.Offering = state.Offering
	state.Project.Product = state.Product
	profile := tools.ProfileForPlatform(platform)
	title := state.Product.Title
	points := tools.NormalizePoints(state.Offering.ValuePoints)
	vocabulary := tools.VocabularyForOffering(state.Offering)
	pain := tools.FirstNonEmpty(state.Offering.Problems, vocabulary.DefaultPain)
	audience := tools.DefaultString(state.Product.TargetAudience, profile.Audience)
	core := fmt.Sprintf("帮%s用更低决策成本判断%s%s", audience, title, vocabulary.Decision)
	emotion := []string{"少走弯路", "更安心", "像真实经验分享而不是硬广"}
	opportunities := []string{"问题开头", "执行前后对比", "选择清单", "真实场景结果"}
	trust := []string{"展示真实过程或结果", "给出适合/不适合人群", "说明边界和客观限制"}
	if platform == domain.PlatformDouyin {
		core = fmt.Sprintf("让%s在前3秒看懂%s的核心价值并完成行动判断", audience, title)
		emotion = []string{"省时间", "少踩坑", "快速判断"}
		opportunities = []string{"强 hook 开场", "问题对比", "核心价值快切", "明确 CTA"}
		trust = []string{"直接展示结果", "给出行动前判断标准", "对比低效做法"}
	}
	state.Analysis = domain.ProductAnalysis{
		CoreSellingPoint:     core,
		FunctionalPoints:     points,
		EmotionalPoints:      emotion,
		ScenarioPoints:       state.Product.UsageScenarios,
		AudiencePainPoints:   []string{pain, fmt.Sprintf("%s信息太多，不知道哪个价值最重要", vocabulary.Noun), "担心投入后无法得到预期结果"},
		TrustBuilders:        trust,
		ForbiddenExpressions: []string{"绝对有效", "全网第一", "治疗/治愈", "保证收益", "永久解决"},
		ContentOpportunities: opportunities,
	}
	return nil
}

// AnglePlannerNode 为商品规划平台化内容角度。
type AnglePlannerNode struct{}

func (AnglePlannerNode) Name() string      { return "AnglePlanner" }
func (AnglePlannerNode) Inputs() []string  { return []string{"ProductAnalysis"} }
func (AnglePlannerNode) Outputs() []string { return []string{"ContentAngle[]"} }

// Run 生成小红书或抖音差异化内容角度。
func (AnglePlannerNode) Run(_ context.Context, state *WorkflowState) error {
	title := state.Product.Title
	vocabulary := tools.VocabularyForOffering(state.Offering)
	pain := tools.FirstNonEmpty(state.Offering.Problems, vocabulary.DefaultPain)
	if state.Offering.Kind != domain.OfferingKindProduct {
		prefix := "先看这个"
		if state.Project.Platform == domain.PlatformDouyin {
			prefix = "3秒看懂"
		}
		state.Angles = []domain.ContentAngle{
			{ID: "problem_solution", Name: "问题解决", Hook: fmt.Sprintf("还在被%s困扰？%s怎么解决。", pain, title), Description: "从真实问题进入，展示解决路径。"},
			{ID: "result_demo", Name: "结果演示", Hook: fmt.Sprintf("%s，%s能做到什么。", prefix, title), Description: "用可验证结果说明价值。"},
			{ID: "workflow", Name: "执行过程", Hook: fmt.Sprintf("把问题交给%s后，会经历哪几步？", title), Description: "公开流程、交付和边界。"},
			{ID: "fit_check", Name: "适用判断", Hook: fmt.Sprintf("%s适不适合你，先确认这3点。", title), Description: "明确适合与不适合的人群。"},
			{ID: "case_proof", Name: "案例证明", Hook: fmt.Sprintf("同样的问题，用%s前后差在哪？", title), Description: "用案例或前后对比建立信任。"},
		}
		return nil
	}
	if state.Project.Platform == domain.PlatformDouyin {
		state.Angles = []domain.ContentAngle{
			{ID: "three_second_hook", Name: "3秒强钩子", Hook: fmt.Sprintf("别再乱买%s，先看这3秒。", title), Description: "直接打断错误购买，快速进入卖点。"},
			{ID: "pain_compare", Name: "痛点对比", Hook: fmt.Sprintf("如果你还在被%s困扰，先看这个。", pain), Description: "用错误做法和正确选择形成对比。"},
			{ID: "quick_check", Name: "快速判断", Hook: fmt.Sprintf("%s适不适合你，一个动作判断。", title), Description: "给用户简单判断标准，促进互动。"},
			{ID: "buy_before", Name: "买前先看", Hook: fmt.Sprintf("买%s前，这3点必须确认。", title), Description: "降低冲动购买和踩坑风险。"},
			{ID: "cta_demo", Name: "行动演示", Hook: fmt.Sprintf("照这个方法选%s，更省时间。", title), Description: "把 CTA 前置为明确动作。"},
		}
		return nil
	}
	state.Angles = []domain.ContentAngle{
		{ID: "pain_solution", Name: "痛点解决", Hook: fmt.Sprintf("你是不是也遇到过：%s？", pain), Description: "先说用户痛点，再给商品解决方案。"},
		{ID: "bestie_seed", Name: "闺蜜种草", Hook: fmt.Sprintf("这个%s我真的想安利给同类人。", title), Description: "像朋友分享真实体验，弱化销售感。"},
		{ID: "review_compare", Name: "测评对比", Hook: fmt.Sprintf("%s值不值得买，主要看这3点。", title), Description: "用理性测评降低决策成本。"},
		{ID: "beginner_pitfall", Name: "新手避坑", Hook: fmt.Sprintf("买%s之前，先避开这几个坑。", title), Description: "用避坑清单提高收藏率。"},
		{ID: "low_price_goodie", Name: "低价好物", Hook: fmt.Sprintf("如果预算有限，%s可以先看这一类。", title), Description: "强调性价比和适用场景。"},
	}
	return nil
}
