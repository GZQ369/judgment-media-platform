# 批量视频工厂使用说明

## 目标

批量视频工厂 MVP 支持一次输入多个商品，生成运营人员可审核的标题、封面文案、脚本、分镜、素材计划、发布正文、A/B 变体、Markdown/JSON 发布包和 batch report。

产品目标见 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`，工程目标见 `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md`。

## API

```text
GET  /api/v1/batches
POST /api/v1/batches/run
GET  /api/v1/batches/{id}
```

最小请求见 `smoke/batch_products_request.json`。关键字段：

- `products`：至少 1 个商品，默认最多 50 个。
- `platform`：`xiaohongshu` 或 `douyin`。
- `variant_count`：默认 10，最大 50。
- `export_formats`：默认 `markdown` 和 `json`。
- `render`：默认建议 `false`。

## 输出目录

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

## render=false / render=true

`render=false` 是稳定主路径，只生成发布包和 report，不依赖 OpenMontage、FFmpeg、Remotion 或外部 API key。

`render=true` 是增强路径，会尝试调用 OpenMontage。渲染失败时 item 会变成 `completed_with_errors`，但已经生成的发布包仍可审核。

## 无 API key fallback

当前 MVP 默认使用确定性 workflow。没有 API key、没有外部模型，也能产出完整发布包、10 个变体和 batch report。

## Smoke

启动服务后运行：

```bash
bash -n smoke/run_batch_smoke.sh
bash smoke/run_batch_smoke.sh
```

脚本会检查 `/health`，POST `/api/v1/batches/run`，并打印 batch id、report path 和每个 project id。
`run_batch_smoke.sh` 会优先用 `python` / `python3` 解析 JSON；如果当前 Windows Git Bash 没有 Python，会自动回退到 PowerShell 解析。
当前机器如果没有 Git Bash / MSYS，也可以使用 Windows PowerShell 版本：

```powershell
powershell -ExecutionPolicy Bypass -File smoke/run_batch_smoke.ps1
```

## 运营审核流程

1. 打开 `batch_report.md`，确认每个商品状态和发布包路径。
2. 打开每个 `publish_package.md`，审核标题、封面、脚本、分镜和素材计划。
3. 先补齐缺失素材，再选 2-3 个变体做首轮发布。
4. 发布后按变体 ID 回填播放、点赞、收藏、评论和完播率。
5. 根据胜出 hook、标题和脚本结构做下一轮 A/B 测试。

## 模块文档索引

总入口：`docs/INDEX.md`。

使用和验收相关：

- `docs/API_REFERENCE.md`
- `docs/DATA_MODEL.md`
- `docs/DEVELOPMENT.md`
- `docs/OPERATIONS_SMOKE.md`
- `docs/ACCEPTANCE_CHECKLIST.md`

模块实现相关：

- `docs/modules/WORKFLOW.md`
- `docs/modules/AGENTKIT_TRPC_AGENT_GO.md`
- `docs/modules/TOOLS.md`
- `docs/modules/EXPORTER.md`
- `docs/modules/BATCH_ENGINE.md`
- `docs/modules/API_BATCH.md`
- `docs/modules/OPENMONTAGE_RENDER.md`
