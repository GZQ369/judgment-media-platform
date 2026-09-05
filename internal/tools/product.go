package tools

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// PlatformProfile 描述平台差异化生成时使用的语气、标题和行动建议。
type PlatformProfile struct {
	Platform        domain.Platform
	Template        string
	Audience        string
	Tone            []string
	TitlePatterns   []string
	CoverPatterns   []string
	CallToActions   []string
	Tags            []string
	ShotPace        string
	DefaultDuration int
}

// OfferingVocabulary 控制不同供给类型在脚本中的自然语言表达。
type OfferingVocabulary struct {
	Kind          domain.OfferingKind
	Noun          string
	Action        string
	Decision      string
	DefaultName   string
	DefaultPain   string
	DefaultScenes []string
	DefaultAssets []string
	DefaultCTA    string
}

// VocabularyForOffering 返回商品、服务或能力对应的内容语言。
func VocabularyForOffering(offering domain.OfferingInput) OfferingVocabulary {
	switch offering.Kind {
	case domain.OfferingKindService:
		return OfferingVocabulary{
			Kind:          domain.OfferingKindService,
			Noun:          "服务",
			Action:        "选择",
			Decision:      "是否适合委托",
			DefaultName:   "未命名服务",
			DefaultPain:   "有明确需求，但不知道该如何高效、可靠地完成",
			DefaultScenes: []string{"业务需要专业支持", "团队人手或经验不足", "希望缩短交付周期"},
			DefaultAssets: []string{"服务流程、案例结果或团队素材"},
			DefaultCTA:    "提交你的需求，先确认是否适合合作",
		}
	case domain.OfferingKindCapability:
		return OfferingVocabulary{
			Kind:          domain.OfferingKindCapability,
			Noun:          "能力",
			Action:        "启用",
			Decision:      "能否解决当前问题",
			DefaultName:   "未命名能力",
			DefaultPain:   "任务重复又耗时，希望用更稳定的能力直接解决",
			DefaultScenes: []string{"重复任务自动化", "专业能力补位", "从输入到结果的一站式执行"},
			DefaultAssets: []string{"能力演示、前后对比或结果样例"},
			DefaultCTA:    "描述你的问题，立即试用这项能力",
		}
	default:
		return OfferingVocabulary{
			Kind:          domain.OfferingKindProduct,
			Noun:          "商品",
			Action:        "购买",
			Decision:      "是否值得入手",
			DefaultName:   "未命名商品",
			DefaultPain:   "想买但不知道是否真的适合自己",
			DefaultScenes: []string{"日常使用", "新手第一次购买", "送礼或自用前做功课"},
			DefaultAssets: []string{"商品主图或手动上传素材"},
			DefaultCTA:    "对照你的场景再决定是否入手",
		}
	}
}

// ProfileForPlatform 返回平台默认内容策略。
func ProfileForPlatform(platform domain.Platform) PlatformProfile {
	switch platform {
	case domain.PlatformDouyin:
		return PlatformProfile{
			Platform:        domain.PlatformDouyin,
			Template:        "douyin_product_hook",
			Audience:        "抖音用户",
			Tone:            []string{"短句", "直接", "前3秒强hook", "节奏快", "CTA明确"},
			TitlePatterns:   []string{"别再乱买%s，3秒看懂差别", "%s买前先看，一个动作判断", "这类人真的适合%s", "%s到底怎么选？先看核心点", "想省时间，%s只看这3点"},
			CoverPatterns:   []string{"别再乱买", "3秒看懂", "买前先看", "这类人适合"},
			CallToActions:   []string{"现在对照你的场景选", "评论区说你的预算", "先点收藏，买前再看一遍", "需要清单直接留言"},
			Tags:            []string{"抖音好物", "买前必看", "实用测评"},
			ShotPace:        "fast",
			DefaultDuration: 35,
		}
	default:
		return PlatformProfile{
			Platform:        domain.PlatformXiaohongshu,
			Template:        "xiaohongshu_product_seed",
			Audience:        "小红书用户",
			Tone:            []string{"真实体验", "朋友分享", "避坑", "收藏", "不硬广"},
			TitlePatterns:   []string{"%s到底值不值得买？先看这3点", "买%s之前，我建议你先看完这篇", "%s新手避坑：别只看价格", "被问很多次的%s，真实体验来了", "适合%s的%s，我整理好了"},
			CoverPatterns:   []string{"别乱买，先看这3点", "真实体验，不硬夸", "新手先收藏", "值不值看这里"},
			CallToActions:   []string{"如果你也在纠结，可以先收藏这条，按这3点去选。", "适合的人可以冲，不适合的人也别盲买。", "评论区告诉我你最担心哪一点。", "需要避坑清单可以先收藏。"},
			Tags:            []string{"小红书种草", "好物分享", "新手避坑"},
			ShotPace:        "steady",
			DefaultDuration: 45,
		}
	}
}

// ResolveOffering 优先使用新 Offering 字段，并兼容旧 Product 字段。
func ResolveOffering(offering, product domain.OfferingInput) domain.OfferingInput {
	if hasOfferingInput(offering) {
		return offering
	}
	return product
}

// NormalizeOffering 规整供给输入，保证无 API key fallback 也能产出完整发布包。
func NormalizeOffering(offering domain.OfferingInput, platform domain.Platform) domain.OfferingInput {
	profile := ProfileForPlatform(platform)
	if offering.Kind == "" {
		offering.Kind = domain.OfferingKindProduct
	}
	vocabulary := VocabularyForOffering(offering)
	offering.Name = DefaultString(offering.Name, offering.Title)
	offering.Name = DefaultString(offering.Name, vocabulary.DefaultName)
	offering.Title = offering.Name
	offering.TargetAudience = DefaultString(offering.TargetAudience, profile.Audience)
	if len(offering.ValuePoints) == 0 {
		offering.ValuePoints = offering.SellingPoints
	}
	offering.ValuePoints = NormalizePoints(offering.ValuePoints)
	offering.SellingPoints = offering.ValuePoints
	if len(offering.Problems) == 0 {
		offering.Problems = offering.PainPoints
	}
	if len(offering.Problems) == 0 {
		offering.Problems = []string{vocabulary.DefaultPain}
	}
	offering.PainPoints = offering.Problems
	if len(offering.UsageScenarios) == 0 {
		offering.UsageScenarios = append([]string(nil), vocabulary.DefaultScenes...)
	}
	if len(offering.Assets) == 0 {
		offering.Assets = append([]string(nil), vocabulary.DefaultAssets...)
	}
	offering.CallToAction = DefaultString(offering.CallToAction, vocabulary.DefaultCTA)
	return offering
}

// NormalizeProduct 兼容旧调用，内部统一进入 Offering 规整逻辑。
func NormalizeProduct(product domain.ProductInput, platform domain.Platform) domain.ProductInput {
	return NormalizeOffering(product, platform)
}

func hasOfferingInput(offering domain.OfferingInput) bool {
	return offering.Kind != "" || strings.TrimSpace(offering.Name) != "" ||
		strings.TrimSpace(offering.Title) != "" || strings.TrimSpace(offering.Description) != "" ||
		len(offering.ValuePoints) > 0 || len(offering.SellingPoints) > 0
}

// NormalizePoints 补齐至少三个卖点，方便脚本和分镜稳定生成。
func NormalizePoints(points []string) []string {
	if len(points) == 0 {
		points = []string{"解决真实痛点", "使用简单", "适合日常高频场景"}
	}
	for len(points) < 3 {
		points = append(points, points[len(points)-1])
	}
	return points
}

// FirstNonEmpty 返回第一个非空字符串，否则使用 fallback。
func FirstNonEmpty(items []string, fallback string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return fallback
}

// DefaultString 在字符串为空时返回 fallback。
func DefaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// SafePoint 安全读取卖点，索引越界时返回最后一个卖点。
func SafePoint(points []string, idx int) string {
	if len(points) == 0 {
		return "适合日常使用"
	}
	if idx < len(points) {
		return points[idx]
	}
	return points[len(points)-1]
}

// CleanTag 把商品名转成平台标签可用的短字符串。
func CleanTag(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "#", "")
	if s == "" {
		return "好物"
	}
	return s
}

// ShortRunes 按 rune 截断中文字符串。
func ShortRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// NewID 生成本地可读的短 ID。
func NewID(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
