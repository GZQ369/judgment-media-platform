# 文档总索引

本文是 Judgment Media Platform / 批量视频工厂的文档入口。新同事、Codex、后续自动化 agent 都应先从这里判断应该读哪份文档。当前实现包含确定性本地 workflow、Research/Judgment parent pipeline、正式 `ContentAtom` 分发契约、Media Worker 和受控 Publication Orchestrator；真实 tRPC-Agent-Go、多 Agent 协商、线上投稿核验和 Analytics 仍未完成。

## 1. 先读哪几份

### 产品和边界

1. `README.md`
   - 项目一句话、当前可运行状态、运行命令、批量视频工厂入口。
2. `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`
   - 产品目标、目标用户、MVP 成功标准、平台差异化规则。
3. `docs/PRD_MVP.md`
   - 早期小红书商品种草发布包 PRD。
4. `docs/PRODUCT_DESIGN.md`
   - 产品设计、参考项目分析、后续扩展方向。

### 工程和架构

1. `docs/ARCHITECTURE.md`
   - 当前实现、目标分层、ContentAtom 边界、目录职责、Docker 与渲染边界。
2. `docs/adr/0001-content-atom-boundary.md`
   - Research -> Judgment -> ContentAtom -> Content Distribution Pipeline 的架构决策。
3. `docs/TRPC_AGENT_GO_PLAN.md`
   - 规划中的 tRPC-Agent-Go 分阶段落地计划。
4. `docs/DEVELOPMENT.md`
   - 本地开发规范、命令、目录职责、代码约束。
5. `docs/API_REFERENCE.md`
   - 当前 HTTP API、请求/响应字段、错误格式和 curl 示例。
6. `docs/DATA_MODEL.md`
   - domain 结构、状态、导出路径和持久化边界。

### 批量视频工厂

1. `docs/BATCH_VIDEO_FACTORY.md`
   - 批量视频工厂使用说明。
2. `docs/ACCEPTANCE_CHECKLIST.md`
   - Codex / 人工验收清单。
3. `docs/OPERATIONS_SMOKE.md`
   - 启动服务、跑 smoke、排查常见问题。
4. `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md`
   - 给 Codex 目标模式使用的工程任务书。
5. `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`
   - 给 Codex 目标模式使用的产品任务书。

### 参考项目和增强路径

1. `docs/OPENMONTAGE_MAPPING.md`
   - OpenMontage 能力如何映射到本项目。
2. `docs/modules/OPENMONTAGE_RENDER.md`
   - OpenMontage 渲染模块说明。
3. `docs/ROADMAP.md`
   - v0.1 到 v0.8 路线图。

## 2. 模块文档

模块文档和 Go 包一一对应，新增模块必须补充中文说明。

| 文档 | 对应代码 | 说明 |
| --- | --- | --- |
| `docs/modules/WORKFLOW.md` | `internal/workflow` | 确定性内容生产 workflow 节点。 |
| `docs/modules/AGENTKIT_TRPC_AGENT_GO.md` | `internal/agentkit` | tRPC-Agent-Go 适配边界、GraphAgent / Tool / Artifact 映射。 |
| `docs/modules/JUDGMENT_ENGINE.md` | `internal/judgment` | Research 结果到 Judgment / Content Thesis / ContentAtom 的边界。 |
| `docs/modules/PIPELINE_RUNS.md` | `internal/pipeline` | ResearchTopic 到 ContentAtom 和子 Distribution Run 的持久化 parent pipeline。 |
| `docs/modules/DISTRIBUTION_RUNS.md` | `internal/distribution`、`internal/runstore` | ContentAtom 分发、异步 Run、artifact manifest 和能力阻塞。 |
| `docs/modules/MEDIA_WORKER.md` | `internal/mediaworker`、`tools/media-worker` | TTS、字幕、Remotion、FFmpeg 和媒体 artifact manifest。 |
| `docs/modules/TOOLS.md` | `internal/tools` | 平台模板、商品规整、QA、素材、字幕等工具函数。 |
| `docs/modules/EXPORTER.md` | `internal/exporter` | Markdown / JSON 发布包和 batch report 导出。 |
| `docs/modules/BATCH_ENGINE.md` | `internal/engine` | 批量运行应用服务。 |
| `docs/modules/API_BATCH.md` | `internal/api` | 批量 HTTP API。 |
| `docs/modules/OPENMONTAGE_RENDER.md` | `internal/render` | OpenMontage 可选渲染路径。 |
| `docs/modules/PUBLICATION_ORCHESTRATOR.md` | `internal/publication` | 精确 fingerprint、双确认、reservation、unknown/reconcile 和证据门禁。 |
| `docs/modules/WINDOWS_BROWSER_BRIDGE.md` | `tools/windows-browser-bridge`、`internal/egopublisher` | Windows Ego/browser bridge 的草稿、提交和恢复边界。 |
| `docs/modules/PUBLISHING.md` | `internal/publisher` | 抖音官方直发、小红书 SDK 交接、视频号辅助发布和扩展接口。 |

## 3. 文档覆盖状态

当前已经覆盖：

- 产品定位和用户边界。
- MVP 输出物。
- 批量视频工厂目标模式。
- Go + net/http + tRPC-Agent-Go 分层。
- OpenMontage 参考边界。
- API、数据模型、开发命令、smoke 验收。
- 无 API key deterministic fallback。
- 小红书 / 抖音平台差异化。
- 商品、服务、能力三类 Offering 输入和多平台发布适配器。
- ContentAtom 作为 Judgment 与 Distribution 之间已经落地的正式契约边界。
- Research/Judgment parent pipeline、Distribution facade、Media Worker 和 Publication Orchestrator 的当前接线及能力阻塞。
- Docker 无渲染依赖的部署边界和 CI 基线。

仍建议后续补充：

- Web 控制台页面交互文档。
- Edge TTS / SRT / ASS 字幕模块文档。
- 真实 tRPC-Agent-Go GraphAgent、多 Agent 协商和运行事件映射。
- OAuth、平台审核回查、Analytics 快照和调度器。
- 真实抖音、小红书、Bilibili 凭据下的线上投稿核验记录。

## 4. 给 Codex / 自动 agent 的阅读顺序

如果要继续开发，请按这个顺序读：

1. `docs/INDEX.md`
2. `README.md`
3. `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`
4. `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md`
5. `docs/ARCHITECTURE.md`
6. `docs/API_REFERENCE.md`
7. `docs/ACCEPTANCE_CHECKLIST.md`
8. 需要改哪个模块，再读对应 `docs/modules/*.md`

不要跳过产品文档直接改代码；本项目核心不是单条视频 demo，而是本地、批量、平台适配、可审核、可测试的内容生产与分发系统。修改 Judgment 或 Research 时，必须保持 `ContentAtom` 边界，不得直接调用视频 workflow 或 Publisher。
