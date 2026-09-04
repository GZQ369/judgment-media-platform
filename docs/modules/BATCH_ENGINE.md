# Batch Engine 模块

## 模块职责

`internal/engine` 是应用服务层。它负责同步批量运行：创建项目、执行 workflow、生成变体、导出文件、可选渲染、保存 batch job。

## 关键文件

- `internal/engine/engine.go`
- `internal/store/memory.go`
- `internal/domain/types.go`

## 输入 / 输出

输入是 `domain.BatchRunRequest`。输出是 `domain.BatchJob`，其中包含每个 item 的 project id、状态、发布包路径、错误和 report 路径。

## 调用链路

`POST /api/v1/batches/run` 调用 `Engine.RunBatch`。RunBatch 按商品循环调用 `CreateProject`、`GenerateVariants`、`ExportProjectFiles`，最后写出 batch report。

## 与 tRPC-Agent-Go 的关系

Engine 不直接实现 Agent 逻辑，只调用 workflow 和 agentkit runner。这样后续替换为真实 GraphAgent 时，API 和 batch 编排不用大改。

## 无 API key fallback

默认走 deterministic workflow。`render=false` 是稳定主路径。`render=true` 是增强路径，失败只记录 item error，不影响已生成发布包。

## 测试方法

运行：

```bash
go test ./internal/engine
```

覆盖空 products、超过 50 个 products、3 商品 batch、平台差异化和文件写出。

## 常见失败与排查

- `products is required`：请求没有商品。
- `products must not exceed 50`：单批商品超过默认上限。
- batch completed_with_errors：查看 item error，通常是可选 render 或单个商品导出失败。
