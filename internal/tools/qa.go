package tools

import (
	"strings"

	"vertical-intel-video-agent/internal/domain"
)

// CheckPlatformStyle 对生成内容做确定性平台风格和风险检查。
func CheckPlatformStyle(platform domain.Platform, kind domain.OfferingKind, titles []string, scripts []domain.Script, caption string, materialPlan domain.MaterialPlan) domain.QAReport {
	score := 88
	issues := []string{}
	advice := []string{}
	if len(titles) < 5 {
		score -= 10
		issues = append(issues, "标题候选少于 5 个")
		advice = append(advice, "补足不同角度的标题候选，方便 A/B 测试。")
	}
	if len(scripts) < 3 {
		score -= 10
		issues = append(issues, "脚本版本少于 3 个")
		advice = append(advice, "至少保留痛点、测评和人群筛选三种脚本结构。")
	}
	if len(materialPlan.NeededAssets) == 0 {
		score -= 8
		issues = append(issues, "素材计划缺少补拍建议")
		if kind == domain.OfferingKindProduct || kind == "" {
			advice = append(advice, "发布前补充商品实拍、使用过程和封面竖图。")
		} else {
			advice = append(advice, "发布前补充真实执行过程、结果证明和封面竖图。")
		}
	}
	switch platform {
	case domain.PlatformDouyin:
		if len(scripts) > 0 && !containsAny(scripts[0].Hook, []string{"3秒", "别", "买前", "马上", "直接", "解决", "做到"}) {
			score -= 8
			issues = append(issues, "抖音首条 hook 不够直接")
		}
		if !containsAny(caption, []string{"买前", "评论", "现在", "收藏", "需求", "试用", "咨询"}) {
			score -= 6
			issues = append(issues, "抖音 caption 缺少明确 CTA")
		}
		advice = append(advice, "前 3 秒用强对比或反常识开场，镜头节奏保持更短。")
	default:
		if !containsAny(caption, []string{"真实", "避坑", "收藏", "小红书"}) {
			score -= 6
			issues = append(issues, "小红书 caption 真实感或收藏引导不足")
		}
		advice = append(advice, "脚本保留真实体验语气，避免绝对化承诺。")
	}
	for _, banned := range []string{"绝对有效", "全网第一", "治疗", "治愈", "永久解决", "保证收益"} {
		if containsAny(caption, []string{banned}) {
			score -= 12
			issues = append(issues, "存在风险表达："+banned)
		}
	}
	if score < 0 {
		score = 0
	}
	passed := score >= 70 && len(issues) <= 2
	if len(advice) == 0 {
		advice = append(advice, "发布前检查素材授权、事实依据和真实体验。")
	}
	return domain.QAReport{Passed: passed, Score: score, Issues: issues, Advice: advice}
}

func containsAny(s string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
