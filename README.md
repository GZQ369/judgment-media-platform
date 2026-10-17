# Judgment Media Platform

本项目是一个本地化内容生产与分发工作流系统，当前以短视频为 MVP，面向抖音 / 小红书 / 视频号矩阵运营、电商商家、MCN、小型内容团队和知识内容创作者。

> 以 `ContentAtom` 为正式内容契约，将 Research/Judgment 与可复用的 Content Distribution Pipeline 解耦；主链由 Go、确定性 workflow 和可选的 OpenAI-compatible gateway 组成。tRPC-Agent-Go 已用于受约束的 OpenMontage 渲染执行，不代表主内容 workflow 已成为自主规划 Agent。

本项目以 Apache License 2.0 开源，见 [LICENSE](LICENSE)。第三方运行时、模型和媒体素材不随仓库提供，使用前请查看[开源与外部依赖说明](docs/OPEN_SOURCE.md)。

## 开源快速启动

需要 Go 1.23 或更新版本。默认可在不配置模型凭据的情况下运行 API 和确定性 workflow：

```bash
go test ./...
go run ./cmd/server
```

服务默认监听 `http://127.0.0.1:8088`，可用 `GET /health` 检查。运行数据写入本地 `data/`，不需要预先准备仓库中不存在的环境文件。

Research/Judgment parent pipeline 需要至少一个 `https://` 来源 URL，以及已配置的 OpenAI-compatible gateway；`AI_GATEWAY_BASE_URL` 和 `AI_GATEWAY_MODEL` 必须同时设置，API key 视服务要求配置为 `AI_GATEWAY_API_KEY`。不配置 gateway 时 API 仍可运行，但 Research pipeline 会报告能力不可用，不会假装完成研究。

可选 Vue 3 + Vite 看板位于 `web/`，需要本机 Node.js/npm；它通过 Vite 开发代理访问 Go API，且不包含在 Docker 镜像中。Docker 仅提供 Go API，不包含看板或完整媒体渲染依赖。详见[开源说明](docs/OPEN_SOURCE.md)。

## 边界

当前优先聚焦本地短视频生产、发布包生成和矩阵批量测试。长期分发层保留向 YouTube、Podcast、Newsletter、X 等格式和平台扩展的能力。

## 解决的问题

真实商业短视频不是“生成一个片段”就结束，而是一整条链路：

```text
商品/服务/能力输入
  -> 卖点与人群分析
  -> 内容角度规划
  -> 脚本生成
  -> 人工审核
  -> 分镜设计
  -> 素材规划
  -> 配音
  -> 字幕
  -> 剪辑/渲染
  -> 质检
  -> 平台适配
  -> 发布包导出
  -> 批量变体测试
```

本项目要把这条链路做成本地可运行、可扩展、可接本地模型的 Agent workflow。

## MVP 切入点

第一版只做一个单一模板：

> 淘宝 / 1688 / 手动商品信息 -> 小红书种草视频发布包

输入：

- 商品标题
- 商品链接，可先手动填写
- 商品图片或素材目录
- 卖点
- 价格
- 目标人群
- 视频时长，默认 30-60 秒
- 平台，先支持小红书，后续扩展抖音和视频号

输出：

- 商品卖点分析
- 内容角度
- 5 个标题
- 3 个封面文案
- 3 个脚本版本
- 分镜表
- 素材需求表
- 配音文案
- 字幕文本
- 小红书发布正文和标签
- 10 个 A/B 测试变体
- Markdown / JSON 发布包

MVP 阶段采用“AI 生成 + 人工确认”的半自动模式，避免全自动质量失控。

## 技术路线

- 后端：Go + net/http 起步，不使用 FastAPI
- Agent workflow：`internal/workflow` 和 `internal/agentkit` 提供确定性主流程；Research/Judgment parent pipeline 可配置 OpenAI-compatible gateway。`internal/mediaworker` 已将 tRPC-Agent-Go GraphAgent/Runner 用于受约束的 OpenMontage 渲染执行；主内容 workflow 并非自主规划 Agent
- 本地模型：Ollama / OpenAI-compatible endpoint，可插拔
- 配音：Edge TTS 起步，后续可接 GPT-SoVITS / CosyVoice
- 字幕：SRT 起步，后续支持 ASS 花字字幕
- 渲染：当前保留 OpenMontage 可选路径；Docker 不安装 OpenMontage、Node.js、Python 或 FFmpeg
- 存储：新的 Distribution Run 使用配置数据目录中的 SQLite；旧项目 / batch API 仍使用内存 store 并将发布包写入文件
- 前端：`web/` 是独立的 Vue 3 + Vite 运行看板，当前通过开发代理访问 Go API，尚未打进 Docker 镜像

## OpenMontage 参考点

OpenMontage 是 agentic video production system，强调从自然语言需求到研究、脚本、素材、配音、字幕、剪辑和最终合成的端到端流程。

本项目不复制 OpenMontage 的通用视频生产方向，而是借鉴它的 pipeline 思想，并垂直化到电商短视频矩阵场景：

- 从 reference / prompt 变成商品信息 / 商品链接 / 素材目录
- 从通用视频 pipeline 变成小红书 / 抖音 / 视频号模板
- 从单条成片变成批量变体和 A/B 测试
- 从开放素材搜索变成本地素材库 + 商品素材 + 可选 AI 生成素材

## Agent 实现状态

当前代码并未接入真实 tRPC-Agent-Go。现有节点使用确定性 fallback，确保没有模型或 API key 时也能测试和运行：

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

主内容 workflow 后续仍可逐步采用 tRPC-Agent-Go 的 Graph、Tool、Session、Memory、Eval 和 Artifact 能力；当前已接入的渲染 GraphAgent 是有界执行适配，不是自主角色动画系统。

## 目标内容边界

```text
Research -> Evidence -> Judgment -> Content Thesis -> ContentAtom
  -> Content Distribution Pipeline -> Render -> Publish -> Analytics
```

`ContentAtom`、`DistributionRequest` 和 `DistributionRun` 已作为正式异步分发契约落地。Research/Judgment parent pipeline 已能在配置 gateway 时编排 `ResearchTopic -> Judgment -> ContentAtom[] -> DistributionRun[]`；未配置真实 gateway 或模型不可用时会如实进入 capability blocker。Offering、Research Report、Member Brief、Manual Content 或 Campaign 都可以先转换为 `ContentAtom`，再复用分发链路。Judgment 不直接生成平台专属脚本或调用 Publisher；完整的 Research -> Judgment -> ContentAtom -> 媒体生产 -> 多平台真实发布 -> Analytics 自动闭环尚未完成。

### 异步 Run API

```text
POST /api/v1/runs
GET  /api/v1/runs
GET  /api/v1/runs/{id}
GET  /api/v1/runs/{id}/events
POST /api/v1/runs/{id}/cancel
POST /api/v1/runs/{id}/resume
POST /api/v1/runs/{id}/retry
```

Run 记录、事件和 artifact manifest 持久化到 SQLite。Offering Run 可以生成本地发布包；配置 Media Worker 后可继续到 `media_ready`。Publication Orchestrator 只接受已登记的 `media_ready` artifact；抖音提交需要官方凭据或本机 Ego bridge，小红书和 Bilibili 提交需要 Ego bridge。草稿、媒体就绪或未知结果不会被伪造成已发布。

## 兼容项目 API

以下为保留的项目接口示例；异步分发与发布的主要入口见上文的 Run API 及 Publication Orchestrator 文档。

```text
GET  /health
GET  /api/v1/templates
POST /api/v1/projects
GET  /api/v1/projects
GET  /api/v1/projects/{id}
POST /api/v1/projects/{id}/workflow/run
POST /api/v1/projects/{id}/brief
POST /api/v1/projects/{id}/variants
POST /api/v1/projects/{id}/voiceover
POST /api/v1/projects/{id}/subtitles
POST /api/v1/projects/{id}/render
POST /api/v1/projects/{id}/export
GET  /api/v1/publishing/platforms
POST /api/v1/projects/{id}/publish
POST /api/v1/projects/{id}/variants/{variant_id}/metrics
```

## 运行当前后端

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
cd /d/agent/judgment-media-platform
go test ./...
go run ./cmd/server
```

如果使用系统 Go，也可以：

```bash
export PATH="$PATH:/c/Program Files/Go/bin"
```

默认服务地址：

```text
http://127.0.0.1:8088
```

健康检查：

```bash
curl http://127.0.0.1:8088/health
```

## 开发优先级

1. 项目纠偏：清理旧的非视频定位，重写 README 和架构文档
2. 技术框架：定义 workflow、domain、API、目录结构和 tRPC-Agent-Go 适配层
3. 小红书商品种草模板：生成 brief、脚本、分镜、素材计划、发布文案
4. 本地 Web 控制台：浏览器里创建项目和查看生成结果
5. 批量变体：同一商品生成多套标题、封面、hook、脚本
6. 配音 + 字幕：Edge TTS + SRT
7. FFmpeg 成片：输出 9:16 MP4
8. tRPC-Agent-Go 正式接入：GraphAgent、tools、memory、eval、telemetry

## 当前状态

当前代码已经具备可运行的后端 MVP 主路径，以及一条持久化的异步 Run 主路径：

- Go `net/http` API
- SQLite-backed `DistributionRun` ledger、事件流、幂等键和 cancel/resume/retry 操作
- `ContentAtom` / `DistributionRequest` / `DistributionRun` 类型，以及 Offering 兼容输入
- 视频项目、商品/服务/能力 Offering、脚本、分镜、素材计划、发布包、发布状态、A/B 变体和 batch job domain
- `internal/workflow` 确定性节点
- `internal/agentkit` 确定性 GraphAgent / Tool / Artifact 适配边界，占位而非真实 tRPC-Agent-Go runtime
- 小红书与抖音平台模板差异化
- 同步 batch 运行、Markdown/JSON 文件导出和 batch report
- OpenMontage 可选渲染路径；Docker 仅支持 API 和无渲染 workflow，不含完整渲染依赖
- `internal/publication` 已接入异步 Run 的受控发布事务；Ego adapter 是小红书和 Bilibili 当前的提交路径，也是抖音未配置官方 adapter 时的 fallback。小红书 `XHS_SHARE_APP_KEY` 只支持旧 publisher contract 的客户端交接，不能完成一键发布，也不会被选为一次性授权 commit 的 adapter。真实线上投稿尚未核验。旧项目直发接口保留兼容代码但正在禁用，不能绕过 Publication Orchestrator

后续重点是配置并验证真实 Research/Judgment 模型、tRPC-Agent-Go 隔离适配器、平台 OAuth 与审核回查，以及跨平台真实投稿闭环。Analytics 已有默认关闭的 opt-in follow-up worker：`ANALYTICS_POLL_ENABLED=false`；启用后只从本地 publication ledger durable discovery reserved publication，并按 `15s` tick 做只读 `GetForAnalytics`（默认 review `10m`、metrics `1h`、metrics window `168h`）。它不自动查询外部平台审核、不发布；metrics 仅在本地已验证 `published`、ID 匹配且配置了 Douyin provider 时轮询。被能力阻塞的 metrics 会按 capped schedule 重新检查，配置完成后可以恢复；XHS/Bili unavailable，live metrics 仍未验证。手动 sync 不变。Media Worker、Publication Orchestrator、Legacy Offering Adapter、Ego 与 Bilibili 适配代码已经存在，但不等于真实平台投稿已验证。

## Windows Browser Draft Runtime

个人创作者可通过独立的 Windows 浏览器运行时，把现有视频发布包准备到抖音、小红书和 Bilibili 的创作者页面；视频号尚未接入当前 Publication Orchestrator。Ego adapter 默认先准备草稿，只有服务端一次性授权、精确 fingerprint 和第二次确认都通过后才允许最终发布。它复用专用浏览器登录态，安装与安全边界见 [WINDOWS_BROWSER_BRIDGE.md](docs/modules/WINDOWS_BROWSER_BRIDGE.md)。

## Docker

Docker 以非 root 用户运行 API；Compose 默认把宿主机 `127.0.0.1:8088` 映射到容器的 `8088` 端口，而容器内固定监听 `0.0.0.0:8088`，确保端口转发可用。若明确需要局域网访问，启动前显式设置 `HTTP_BIND_ADDRESS=0.0.0.0`；这会把 API 暴露给局域网，仍需按部署环境配置防火墙、反向代理和发布 API 鉴权。镜像不包含 OpenMontage、Node.js、Python、FFmpeg，也不包含平台账号授权，因此只保证健康检查和无渲染确定性 workflow。本机当前没有 Docker 命令，Docker 验收依赖 CI 或具备 Docker 的环境。

```bash
docker compose up --build
curl http://127.0.0.1:8088/health
```

Compose 固定向容器注入 `ADDR=0.0.0.0:8088`，不读取本地 `ADDR` 或 `CONTAINER_ADDR`，避免容器误监听自身回环地址。宿主绑定通过 `HTTP_BIND_ADDRESS` 控制，默认是 `127.0.0.1`；局域网部署可显式使用 `HTTP_BIND_ADDRESS=0.0.0.0 docker compose up --build`。所有 Compose 环境变量都有默认值，不要求本地存在 `.env`；可复制 `.env.example` 后按需覆盖。

## Publication Safety

Publication mutation endpoints require a server-side `PUBLICATION_API_TOKEN` and a `Bearer` token. The dashboard keeps the token only in page memory and never writes it to a run or export artifact. Authorization and final commit are separate confirmations. The commit is bound to an exact fingerprint covering the selected video, every platform cover asset, title/tags/copy, account, platform, run, variant and policy; any change invalidates preparation.

If an adapter times out or returns insufficient evidence, the publication becomes `unknown`. It cannot be retried by calling commit again; only read-oriented `reconcile` may query the saved external reference. A publication is shown as submitted/reviewing/published only when platform-specific evidence includes a bound external ID or allow-listed URL. A draft click, `READY` signal, generic success text, or `media_ready` status is not proof of publication.

## 批量视频工厂 MVP

后端保留旧的同步 batch 主路径，并新增持久化异步 Run 主路径：

```text
GET  /api/v1/batches
POST /api/v1/batches/run
GET  /api/v1/batches/{id}
```

请求样例见 `smoke/batch_products_request.json`。服务启动后可运行：

```bash
bash -n smoke/run_batch_smoke.sh
bash smoke/run_batch_smoke.sh
```

如果当前 Windows 环境没有 Git Bash / MSYS，可以运行同等 HTTP smoke：

```powershell
powershell -ExecutionPolicy Bypass -File smoke/run_batch_smoke.ps1
```

Bash 版本会优先使用 `python` / `python3` 解析响应；如果 Git Bash 环境没有 Python，会自动回退到 PowerShell。

默认 `render=false` 是稳定主路径，会在本地写出：

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

`render=true` 是 OpenMontage 增强路径，失败时只记录 item error，不阻断发布包生成。无 API key、无外部模型时，系统仍通过 deterministic workflow 生成 brief、脚本、分镜、素材计划、10 个变体和 batch report。

更多说明见 `docs/BATCH_VIDEO_FACTORY.md`。

## 文档入口

建议先从 `docs/INDEX.md` 进入。核心文档：

- `docs/INDEX.md`：总索引和阅读顺序。
- `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`：产品目标、用户、MVP 边界和平台差异化。
- `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md`：给 Codex 目标模式使用的工程任务书。
- `docs/ARCHITECTURE.md`：整体架构和 tRPC-Agent-Go / OpenMontage 映射。
- `docs/API_REFERENCE.md`：当前 HTTP API、请求字段和示例。
- `docs/DATA_MODEL.md`：domain 数据模型、状态和输出路径。
- `docs/DEVELOPMENT.md`：本地开发规范、命令和目录职责。
- `docs/ACCEPTANCE_CHECKLIST.md`：人工 / Codex 验收清单。
- `docs/OPERATIONS_SMOKE.md`：启动服务和 smoke 验证步骤。
- `docs/BATCH_VIDEO_FACTORY.md`：批量视频工厂使用说明。

模块文档索引：

- `docs/modules/WORKFLOW.md`
- `docs/modules/AGENTKIT_TRPC_AGENT_GO.md`
- `docs/modules/TOOLS.md`
- `docs/modules/EXPORTER.md`
- `docs/modules/BATCH_ENGINE.md`
- `docs/modules/API_BATCH.md`
- `docs/modules/OPENMONTAGE_RENDER.md`
- `docs/modules/PUBLISHING.md`
