# Workflow 模块

## 模块职责

`internal/workflow` 是批量视频工厂的主业务流程层。它把商品输入按节点拆成 ProductAnalyzer、AnglePlanner、ScriptWriter、Director、MaterialPlanner、VoicePlanner、SubtitlePlanner、QAReviewer、VariantGenerator 和 ExportPackager。

## 关键文件

- `internal/workflow/state.go`
- `internal/workflow/node.go`
- `internal/workflow/product_seed.go`
- `internal/workflow/nodes_product.go`
- `internal/workflow/nodes_script.go`
- `internal/workflow/nodes_material.go`
- `internal/workflow/nodes_qa.go`
- `internal/workflow/nodes_export.go`

## 输入 / 输出

输入是 `domain.VideoProject` 和可选 `RunOptions.VariantCount`。输出是 `WorkflowState`，其中包含 `PublishingBrief`、`Variant[]`、节点日志和标准 artifact 名称。

## 调用链路

HTTP API 调用 `internal/engine`，Engine 调用 `workflow.NewProductSeedRunner(...).Run(...)`。Run 按固定顺序执行节点，最后由 `ProjectWithOutputs()` 写回项目。

## 与 tRPC-Agent-Go 的关系

`WorkflowNode` 对齐 GraphAgent node。`WorkflowState` 对齐 Session State。节点产物对应 Artifact。后续可以将某个节点替换为 LLM Agent，而不改变 API 和 exporter。

## 无 API key fallback

所有节点都是确定性 Go 逻辑，不依赖外部模型。商品字段缺失时会补默认卖点、痛点、人群、场景和素材建议。

## 测试方法

运行：

```bash
go test ./internal/workflow
```

重点看 `TestWorkflowNodesGenerateBriefState`，它验证节点顺序、brief 产出、变体数量和 artifact 名称。

## 常见失败与排查

- scripts 为空：检查 ScriptWriter 前的 AnglePlanner 是否执行。
- variants 为空：确认 RunOptions.VariantCount 大于 0。
- 平台差异不明显：检查 `nodes_script.go` 和 `tools.ProfileForPlatform`。
