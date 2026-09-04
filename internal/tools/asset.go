package tools

import (
	"strings"

	"vertical-intel-video-agent/internal/domain"
)

// NormalizeAssetPaths 去掉空素材路径，保留用户给出的本地素材引用。
func NormalizeAssetPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// MissingAssetHints 根据素材数量给出补拍和补图建议。
func MissingAssetHints(productTitle string, assets []string) []string {
	hints := []string{
		"商品 3-5 秒竖屏特写",
		"真实使用场景 8-12 秒",
		"使用前/后或痛点对比图",
		"包装、细节、尺寸或核心功能近景",
	}
	if len(NormalizeAssetPaths(assets)) >= 3 {
		return []string{"补一段" + productTitle + "真实使用过程", "补一张可做封面的竖图"}
	}
	return hints
}

// MissingOfferingAssetHints 根据供给类型生成素材缺口，避免服务和能力沿用商品实拍要求。
func MissingOfferingAssetHints(offering domain.OfferingInput) []string {
	if offering.Kind == domain.OfferingKindProduct || offering.Kind == "" {
		return MissingAssetHints(offering.Title, offering.Assets)
	}
	hints := []string{
		"真实问题或输入场景 3-5 秒",
		"服务过程或能力执行录屏 8-12 秒",
		"执行前/后结果对比",
		"案例、交付物或结果证明近景",
	}
	if len(NormalizeAssetPaths(offering.Assets)) >= 3 {
		return []string{"补一段" + offering.Title + "真实执行过程", "补一张可做封面的结果对比竖图"}
	}
	return hints
}
