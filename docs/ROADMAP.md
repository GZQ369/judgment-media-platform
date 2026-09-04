# 开发路线图

## 当前目标

先出技术框架，不急着做完整视频渲染。

第一阶段要把项目从旧的混合定位改成纯视频生产系统，并建立后续接 tRPC-Agent-Go 的工程骨架。

## v0.1：项目纠偏与技术框架

### 目标

- 移除 README 和架构文档里的旧定位内容。
- 明确项目只做本地 Agentic 短视频生产 + 批量测试。
- 给出 Go + tRPC-Agent-Go + OpenMontage-style pipeline 的工程结构。

### 任务

1. 文档
   - README.md
   - docs/ARCHITECTURE.md
   - docs/OPENMONTAGE_MAPPING.md
   - docs/TRPC_AGENT_GO_PLAN.md
   - docs/ROADMAP.md

2. 代码重构准备
   - 废弃旧的非视频生产路由
   - 定义 `VideoProject`
   - 定义 `ProductInput`
   - 定义 `Script`
   - 定义 `Storyboard`
   - 定义 `MaterialPlan`
   - 定义 `Variant`
   - 定义 `ExportPackage`

3. 目录准备
   - internal/workflow
   - internal/agentkit
   - internal/templates
   - internal/render
   - internal/tts
   - internal/subtitle
   - internal/qa
   - web
   - data/projects
   - data/assets
   - data/exports

### 验收

- README 不再把项目描述成情报系统。
- 架构文档只讨论视频生产 workflow。
- 代码可以继续 `go test ./...`。

## v0.2：小红书商品种草 Brief Workflow

### 目标

输入一个商品，生成完整小红书发布包，但暂不自动成片。

### API

```text
POST /api/v1/projects
GET  /api/v1/projects
GET  /api/v1/projects/{id}
POST /api/v1/projects/{id}/workflow/run
POST /api/v1/projects/{id}/brief
POST /api/v1/projects/{id}/export
```

### Workflow

```text
ProductAnalyzer
  -> AnglePlanner
  -> ScriptWriter
  -> Director
  -> MaterialPlanner
  -> QAReviewer
  -> ExportPackager
```

### 输出

- brief.md
- scripts.json
- storyboard.json
- material_plan.json
- publish_package.md

## v0.3：批量视频工厂 MVP

### 目标

把单项目发布包扩展为可批量运行的视频工厂：一次输入多个商品，批量生成项目、发布包、A/B 变体和 batch report。

### 产品目标

- 3 个以上商品一次性处理。
- 15 分钟内得到可审核发布包。
- 每个商品至少 10 个变体。
- 小红书 / 抖音模板有明显平台差异。
- 无 API key 时也能通过 deterministic fallback 产出完整发布包。

### API

```text
GET  /api/v1/batches
POST /api/v1/batches/run
GET  /api/v1/batches/{id}
```

### 工程要求

- `internal/workflow` 成为业务主流程。
- `internal/agentkit` 提供 tRPC-Agent-Go GraphAgent / Tool / Artifact 适配层。
- `internal/exporter` 负责写出 publish package 和 batch report。
- `internal/tools` 承载商品规整、模板、素材、字幕、QA 等可复用工具。
- 每个模块必须有中文 `doc.go` 和 `docs/modules/*.md`。

### 输出

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

### 验收

- `smoke/batch_products_request.json` 至少包含 3 个商品。
- `bash -n smoke/run_batch_smoke.sh` 通过。
- `go test ./...` 通过。
- Batch report 能说明每个商品的状态、发布包路径、失败原因和下一步建议。

## v0.4：本地 Web 控制台

### 目标

打开浏览器即可使用，不依赖 curl。

页面：

- 首页 / 产品说明。
- 新建单项目。
- 新建 batch。
- 项目详情。
- Batch report 浏览。
- 发布包预览。
- 指标回填表单。

## v0.5：指标回流和 A/B 复盘

### 目标

让用户发布后能手动回填数据，并看到哪些标题、hook、封面和脚本结构表现更好。

变体维度：

- 标题
- 封面文案
- 开头 hook
- 脚本结构
- 卖点顺序
- CTA
- 平台
- 发布时间

指标：

- 播放量
- 点赞
- 收藏
- 评论
- 完播率
- 线索/成交备注

## v0.6：配音和字幕

### 目标

生成 voiceover 和 subtitles。

技术：

- Edge TTS
- SRT
- ASS 基础样式

## v0.7：FFmpeg / OpenMontage 自动成片

### 目标

输入商品图片、脚本、配音和字幕，输出 9:16 MP4。

## v0.8：tRPC-Agent-Go GraphAgent 深度增强

### 目标

把 v0.3 的 adapter interface 和确定性 workflow 升级为真实 tRPC-Agent-Go graph workflow，接入模型执行、工具调用、记忆、评估和可观测性。

能力：

- GraphAgent
- Tool calling
- Memory
- Artifact
- Eval
- Telemetry
