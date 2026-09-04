# 开发指南

本文记录本项目的本地开发规范。目标是让后续 Codex / 人工开发都沿着同一个工程边界推进。

## 1. 项目边界

本项目只做：

```text
本地 Agentic 短视频生产工作流
  -> 发布包生成
  -> 批量变体测试
  -> 可选本地渲染
  -> 手动指标回填
```

不要把情报监控、新闻监测、泛数据看板、自动搬运、自动养号等内容混入本仓库。

## 2. 技术栈

- 后端：Go + `net/http`。
- Agent workflow：先用确定性节点，逐步映射到 tRPC-Agent-Go。
- 本地模型：Ollama / OpenAI-compatible endpoint 作为可选增强。
- 渲染：`render=false` 是稳定主路径；OpenMontage / FFmpeg / Remotion 是增强路径。
- 存储：当前内存 store + 本地导出文件；后续可补 SQLite。

禁止：

- 把后端改成 FastAPI。
- 让核心流程强依赖云 API key。
- 新增模块但不写中文文档。
- 把所有逻辑继续堆进 `engine.go`。

## 3. Windows / Git Bash 命令

在本机推荐使用便携 Go：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
cd /d/agent/vertical-intel-video-agent
```

常用命令：

```bash
gofmt -w ./cmd ./internal
go test ./...
go run ./cmd/server
```

默认服务：

```text
http://127.0.0.1:8088
```

Smoke：

```bash
bash -n smoke/run_batch_smoke.sh
bash smoke/run_batch_smoke.sh
```

如果 Bash 脚本需要解析 JSON，会优先用 `python` / `python3`，没有 Python 时回退到 PowerShell。

## 4. 目录职责

```text
cmd/server/          服务入口
internal/api/        HTTP API 路由、请求解析、错误响应
internal/domain/     稳定领域类型和 JSON schema
internal/engine/     应用服务层，串联 workflow/export/render/store
internal/workflow/   确定性 Agentic workflow 节点
internal/agentkit/   tRPC-Agent-Go 适配边界
internal/tools/      平台模板、商品规整、QA、素材、字幕等工具
internal/exporter/   发布包和 batch report 文件导出
internal/render/     OpenMontage / 后续 FFmpeg 渲染适配
internal/store/      当前内存 store，后续持久化 store
smoke/               可人工运行的请求和 smoke 脚本
data/                本地运行产物，不应把用户数据硬编码进代码
docs/                中文产品、架构、模块和验收文档
```

## 5. 新增功能流程

新增功能前先判断属于哪类：

1. 产品目标变化：先改 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md` 或新增 PRD。
2. API 变化：先改 `docs/API_REFERENCE.md`，再改 `internal/api`。
3. Domain 变化：先改 `docs/DATA_MODEL.md`，再改 `internal/domain`。
4. Workflow 节点变化：改 `internal/workflow`，同步 `docs/modules/WORKFLOW.md`。
5. 工具能力变化：改 `internal/tools`，同步 `docs/modules/TOOLS.md`。
6. 渲染变化：改 `internal/render`，同步 `docs/modules/OPENMONTAGE_RENDER.md`。
7. 验收变化：同步 `docs/ACCEPTANCE_CHECKLIST.md` 和 smoke 脚本。

## 6. 注释和文档要求

每个新增模块必须有：

- 包级中文注释或 `doc.go`。
- 关键导出类型/函数中文注释。
- `docs/modules/*.md` 模块文档。
- 如果影响用户使用，更新 `README.md` 或 `docs/INDEX.md`。

## 7. 测试要求

每次交付至少运行：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
gofmt -w ./cmd ./internal
bash -n smoke/run_batch_smoke.sh
go test ./...
```

如果改了 API 或导出文件，还要实际启动服务并跑 smoke：

```bash
go run ./cmd/server
bash smoke/run_batch_smoke.sh
```

验收时要确认：

- 无 API key 也能生成发布包。
- batch 至少 3 个商品。
- 每个成功商品都有 Markdown 和 JSON 发布包。
- 小红书 / 抖音输出不是同一套文案换标签。
- render=true 失败不会破坏 render=false 主路径。

## 8. 产物和安全

不要随意删除：

- `data/`
- `media/`
- `smoke/`
- 用户手工放入的素材和样例产物

如果需要清理运行产物，先说明路径和影响，再获得明确确认。

## 9. 当前已知工程债

- Store 仍是内存实现，重启后项目列表不保留；但发布包文件会留在 `data/batches/`。
- Web 控制台尚未实现。
- tRPC-Agent-Go 目前是适配边界和确定性 runner，后续需要接真实 GraphAgent。
- Edge TTS / SRT / ASS 还在路线图中。
- SQLite 持久化未正式纳入当前视频工厂主路径。
