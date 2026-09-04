# Exporter 模块

## 模块职责

`internal/exporter` 负责写出单项目发布包和 batch report，不参与业务生成。它保证 Markdown 面向运营可读，JSON 面向系统可追踪。

## 关键文件

- `internal/exporter/package.go`
- `internal/exporter/batch_report.go`

## 输入 / 输出

输入是 `domain.VideoProject` 或 `domain.BatchJob`。输出是：

- `publish_package.md`
- `publish_package.json`
- `batch_report.md`
- `batch_report.json`

## 调用链路

Engine 在 brief/variants 生成后调用 `ExportProjectFiles`。RunBatch 完成所有 item 后调用 `ExportBatchReportFiles`。

## 与 tRPC-Agent-Go 的关系

Exporter 对应 ExportPackagerAgent 的 Artifact 写入工具。后续可以把写文件替换为 tRPC-Agent-Go Artifact service 或对象存储。

## 无 API key fallback

Exporter 不依赖模型和网络。只要 project 有 brief，就能写出完整发布包。

## 测试方法

运行：

```bash
go test ./internal/engine
```

`TestRunBatchCreatesProjectsBriefsVariantsAndExports` 会 read back Markdown/JSON 文件并确认包含商品标题。

## 常见失败与排查

- 文件不存在：检查 Engine 传入的 dataDir 和 batch/project 目录。
- Markdown 缺少变体：确认 RunBatch 或 GenerateVariants 已先生成 `project.Variants`。
