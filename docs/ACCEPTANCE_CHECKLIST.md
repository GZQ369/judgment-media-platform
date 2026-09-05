# 验收清单

本文用于人工检查或 Codex 完成任务后的最终验收。结论建议写成：

```text
静态文档：通过/不通过
代码测试：通过/不通过
HTTP smoke：通过/不通过
产品验收：通过/不通过
阻塞项：...
```

## 1. 产品边界验收

必须通过：

- [ ] README 和 docs 不再把本项目描述为情报监控、新闻监测或泛数据看板。
- [ ] 项目定位清楚：本地 Agentic 短视频生产、发布包、批量变体测试。
- [ ] MVP 聚焦商品/选题到平台发布包，不追求一步到位全自动发号。
- [ ] 明确 `render=false` 是稳定主路径，OpenMontage 是增强路径。
- [ ] 明确无 API key / 无外部模型时也能跑 deterministic fallback。

## 2. 文档验收

必须存在：

- [ ] `README.md`
- [ ] `docs/INDEX.md`
- [ ] `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`
- [ ] `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md`
- [ ] `docs/ARCHITECTURE.md`
- [ ] `docs/API_REFERENCE.md`
- [ ] `docs/DATA_MODEL.md`
- [ ] `docs/DEVELOPMENT.md`
- [ ] `docs/BATCH_VIDEO_FACTORY.md`
- [ ] `docs/OPERATIONS_SMOKE.md`
- [ ] `docs/OPENMONTAGE_MAPPING.md`
- [ ] `docs/TRPC_AGENT_GO_PLAN.md`
- [ ] `docs/ROADMAP.md`
- [ ] `docs/modules/*.md`

每个新增模块：

- [ ] 有中文模块文档。
- [ ] 有包级或关键类型中文注释。
- [ ] README 或 INDEX 能找到它。

## 3. API 验收

基础 API：

- [ ] `GET /health` 返回 `status=ok`。
- [ ] `GET /api/v1/templates` 返回小红书和抖音模板。
- [ ] `POST /api/v1/projects` 能创建含中文商品名的项目。
- [ ] `GET /api/v1/projects/{id}` 能查回项目。
- [ ] `POST /api/v1/projects/{id}/brief` 能生成 brief。
- [ ] `POST /api/v1/projects/{id}/variants` 能生成默认 10 个变体。
- [ ] `POST /api/v1/projects/{id}/export` 能返回 Markdown 或 JSON。
- [ ] `POST /api/v1/projects/{id}/variants/{variant_id}/metrics` 能保存人工指标。

批量 API：

- [ ] `GET /api/v1/batches` 能返回 batch 列表。
- [ ] `POST /api/v1/batches/run` 能同步运行 batch。
- [ ] `GET /api/v1/batches/{id}` 能查回 batch。
- [ ] `products=[]` 返回明确错误。
- [ ] 商品数量超过 50 返回明确错误。
- [ ] `variant_count` 默认 10，最大 50。

## 4. 批量视频工厂产品验收

一次 batch 至少包含 3 个商品。每个成功商品必须生成：

- [ ] 项目 ID。
- [ ] 商品分析。
- [ ] 内容角度。
- [ ] 至少 5 个标题。
- [ ] 至少 3 个封面文案。
- [ ] 至少 3 个脚本版本。
- [ ] 分镜表。
- [ ] 素材计划。
- [ ] 配音文案。
- [ ] 字幕文本。
- [ ] 平台 caption 和 tags。
- [ ] 10 个 A/B 变体。
- [ ] Markdown 发布包。
- [ ] JSON 发布包。

Batch report 必须说明：

- [ ] batch ID 和状态。
- [ ] 每个商品状态。
- [ ] 每个 project ID。
- [ ] 发布包路径。
- [ ] 失败原因。
- [ ] 运营下一步审核建议。
- [ ] 如何回填数据。

## 5. 平台差异化验收

同一个商品分别跑小红书和抖音时：

小红书应体现：

- [ ] 真实体验。
- [ ] 避坑 / 收藏。
- [ ] 标题偏“值不值得买 / 新手避坑 / 真实体验”。
- [ ] caption 带小红书风格标签。

抖音应体现：

- [ ] 前 3 秒强 hook。
- [ ] 短句和快节奏。
- [ ] 明确 CTA。
- [ ] 标题偏“别再乱买 / 3 秒看懂 / 买前先看”。
- [ ] 分镜镜头建议更短或更直接。

不通过示例：

- 两个平台只换标签，标题、hook、CTA、caption 基本一样。

## 6. 工程分层验收

必须保持：

- [ ] `internal/api` 只处理 HTTP 层。
- [ ] `internal/engine` 是应用服务层，不继续膨胀为所有业务逻辑所在地。
- [ ] `internal/workflow` 有确定性节点和 runner。
- [ ] `internal/agentkit` 有 tRPC-Agent-Go 适配边界。
- [ ] `internal/tools` 承载可复用工具。
- [ ] `internal/exporter` 负责文件导出。
- [ ] `internal/render` 负责 OpenMontage 或后续渲染适配。
- [ ] 核心 workflow 无 API key 也能跑。

## 7. 命令验收

在 Windows Git Bash / MSYS：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
cd /d/agent/judgment-media-platform
gofmt -w ./cmd ./internal
bash -n smoke/run_batch_smoke.sh
go test ./...
```

HTTP smoke：

```bash
go run ./cmd/server
bash smoke/run_batch_smoke.sh
```

验收报告必须写真实输出，不允许编造。

## 8. 渲染路径验收

`render=false`：

- [ ] 不依赖 OpenMontage。
- [ ] 不依赖 FFmpeg / Remotion。
- [ ] 不依赖 API key。
- [ ] 稳定生成发布包和 report。

`render=true`：

- [ ] 尝试调用 OpenMontage。
- [ ] 单个 item 渲染失败时记录 `error`。
- [ ] 不删除已经生成的发布包。
- [ ] 不让整个服务 panic。

## 9. 不应做的事

- [ ] 不删除用户已有 `data/`、`media/`、`smoke/` 文件。
- [ ] 不把项目改成 FastAPI。
- [ ] 不把 OpenMontage 当成必须依赖。
- [ ] 不加入自动发号 / 自动搬运 / 自动抓指标作为 MVP 必需功能。
- [ ] 不把旧情报监控概念重新写进视频工厂文档。
