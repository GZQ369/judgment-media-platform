package tools

import "github.com/GZQ369/judgment-media-platform/internal/domain"

// DefaultTemplateForPlatform 返回指定平台的默认 MVP 模板。
func DefaultTemplateForPlatform(platform domain.Platform) string {
	return ProfileForPlatform(platform).Template
}

// NormalizePlatform 在请求未传平台时默认使用小红书。
func NormalizePlatform(platform domain.Platform) domain.Platform {
	if platform == "" {
		return domain.PlatformXiaohongshu
	}
	return platform
}

// NormalizeTemplate 在请求未传模板时按平台选择默认模板。
func NormalizeTemplate(platform domain.Platform, template string) string {
	if template != "" {
		return template
	}
	return DefaultTemplateForPlatform(platform)
}
