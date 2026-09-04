package workflow

import "context"

// ExportPackagerNode 标记发布包和结构化产物的 artifact 名称。
type ExportPackagerNode struct{}

func (ExportPackagerNode) Name() string      { return "ExportPackager" }
func (ExportPackagerNode) Inputs() []string  { return []string{"PublishingBrief", "Variant[]"} }
func (ExportPackagerNode) Outputs() []string { return []string{"ArtifactPaths"} }

// Run 记录导出层将要写出的标准产物名称，实际文件写入由 exporter 完成。
func (ExportPackagerNode) Run(_ context.Context, state *WorkflowState) error {
	state.Artifacts["publish_package_markdown"] = "publish_package.md"
	state.Artifacts["publish_package_json"] = "publish_package.json"
	state.Artifacts["brief_json"] = "brief.json"
	state.Artifacts["variants_json"] = "variants.json"
	return nil
}
