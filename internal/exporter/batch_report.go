package exporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// ExportBatchReportFiles 写出 batch_report.md 和 batch_report.json。
func ExportBatchReportFiles(job domain.BatchJob, dir string) (string, string, error) {
	if dir == "" {
		return "", "", fmt.Errorf("batch report dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	mdPath := filepath.Join(dir, "batch_report.md")
	jsonPath := filepath.Join(dir, "batch_report.json")
	job.ReportPath = mdPath
	job.ReportJSONPath = jsonPath
	report := BuildBatchReport(job)
	if err := os.WriteFile(mdPath, []byte(RenderBatchReportMarkdown(report)), 0o644); err != nil {
		return "", "", err
	}
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(jsonPath, payload, 0o644); err != nil {
		return "", "", err
	}
	return mdPath, jsonPath, nil
}

// BuildBatchReport 把 BatchJob 转成面向运营审核的报告结构。
func BuildBatchReport(job domain.BatchJob) domain.BatchReport {
	items := make([]domain.BatchReportItem, 0, len(job.Items))
	for _, item := range job.Items {
		next := []string{"打开发布包审核标题、封面、脚本和分镜。", "按素材计划补齐缺失镜头。", "选择 2-3 个变体发布首轮测试。", "发布后回填播放、点赞、收藏、评论和完播率。"}
		if item.Status == domain.BatchStatusFailed {
			next = []string{"查看 error 字段定位失败原因。", "修正供给输入或素材路径后重新运行该条目。"}
		}
		if item.Status == domain.BatchStatusCompletedWithErrors {
			next = append([]string{"发布包已生成，但增强渲染失败；可以先审核发布包。"}, next...)
		}
		items = append(items, domain.BatchReportItem{
			Index:                  item.Index,
			OfferingName:           item.OfferingName,
			ProductTitle:           item.ProductTitle,
			ProjectID:              item.ProjectID,
			Status:                 item.Status,
			Error:                  item.Error,
			PublishPackagePath:     item.PublishPackagePath,
			PublishPackageJSONPath: item.PublishPackageJSONPath,
			RenderVideoPath:        item.RenderVideoPath,
			NextSteps:              next,
		})
	}
	return domain.BatchReport{
		ID:             job.ID,
		Name:           job.Name,
		Status:         job.Status,
		Platform:       job.Platform,
		Template:       job.Template,
		OfferingCount:  len(job.Items),
		ProductCount:   len(job.Items),
		VariantCount:   job.VariantCount,
		Render:         job.Render,
		Items:          items,
		NextSteps:      batchNextSteps(job),
		ExportDir:      job.ExportDir,
		ReportPath:     job.ReportPath,
		ReportJSONPath: job.ReportJSONPath,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
	}
}

// RenderBatchReportMarkdown 渲染 batch report Markdown。
func RenderBatchReportMarkdown(report domain.BatchReport) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "# Batch Report: %s\n\n", report.Name)
	fmt.Fprintf(b, "- Batch ID：`%s`\n", report.ID)
	fmt.Fprintf(b, "- 状态：%s\n", report.Status)
	fmt.Fprintf(b, "- 平台：%s\n", report.Platform)
	fmt.Fprintf(b, "- 模板：%s\n", report.Template)
	fmt.Fprintf(b, "- 供给数：%d\n", report.OfferingCount)
	fmt.Fprintf(b, "- 每项供给变体数：%d\n", report.VariantCount)
	fmt.Fprintf(b, "- render：%t\n", report.Render)
	fmt.Fprintf(b, "- 输出目录：`%s`\n\n", report.ExportDir)
	fmt.Fprintf(b, "## 供给产物\n\n")
	fmt.Fprintf(b, "| # | 供给 | 项目 ID | 状态 | Markdown 发布包 | JSON 发布包 | 错误 |\n")
	fmt.Fprintf(b, "| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, item := range report.Items {
		name := item.OfferingName
		if name == "" {
			name = item.ProductTitle
		}
		fmt.Fprintf(b, "| %d | %s | `%s` | %s | `%s` | `%s` | %s |\n", item.Index, escapeTable(name), item.ProjectID, item.Status, item.PublishPackagePath, item.PublishPackageJSONPath, escapeTable(item.Error))
	}
	fmt.Fprintf(b, "\n## 下一步\n\n")
	for _, step := range report.NextSteps {
		fmt.Fprintf(b, "- %s\n", step)
	}
	fmt.Fprintf(b, "\n## 单项审核建议\n\n")
	for _, item := range report.Items {
		fmt.Fprintf(b, "### %d. %s\n\n", item.Index, item.ProductTitle)
		for _, step := range item.NextSteps {
			fmt.Fprintf(b, "- %s\n", step)
		}
		fmt.Fprintf(b, "\n")
	}
	return b.String()
}

func batchNextSteps(job domain.BatchJob) []string {
	steps := []string{
		"逐个打开 publish_package.md，先审标题、封面文案、脚本和分镜。",
		"按素材计划补齐真实过程、结果证明和封面竖图。",
		"每项供给挑选 2-3 个变体做首轮发布测试。",
		"发布后用变体 ID 回填 views / likes / saves / comments / completion_rate。",
	}
	if job.Render {
		steps = append([]string{"render=true 是增强路径；若渲染失败，先使用已生成的发布包。"}, steps...)
	}
	if job.Status == domain.BatchStatusCompletedWithErrors {
		steps = append([]string{"本 batch 有部分错误，优先查看表格中的 error 字段。"}, steps...)
	}
	return steps
}
