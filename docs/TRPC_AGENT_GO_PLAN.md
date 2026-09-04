# tRPC-Agent-Go 接入计划

本文记录本项目如何使用 tRPC-Agent-Go 构建本地 Agentic 视频生产 workflow。

参考项目：

- tRPC-Agent-Go: https://github.com/trpc-group/trpc-agent-go

## 为什么使用 tRPC-Agent-Go

tRPC-Agent-Go 是 Go-native agent framework，适合构建生产级 Agent 系统。它提供：

- LLM Agent
- Graph workflow
- Tool calling
- Session / Memory / Artifact
- Knowledge retrieval
- Agent Skills
- Evaluation
- OpenTelemetry observability
- AG-UI / A2A / MCP 集成能力

本项目后端方向是 Go，不使用 FastAPI，因此 tRPC-Agent-Go 适合作为 Agent workflow 编排层。

## 使用边界

tRPC-Agent-Go 不直接替代业务 API。

分层如下：

```text
Browser / Web Console
  -> Go net/http API
  -> Workflow service
  -> tRPC-Agent-Go GraphAgent
  -> Tools: TTS / subtitle / FFmpeg / storage / asset search
  -> Artifacts: script, storyboard, subtitles, render plan, video, export package
```

业务 API 继续由 `internal/api` 提供；Agent 编排能力放在 `internal/agentkit` 和 `internal/workflow`。

## 三阶段接入

### 阶段 1：本地确定性 Workflow

先定义和 tRPC-Agent-Go 兼容的节点模型，但不强依赖 LLM。

```go
type WorkflowNode interface {
    Name() string
    Run(ctx context.Context, state *WorkflowState) error
}
```

好处：

- 没有 API key 也能跑
- 可以稳定测试 API 和数据结构
- 后续节点可以平滑替换为 LLM Agent

### 阶段 2：LLM Executor

为每个节点提供可选 LLM executor：

```text
ProductAnalyzerNode
  deterministic executor
  llm executor

ScriptWriterNode
  deterministic executor
  llm executor
```

默认优先本地 Ollama OpenAI-compatible endpoint。

配置示例：

```text
LLM_PROVIDER=ollama
LLM_BASE_URL=http://127.0.0.1:11434/v1
LLM_MODEL=qwen3:8b
```

### 阶段 3：GraphAgent

把 workflow 映射为 tRPC-Agent-Go GraphAgent。

映射：

| 本项目概念 | tRPC-Agent-Go 概念 |
| --- | --- |
| ProductSeedVideoWorkflow | GraphAgent |
| WorkflowNode | Graph node / Agent |
| FFmpeg / TTS / subtitle / storage | Tool |
| VideoProject | Session state |
| 品牌语气、账号定位、历史胜出版本 | Memory |
| 脚本、分镜、字幕、视频文件 | Artifact |
| 质检规则、平台适配评分 | Evaluation |
| 节点耗时、错误、生成质量 | OpenTelemetry |

## Agent 设计

### ProductAnalyzerAgent

任务：分析商品信息，提取卖点、目标人群、痛点、场景和禁用表达。

Tools：

- parse_product_input
- read_asset_metadata
- extract_text_from_image，后续 OCR

### AnglePlannerAgent

任务：规划内容角度。

输出：

- pain_solution
- bestie_recommendation
- low_price_goodie
- review_compare
- beginner_pitfall
- scene_demo

### ScriptWriterAgent

任务：根据平台和角度写短视频脚本。

Tools：

- load_template
- check_forbidden_words
- estimate_duration

### DirectorAgent

任务：将脚本拆成分镜。

Tools：

- estimate_shot_duration
- match_visual_style

### MaterialPlannerAgent

任务：根据分镜生成素材需求并匹配本地素材。

Tools：

- list_assets
- search_assets
- classify_asset
- mark_missing_assets

### VoicePlannerAgent

任务：生成配音文本和 TTS 参数。

Tools：

- split_voice_lines
- estimate_voice_duration
- synthesize_voice，后续接 Edge TTS

### SubtitlePlannerAgent

任务：生成 SRT / ASS 字幕。

Tools：

- generate_srt
- generate_ass
- highlight_keywords

### RenderPlannerAgent

任务：生成 FFmpeg 渲染计划。

Tools：

- probe_media
- build_ffmpeg_plan
- validate_render_inputs

### QAReviewerAgent

任务：做平台适配和质量检查。

Tools：

- check_platform_style
- check_claim_risk
- check_subtitle_length
- check_material_completeness
- score_hook

### VariantGeneratorAgent

任务：批量生成 A/B 测试版本。

Tools：

- mutate_title
- mutate_hook
- mutate_cover_text
- mutate_script_structure
- assign_variant_id

### ExportPackagerAgent

任务：导出发布包。

Tools：

- write_markdown
- write_json
- write_srt
- zip_export_package

## Graph 路由

基础流程：

```text
START
  -> ProductAnalyzer
  -> AnglePlanner
  -> ScriptWriter
  -> Director
  -> MaterialPlanner
  -> VoicePlanner
  -> SubtitlePlanner
  -> RenderPlanner
  -> QAReviewer
  -> ExportPackager
  -> END
```

带人工审核：

```text
ScriptWriter -> HumanReview(script)
Director -> HumanReview(storyboard)
MaterialPlanner -> HumanReview(materials)
QAReviewer -> if failed then route to repair node
```

批量测试：

```text
ExportPackager
  -> VariantGenerator
  -> parallel render/export variants
  -> MetricsCollector
```

## Tool 目录规划

```text
internal/agentkit/
  runner.go          # tRPC-Agent-Go runner 封装
  graph.go           # ProductSeedVideoWorkflow graph
  agents.go          # agent 构造
  tools.go           # 工具注册
  memory.go          # memory service 适配
  artifacts.go       # artifact 管理

internal/workflow/
  state.go           # WorkflowState
  node.go            # WorkflowNode 接口
  product_seed.go    # 确定性 workflow runner

internal/tools/
  product.go
  template.go
  asset.go
  tts.go
  subtitle.go
  render.go
  qa.go
  export.go
```

## 近期落地顺序

1. 先实现 `internal/workflow` 的确定性节点接口。
2. 重构 API 到 `projects` 和 `workflow/run`。
3. 做 `xiaohongshu_product_seed` 模板。
4. 输出 Markdown / JSON 发布包。
5. 再接 Ollama LLM executor。
6. 最后引入 tRPC-Agent-Go GraphAgent。

## 验收标准

第一阶段技术框架完成时，应满足：

- 代码里没有业务上的非视频生产 workflow。
- workflow 节点名称和输出结构清晰。
- API 能创建项目并运行 brief workflow。
- 无模型情况下可用 deterministic fallback。
- 有模型情况下可切到 LLM executor。
- 文档明确 tRPC-Agent-Go 的 Graph、Tool、Memory、Artifact、Eval、Telemetry 如何映射。
