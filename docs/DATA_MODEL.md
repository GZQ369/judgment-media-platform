# 数据模型说明

本文解释当前 Go domain 类型和输出文件结构，方便后续 API、前端、持久化和 Codex 开发保持一致。

代码位置：

```text
internal/domain/types.go
```

## 1. Platform

```text
xiaohongshu
douyin
wechat_channels
```

当前稳定主路径是小红书和抖音。视频号模板是后续扩展占位。

## 2. Template

模板描述一个平台内容工作流：

- `id`：模板 ID。
- `name`：中文名称。
- `platform`：目标平台。
- `description`：用途说明。
- `stages`：workflow 阶段名。

当前模板：

- `xiaohongshu_product_seed`
- `douyin_product_hook`
- `wechat_channels_product_trust`

## 3. OfferingInput

商品、服务或问题解决能力的统一输入：

- `kind`：`product`、`service` 或 `capability`。
- `name`：统一名称。
- `description`：描述。
- `link`：链接。
- `price`：价格。
- `category`：类目。
- `value_points`：价值点。
- `problems`：要解决的问题。
- `target_audience`：目标人群。
- `usage_scenarios`：使用场景。
- `assets`：素材路径或素材描述。
- `delivery_mode`：交付方式。
- `proof_points`：案例、数据或结果证明。
- `call_to_action`：行动建议。

规则：

- 输入可以不完整，workflow 会做 deterministic fallback。
- 旧 `ProductInput` 是类型别名，`title` / `selling_points` / `pain_points` 会同步到新字段。

## 4. VideoProject

单个商品/选题的视频生产项目：

- `id`
- `name`
- `platform`
- `template`
- `duration_seconds`
- `status`
- `offering`
- `product`，兼容镜像
- `brief`
- `variants`
- `render`
- `publications`
- `created_at`
- `updated_at`

常见状态：

- `draft`
- `brief_generated`
- `variants_generated`
- `metrics_recorded`
- `rendered`
- `render_failed`

## 5. PublishingBrief

运营审核的核心产物：

- `product_analysis`：商品分析。
- `angles`：内容角度和 hook。
- `title_options`：标题候选。
- `cover_texts`：封面文案。
- `scripts`：脚本版本。
- `storyboard`：分镜。
- `material_plan`：素材计划。
- `voiceover_text`：配音文案。
- `subtitle_text`：字幕文本。
- `caption`：平台发布正文。
- `tags`：标签。
- `qa`：质检结果。

MVP 要求每个成功项目至少：

- 5 个标题。
- 3 个封面文案。
- 3 个脚本版本。
- 1 份分镜。
- 1 份素材计划。

## 6. Variant 和 VariantMetric

`Variant` 是 A/B 测试版本，覆盖：

- 标题。
- 封面文案。
- Hook。
- 脚本风格。
- CTA。
- Caption。
- 差异点说明。
- 人工指标。

`VariantMetric` 支持手动回填：

- `views`
- `likes`
- `saves`
- `comments`
- `completion_rate`
- `note`
- `updated_at`

后续复盘模块应按 variant 维度比较标题、hook、封面、脚本结构和 CTA。

## 7. BatchRunRequest

批量运行请求：

- `name`
- `platform`
- `template`
- `duration_seconds`
- `offerings`，推荐
- `products`，兼容旧请求
- `variant_count`
- `export_formats`
- `render`
- `render_request`

约束：

- `offerings` 和 `products` 至少一个非空。
- 每批最多 50 个供给。
- `variant_count` 默认 10，最大 50。
- `export_formats` 默认 `markdown` 和 `json`。
- `render=false` 是稳定主路径。

## 8. BatchJob / BatchItem

`BatchJob` 记录一次批量生产任务：

- `id`
- `name`
- `status`
- `platform`
- `template`
- `variant_count`
- `render`
- `items`
- `report_path`
- `report_json_path`
- `export_dir`
- `created_at`
- `updated_at`

`BatchItem` 记录单个商品在 batch 中的结果：

- `index`
- `product_title`
- `project_id`
- `status`
- `error`
- `publish_package_path`
- `publish_package_json_path`
- `render_video_path`

Batch 状态：

- `pending`
- `running`
- `completed`
- `completed_with_errors`
- `failed`

规则：

- 单个 item 失败不应阻断后续 item。
- 至少一个成功时，batch 可以是 `completed` 或 `completed_with_errors`。
- 全部失败才是 `failed`。

## 9. BatchReport

写入 `batch_report.json` 的结构化报告，用于后续前端或自动复盘读取。

它应包含：

- Batch 元信息。
- Items 列表。
- 产物路径。
- 汇总统计。
- 下一步建议。

Markdown 版本 `batch_report.md` 面向运营人员阅读。

## 10. RenderResult

OpenMontage 或后续渲染模块返回：

- 视频路径。
- 状态。
- 错误信息。
- 运行摘要。

渲染结果不能成为发布包生成的前置条件。

## 11. 当前持久化边界

当前 store 是内存实现：

```text
internal/store/memory.go
```

含义：

- 服务重启后内存里的 projects / batches 会清空。
- 但 batch 导出的 Markdown / JSON 文件会保留在 `data/batches/`。

后续 SQLite 方案建议：

- projects、briefs、variants、metrics、batches 入库。
- publish package 和视频文件仍走文件系统。
- DB 只记录 artifact path 和 metadata。

## 12. 输出路径约定

批量输出：

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

路径要稳定，因为：

- Batch API 会返回这些路径。
- Smoke 脚本会检查这些路径。
- Web 控制台后续会直接读取这些路径。
