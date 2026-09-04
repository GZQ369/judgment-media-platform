# Agentkit / tRPC-Agent-Go 适配模块

## 模块职责

`internal/agentkit` 定义本项目接入 tRPC-Agent-Go 的边界。当前阶段不引入真实第三方依赖，而是提供 deterministic runner、GraphSpec、ToolRegistry 和 ArtifactManager。

## 关键文件

- `internal/agentkit/runner.go`
- `internal/agentkit/graph.go`
- `internal/agentkit/tools.go`
- `internal/agentkit/artifacts.go`

## 输入 / 输出

输入是 workflow 节点名和 workflow state。输出是节点执行模式记录，以及 GraphAgent、Tool、Artifact 的映射描述。

## 调用链路

Engine 创建 `agentkit.NewDeterministicRunner()`，传入 workflow runner。workflow 每执行一个节点前调用 `ExecuteNode`，当前实现记录 `deterministic_fallback`。

## 与 tRPC-Agent-Go 的关系

- `ProductSeedGraphSpec` 映射 GraphAgent。
- `GraphNodeMapping` 映射 graph node / Agent。
- `ToolRegistry` 映射 tools。
- `ArtifactManager` 映射 Artifact 路径。
- QA 分数可映射 Evaluation。
- `NodeExecution` 可映射 Telemetry。

## 无 API key fallback

`DeterministicRunner` 不调用模型，确保本地测试和 smoke 稳定。真实 LLM executor 只应作为可选增强。

## 测试方法

运行：

```bash
go test ./internal/workflow ./internal/engine
```

检查 workflow 节点日志中的 mode 是否为 `deterministic_fallback`。

## 常见失败与排查

- 后续引入真实依赖后编译失败：先回退到 adapter interface，不要在业务层直接 import 不稳定包。
- Artifact 路径混乱：统一使用 `ArtifactManager` 生成 batch 和 project 目录。
