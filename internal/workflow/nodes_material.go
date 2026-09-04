package workflow

import (
	"context"

	"vertical-intel-video-agent/internal/domain"
	"vertical-intel-video-agent/internal/tools"
)

// MaterialPlannerNode 根据分镜生成素材需求和补拍建议。
type MaterialPlannerNode struct{}

func (MaterialPlannerNode) Name() string      { return "MaterialPlanner" }
func (MaterialPlannerNode) Inputs() []string  { return []string{"Storyboard", "ProductInput.Assets"} }
func (MaterialPlannerNode) Outputs() []string { return []string{"MaterialPlan"} }

// Run 生成可执行的素材计划。
func (MaterialPlannerNode) Run(_ context.Context, state *WorkflowState) error {
	needed := tools.MissingOfferingAssetHints(state.Offering)
	shootList := []string{"手机竖屏拍商品全貌", "拍一次真实使用过程", "拍一个用户痛点场景", "拍一个结尾收藏/下单提示画面"}
	aiIdeas := []string{"生成平台风格封面背景", "生成场景补充图，但商品主体尽量用真实素材"}
	if state.Offering.Kind != domain.OfferingKindProduct {
		shootList = []string{"录制真实问题和输入", "录制执行流程或服务现场", "展示可验证的交付结果", "拍摄案例前后对比和行动入口"}
		aiIdeas = []string{"生成流程说明背景", "只生成辅助场景，案例结果和能力演示使用真实素材"}
	}
	if state.Project.Platform == domain.PlatformDouyin {
		if state.Offering.Kind == domain.OfferingKindProduct {
			shootList = []string{"拍商品 2 秒快切特写", "拍痛点错误示范", "拍正确用法对比", "拍结尾 CTA 手势或文字画面"}
		} else {
			shootList = []string{"拍问题场景 2 秒快切", "录制能力或服务执行过程", "拍前后结果对比", "拍结尾试用或咨询入口"}
		}
		aiIdeas = []string{"生成强对比封面背景", "生成节奏更快的转场素材占位图"}
	}
	state.MaterialPlan = domain.MaterialPlan{
		AvailableAssets: tools.NormalizeAssetPaths(state.Product.Assets),
		NeededAssets:    needed,
		ShootList:       shootList,
		AIAssetIdeas:    aiIdeas,
	}
	return nil
}

// VoicePlannerNode 规划配音文案。
type VoicePlannerNode struct{}

func (VoicePlannerNode) Name() string      { return "VoicePlanner" }
func (VoicePlannerNode) Inputs() []string  { return []string{"Script[]"} }
func (VoicePlannerNode) Outputs() []string { return []string{"VoiceoverText"} }

// Run 复用主脚本生成配音文本。
func (VoicePlannerNode) Run(_ context.Context, state *WorkflowState) error {
	if len(state.Scripts) > 0 {
		state.VoiceoverText = state.Scripts[0].VoiceoverText
	}
	return nil
}

// SubtitlePlannerNode 规划字幕文本。
type SubtitlePlannerNode struct{}

func (SubtitlePlannerNode) Name() string      { return "SubtitlePlanner" }
func (SubtitlePlannerNode) Inputs() []string  { return []string{"VoiceoverText"} }
func (SubtitlePlannerNode) Outputs() []string { return []string{"SubtitleText"} }

// Run 把配音文本切成短字幕行。
func (SubtitlePlannerNode) Run(_ context.Context, state *WorkflowState) error {
	state.SubtitleText = tools.SubtitleTextFromScript(state.VoiceoverText)
	return nil
}
