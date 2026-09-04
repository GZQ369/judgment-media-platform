package agentkit

// GraphSpec 描述本项目 workflow 到 tRPC-Agent-Go GraphAgent 的映射。
type GraphSpec struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Nodes       []GraphNodeMapping `json:"nodes"`
	Edges       [][2]string        `json:"edges"`
}

// GraphNodeMapping 描述一个 workflow 节点对应的 Agent/Tool/Artifact 概念。
type GraphNodeMapping struct {
	WorkflowNode string   `json:"workflow_node"`
	AgentName    string   `json:"agent_name"`
	Tools        []string `json:"tools"`
	Artifacts    []string `json:"artifacts"`
	Evaluation   string   `json:"evaluation"`
}

// ProductSeedGraphSpec 返回商品短视频 workflow 的 GraphAgent 映射。
func ProductSeedGraphSpec() GraphSpec {
	nodes := []GraphNodeMapping{
		{WorkflowNode: "ProductAnalyzer", AgentName: "ProductAnalyzerAgent", Tools: []string{"parse_product_input", "read_asset_metadata"}, Artifacts: []string{"product_analysis.json"}, Evaluation: "claim_risk"},
		{WorkflowNode: "AnglePlanner", AgentName: "AnglePlannerAgent", Tools: []string{"load_template"}, Artifacts: []string{"content_angles.json"}, Evaluation: "platform_fit"},
		{WorkflowNode: "ScriptWriter", AgentName: "ScriptWriterAgent", Tools: []string{"load_template", "estimate_duration"}, Artifacts: []string{"scripts.json"}, Evaluation: "hook_score"},
		{WorkflowNode: "Director", AgentName: "DirectorAgent", Tools: []string{"estimate_shot_duration"}, Artifacts: []string{"storyboard.json"}, Evaluation: "shootability"},
		{WorkflowNode: "MaterialPlanner", AgentName: "MaterialPlannerAgent", Tools: []string{"list_assets", "mark_missing_assets"}, Artifacts: []string{"material_plan.json"}, Evaluation: "material_completeness"},
		{WorkflowNode: "VoicePlanner", AgentName: "VoicePlannerAgent", Tools: []string{"split_voice_lines"}, Artifacts: []string{"voiceover.txt"}, Evaluation: "voice_duration"},
		{WorkflowNode: "SubtitlePlanner", AgentName: "SubtitlePlannerAgent", Tools: []string{"generate_srt"}, Artifacts: []string{"subtitles.txt"}, Evaluation: "subtitle_length"},
		{WorkflowNode: "QAReviewer", AgentName: "QAReviewerAgent", Tools: []string{"check_platform_style", "check_claim_risk"}, Artifacts: []string{"qa_report.json"}, Evaluation: "qa_score"},
		{WorkflowNode: "VariantGenerator", AgentName: "VariantGeneratorAgent", Tools: []string{"mutate_title", "assign_variant_id"}, Artifacts: []string{"variants.json"}, Evaluation: "variant_diversity"},
		{WorkflowNode: "ExportPackager", AgentName: "ExportPackagerAgent", Tools: []string{"write_markdown", "write_json"}, Artifacts: []string{"publish_package.md", "publish_package.json"}, Evaluation: "export_completeness"},
	}
	edges := [][2]string{}
	for i := 0; i < len(nodes)-1; i++ {
		edges = append(edges, [2]string{nodes[i].WorkflowNode, nodes[i+1].WorkflowNode})
	}
	return GraphSpec{Name: "ProductSeedVideoWorkflow", Description: "商品短视频发布包确定性 workflow 到 tRPC-Agent-Go GraphAgent 的映射", Nodes: nodes, Edges: edges}
}
