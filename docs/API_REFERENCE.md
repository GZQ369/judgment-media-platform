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
  "service": "agentic-video-factory",
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

## 3. 单项目 API

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

## 4. OpenMontage 渲染 API

### POST /api/v1/projects/{id}/render/openmontage

用途：可选调用 OpenMontage 生成视频。该路径是增强能力，不是 MVP 稳定主路径。

原则：

- `render=false` 的发布包生成不依赖它。
- 渲染失败不应破坏已有 brief、variants、export。
- 本地 OpenMontage 默认路径见 `docs/OPENMONTAGE_MAPPING.md` 和 `docs/modules/OPENMONTAGE_RENDER.md`。

## 5. 多平台发布 API

### GET /api/v1/publishing/platforms

返回抖音、小红书、视频号的发布模式、配置状态和是否需要用户确认。

### POST /api/v1/projects/{id}/publish

一次请求一个或多个平台。`confirm=true` 必填；不传标题、正文和标签时使用项目 brief。

```json
{
  "platforms": ["douyin", "xiaohongshu", "wechat_channels"],
  "video_path": "data/videos/final.mp4",
  "video_url": "https://cdn.example.com/final.mp4",
  "confirm": true
}
```

平台结果状态：`submitted`、`action_required`、`failed`。抖音可经官方 API 直接提交；小红书需官方分享 SDK 唤起客户端；视频号需在官方助手完成确认。详见 `docs/modules/PUBLISHING.md`。

## 6. 批量视频工厂 API

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

## 7. 输出文件约定

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

## 8. 当前限制

- Batch 当前是同步执行，不是异步队列。
- Store 当前是内存存储；输出文件会落盘，但服务重启后内存里的项目列表会清空。
- OpenMontage 是可选增强，失败不影响 render=false 主路径。
- 抖音 OAuth/token 生命周期和审核状态查询尚未实现。
- 小红书和视频号受官方开放能力约束，需要客户端或创作者后台最终确认。
- 自动抓指标和完整可视化剪辑器尚未实现。
