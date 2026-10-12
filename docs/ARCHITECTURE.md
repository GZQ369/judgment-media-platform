# 架构说明

本项目是本地化 Agentic 短视频生产工作流系统，面向小红书 / 抖音 / 视频号 / 电商矩阵运营。

核心目标：用 Go + tRPC-Agent-Go 实现一个可观测、可扩展、可人工审核的 Agent workflow，把 OpenMontage 式端到端视频生产能力垂直化到商品种草、工具测评、知识内容和矩阵批量测试场景。

当前实现状态：主业务 workflow 仍以确定性节点执行，不是自主规划或自主决策 Agent。`internal/mediaworker` 已真实接入 tRPC-Agent-Go GraphAgent/Runner，用于受约束的 OpenMontage 渲染执行；每个 attempt 创建新的 tRPC session，checkpoint 记录图内执行状态，重启后的恢复和重试由 runstore 驱动，不承诺从图中某个节点续跑。这不代表整个视频生产 workflow 已迁移为 tRPC，也不代表模型自主规划已实现。当前可运行链路由 Go `net/http`、Engine、确定性 workflow、本地工具、导出器、可选渲染器和既有发布适配器组成；新的异步 Distribution Run 由 SQLite ledger 驱动。

## 架构原则

1. 本地优先
   - 优先支持本地运行、本地文件、本地素材、本地模型。
   - 外部 API 只能作为可选增强，不作为 MVP 必需条件。

2. 工作流优先，不做单点工具
   - 不是只生成脚本，也不是只生成视频片段。
   - 核心是从商品/选题到发布包的完整生产链路。

3. 人工审核节点内置
   - 商品信息、卖点、脚本、分镜、素材、成片都要允许人工确认和编辑。
   - MVP 不追求不可控的全自动生成。

4. 模板化和批量测试
   - 一个商品要生成多个标题、封面、hook、脚本和发布包。
   - 支持小红书 / 抖音 / 视频号不同平台风格。

5. tRPC-Agent-Go 渐进式接入
   - 先实现确定性 workflow，让无模型环境也能跑通。
   - 再把节点升级为 tRPC-Agent-Go Agent / Graph / Tool / Memory / Eval。

## ContentAtom Contract Boundary

长期内容链路为：

```text
Research -> Evidence / Contradiction / Historical Context -> Judgment
  -> Content Thesis -> ContentAtom
  -> Content Distribution Pipeline -> Render -> Publish -> Analytics
```

`ContentAtom` 是 Judgment 层和内容分发层之间的正式 Go 契约，`DistributionRequest` 是异步入口，`DistributionRun` 是持久化执行记录。它使 Research/Judgment 的内部结构与现有视频生产、渲染、平台发布能力解耦；Offering、Research Report、Member Brief、Manual Content 或 Campaign 也可以先转换为 ContentAtom，再复用分发链路。

当前边界是刻意不完整的：Offering 输入仍通过兼容路径生成确定性发布包；Research 需要 ResearchTopic 显式提供 `SourceURLs`，并配置可用的文本 gateway 才能形成证据结果，基于关键词自动发现来源尚未实现。Media Worker 和 publication adapter 已由 Go Run 接线，但只有运行时配置完整时才会执行。因此 `package_ready` 只代表发布包就绪，`media_ready` 才代表媒体产物已登记，二者都不代表已经投稿。Analytics 的持久化、API、调度和能力查询已存在；平台 metrics 当前仅提供 Douyin 实际 provider，使用仍需有效配置与凭证，小红书和 Bilibili 注册为 `UnavailableProvider`，视频号 metrics 尚未接入。其余平台不可视为已具备指标采集能力。新 publication 事务入口支持抖音、小红书和 Bilibili，视频号尚未接入该入口。

正式架构决策见 [`docs/adr/0001-content-atom-boundary.md`](adr/0001-content-atom-boundary.md)。

## 总体结构

```text
web console
  -> REST API
  -> workflow runner
  -> agent nodes
  -> local tools
  -> storage / assets / exports
```

对应目录：

```text
cmd/server/                 # Go 服务入口
internal/api/               # HTTP API
internal/domain/            # 视频项目、Offering、脚本、分镜、素材、发布包、发布状态和测试变体
internal/workflow/          # 视频生产 workflow 状态机和节点接口
internal/agentkit/          # 确定性业务 workflow 的 agentkit 兼容层；不代表自主规划
internal/mediaworker/       # 真实 tRPC-Agent-Go GraphAgent 渲染执行适配器
internal/tools/             # 商品规整、模板、QA、素材和字幕工具
internal/exporter/          # Markdown / JSON 发布包
internal/render/            # OpenMontage 可选渲染适配
internal/publisher/         # 平台发布注册表和官方适配器
internal/pipeline/          # Research/Judgment 父任务、ContentAtom 分发子 Run 协调和持久化
internal/distribution/      # 异步 Distribution facade 和单进程 worker
internal/runstore/          # SQLite runs / events / artifacts ledger、lease/fencing 和 RetryOnce 子库回执
internal/research/          # source reader、证据采集和 OpenAI-compatible gateway adapter
internal/publication/       # 持久化授权、发布、未知态和 reconcile 事务核心
internal/egopublisher/      # Ego Lite draft/publish/reconcile adapter with durable references
internal/analytics/         # 审核/指标快照、同步任务、调度和查询
internal/platforminsights/  # Douyin metrics provider；小红书/Bilibili 为 UnavailableProvider
internal/store/             # 旧项目 / batch API 的内存存储
web/                        # Vue 3 + Vite 运行和发布看板，不随 API 镜像部署
docs/                       # 架构和技术规划
data/                       # 本地运行时数据
```

`internal/storage`、`internal/assets`、`internal/tts`、`internal/subtitle`、`internal/qa`、`templates/` 以及 `data/projects/`、`data/assets/`、`data/exports/` 仍是后续演进方向或运行时目录，不代表当前已经存在的独立 Go 包或固定目录。

## 异步 Distribution Run

```text
POST /api/v1/runs
  -> normalize DistributionRequest
  -> SQLite queued Run + idempotency record
  -> single-process worker / lease / fencing
  -> legacy deterministic workflow for Offering
  -> package.json artifact + event ledger
  -> package_ready | media_ready | blocked | failed
  -> Publication candidate -> explicit authorization -> prepare -> commit/reconcile
```

Run API 支持查询、事件流、取消、恢复和重试。SQLite 负责服务重启后的 durable record、artifact manifest、lease 和 fencing；当前部署约束是单个活跃 worker，不能表述成多实例队列。

## Workflow 状态模型

一个视频项目的状态从 `VideoProject` 开始，在 workflow 中逐步补齐：

```text
VideoProject
  OfferingInput(product/service/capability)
  ProductAnalysis
  ContentAngles[]
  Scripts[]
  Storyboards[]
  MaterialPlan
  VoiceoverPlan
  SubtitlePlan
  RenderPlan
  QAReport
  Variants[]
  ExportPackage
  PublicationResult[]
```

## Agent Workflow 节点

第一条工作流：`ProductSeedVideoWorkflow`

```text
ProductAnalyzer
  -> AnglePlanner
  -> ScriptWriter
  -> Director
  -> MaterialPlanner
  -> VoicePlanner
  -> SubtitlePlanner
  -> RenderPlanner
  -> QAReviewer
  -> VariantGenerator
  -> ExportPackager
```

### ProductAnalyzer

输入商品标题、链接、图片、价格、卖点、目标人群，输出结构化卖点和风险表达。

输出：

- 核心卖点
- 功能卖点
- 情绪卖点
- 场景卖点
- 人群痛点
- 信任补强点
- 禁用表达

### AnglePlanner

基于商品分析生成内容角度。

首批角度：

- 痛点解决
- 闺蜜种草
- 低价好物
- 测评对比
- 新手避坑
- 场景演示
- 评论区问题回答

### ScriptWriter

根据平台和角度生成短视频脚本。

脚本结构：

- 3 秒 hook
- 痛点/场景
- 商品出现
- 卖点解释
- 体验/对比
- 信任补强
- CTA

### Director

把脚本拆成可剪辑分镜。

每个镜头包含：

- 时间段
- 画面描述
- 配音
- 字幕
- 素材类型
- 素材提示
- 转场
- 镜头目的

### MaterialPlanner

把分镜映射为素材需求。

输出：

- 已有素材可用项
- 缺失素材
- 建议补拍素材
- 建议 AI 生成素材
- 可替代素材

### VoicePlanner

生成配音文本、断句、语速和音色建议。MVP 使用 Edge TTS。

### SubtitlePlanner

生成 SRT 字幕，后续支持 ASS 花字字幕和关键词高亮。

### RenderPlanner

生成 FFmpeg 渲染计划：9:16、1080x1920、字幕烧录、音频混音、镜头拼接。

### QAReviewer

检查：

- 时长是否符合
- 开头 3 秒是否有钩子
- 字幕是否太长
- 是否有禁用词或夸大承诺
- 素材是否缺失
- 是否符合小红书/抖音/视频号风格

### VariantGenerator

生成批量变体：标题、封面、hook、脚本结构、卖点顺序、CTA、字幕风格。

### ExportPackager

导出发布包：

- script.md
- storyboard.md
- subtitles.srt
- voiceover.txt / voiceover.mp3
- render_plan.json
- title_options.txt
- caption.txt
- tags.txt
- qa_report.json
- metadata.json
- video.mp4，后续阶段生成

## tRPC-Agent-Go 接入现状与设计

`internal/mediaworker` 中的 OpenMontage 渲染适配器已使用 tRPC-Agent-Go GraphAgent、Runner 和 SQLite checkpoint 执行有界的生产/渲染步骤。它承接明确输入与受限任务，不负责自主生成选题、研究或规划整个生产流程。`internal/agentkit` 与主 `ProductSeedVideoWorkflow` 仍以确定性执行为主；下列映射描述的是演进设计，不应当作全部已交付能力。

当前每个 attempt 使用新的 tRPC session。SQLite checkpoint 保存图内执行记录；持久化 Run 状态、重启恢复和重试决策由 `internal/runstore` 驱动，不承诺跨 attempt 从某个 Graph 节点继续执行。

### 阶段 1：兼容式 workflow 接口

先定义本地节点接口：

```go
type WorkflowNode interface {
    Name() string
    Run(ctx context.Context, state *WorkflowState) error
}
```

每个节点提供 deterministic fallback，保证无 LLM 也能运行和测试。

### 阶段 2：模型适配

每个节点支持两种执行器：

```text
deterministic executor
llm executor through Ollama / OpenAI-compatible endpoint
```

优先接本地 Ollama，例如 qwen3:8b。

### 阶段 3：tRPC-Agent-Go GraphAgent

映射关系：

```text
WorkflowNode -> Graph node
Local function -> Tool
Project context -> Session
Brand/platform preference -> Memory
QA rules -> Eval
Generated files -> Artifact
Trace/cost/latency -> OpenTelemetry
```

### 阶段 4：Agent 能力增强

使用 tRPC-Agent-Go：

- GraphAgent：多节点视频生产 workflow
- Tool calling：FFmpeg、TTS、字幕、素材检索、导出
- Memory：品牌语气、账号定位、历史胜出 hook
- Artifact：脚本、字幕、视频、发布包
- Evaluation：脚本质量、平台适配、禁用词、素材缺失
- Observability：节点耗时、失败率、生成质量

## OpenMontage 映射

OpenMontage 的通用流程：

```text
prompt/reference video
  -> analysis
  -> concept/script
  -> scene plan
  -> assets
  -> voice/music/subtitles
  -> composition
  -> validation
  -> final video
```

本项目垂直化后：

```text
product/link/materials
  -> selling point analysis
  -> platform content angle
  -> short-video script
  -> storyboard
  -> material plan
  -> voice/subtitles
  -> FFmpeg/Remotion render plan
  -> platform QA
  -> publishing package + variants
```

重点差异：

- OpenMontage 偏通用创意视频，本项目偏电商/矩阵运营。
- OpenMontage 偏单条生产，本项目强调批量变体和测试回流。
- OpenMontage 可大量使用云端生成，本项目优先本地化和低成本。

Docker 边界：当前 Dockerfile 构建并运行 Go API 服务，提供持久化数据卷和健康检查；它没有打包 Web 控制台，也没有闭合 OpenMontage、Python/Node 运行时、FFmpeg/ffprobe、TTS 模型与素材输入等生产渲染依赖。因此这是 API-only 容器，不是包含控制台和媒体生产依赖的全容器闭环。渲染能力仍需按部署环境单独安装、挂载并配置。

## API 分层

MVP API：

```text
GET  /health
GET  /api/v1/templates
POST /api/v1/projects
GET  /api/v1/projects
GET  /api/v1/projects/{id}
POST /api/v1/projects/{id}/workflow/run
POST /api/v1/projects/{id}/brief
POST /api/v1/projects/{id}/variants
POST /api/v1/projects/{id}/export
```

当前异步 Run API：

```text
POST /api/v1/runs
GET  /api/v1/runs
GET  /api/v1/runs/{id}
GET  /api/v1/runs/{id}/events
POST /api/v1/runs/{id}/cancel|resume|retry
```

旧项目 API 仍保留以下扩展接口：

```text
POST /api/v1/projects/{id}/voiceover
POST /api/v1/projects/{id}/subtitles
POST /api/v1/projects/{id}/render
POST /api/v1/projects/{id}/variants/{variant_id}/metrics
```

## 平台模板

### xiaohongshu_product_seed

小红书商品种草模板。

特点：

- 真实体验感
- 标题更像笔记
- 强调痛点、场景和收藏价值
- 发布文案含标签和评论引导

### douyin_product_seed

抖音商品短视频模板。

特点：

- 前 3 秒强钩子
- 更快节奏
- 强对比、强情绪
- CTA 明确

### wechat_channels_product_seed

视频号商品推荐模板。

特点：

- 更生活化
- 更稳重可信
- 适合私域转化

## 文档和验收体系

完整文档入口见 `docs/INDEX.md`。架构层面需要保持以下文档同步：

- API 或路由变化：同步 `docs/API_REFERENCE.md` 和 `docs/modules/API_BATCH.md`。
- Domain 字段变化：同步 `docs/DATA_MODEL.md`。
- Workflow 节点变化：同步 `docs/modules/WORKFLOW.md`。
- tRPC-Agent-Go 适配变化：同步 `docs/TRPC_AGENT_GO_PLAN.md` 和 `docs/modules/AGENTKIT_TRPC_AGENT_GO.md`。
- OpenMontage / 渲染变化：同步 `docs/OPENMONTAGE_MAPPING.md` 和 `docs/modules/OPENMONTAGE_RENDER.md`。
- 运行方式或 smoke 变化：同步 `docs/OPERATIONS_SMOKE.md`。
- 验收口径变化：同步 `docs/ACCEPTANCE_CHECKLIST.md`。

架构验收的最低命令：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
gofmt -w ./cmd ./internal
bash -n smoke/run_batch_smoke.sh
go test ./...
```

HTTP smoke 还需要启动服务后运行：

```bash
bash smoke/run_batch_smoke.sh
```

## MVP 里程碑

### v0.1 技术框架

- 清理旧定位文档
- 定义 domain / workflow / agentkit / template / render / qa 架构
- 明确 tRPC-Agent-Go 接入边界
- 明确 OpenMontage 能力映射

### v0.2 小红书 Brief Workflow

- 商品输入
- 卖点分析
- 角度规划
- 脚本生成
- 分镜生成
- 素材计划
- 发布包导出

### v0.3 批量视频工厂 MVP

- 3 个以上商品一次性处理
- 同步 batch API
- 每个商品生成发布包和 10 个变体
- batch_report.md/json
- 小红书 / 抖音平台差异化
- render=false 稳定主路径

### v0.4 Web 控制台

- 新建项目
- 新建 batch
- 查看 batch report
- 查看和编辑脚本
- 导出 Markdown / JSON
- 指标回填表单

当前 `web/` 已提供 Vue + Vite 的真实 Run、Research/Distribution 和 Publication 看板，可读取 runs、events、artifacts、候选发布、授权、提交、reconcile 结果和能力阻塞；它尚未接入 Go 服务静态托管，也不会伪造投稿成功。Publication 变更需要 bearer token 和独立确认，`unknown` 只能通过 reconcile 处理。

### v0.5 指标回流和 A/B 复盘

- 手动回填播放、点赞、收藏、评论、完播率
- 对比标题 / hook / 封面 / 脚本结构
- 给下一轮 batch 提供建议

### v0.6 配音字幕

- Edge TTS
- SRT
- ASS 基础样式

### v0.7 FFmpeg / OpenMontage 自动成片

- FFmpeg 9:16 渲染
- 图片/视频素材拼接
- 配音、字幕、背景音乐混合
- OpenMontage 增强路径稳定化

### v0.8 tRPC-Agent-Go GraphAgent 深度增强

- Graph workflow
- Agent tools
- Memory
- Artifact
- Eval
- Observability
