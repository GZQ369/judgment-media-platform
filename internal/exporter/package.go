package exporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vertical-intel-video-agent/internal/domain"
)

// ExportProjectFiles 把单项目发布包写成 Markdown/JSON 文件。
func ExportProjectFiles(project domain.VideoProject, dir string, formats []string) ([]string, error) {
	if dir == "" {
		return nil, fmt.Errorf("export dir is required")
	}
	if len(formats) == 0 {
		formats = []string{"markdown", "json"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	paths := []string{}
	for _, format := range normalizeFormats(formats) {
		switch format {
		case "markdown", "md":
			path := filepath.Join(dir, "publish_package.md")
			if err := os.WriteFile(path, []byte(RenderMarkdown(project)), 0o644); err != nil {
				return nil, err
			}
			paths = append(paths, path)
		case "json":
			path := filepath.Join(dir, "publish_package.json")
			payload, err := json.MarshalIndent(project, "", "  ")
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, payload, 0o644); err != nil {
				return nil, err
			}
			paths = append(paths, path)
		default:
			return nil, fmt.Errorf("unsupported export format: %s", format)
		}
	}
	return paths, nil
}

// RenderMarkdown 渲染运营人员可审核的发布包 Markdown。
func RenderMarkdown(project domain.VideoProject) string {
	b := &strings.Builder{}
	offering := project.Offering
	if offering.Name == "" && offering.Title == "" {
		offering = project.Product
	}
	name := fallback(offering.Name, offering.Title)
	fmt.Fprintf(b, "# %s\n\n", project.Name)
	fmt.Fprintf(b, "- 项目 ID：`%s`\n", project.ID)
	fmt.Fprintf(b, "- 平台：%s\n", project.Platform)
	fmt.Fprintf(b, "- 模板：%s\n", project.Template)
	fmt.Fprintf(b, "- 供给类型：%s\n", offering.Kind)
	fmt.Fprintf(b, "- 名称：%s\n", name)
	fmt.Fprintf(b, "- 价格：%s\n", fallback(offering.Price, "未填写"))
	fmt.Fprintf(b, "- 类目：%s\n", fallback(offering.Category, "未填写"))
	fmt.Fprintf(b, "- 目标人群：%s\n\n", fallback(offering.TargetAudience, "未填写"))
	if project.Brief == nil {
		fmt.Fprintf(b, "当前项目还没有生成 brief。\n")
		return b.String()
	}
	brief := project.Brief
	fmt.Fprintf(b, "## 供给分析\n\n")
	fmt.Fprintf(b, "- 核心价值：%s\n", brief.ProductAnalysis.CoreSellingPoint)
	fmt.Fprintf(b, "- 功能价值：%s\n", strings.Join(brief.ProductAnalysis.FunctionalPoints, "、"))
	fmt.Fprintf(b, "- 情绪价值：%s\n", strings.Join(brief.ProductAnalysis.EmotionalPoints, "、"))
	fmt.Fprintf(b, "- 痛点：%s\n", strings.Join(brief.ProductAnalysis.AudiencePainPoints, "、"))
	fmt.Fprintf(b, "- 信任点：%s\n\n", strings.Join(brief.ProductAnalysis.TrustBuilders, "、"))
	fmt.Fprintf(b, "## 内容角度\n\n")
	for _, angle := range brief.Angles {
		fmt.Fprintf(b, "- **%s**：%s（Hook：%s）\n", angle.Name, angle.Description, angle.Hook)
	}
	fmt.Fprintf(b, "\n## 标题候选\n\n")
	for _, title := range brief.TitleOptions {
		fmt.Fprintf(b, "- %s\n", title)
	}
	fmt.Fprintf(b, "\n## 封面文案\n\n")
	for _, cover := range brief.CoverTexts {
		fmt.Fprintf(b, "- %s\n", cover)
	}
	fmt.Fprintf(b, "\n## 脚本版本\n\n")
	for _, script := range brief.Scripts {
		fmt.Fprintf(b, "### %s｜%s\n\n", script.ID, script.Style)
		fmt.Fprintf(b, "- 标题：%s\n", script.Title)
		fmt.Fprintf(b, "- Hook：%s\n", script.Hook)
		fmt.Fprintf(b, "- CTA：%s\n", script.CallToAction)
		fmt.Fprintf(b, "- 预计时长：%d 秒\n\n", script.EstimatedS)
		fmt.Fprintf(b, "```text\n%s\n```\n\n", script.FullScript)
	}
	fmt.Fprintf(b, "## 分镜表\n\n")
	fmt.Fprintf(b, "| 镜头 | 时间 | 画面 | 旁白 | 字幕 | 素材 | 目的 |\n")
	fmt.Fprintf(b, "| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, shot := range brief.Storyboard.Shots {
		fmt.Fprintf(b, "| %d | %d-%d秒 | %s | %s | %s | %s | %s |\n", shot.Index, shot.StartSecond, shot.EndSecond, escapeTable(shot.Visual), escapeTable(shot.Voiceover), escapeTable(shot.Subtitle), escapeTable(shot.AssetHint), escapeTable(shot.Purpose))
	}
	fmt.Fprintf(b, "\n## 素材计划\n\n")
	writeList(b, "可用素材", brief.MaterialPlan.AvailableAssets)
	writeList(b, "需要补充", brief.MaterialPlan.NeededAssets)
	writeList(b, "补拍清单", brief.MaterialPlan.ShootList)
	writeList(b, "AI 素材想法", brief.MaterialPlan.AIAssetIdeas)
	fmt.Fprintf(b, "## 配音文案\n\n```text\n%s\n```\n\n", brief.VoiceoverText)
	fmt.Fprintf(b, "## 字幕文本\n\n```text\n%s\n```\n\n", brief.SubtitleText)
	fmt.Fprintf(b, "## 发布正文和标签\n\n%s\n\n", brief.Caption)
	fmt.Fprintf(b, "标签：%s\n\n", strings.Join(brief.Tags, "、"))
	fmt.Fprintf(b, "## A/B 测试变体\n\n")
	if len(project.Variants) == 0 {
		fmt.Fprintf(b, "当前项目还没有生成变体。\n\n")
	} else {
		fmt.Fprintf(b, "| 变体 | 标题 | 封面 | Hook | CTA | 差异点 |\n")
		fmt.Fprintf(b, "| --- | --- | --- | --- | --- | --- |\n")
		for _, variant := range project.Variants {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n", variant.ID, escapeTable(variant.Title), escapeTable(variant.CoverText), escapeTable(variant.Hook), escapeTable(variant.CallToAction), escapeTable(variant.Differentiator))
		}
		fmt.Fprintf(b, "\n")
	}
	fmt.Fprintf(b, "## QA 结果\n\n")
	fmt.Fprintf(b, "- 是否通过：%t\n", brief.QA.Passed)
	fmt.Fprintf(b, "- 分数：%d\n", brief.QA.Score)
	writeList(b, "问题", brief.QA.Issues)
	writeList(b, "建议", brief.QA.Advice)
	fmt.Fprintf(b, "## 人工审核 checklist\n\n")
	checklist := []string{
		"确认标题没有夸大承诺或违禁表达。",
		"确认封面文案和平台语气匹配。",
		"补齐素材计划中的缺失镜头。",
		"从 10 个变体中选择 2-3 个进入首轮测试。",
		"发布后回填播放、点赞、收藏、评论和完播率。",
	}
	for _, item := range checklist {
		fmt.Fprintf(b, "- [ ] %s\n", item)
	}
	return b.String()
}

func normalizeFormats(formats []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, format := range formats {
		format = strings.ToLower(strings.TrimSpace(format))
		if format == "" {
			continue
		}
		if !seen[format] {
			seen[format] = true
			out = append(out, format)
		}
	}
	if len(out) == 0 {
		return []string{"markdown", "json"}
	}
	return out
}

func writeList(b *strings.Builder, title string, items []string) {
	fmt.Fprintf(b, "### %s\n\n", title)
	if len(items) == 0 {
		fmt.Fprintf(b, "- 暂无\n\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(b, "- %s\n", item)
	}
	fmt.Fprintf(b, "\n")
}

func escapeTable(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", "<br>"), "|", "\\|")
}

func fallback(v, fb string) string {
	if strings.TrimSpace(v) == "" {
		return fb
	}
	return v
}
