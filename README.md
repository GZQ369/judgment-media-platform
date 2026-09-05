# Judgment Media Platform

本项目是一个本地化内容生产与分发工作流系统，当前以短视频为 MVP，面向抖音 / 小红书 / 视频号矩阵运营、电商商家、MCN、小型内容团队和知识内容创作者。

> 以 `ContentAtom` 为内容契约，将 Research/Judgment 与可复用的 Content Distribution Pipeline 解耦；当前使用 Go 和确定性 workflow，未来接入 tRPC-Agent-Go。

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
- Agent workflow：当前为确定性 `internal/agentkit` / `internal/workflow` 占位实现，tRPC-Agent-Go 为规划中的编排运行时
- 本地模型：Ollama / OpenAI-compatible endpoint，可插拔
- 配音：Edge TTS 起步，后续可接 GPT-SoVITS / CosyVoice
- 字幕：SRT 起步，后续支持 ASS 花字字幕
- 渲染：当前保留 OpenMontage 可选路径；Docker 不安装 OpenMontage、Node.js、Python 或 FFmpeg
- 存储：当前为内存存储并将运行产物写入配置的数据目录，后续接入 SQLite
- 前端：Go 内置静态 Web 控制台起步

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

后续接入真实 tRPC-Agent-Go 时，Graph、Tool、Session、Memory、Eval 和 Artifact 会在不破坏现有视频链路的前提下逐步替换或包裹这些节点。

## 目标内容边界

```text
Research -> Evidence -> Judgment -> Content Thesis -> ContentAtom
  -> Content Distribution Pipeline -> Render -> Publish -> Analytics
```

Judgment 不直接生成平台专属脚本或调用 Publisher。商品、服务、能力以及未来的研究报告、会员简报和活动内容，都应先转换为 `ContentAtom`，再复用同一套分发流程。P0 只记录边界，正式 `ContentAtom` 类型和 facade 属于后续阶段。

## 计划 API

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

当前代码已经具备可运行的后端 MVP 主路径：

- Go `net/http` API
- 视频项目、商品/服务/能力 Offering、脚本、分镜、素材计划、发布包、发布状态、A/B 变体和 batch job domain
- `internal/workflow` 确定性节点
- `internal/agentkit` 确定性 GraphAgent / Tool / Artifact 适配边界，占位而非真实 tRPC-Agent-Go runtime
- 小红书与抖音平台模板差异化
- 同步 batch 运行、Markdown/JSON 文件导出和 batch report
- OpenMontage 可选渲染路径；Docker 仅支持 API 和无渲染 workflow，不含完整渲染依赖
- 抖音官方 API 投稿、小红书官方分享 SDK 交接、视频号官方助手辅助发布，以及可扩展 Publisher 注册表

后续重点是持久化、Judgment/ContentAtom 契约、真实 tRPC-Agent-Go GraphAgent 接入、Content Distribution Pipeline facade、Edge TTS / 字幕增强、FFmpeg 或 OpenMontage 成片稳定化，以及指标回填复盘。

## Docker

Docker 以非 root 用户运行 API，默认监听 `0.0.0.0:8088`，并通过 Compose named volume `video-agent-data` 持久化容器 `/app/data`。镜像不包含 OpenMontage、Node.js、Python、FFmpeg，也不包含平台账号授权，因此只保证健康检查和无渲染确定性 workflow。

```bash
docker compose up --build
curl http://127.0.0.1:8088/health
```

Compose 使用独立的 `CONTAINER_ADDR` 注入容器内的 `ADDR`，因此本地 `.env` 可以继续保留 `ADDR=127.0.0.1:8088`，不会导致容器只监听自身回环地址。所有 Compose 环境变量都有默认值，不要求本地存在 `.env`；可复制 `.env.example` 后按需覆盖。

## 批量视频工厂 MVP

当前后端已新增同步 batch 主路径：

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
