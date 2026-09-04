package agentkit

// ToolDescriptor 描述一个未来可注册到 tRPC-Agent-Go 的工具。
type ToolDescriptor struct {
	Name        string `json:"name"`
	Package     string `json:"package"`
	Description string `json:"description"`
}

// ToolRegistry 保存 workflow 可用工具的声明。
type ToolRegistry struct {
	Tools []ToolDescriptor `json:"tools"`
}

// DefaultToolRegistry 返回当前 MVP 的工具映射清单。
func DefaultToolRegistry() ToolRegistry {
	return ToolRegistry{Tools: []ToolDescriptor{
		{Name: "parse_product_input", Package: "internal/tools", Description: "规整商品标题、卖点、痛点、人群和场景"},
		{Name: "load_template", Package: "internal/tools", Description: "按平台选择小红书或抖音模板"},
		{Name: "mark_missing_assets", Package: "internal/tools", Description: "根据素材输入生成补拍建议"},
		{Name: "generate_srt", Package: "internal/tools", Description: "把脚本文案拆成字幕行"},
		{Name: "check_platform_style", Package: "internal/tools", Description: "检查平台语气、hook、CTA 和风险表达"},
		{Name: "write_markdown", Package: "internal/exporter", Description: "写出人工可审核发布包 Markdown"},
		{Name: "write_json", Package: "internal/exporter", Description: "写出结构化 JSON 发布包"},
	}}
}
