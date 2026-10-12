package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/tools"
)

// ScriptWriterNode 生成标题、封面文案和三个脚本版本。
type ScriptWriterNode struct{}

func (ScriptWriterNode) Name() string     { return "ScriptWriter" }
func (ScriptWriterNode) Inputs() []string { return []string{"ContentAngle[]", "ProductAnalysis"} }
func (ScriptWriterNode) Outputs() []string {
	return []string{"TitleOptions", "CoverTexts", "Script[]", "Caption"}
}

// Run 根据平台模板生成文案，抖音和小红书会有明显差异。
func (ScriptWriterNode) Run(_ context.Context, state *WorkflowState) error {
	product := state.Product
	points := tools.NormalizePoints(state.Offering.ValuePoints)
	audience := product.TargetAudience
	title := product.Title
	if state.Project.Platform == domain.PlatformBilibili {
		writeBilibiliScripts(state, title, audience, points)
		return nil
	}
	if state.Offering.Kind != domain.OfferingKindProduct {
		vocabulary := tools.VocabularyForOffering(state.Offering)
		state.TitleOptions = []string{
			fmt.Sprintf("%s能解决什么问题？先看这3点", title),
			fmt.Sprintf("需要%s前，先确认这件事", title),
			fmt.Sprintf("%s适合谁？真实流程讲清楚", title),
			fmt.Sprintf("同样的问题，用%s前后差在哪", title),
			fmt.Sprintf("给%s的%s选择清单", audience, vocabulary.Noun),
		}
		state.CoverTexts = []string{"3点看懂价值", "真实流程", "适合谁", "结果怎么验证"}
		state.Scripts = []domain.Script{
			makeOfferingScript("script_problem", "problem_solution", state.TitleOptions[0], state.Angles[0].Hook, title, points, state.Offering.CallToAction, 42),
			makeOfferingScript("script_result", "result_demo", state.TitleOptions[3], state.Angles[1].Hook, title, points, state.Offering.CallToAction, 38),
			makeOfferingScript("script_fit", "fit_check", state.TitleOptions[2], state.Angles[3].Hook, title, points, state.Offering.CallToAction, 45),
		}
		platformTag := "解决方案"
		if state.Project.Platform == domain.PlatformDouyin {
			platformTag = "效率工具"
		}
		state.Caption = fmt.Sprintf("%s｜面向%s，重点看结果、流程和适用边界。\n\n%s\n\n#%s #%s #%s", title, audience, strings.Join(points, "｜"), platformTag, vocabulary.Noun, tools.CleanTag(title))
		state.Tags = []string{platformTag, vocabulary.Noun, "真实案例", tools.CleanTag(title)}
		return nil
	}
	if state.Project.Platform == domain.PlatformDouyin {
		state.TitleOptions = []string{
			fmt.Sprintf("别再乱买%s，3秒看懂差别", title),
			fmt.Sprintf("%s买前先看，一个动作判断", title),
			fmt.Sprintf("这类人真的适合%s", title),
			fmt.Sprintf("%s到底怎么选？先看核心点", title),
			fmt.Sprintf("想省时间，%s只看这3点", title),
		}
		state.CoverTexts = []string{"别再乱买", "3秒看懂", "买前先看", "这类人适合"}
		state.Scripts = []domain.Script{
			makeScript("script_fast_hook", "three_second_hook", state.TitleOptions[0], state.Angles[0].Hook, title, points, "现在对照你的场景选，别等买错再后悔。", 32, true),
			makeScript("script_quick_check", "quick_check", state.TitleOptions[1], state.Angles[2].Hook, title, points, "评论区说你的预算，我给你拆选择点。", 35, true),
			makeScript("script_buy_before", "buy_before", state.TitleOptions[3], state.Angles[3].Hook, title, points, "先收藏，买前再看一遍。", 38, true),
		}
		state.Caption = fmt.Sprintf("买%s前先看这3点：%s。适合%s快速判断，评论区说你的使用场景。\n\n#抖音好物 #买前必看 #实用测评 #%s", title, strings.Join(points[:3], " / "), audience, tools.CleanTag(title))
		state.Tags = []string{"抖音好物", "买前必看", "实用测评", tools.CleanTag(title)}
		return nil
	}
	state.TitleOptions = []string{
		fmt.Sprintf("%s到底值不值得买？先看这3点", title),
		fmt.Sprintf("买%s之前，我建议你先看完这篇", title),
		fmt.Sprintf("%s新手避坑：别只看价格", title),
		fmt.Sprintf("被问很多次的%s，真实体验来了", title),
		fmt.Sprintf("适合%s的%s，我整理好了", audience, title),
	}
	state.CoverTexts = []string{"别乱买，先看这3点", "真实体验，不硬夸", "新手先收藏", "值不值看这里"}
	state.Scripts = []domain.Script{
		makeScript("script_pain", "pain_solution", state.TitleOptions[0], state.Angles[0].Hook, title, points, "如果你也在纠结，可以先收藏这条，按这3点去选。", 42, false),
		makeScript("script_bestie", "bestie_seed", state.TitleOptions[3], state.Angles[1].Hook, title, points, "适合的人可以冲，不适合的人也别盲买。", 45, false),
		makeScript("script_review", "review_compare", state.TitleOptions[2], state.Angles[2].Hook, title, points, "买之前对照自己的使用场景，别只看别人种草。", 48, false),
	}
	state.Caption = fmt.Sprintf("%s｜真实体验整理，适合%s先收藏慢慢看。\n\n%s\n\n#小红书种草 #好物分享 #新手避坑 #%s", title, audience, strings.Join(points, "｜"), tools.CleanTag(title))
	state.Tags = []string{"小红书种草", "好物分享", "新手避坑", tools.CleanTag(title)}
	return nil
}

// writeBilibiliScripts keeps the platform's longer explanatory shape while
// still using the same deterministic workflow and output contract. For
// content atoms the three points are explicitly framed as viewpoint, insight,
// and boundary rather than product selling points.
func writeBilibiliScripts(state *WorkflowState, title, audience string, points []string) {
	titleOptions := []string{
		"%s：证据、观点和适用边界",
		"%s到底说明了什么？把依据讲清楚",
		"关于%s，先看这几个关键判断",
		"%s的来龙去脉与限制",
		"一个问题讲清%s",
	}
	state.TitleOptions = make([]string, 0, len(titleOptions))
	for _, pattern := range titleOptions {
		state.TitleOptions = append(state.TitleOptions, tools.ShortRunes(fmt.Sprintf(pattern, title), 80))
	}
	state.CoverTexts = []string{"证据和观点", "先看关键判断", "边界也要讲清", "这件事说明什么"}
	cta := tools.DefaultString(state.Offering.CallToAction, "欢迎在评论区补充你的观察")
	if state.Offering.Kind == domain.OfferingKindProduct {
		state.Scripts = []domain.Script{
			makeScript("script_bili_explainer", "knowledge_explainer", state.TitleOptions[0], state.Angles[0].Hook, title, points, cta, 75, false),
			makeScript("script_bili_compare", "evidence_compare", state.TitleOptions[1], state.Angles[1].Hook, title, points, cta, 90, false),
			makeScript("script_bili_fit", "boundary_check", state.TitleOptions[2], state.Angles[2].Hook, title, points, cta, 85, false),
		}
	} else {
		state.Scripts = []domain.Script{
			makeEvidenceScript("script_bili_explainer", "knowledge_explainer", state.TitleOptions[0], state.Angles[0].Hook, title, points, cta, 75),
			makeEvidenceScript("script_bili_compare", "evidence_compare", state.TitleOptions[1], state.Angles[1].Hook, title, points, cta, 90),
			makeEvidenceScript("script_bili_fit", "boundary_check", state.TitleOptions[2], state.Angles[2].Hook, title, points, cta, 85),
		}
	}
	state.Caption = tools.ShortRunes(fmt.Sprintf("%s｜这期把核心观点、证据和适用边界拆开讲，面向%s。\n\n%s\n\n欢迎理性讨论。", title, audience, strings.Join(points, "\n")), 2000)
	state.Tags = bilibiliTags(title)
}

func makeEvidenceScript(id, style, title, hook, subject string, points []string, cta string, seconds int) domain.Script {
	bodyLines := []string{
		hook,
		"先拆开核心观点：" + tools.SafePoint(points, 0),
		"再看证据和推理：" + tools.SafePoint(points, 1),
		"最后说明适用边界：" + tools.SafePoint(points, 2),
		fmt.Sprintf("关于%s，结论要和证据范围一起理解，不把观点说成普遍事实。", subject),
		cta,
	}
	full := strings.Join(bodyLines, "\n")
	return domain.Script{
		ID:            id,
		Style:         style,
		Title:         title,
		Hook:          hook,
		Body:          strings.Join(bodyLines[1:len(bodyLines)-1], "\n"),
		CallToAction:  cta,
		FullScript:    full,
		VoiceoverText: full,
		SubtitleText:  tools.SubtitleTextFromScript(full),
		EstimatedS:    seconds,
	}
}

func bilibiliTags(title string) []string {
	profile := tools.ProfileForPlatform(domain.PlatformBilibili)
	tags := append([]string(nil), profile.Tags...)
	tags = append(tags, tools.CleanTag(title))
	if len(tags) > 5 {
		tags = tags[:5]
	}
	for i := range tags {
		tags[i] = tools.ShortRunes(tools.CleanTag(tags[i]), 20)
	}
	return tags
}

func makeOfferingScript(id, style, title, hook, offering string, points []string, cta string, seconds int) domain.Script {
	bodyLines := []string{
		hook,
		fmt.Sprintf("先看它解决的核心问题：%s。", points[0]),
		fmt.Sprintf("第二，看执行方式和交付结果：%s。", tools.SafePoint(points, 1)),
		fmt.Sprintf("第三，看适用边界：%s。", tools.SafePoint(points, 2)),
		fmt.Sprintf("%s的价值不在口号，而在过程和结果是否可验证。", offering),
		cta,
	}
	full := strings.Join(bodyLines, "\n")
	return domain.Script{
		ID:            id,
		Style:         style,
		Title:         title,
		Hook:          hook,
		Body:          strings.Join(bodyLines[1:len(bodyLines)-1], "\n"),
		CallToAction:  cta,
		FullScript:    full,
		VoiceoverText: full,
		SubtitleText:  tools.SubtitleTextFromScript(full),
		EstimatedS:    seconds,
	}
}

func makeScript(id, style, title, hook, product string, points []string, cta string, seconds int, fast bool) domain.Script {
	var bodyLines []string
	if fast {
		bodyLines = []string{
			hook,
			fmt.Sprintf("第一，看%s。", points[0]),
			fmt.Sprintf("第二，看%s。", tools.SafePoint(points, 1)),
			fmt.Sprintf("第三，看%s。", tools.SafePoint(points, 2)),
			fmt.Sprintf("%s不是越贵越好，关键是适不适合你。", product),
			cta,
		}
	} else {
		bodyLines = []string{
			hook,
			fmt.Sprintf("我会先看它是不是解决真实问题，而不是只看参数。%s的第一个重点是：%s。", product, points[0]),
			fmt.Sprintf("第二个重点是：%s。这个决定了你会不会愿意长期用。", tools.SafePoint(points, 1)),
			fmt.Sprintf("第三个重点是：%s。细节做得好，体验差别会很明显。", tools.SafePoint(points, 2)),
			"我建议你按自己的使用场景去判断，不要只看别人说好。",
			cta,
		}
	}
	full := strings.Join(bodyLines, "\n")
	return domain.Script{
		ID:            id,
		Style:         style,
		Title:         title,
		Hook:          hook,
		Body:          strings.Join(bodyLines[1:len(bodyLines)-1], "\n"),
		CallToAction:  cta,
		FullScript:    full,
		VoiceoverText: full,
		SubtitleText:  tools.SubtitleTextFromScript(full),
		EstimatedS:    seconds,
	}
}

// DirectorNode 将主脚本拆成可拍摄分镜。
type DirectorNode struct{}

func (DirectorNode) Name() string      { return "Director" }
func (DirectorNode) Inputs() []string  { return []string{"Script[]"} }
func (DirectorNode) Outputs() []string { return []string{"Storyboard"} }

// Run 生成平台化节奏的分镜表。
func (DirectorNode) Run(_ context.Context, state *WorkflowState) error {
	if len(state.Scripts) == 0 {
		return fmt.Errorf("scripts are required before storyboard")
	}
	points := tools.NormalizePoints(state.Offering.ValuePoints)
	state.Storyboard = buildStoryboard(state.Project.Platform, state.Scripts[0], state.Offering, points, state.Project.DurationSeconds)
	return nil
}

func buildStoryboard(platform domain.Platform, script domain.Script, offering domain.OfferingInput, points []string, duration int) domain.Storyboard {
	if duration <= 0 {
		duration = 45
		if platform == domain.PlatformBilibili {
			duration = 90
		}
	}
	product := offering.Title
	if platform == domain.PlatformBilibili {
		takeawayVoiceover := script.Body
		takeawaySubtitle := "按证据范围理解结论"
		if offering.DeliveryMode == "content_atom" {
			takeawayVoiceover = "把观点放回证据范围内理解，不作超出材料的推断。"
			takeawaySubtitle = takeawayVoiceover
		}
		shots := []domain.Shot{
			{Index: 1, StartSecond: 0, EndSecond: 5, Visual: "标题卡 + 问题或结论预告", Voiceover: script.Hook, Subtitle: script.Hook, AssetType: "knowledge_hook", AssetHint: "主题标题卡、原始问题或关键画面", Transition: "直接切入", Purpose: "建立讨论问题"},
			{Index: 2, StartSecond: 5, EndSecond: 20, Visual: "观点拆解与关键词标注", Voiceover: points[0], Subtitle: points[0], AssetType: "claim_breakdown", AssetHint: "观点文字卡、流程图或录屏", Transition: "章节切换", Purpose: "说清核心观点"},
			{Index: 3, StartSecond: 20, EndSecond: 40, Visual: "证据引用与推理过程", Voiceover: tools.SafePoint(points, 1), Subtitle: tools.SafePoint(points, 1), AssetType: "evidence_reference", AssetHint: "证据来源、数据截图或引用卡", Transition: "重点放大", Purpose: "保留可复核依据"},
			{Index: 4, StartSecond: 40, EndSecond: 60, Visual: "反方观点和适用边界", Voiceover: tools.SafePoint(points, 2), Subtitle: tools.SafePoint(points, 2), AssetType: "counterpoint", AssetHint: "限制条件、反例或对比图", Transition: "左右对照", Purpose: "避免过度概括"},
			{Index: 5, StartSecond: 60, EndSecond: duration - 12, Visual: "结论回收与实际场景", Voiceover: takeawayVoiceover, Subtitle: takeawaySubtitle, AssetType: "takeaway", AssetHint: "结论清单、场景演示或章节总结", Transition: "稳定推进", Purpose: "帮助观众应用"},
			{Index: 6, StartSecond: duration - 12, EndSecond: duration, Visual: "评论区讨论入口", Voiceover: script.CallToAction, Subtitle: script.CallToAction, AssetType: "discussion_cta", AssetHint: "评论提示、资料补充或关注引导", Transition: "收束", Purpose: "邀请理性讨论"},
		}
		return domain.Storyboard{ScriptID: script.ID, Shots: shots}
	}
	if offering.Kind != domain.OfferingKindProduct {
		shots := []domain.Shot{
			{Index: 1, StartSecond: 0, EndSecond: 3, Visual: "问题场景 + 结果预告", Voiceover: script.Hook, Subtitle: script.Hook, AssetType: "problem_hook", AssetHint: "真实问题场景或结果对比", Transition: "快速切入", Purpose: "建立需求"},
			{Index: 2, StartSecond: 3, EndSecond: 10, Visual: "展示输入和执行流程", Voiceover: points[0], Subtitle: points[0], AssetType: "workflow_demo", AssetHint: "录屏、流程节点或服务执行现场", Transition: "步骤切换", Purpose: "解释如何解决"},
			{Index: 3, StartSecond: 10, EndSecond: 20, Visual: "关键交付过程", Voiceover: tools.SafePoint(points, 1), Subtitle: tools.SafePoint(points, 1), AssetType: "delivery_process", AssetHint: "交付物、操作过程或团队协作", Transition: "快切", Purpose: "证明可执行"},
			{Index: 4, StartSecond: 20, EndSecond: 32, Visual: "前后结果和适用边界", Voiceover: tools.SafePoint(points, 2), Subtitle: tools.SafePoint(points, 2), AssetType: "result_compare", AssetHint: "案例数据、前后对比或结果样例", Transition: "左右对比", Purpose: "建立信任"},
			{Index: 5, StartSecond: 32, EndSecond: duration, Visual: "需求入口 + 下一步行动", Voiceover: script.CallToAction, Subtitle: script.CallToAction, AssetType: "result_cta", AssetHint: "需求表单、试用入口或咨询方式", Transition: "稳定收尾", Purpose: "行动引导"},
		}
		return domain.Storyboard{ScriptID: script.ID, Shots: shots}
	}
	if platform == domain.PlatformDouyin {
		shots := []domain.Shot{
			{Index: 1, StartSecond: 0, EndSecond: 2, Visual: "强对比封面大字 + 商品快速闪现", Voiceover: script.Hook, Subtitle: script.Hook, AssetType: "fast_hook", AssetHint: "商品主图、错误选择对比或结果画面", Transition: "快速切入", Purpose: "前2秒打断滑走"},
			{Index: 2, StartSecond: 2, EndSecond: 6, Visual: "直接展示核心卖点", Voiceover: points[0], Subtitle: points[0], AssetType: "product_demo", AssetHint: "商品功能近景", Transition: "快切", Purpose: "立刻给理由"},
			{Index: 3, StartSecond: 6, EndSecond: 12, Visual: "痛点错误示范 vs 正确用法", Voiceover: tools.SafePoint(points, 1), Subtitle: tools.SafePoint(points, 1), AssetType: "compare", AssetHint: "左右对比或前后对比", Transition: "跳切", Purpose: "强化差异"},
			{Index: 4, StartSecond: 12, EndSecond: 22, Visual: "适用人群和细节特写", Voiceover: tools.SafePoint(points, 2), Subtitle: tools.SafePoint(points, 2), AssetType: "detail", AssetHint: "手持细节/尺寸/使用结果", Transition: "推近", Purpose: "确认适合谁"},
			{Index: 5, StartSecond: 22, EndSecond: duration, Visual: "明确 CTA + 商品全貌", Voiceover: script.CallToAction, Subtitle: script.CallToAction, AssetType: "result_cta", AssetHint: "商品全貌和评论/收藏引导", Transition: "快切收尾", Purpose: "行动引导"},
		}
		return domain.Storyboard{ScriptID: script.ID, Shots: shots}
	}
	shots := []domain.Shot{
		{Index: 1, StartSecond: 0, EndSecond: 3, Visual: "封面大字 + 商品/痛点快速特写", Voiceover: script.Hook, Subtitle: script.Hook, AssetType: "cover_or_hook", AssetHint: "商品主图、痛点场景或强对比画面", Transition: "快切", Purpose: "前3秒钩子"},
		{Index: 2, StartSecond: 3, EndSecond: 10, Visual: "展示用户痛点或使用前状态", Voiceover: fmt.Sprintf("先看它是否真的解决%s的核心问题。", product), Subtitle: "先看真实痛点", AssetType: "pain_scene", AssetHint: "用户痛点场景/使用前画面", Transition: "直接切", Purpose: "建立需求"},
		{Index: 3, StartSecond: 10, EndSecond: 20, Visual: "商品功能一和使用过程", Voiceover: points[0], Subtitle: points[0], AssetType: "product_demo", AssetHint: "商品实拍操作过程", Transition: "轻微推近", Purpose: "证明核心卖点"},
		{Index: 4, StartSecond: 20, EndSecond: 32, Visual: "细节、对比或第二卖点", Voiceover: tools.SafePoint(points, 1), Subtitle: tools.SafePoint(points, 1), AssetType: "detail_compare", AssetHint: "细节特写/前后对比", Transition: "左右对比", Purpose: "增加信任"},
		{Index: 5, StartSecond: 32, EndSecond: duration, Visual: "结果展示 + 收藏/评论引导", Voiceover: script.CallToAction, Subtitle: script.CallToAction, AssetType: "result_cta", AssetHint: "结果图、收纳后效果、商品全貌", Transition: "淡出", Purpose: "行动引导"},
	}
	return domain.Storyboard{ScriptID: script.ID, Shots: shots}
}
