# Codex 目标模式任务：把 vertical-intel-video-agent 完善成可批量跑的小红书 / 抖音视频工厂

> 使用方式：在 `D:/agent/vertical-intel-video-agent` 仓库根目录打开 Codex，把本文档整体作为目标模式 / target prompt 投喂给 Codex。Codex 要先读取 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md` 理解产品目标，再按本文档实施代码改造、补测试、运行验证命令，并在最后报告真实命令输出。

## 0. 绝对约束

1. 工作目录：`D:/agent/vertical-intel-video-agent`
2. 这是一个 Go 项目，当前使用 `net/http`，不要改成 FastAPI。
3. 在 Windows Git Bash / MSYS 环境下运行命令。Go 推荐使用：

   ```bash
   export PATH=/d/agent/.tools/go/bin:$PATH
   ```

4. 不要删除现有 `data/`、`media/`、`smoke/` 里的样例产物，除非明确只是新增测试文件或临时文件。
5. 保持 OpenMontage 集成路径：

   ```text
   D:/agent/video-workers/OpenMontage
   D:/agent/video-workers/OpenMontage/video_factory_pipeline_runner.py
   ```

6. 保持无 API key 也能跑通：所有核心工作流必须有 deterministic fallback。外部 API provider 只能作为可选增强。
7. 本次开发必须同时遵守产品目标和工程目标：先读 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`，再读 `docs/TRPC_AGENT_GO_PLAN.md` 和本文档。产品上要服务“批量短视频发布包 + A/B 测试”，工程上要落地 tRPC-Agent-Go 分层。
8. 本次开发必须按 `docs/TRPC_AGENT_GO_PLAN.md` 的方向落地 tRPC-Agent-Go 编排层：先实现 `internal/workflow` 的确定性节点，再提供 `internal/agentkit` 的 tRPC-Agent-Go GraphAgent 适配/封装；如果真实依赖接入受阻，必须保留接口、adapter 和中文说明，不能继续把所有业务逻辑堆在 `engine.go`。
9. 每一个新增模块都必须配套清晰中文文档：包级注释、关键类型/函数中文注释，以及 `docs/modules/*.md` 模块文档。
10. 实现后必须能跑：

   ```bash
   export PATH=/d/agent/.tools/go/bin:$PATH
   gofmt -w ./cmd ./internal
   bash -n smoke/run_batch_smoke.sh
   go test ./...
   ```

11. 不要只做文档或伪代码。目标是可运行的批量视频工厂后端 MVP。

## 1. 当前项目背景

项目定位：本地化 Agentic 短视频生产工作流系统，面向小红书 / 抖音 / 视频号矩阵运营、电商商家、MCN、小型内容团队和知识内容创作者。

当前核心产品文档：

- `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md`：产品目标、用户、MVP 边界、平台差异化、验收口径。
- `docs/PRD_MVP.md`：早期小红书发布包 PRD。
- `docs/PRODUCT_DESIGN.md`：整体产品设计和参考项目分析。
- `docs/TRPC_AGENT_GO_PLAN.md`：tRPC-Agent-Go 接入计划。

当前核心链路已经有：

- `cmd/server/main.go`：启动 HTTP 服务。
- `internal/api/handler.go`：API 路由。
- `internal/domain/types.go`：项目、商品、脚本、分镜、变体、渲染结果等 domain 类型。
- `internal/engine/engine.go`：确定性 brief / variant / export / render 调度逻辑。
- `internal/render/openmontage.go`：OpenMontage 渲染适配。
- `internal/store/memory.go`：内存存储。

现有目标不是“单条视频 demo”，而是把它完善成批量生产系统：一次输入商品 / 选题列表，可以自动生成多平台、多脚本、多标题、多封面、多变体、发布包，并可选调用 OpenMontage 渲染成 MP4。

## 2. 本次 Codex 要实现的总目标

把当前 MVP 改造成“可批量跑”的视频工厂后端，并且不是继续用单个 `engine.go` 写死流程，而是按 tRPC-Agent-Go 的 Agentic workflow 架构拆分：

```text
HTTP API
  -> Engine / Application Service
  -> internal/workflow 确定性节点 runner
  -> internal/agentkit tRPC-Agent-Go GraphAgent 适配层
  -> internal/tools 工具能力
  -> internal/exporter / render / store
```

至少支持：

1. 批量创建项目。
2. 批量运行 brief / variants / export / optional render。
3. 小红书与抖音模板差异化。
4. 每个 batch 生成可追踪的 batch job / batch report。
5. 输出本地 JSON / Markdown 发布包文件，而不只是 HTTP response。
6. 提供 smoke 测试脚本或测试夹具，证明可以一次跑多个商品。
7. 所有新增功能有 Go 单元测试或集成式 handler/engine 测试。

产品验收口径：

1. 用户能一次输入 3 个以上商品，并清楚看到每个商品的项目 ID 和发布包路径。
2. 每个商品输出必须能让运营人员直接审核：标题、封面、脚本、分镜、素材计划、发布正文、变体、QA 都要在 Markdown 中可读。
3. 小红书和抖音不是“同一套文案换标签”，必须在 hook、标题、CTA、分镜节奏和 caption 上体现差异。
4. Batch report 不只是机器状态，还要告诉用户下一步：审核哪些内容、补哪些素材、如何发布、如何回填数据。
5. render=false 必须是稳定主路径；render=true 是增强路径，失败不能破坏发布包生产。
6. 无 API key / 无外部模型时仍可演示完整产品价值。

## 3. 要新增或完善的能力

### 3.1 Domain：BatchJob / BatchItem / BatchReport

在 `internal/domain/types.go` 增加批量任务模型。建议字段如下，可根据现有风格调整：

```go
type BatchStatus string

const (
    BatchStatusPending   BatchStatus = "pending"
    BatchStatusRunning   BatchStatus = "running"
    BatchStatusCompleted BatchStatus = "completed"
    BatchStatusFailed    BatchStatus = "failed"
)

type BatchRunRequest struct {
    Name                 string                 `json:"name"`
    Platform             Platform               `json:"platform"`
    Template             string                 `json:"template"`
    DurationSeconds      int                    `json:"duration_seconds"`
    Products             []ProductInput          `json:"products"`
    VariantCount         int                    `json:"variant_count"`
    ExportFormats        []string               `json:"export_formats"`
    Render               bool                   `json:"render"`
    RenderRequest        RenderOpenMontageRequest `json:"render_request,omitempty"`
}

type BatchItem struct {
    Index       int           `json:"index"`
    ProductTitle string       `json:"product_title"`
    ProjectID   string       `json:"project_id,omitempty"`
    Status      BatchStatus  `json:"status"`
    Error       string       `json:"error,omitempty"`
}

type BatchJob struct {
    ID           string         `json:"id"`
    Name         string         `json:"name"`
    Status       BatchStatus    `json:"status"`
    Platform     Platform       `json:"platform"`
    Template     string         `json:"template"`
    Items        []BatchItem    `json:"items"`
    ReportPath   string         `json:"report_path,omitempty"`
    ExportDir    string         `json:"export_dir,omitempty"`
    CreatedAt    time.Time      `json:"created_at"`
    UpdatedAt    time.Time      `json:"updated_at"`
}
```

要求：

- 字段命名和 JSON 输出要稳定。
- Product 数量为空时返回明确错误。
- 单次 batch 限制默认最多 50 个商品，避免误跑太大。

### 3.2 Store：支持 BatchJob 存取

在 `internal/store/memory.go` 增加：

- `SaveBatch(job domain.BatchJob)`
- `Batch(id string) (domain.BatchJob, bool)`
- `Batches() []domain.BatchJob`

要求：

- 保持线程安全，如果当前 MemoryStore 已有 mutex，就复用。
- 返回列表按 CreatedAt 或 UpdatedAt 稳定排序。

### 3.3 Engine：批量运行核心方法

在 `internal/engine/engine.go` 增加：

```go
func (e *Engine) RunBatch(ctx context.Context, req domain.BatchRunRequest) (domain.BatchJob, error)
func (e *Engine) ListBatches() []domain.BatchJob
func (e *Engine) GetBatch(id string) (domain.BatchJob, bool)
```

RunBatch 行为：

1. 校验 Products 非空，数量 <= 50。
2. 为每个 Product 创建 Project。
3. 自动生成 Brief。
4. 自动生成 Variants，默认 10 个；如果 `variant_count` 指定则使用指定数量，最大 50。
5. 自动导出发布包：默认 `markdown` 和 `json`。
6. 如果 `render=true`，则对每个项目调用现有 OpenMontage renderer。注意：无 API key 时也必须尽量通过已有 fallback / local asset 渲染，失败时记录到 item.Error，不要让整个 batch panic。
7. 生成 batch report 文件，建议位置：

   ```text
   data/batches/<batch_id>/batch_report.json
   data/batches/<batch_id>/batch_report.md
   data/batches/<batch_id>/projects/<project_id>/publish_package.md
   data/batches/<batch_id>/projects/<project_id>/publish_package.json
   ```

8. 单个 item 失败时：该 item 标记 failed，继续处理下一个。最终只要至少一个成功，batch 可以 completed_with_errors 或 completed。若新增 `completed_with_errors` 状态更清晰，也可以加。

9. Engine 必须把 BatchJob 保存进 store。

### 3.4 Export：真实写出本地文件

现在 `ExportProject` 主要返回 response。请新增文件导出能力，建议：

```go
func (e *Engine) ExportProjectFiles(project domain.VideoProject, dir string, formats []string) ([]string, error)
```

或放到单独 `internal/exporter` 包。

要求：

- Markdown 内容复用现有 `renderMarkdown(project)`。
- JSON 使用 pretty print。
- 自动创建目录。
- 返回写出的文件路径。
- 测试中要实际 read back 确认文件存在且包含商品标题。

### 3.5 API：新增 batch endpoints

在 `internal/api/handler.go` 增加路由：

```text
GET  /api/v1/batches
POST /api/v1/batches/run
GET  /api/v1/batches/{id}
```

行为：

- `POST /api/v1/batches/run` 接收 `BatchRunRequest`。
- 首版可以同步执行，先不做异步队列。
- response 返回完整 BatchJob，包括 item 的 project_id、status、error、report_path。
- 错误统一 JSON：`{"error":"..."}`。

### 3.6 平台模板差异化：小红书 / 抖音

当前模板里已有 `xiaohongshu_product_seed` 和 `douyin_product_hook`，但生成逻辑偏小红书。请让 `buildBrief` / scripts / captions 根据 `project.Platform` 或 `project.Template` 做基础差异化。

要求：

小红书：

- 语气：真实体验、避坑、收藏。
- 标题偏“值不值得买 / 新手避坑 / 真实体验”。
- caption 带小红书风格标签。

抖音：

- 前 3 秒更强 hook。
- 语气更短、更直接。
- CTA 更明确。
- 标题偏“别再乱买 / 3 秒看懂 / 这类人适合”。
- 分镜节奏更快，shot 建议更短。

请增加测试证明同一 Product 在小红书和抖音输出标题 / caption / hook 有明显差异。

### 3.7 Smoke 输入样例

新增一个样例请求文件：

```text
smoke/batch_products_request.json
```

内容至少包含 3 个商品，例如：

1. 宠物自动饮水机
2. 桌面氛围灯
3. 便携筋膜枪

字段包含：title、price、category、selling_points、pain_points、target_audience、usage_scenarios、assets。

新增一个运行脚本可选：

```text
smoke/run_batch_smoke.sh
```

脚本行为：

1. 检查 `/health`。
2. POST `/api/v1/batches/run`。
3. 打印 batch id、report path、每个 project id。

注意：脚本不要依赖 jq，尽量用 `python` 解析 JSON，因为当前 Windows 环境 python 可用。

### 3.8 tRPC-Agent-Go workflow 模块化落地

必须按 `docs/TRPC_AGENT_GO_PLAN.md` 实施，不允许继续把所有生产流程写在 `internal/engine/engine.go` 里。

新增或完善目录：

```text
internal/workflow/
  doc.go              # 中文包说明：确定性 workflow 层职责
  state.go            # WorkflowState，承载 VideoProject、Brief、Variants、Artifacts
  node.go             # WorkflowNode 接口，Name/Run/Inputs/Outputs
  product_seed.go     # 小红书/抖音商品短视频 workflow runner
  nodes_product.go    # ProductAnalyzer / AnglePlanner
  nodes_script.go     # ScriptWriter / Director
  nodes_material.go   # MaterialPlanner / VoicePlanner / SubtitlePlanner
  nodes_qa.go         # QAReviewer / VariantGenerator
  nodes_export.go     # ExportPackager

internal/agentkit/
  doc.go              # 中文包说明：tRPC-Agent-Go 适配层职责
  runner.go           # AgentRunner 接口，默认 deterministic runner
  graph.go            # GraphAgent 构建入口/占位适配
  tools.go            # Tool registry 适配
  artifacts.go        # Artifact path 管理

internal/tools/
  doc.go              # 中文包说明：工具层职责
  product.go          # 商品输入规整、卖点清洗
  template.go         # 平台模板加载/选择
  asset.go            # 素材路径校验/缺失素材标记
  subtitle.go         # SRT/ASS 规划工具
  qa.go               # 平台风格/禁用词/字幕长度检查

internal/exporter/
  doc.go              # 中文包说明：发布包与 batch report 文件导出
  package.go          # publish_package.md/json
  batch_report.go     # batch_report.md/json
```

最低要求：

1. `internal/workflow` 要成为生成 brief / variants / export 输入的主路径。
2. `engine.GenerateBrief`、`engine.GenerateVariants`、`engine.RunBatch` 应调用 workflow/agentkit，而不是复制一套逻辑。
3. `internal/agentkit` 要体现 tRPC-Agent-Go 映射关系：GraphAgent、Tool、Session State、Memory、Artifact、Evaluation、Telemetry。若暂时不引入真实第三方包，必须提供 adapter interface 和 TODO 文档，保证后续替换成本低。
4. 如果选择真实引入 tRPC-Agent-Go 依赖，需要先确认模块路径和版本，更新 `go.mod`，并保证 `go test ./...` 能通过。不要盲目写无法编译的 import。
5. Workflow 节点必须可独立测试，至少覆盖 ProductAnalyzer、ScriptWriter、VariantGenerator、ExportPackager。

### 3.9 每个模块必须有中文文档

除 README 外，必须新增：

```text
docs/modules/WORKFLOW.md
docs/modules/AGENTKIT_TRPC_AGENT_GO.md
docs/modules/TOOLS.md
docs/modules/EXPORTER.md
docs/modules/BATCH_ENGINE.md
docs/modules/API_BATCH.md
docs/modules/OPENMONTAGE_RENDER.md
```

每篇中文文档至少包含：

1. 模块职责。
2. 关键文件。
3. 输入 / 输出数据结构。
4. 调用链路。
5. 与 tRPC-Agent-Go 的关系。
6. 无 API key fallback 行为。
7. 测试方法。
8. 常见失败原因和排查方式。

代码层面也要有中文注释：

- 每个新增 package 至少一个 `doc.go` 包注释。
- 每个 exported type/function 都要有中文注释。
- 复杂流程函数要在函数顶部用中文说明执行步骤。

### 3.10 文档更新

更新 README 或新增 docs：

- 批量 API 使用方法。
- smoke 命令。
- 输出目录说明。
- render=false 与 render=true 的区别。
- 无 API key 的 fallback 说明。
- tRPC-Agent-Go 分层设计。
- 每个模块文档索引。
- 产品目标文档 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md` 的入口链接。
- 面向运营人员的“如何审核发布包 / 如何回填数据 / 如何做下一轮 A/B 测试”说明。

建议新增：

```text
docs/BATCH_VIDEO_FACTORY.md
```

## 4. 验收测试

Codex 实现完成后必须运行并报告真实输出：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
gofmt -w ./cmd ./internal
go test ./...
```

如果新增 shell 脚本，也要做语法/可执行检查：

```bash
bash -n smoke/run_batch_smoke.sh
```

建议增加并通过这些测试：

1. `TestWorkflowNodesGenerateBriefState`
   - 直接测试 `internal/workflow` runner。
   - 断言 ProductAnalyzer、AnglePlanner、ScriptWriter、Director、QAReviewer 顺序执行。
   - 断言输出 PublishingBrief 不为空。

2. `TestRunBatchCreatesProjectsBriefsVariantsAndExports`
   - 构造 3 个 Product。
   - 调用 Engine.RunBatch。
   - 断言 batch 有 3 个 item。
   - 每个 item 有 ProjectID。
   - 每个项目存在 Brief 和 Variants。
   - batch_report.json / md 写出。
   - 每个 publish_package.md/json 写出。

3. `TestRunBatchRejectsEmptyProducts`
   - Products 为空应返回错误。

4. `TestRunBatchLimitsProductCount`
   - 超过 50 个商品应返回错误。

5. `TestPlatformSpecificBriefDiffers`
   - 同一个 Product，小红书和抖音输出不同 hook/caption/title。

6. Handler 测试或端到端 smoke：
   - POST `/api/v1/batches/run` 返回 200。
   - GET `/api/v1/batches/{id}` 能取回。

6. 文档完整性检查：
   - `docs/modules/WORKFLOW.md` 等 7 个模块中文文档存在。
   - 新增 package 都有 `doc.go`。
   - README 或 `docs/BATCH_VIDEO_FACTORY.md` 有模块文档索引。

## 5. 建议实施顺序

请 Codex 按以下顺序改，避免大改失控：

1. 先读代码：`internal/domain/types.go`、`internal/store/memory.go`、`internal/engine/engine.go`、`internal/api/handler.go`、`docs/TRPC_AGENT_GO_PLAN.md`。
2. 建立 `internal/workflow`：WorkflowState、WorkflowNode、确定性节点 runner，并把现有 brief 生成逻辑迁移进去。
3. 建立 `internal/agentkit`：定义 tRPC-Agent-Go adapter interface、GraphAgent 映射入口、Tool/Artifact 抽象；真实依赖不可确认时不要写坏 import。
4. 建立 `internal/tools` 和 `internal/exporter`，把商品规整、模板、QA、导出文件能力拆出去。
5. 加 domain batch 类型。
6. 加 store batch 方法。
7. 实现 Engine.RunBatch，让它调用 workflow/agentkit/exporter，不要在 engine 里堆业务细节。
8. 加 API endpoints。
9. 加平台差异化。
10. 加测试。
11. 加 smoke JSON / shell。
12. 新增 `docs/modules/*.md` 中文模块文档，并更新 README / `docs/BATCH_VIDEO_FACTORY.md`。
13. gofmt + go test。
14. 如果时间足够，再支持 batch render=true；若 OpenMontage 本地依赖不稳定，render=true 失败要记录 item error，不能影响 render=false。

## 6. 最终交付要求

Codex 最后回复必须包含：

1. 修改了哪些文件。
2. 新增了哪些 API。
3. 新增了哪些 tRPC-Agent-Go / workflow / agentkit 模块，以及它们如何调用。
4. 每个模块对应的中文文档路径。
5. 输出目录在哪里。
6. 运行了哪些验证命令。
7. `go test ./...` 的真实结果。
8. 如果有任何未完成项或 blocker，明确说明。

## 7. 非目标

本次不要做：

- 不要接真实发布到小红书 / 抖音账号。
- 不要强制接云 API key。
- 不要引入大型前端框架。
- 不要删除或重写 OpenMontage worker 仓库。
- 不要把项目迁移到 Python/FastAPI。
- 不要做复杂异步队列，第一版同步 batch 即可。

## 8. 可以直接粘给 Codex 的一句话目标

请在 `D:/agent/vertical-intel-video-agent` 中实现 `docs/CODEX_TARGET_BATCH_VIDEO_FACTORY.md` 描述的批量视频工厂 MVP：先读取 `docs/PRODUCT_TARGET_BATCH_VIDEO_FACTORY.md` 理解产品目标和验收口径，再以 `docs/TRPC_AGENT_GO_PLAN.md` 为架构约束，新增 `internal/workflow` 确定性节点、`internal/agentkit` tRPC-Agent-Go GraphAgent/Tool/Artifact 适配层、`internal/tools` 工具层和 `internal/exporter` 导出层；新增 batch domain/store/engine/API，支持一次输入多个商品，批量生成运营人员可审核的 brief、titles、cover texts、scripts、storyboard、material plan、variants、Markdown/JSON 发布包、本地 batch report，并加入小红书/抖音模板差异化、smoke 请求文件、中文模块文档和 Go 测试。每个新增 package 要有中文 `doc.go`，每个 exported 类型/函数要有中文注释，`docs/modules/` 下要写清楚每个模块的职责、输入输出、调用链路、与 tRPC-Agent-Go 的关系、fallback 和测试方法。保持 Go net/http 架构、OpenMontage 集成不破坏、无 API key fallback 可跑通。最后运行 `gofmt -w ./cmd ./internal`、`bash -n smoke/run_batch_smoke.sh`、`go test ./...` 并报告真实输出。
