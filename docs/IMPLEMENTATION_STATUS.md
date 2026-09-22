# Implementation Status

更新时间：2026-09-22

这份状态只描述仓库当前已经存在的代码和明确的能力边界，不把接口、占位实现或文档目标表述成已经上线的完整链路。

## 已有组件

- Go API 服务、配置、SQLite 持久化、运行记录和确定性 workflow 已实现并有 Go 测试。
- `ContentAtom`、`DistributionRequest` 和 `DistributionRun` 已落地；`internal/distribution` 提供可持久化的异步分发 facade，并能生成本地发布包。当前支持的输入和目标范围仍受代码中的 capability blocker 约束。
- `internal/runstore` 保存 Run、事件、artifact manifest、幂等键和 worker lease；当前是单进程活跃 worker，不是多实例队列。
- `internal/research` 已实现 source reader、RSS/Atom 解析、证据快照、结果校验和 OpenAI-compatible gateway client；`internal/pipeline` 已将 Research/Judgment parent pipeline 接入 Go 服务。JSON 文本提取按固定字段优先级遍历，保证同一输入输出稳定；没有真实 gateway 凭据时会进入 capability blocker，不代表仓库自带生产模型。
- `internal/publication` 已实现授权、准备、幂等事务、reservation、unknown/reconcile 状态和 adapter contract；`cmd/server` 已接入 publication SQLite、配置后优先使用的官方 Douyin adapter，以及 Ego Douyin/Xiaohongshu/Bilibili adapter。一次性授权 commit 的小红书提交路径只选 Ego。
- `internal/analytics` 与 `internal/platforminsights` 已接入；`cmd/server` 已提供 publication insights、手动 sync、provider capabilities 和 analytics summary endpoints。Analytics 使用独立 SQLite 持久化 review/metric snapshot、事件和 sync-job 状态，并提供默认关闭的可选跟进 worker。
- Analytics 当前只接入 Douyin 的只读 metrics provider（需要配置 access token、open ID 和 metrics scope）；XHS 与 Bilibili 使用明确的 truthful unavailable provider，不发起网络请求，也不伪造平台观测。
- `internal/egopublisher` 先执行 `--draft-only`，只有 publication core 消费一次性授权并建立 reservation 后才调用 Ego `publishOnce`；task-space 引用持久化在受限 SQLite 中，重启后的 Reconcile 只读且不会再次发布。
- `tools/media-worker` 已实现 Python 标准库媒体任务校验、能力探测、Edge TTS、字幕、Remotion/FFmpeg 渲染和封面 artifact 输出；`internal/mediaworker` 已由 Distribution service 接入，它仍是可选的独立 CLI/worker 组件。
- `tools/windows-browser-bridge` 已实现 Windows 浏览器桥接及其测试。
- `web` 已实现 Vue 3 + Vite 运行记录、Research/Distribution 和投稿状态看板、响应映射及 Vitest 测试；会区分 `package_ready`、`media_ready`、草稿、提交审核、审核中和已发布，并显示能力阻塞。它不是 Docker 镜像内置的前端服务。

## 当前限制与未完成事项

- Research source reader、OpenAI-compatible gateway 和 parent pipeline wiring 已接入 Go 服务。没有配置完整 gateway 时服务仍可启动，但 parent pipeline 会以 truthful `capability_blocked` 结束；当前没有真实模型凭据验证。
- Media Worker 已由 Distribution service 接入；配置 Python、TTS、Remotion 和 FFmpeg 后可登记 `media_ready`，否则会明确停在 capability blocker，而不会伪报媒体就绪。
- Publication Orchestrator 已接入 Ego 的抖音/小红书/Bilibili adapter；配置官方抖音 adapter 时优先选用它。小红书官方 Share Open SDK adapter 仅生成客户端交接数据，`XHS_SHARE_APP_KEY` 不能完成一键发布，也不会使一次性授权 commit 选用该 adapter；Ego 未配置时小红书提交会被能力阻塞。当前没有线上投稿核验。账号 OAuth、平台回调和平台审核查询仍未完成。
- 旧项目 direct publishing 路径正在禁用；新的发布变更必须经过 Publication Orchestrator 的 bearer token、精确 fingerprint、双确认和证据门禁。
- `ANALYTICS_POLL_ENABLED` 默认 `false`。启用后，worker 按 `15s` tick 做 durable discovery，只发现 reserved、非 draft publication；每个周期对本地 publication ledger 做只读 `GetForAnalytics`。预先由手动 sync 完成的 `JobDone` job 只会对仍符合条件的非 terminal、非 expired publication 重新激活，terminal/expired publication 不会重新激活。它不自动查询外部平台审核状态、不发布，也不改变 publication state。默认 review interval 为 `10m`，metrics interval 为 `1h`，metrics window 为 `168h`；被能力阻塞的 metrics 会按 capped schedule 重新检查。
- Analytics 已实现持久化同步、publication insights、summary 和 capability API；scheduled metrics 只在本地 ledger 已验证为 `published` 后调用 Douyin read-only provider，并要求返回 external ID 与 publication 匹配。Douyin 请求使用官方 `POST /video/data/?open_id=...`、JSON `item_ids` 和精确的 `data.list[].item_id`/`statistics` 匹配。XHS/Bili provider 保持 truthful unavailable，不发起网络请求；当前没有 live platform credentials、平台审核状态查询或 publication verification，不能宣称真实平台数据回收或生产分析视图已经完成。
- Web 前端尚未形成生产部署链路；当前可通过 Vite 开发服务器访问，并未由 Go API 或 Docker 镜像托管。
- CI 不声称完成真实平台发布；Docker smoke 只验证 API 镜像构建、容器启动和 `/health`。

## Docker 与 CI 边界

- Docker 镜像仍是 API-only：只构建并运行 Go API 二进制，不内置 Python media worker，也不内置或托管 Web frontend。Compose 默认把宿主端口绑定到 `127.0.0.1`，容器内固定监听 `0.0.0.0:8088`；需要局域网访问时必须显式设置 `HTTP_BIND_ADDRESS=0.0.0.0`。本机当前没有 Docker 命令，Docker smoke 只能由 CI 或具备 Docker 的环境执行。
- Docker 构建的依赖缓存层复制 `go.mod` 和 `go.sum`；镜像构建不会下载或打包上述未接入组件的运行依赖。
- CI 保留 Go 格式检查、vet、test 和 build，并包含 Linux race test、Python media-worker unittest、Windows browser bridge test、Web 的 `npm ci`、test 和 build，以及 Docker image build + container `/health` smoke。

## 当前不应宣称的能力

- Research source reader 和 OpenAI-compatible gateway 已接入 Distribution Run 的 Research -> Judgment -> ContentAtom parent pipeline；是否真正执行取决于运行时 gateway 配置和其可用性，仓库不声称自带生产模型。
- Media Worker 已由 Go Run 接入；没有配置 Python/渲染能力时会明确停在 capability blocker，`package_ready` 仍不是视频成片完成，`media_ready` 也不是已发布。
- Publication Orchestrator 的抖音路径可选配置后的官方 adapter 或 Ego fallback；小红书和 Bilibili 的一次性授权 commit 只选 Ego，小红书 SDK 交接不能替代真实提交。所有变更要求 bearer token、两次明确确认和与视频/全部平台封面/文案绑定的 exact fingerprint。超时或证据不足进入 `unknown`，只能走 reconcile；只有绑定外部 ID 或 allow-listed 平台 URL 的证据才可进入提交/审核/发布状态。
- Analytics 的持久化、手动 sync 与 opt-in scheduled follow-up 已实现，但 live metrics 仍未验证；XHS/Bili metrics truthful unavailable。scheduled follow-up 只依据本地 ledger，不查询外部平台审核、不发布，也不做 publication verification；当前没有 live platform credentials，账号 OAuth、平台审核回调、tRPC-Agent-Go 和跨平台真实投稿闭环仍未实现或验证。
