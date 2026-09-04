# Batch API 模块

## 模块职责

`internal/api` 暴露批量视频工厂 HTTP 接口，保持 Go `net/http` 实现。

## 关键文件

- `internal/api/handler.go`
- `internal/api/handler_test.go`

## 输入 / 输出

新增接口：

```text
GET  /api/v1/batches
POST /api/v1/batches/run
GET  /api/v1/batches/{id}
```

`POST /api/v1/batches/run` 接收 `BatchRunRequest`，返回完整 `BatchJob`。

## 调用链路

Handler 解码 JSON，创建 15 分钟 context，调用 `Engine.RunBatch`，再返回 JSON。错误统一为 `{"error":"..."}`。

## 与 tRPC-Agent-Go 的关系

API 不直接依赖 tRPC-Agent-Go。Agent 编排只存在于 engine -> workflow -> agentkit 链路中。

## 无 API key fallback

API 默认 render=false 时完全本地可运行。没有外部模型和 API key 也能生成发布包和 report。

## 测试方法

运行：

```bash
go test ./internal/api
```

`TestBatchEndpoints` 覆盖 POST run 和 GET by id。

## 常见失败与排查

- 404：检查路径是否为 `/api/v1/batches/run`。
- 400：检查 JSON 字段名是否为 `products`、`variant_count`、`export_formats`。
- 返回 completed_with_errors：优先看 item error。
