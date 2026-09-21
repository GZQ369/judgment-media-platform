# Web 内容分发与研究流水线看板

Vue 3 + TypeScript + Vite 本地看板，包含“内容分发”和“研究流水线”两个工作区，并保留“发布状态”视图。界面只展示后端返回的运行记录、步骤、事件、阻塞原因、产物和投稿状态；API 返回为空或不可用时显示空状态/错误，不在前端生成演示任务或假投稿结果。

## 使用

```powershell
cd web
npm install
npm run dev
```

默认开发代理将 `/api` 转发到 `http://127.0.0.1:8088`。可通过 `.env.local` 配置：

```text
VITE_DEV_API_TARGET=http://127.0.0.1:8088
VITE_API_BASE=/api
```

生产构建默认使用同源 `/api`，也可用 `VITE_API_BASE` 指向明确的 API 前缀。

## 当前接口边界

- 已封装 `GET /api/v1/runs`、`GET /api/v1/runs/:id` 和独立的 `GET /api/v1/runs/:id/events`。
- 研究流水线封装 `GET /api/v1/pipeline-runs`、`GET /api/v1/pipeline-runs/:id` 和 `GET /api/v1/pipeline-runs/:id/events?after_id=...`，详情展示研究主题、内容原子、分发子运行、能力阻塞和事件。
- 新建任务使用 `POST /api/v1/runs`，请求体严格使用 Go 契约的 `{ offering, platform }` 或 `{ atom, platform }`，两者只能有一个。
- `atom` 使用 `schema_version`、`revision`、`kind`、`source_type`、`source_id`、`thesis_id`、`claim` 等真实字段；不会发送旧的 `type`、`name`、`brief` 顶层别名。
- 取消、恢复、重试分别调用 `POST /api/v1/runs/:id/cancel`、`/resume`、`/retry`，并发送 `Content-Type: application/json`。
- `src/mapper.ts` 只适配 `DistributionRun`、`RunEvent` 的真实 JSON 字段。Run 的名称、类型和平台来自 `request`；事件不会从 Run 猜测，而是从独立 events 响应展示。
- 当前 Run 契约没有 publication 字段，抖音、小红书、B站三平台保持 `not_connected`（未接入）；前端不会根据产物状态推断投稿或发布。
- Pipeline 的 `distribution_ready` 只表示“分发产物已就绪”，不等于发布完成。
- Distribution Run 的 `media_ready` 显示“媒体已就绪”，不等于发布，也不会显示为 100%。`package_ready` 只表示产物包已就绪。
- `capability_block` 和结构化 `error` 会在详情中显示 code、能力和消息。
- 当前不包含发布动作、账户登录、平台授权、视频上传或秘钥读取。

## 检查

```powershell
npm test
npm run typecheck
npm run build
```

开发代理开启 `changeOrigin`，并把转发请求的 `Origin` 改为 API target origin，以通过后端同源 write guard。
