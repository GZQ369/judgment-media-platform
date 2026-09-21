# API 参考

当前后端是 Go `net/http` 服务，默认地址：

```text
http://127.0.0.1:8088
```

所有业务 API 使用 JSON。错误统一返回：

```json
{"error":"错误说明"}
```

## 1. 健康检查

### GET /health

用途：确认服务已启动。

响应示例：

```json
{
  "service": "judgment-media-platform",
  "status": "ok",
  "time": "2026-07-01T00:00:00Z"
}
```

## 2. 模板

### GET /api/v1/templates

用途：列出当前支持的平台模板。

当前模板：

- `xiaohongshu_product_seed`：小红书商品种草发布包。
- `douyin_product_hook`：抖音强钩子商品短视频。
- `wechat_channels_product_trust`：视频号生活化推荐，当前是后续模板占位。

## 3. 异步 Distribution Run API

这组接口使用 `internal/domain/distribution.go` 中的
`ContentAtom`、`DistributionRequest` 和 `DistributionRun` 契约，运行记录、事件和 artifact manifest 持久化到配置数据目录下的 SQLite。它与旧的 project / batch API 并存。

### POST /api/v1/runs

创建并持久化一个异步 Run。请求必须且只能提供 `offering` 或 `atom` 之一；当前只接受一个 `platform` 或一个 `targets` 项，平台为 `douyin`、`xiaohongshu` 或 `bilibili`。视频号不接受新的异步 Run。

```json
{
  "idempotency_key": "creator-2026-09-20-001",
  "offering": {
    "kind": "capability",
    "name": "自动剪辑能力",
    "value_points": ["自动切片", "自动字幕"],
    "target_audience": "内容团队"
  },
  "platform": "xiaohongshu",
  "variant_count": 10
}
```

`202 Accepted` 表示 Run 已进入 durable ledger，不表示媒体或平台投稿已完成。重复的幂等键和相同请求返回已有 Run；相同幂等键但请求不同返回 `409`。

### GET /api/v1/runs

按更新时间倒序列出 durable Runs。可用 `limit` 查询参数限制返回数量。

### GET /api/v1/runs/{id}

返回单个 `DistributionRun`，包括 `status`、`checkpoint`、`capability_block`、`error`、`artifacts` 和请求原文。

### GET /api/v1/runs/{id}/events

默认返回 append-only `RunEvent` JSON 列表，支持 `after_id`；请求头带 `Accept: text/event-stream` 时切换为 SSE 流。

### POST /api/v1/runs/{id}/cancel|resume|retry

对 Run 执行状态操作。恢复和重试仍受能力边界、租约和幂等规则约束，不会绕过平台发布或人工审核。

Offering Run 当前复用既有确定性 workflow 并生成本地 `package.json` artifact；配置 Media Worker 后可继续到 `media_ready`。Research atom 需要已配置的模型执行器；Bilibili 已有内容模板和平台约束；publication API 只从 `media_ready` artifact 进入授权和发布事务；Analytics 仍未接入。

## 4. 异步 Pipeline Run API

这组接口暴露 `internal/pipeline.Service` 的 durable parent run。它负责
`ResearchTopic -> Judgment -> ContentAtom[] -> DistributionRun[]` 的编排；
`ResearchTopic` 只出现在 pipeline 请求中，不会进入任何
`DistributionRequest`，这些接口也不会产生平台投稿副作用。

### POST /api/v1/pipeline-runs

创建并排队一个 pipeline run。请求使用严格 JSON 解码：未知字段会返回
`400`，请求体上限为 2 MiB，且请求体必须只包含一个 JSON 值。

```json
{
  "idempotency_key": "research-2026-09-21-001",
  "topic": {
    "topic": "租房用户的桌面收纳需求",
    "source_urls": ["https://example.com/source"],
    "desired_action": "形成可核验的内容判断"
  },
  "targets": [
    {"platform": "xiaohongshu", "account_id": "brand-account"}
  ],
  "template": "xiaohongshu_product_seed",
  "variant_count": 3,
  "media": {"enabled": false}
}
```

首次创建返回 `202 Accepted`，表示已持久化并排队；不表示判断、媒体生成或
发布已完成。相同幂等键和相同请求返回 `200` 与已有 run；相同幂等键但请求
不同返回 `409`。pipeline service 未注入或未运行时返回 `503`。

### GET /api/v1/pipeline-runs

按创建时间倒序列出 pipeline runs。可用 `limit` 查询参数限制返回数量。

### GET /api/v1/pipeline-runs/{id}

返回单个 `PipelineRun`，包括请求中的 `topic`、当前 checkpoint、判断产物、
生成的 ContentAtom、子 Distribution Run、能力阻塞和错误信息。

### GET /api/v1/pipeline-runs/{id}/events

默认返回 append-only `RunEvent` JSON 数组，支持 `after_id` 和
`Last-Event-ID`。请求头带 `Accept: text/event-stream` 时返回 SSE；流在
pipeline 进入 `blocked`、`failed`、`distribution_ready` 或 `cancelled` 时结束。

### POST /api/v1/pipeline-runs/{id}/cancel

取消尚未结束的 pipeline run。成功返回 `200`；重复取消是幂等的。

### POST /api/v1/pipeline-runs/{id}/retry

重试 `blocked`、`failed` 或 `cancelled` 的 pipeline run，并增加 attempt。
成功重新排队时返回 `202`；`202` 只表示重新排队，不表示任何发布动作。

## 5. 单项目 API

### POST /api/v1/projects

用途：创建单个视频生产项目。

请求字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 项目名，可空。 |
| `platform` | string | `xiaohongshu` / `douyin` / `wechat_channels`。 |
| `template` | string | 模板 ID，可空，系统按平台默认。 |
| `duration_seconds` | number | 视频时长，可空。 |
| `offering` | object | 商品、服务或能力输入，推荐使用。 |
| `product` | object | 兼容旧客户端的商品输入。 |

`offering` 字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `kind` | string | `product` / `service` / `capability`。 |
| `name` | string | 供给名称。 |
| `description` | string | 供给描述。 |
| `link` | string | 商品链接，可手动填。 |
| `price` | string | 价格。 |
| `category` | string | 类目。 |
| `value_points` | string[] | 价值点。 |
| `problems` | string[] | 要解决的问题。 |
| `target_audience` | string | 目标人群。 |
| `usage_scenarios` | string[] | 使用场景。 |
| `assets` | string[] | 本地素材路径或素材描述。 |
| `delivery_mode` | string | 交付方式。 |
| `proof_points` | string[] | 案例、数据或结果证明。 |
| `call_to_action` | string | 期望用户执行的动作。 |

旧 `product.title`、`selling_points`、`pain_points` 会自动映射到新模型。

最小示例：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/projects \
  -H 'Content-Type: application/json' \
  -d '{"platform":"xiaohongshu","offering":{"kind":"capability","name":"自动剪辑能力","value_points":["自动切片","自动字幕","自动导出"],"target_audience":"内容团队"}}'
```

### GET /api/v1/projects

用途：列出所有项目。

### GET /api/v1/projects/{id}

用途：查看项目详情。

### POST /api/v1/projects/{id}/brief

用途：运行确定性 workflow，生成商品分析、标题、封面、脚本、分镜、素材计划、配音文案、字幕、caption、QA。

`POST /api/v1/projects/{id}/workflow/run` 当前等价于此接口。

### POST /api/v1/projects/{id}/variants

用途：生成 A/B 测试变体。

请求：

```json
{"count":10}
```

规则：

- `count <= 0` 时默认 10。
- 最大 50。
- 如果项目还没有 brief，会先生成 brief。

### POST /api/v1/projects/{id}/export

用途：返回单项目发布包。

请求：

```json
{"format":"markdown"}
```

或：

```json
{"format":"json"}
```

说明：单项目 export 返回内存响应；批量接口会真实写出 `publish_package.md/json` 文件。

### POST /api/v1/projects/{id}/variants/{variant_id}/metrics

用途：手动回填变体指标，用于后续 A/B 复盘。

请求字段：

```json
{
  "views": 1200,
  "likes": 88,
  "saves": 31,
  "comments": 9,
  "completion_rate": 0.37,
  "note": "标题点击好，后半段流失"
}
```

## 6. OpenMontage 渲染 API

### POST /api/v1/projects/{id}/render/openmontage

用途：可选调用 OpenMontage 生成视频。该路径是增强能力，不是 MVP 稳定主路径。

原则：

- `render=false` 的发布包生成不依赖它。
- 渲染失败不应破坏已有 brief、variants、export。
- 本地 OpenMontage 默认路径见 `docs/OPENMONTAGE_MAPPING.md` 和 `docs/modules/OPENMONTAGE_RENDER.md`。

## 7. 多平台发布 API

### GET /api/v1/publishing/platforms（legacy compatibility）

返回旧 publisher registry 的抖音、小红书、视频号能力。该接口只用于兼容旧 project API；新的异步 Run 发布必须使用下面的 Publication Orchestrator API，视频号不在其 allowlist 中。

### POST /api/v1/projects/{id}/publish（legacy compatibility, being disabled）

一次请求一个或多个旧平台。`confirm=true` 必填；不传标题、正文和标签时使用项目 brief。此路径正在禁用，不应被用于真实线上发布。

```json
{
  "platforms": ["douyin", "xiaohongshu", "wechat_channels"],
  "video_path": "data/videos/final.mp4",
  "video_url": "https://cdn.example.com/final.mp4",
  "confirm": true
}
```

平台结果状态：`submitted`、`action_required`、`failed`。此旧接口没有新的精确 fingerprint、reservation、unknown/reconcile 和双确认保证。详见 `docs/modules/PUBLISHING.md`；正式入口见第 11 节。

## 8. 批量视频工厂 API

### POST /api/v1/batches/run

用途：同步运行一次批量视频工厂。

请求字段：

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 自动生成 | batch 名称。 |
| `platform` | string | `xiaohongshu` | 平台。 |
| `template` | string | 按平台默认 | 模板。 |
| `duration_seconds` | number | 按平台默认 | 单条视频时长。 |
| `offerings` | object[] | 推荐 | 商品、服务或能力列表，最多 50。 |
| `products` | object[] | 兼容 | 旧商品列表。 |
| `variant_count` | number | 10 | 每项供给生成多少变体，最多 50。 |
| `export_formats` | string[] | `markdown,json` | 发布包格式。 |
| `render` | bool | false | 是否尝试 OpenMontage 渲染。 |
| `render_request` | object | 空 | OpenMontage 渲染参数。 |

最小示例文件：

```text
smoke/batch_products_request.json
```

运行示例：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/batches/run \
  -H 'Content-Type: application/json' \
  --data-binary @smoke/batch_products_request.json
```

响应核心字段：

| 字段 | 说明 |
| --- | --- |
| `id` | batch id。 |
| `status` | `completed` / `completed_with_errors` / `failed`。 |
| `items[].project_id` | 每个商品对应项目。 |
| `items[].publish_package_path` | Markdown 发布包路径。 |
| `items[].publish_package_json_path` | JSON 发布包路径。 |
| `report_path` | batch_report.md。 |
| `report_json_path` | batch_report.json。 |
| `export_dir` | 本次 batch 输出目录。 |

### GET /api/v1/batches

用途：列出 batch 任务。

### GET /api/v1/batches/{id}

用途：查看 batch 详情。

## 9. 输出文件约定

批量主路径会写出：

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

`batch_report.md` 面向运营审核，必须能回答：

1. 哪些商品成功了。
2. 每个商品的项目 ID 是什么。
3. 发布包在哪里。
4. 如果失败，失败原因是什么。
5. 下一步应该审核什么、补什么素材、如何回填数据。

## 10. 当前限制

- Batch 当前是同步执行，不是异步队列。
- 新的 Distribution Run 使用 SQLite 持久化 runs、events 和 artifacts；旧 project / batch store 仍是内存实现，服务重启后旧项目列表和 batch 列表会清空，但已导出的文件保留。
- OpenMontage 是可选增强，失败不影响 render=false 主路径。
- 抖音 OAuth/token 生命周期和审核状态查询尚未实现。
- 新异步 Run 已接入 publication coordinator；抖音提交需要官方凭据或 Ego bridge，小红书和 Bilibili 的一次性授权提交需要 Ego bridge，测试不会执行真实投稿。
- 小红书 `XHS_SHARE_APP_KEY` 仅支持旧 publisher contract 的 SDK 客户端交接，不能完成一键发布，也不会被选为一次性授权 commit 的 adapter；Ego 未配置时小红书能力被阻塞。视频号仍需在创作者后台完成最终确认。
- Research gateway、Media Worker 和 Publication Orchestrator 已有 Go 服务接线；Analytics 尚未实现指标采集和平台数据回流，因此仍不是完整自动闭环。
- 自动抓指标和完整可视化剪辑器尚未实现。

## 11. Publication Orchestrator API

Publication APIs coordinate only durable `media_ready` distribution runs. They
do not accept client media paths or client-provided digests.

### GET /api/v1/publications/capabilities

Returns truthful capability entries for Douyin, Xiaohongshu, and Bilibili.
Unconfigured official/browser adapters are reported as blockers and are not
treated as successful publication.

### POST /api/v1/publications/candidate

Builds a read-only candidate from durable run artifacts. Example body:

```json
{"run_id":"run_123","creator":"creator_1","account":"account_1","platform":"xiaohongshu","variant_id":"variant-1"}
```

The run must be `media_ready`, target the requested platform/account, contain a
bounded `publish_package.json`, a rendered video and a cover. The response
contains the reconstructed `package` and its `fingerprint`. No adapter is
called.

### POST /api/v1/publications/authorize

Authorizes an exact candidate only when `confirm` is true. The request contains
`candidate`, `policy` (`version` and `selected`), and an RFC3339 `expires_at`.
Expiry is bounded by the service's short maximum lifetime. The response is a
one-use authorization record and contains no reservation token.

### POST /api/v1/publications/prepare

The request contains the same `candidate`, `authorization_id`, candidate
`fingerprint`, and `policy`. The candidate is rebuilt from durable artifacts;
changes to media or complete copy fail before preparation. Preparation does not
invoke a platform adapter.

### GET /api/v1/publications and GET /api/v1/publications/{id}

List or inspect durable publication snapshots. Reservation tokens are omitted
from all JSON responses.

### POST /api/v1/publications/{id}/commit

Consumes the one-use authorization and invokes the configured adapter once.
Submit `fingerprint`; a lost or uncertain outcome becomes `unknown` and must be
handled by reconcile.

### POST /api/v1/publications/{id}/reconcile

Queries the adapter through its official reconciliation capability. It never
retries commit and never converts a browser draft or `READY` signal into
`submitted`.

Errors use `400` for malformed requests, `404` for missing durable runs or
publications, `409` for binding/state conflicts, `412` for an expired or
not-ready precondition, and `503` for unavailable publication capability.
