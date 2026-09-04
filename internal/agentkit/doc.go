// Package agentkit 定义本项目到 tRPC-Agent-Go 的适配边界。
//
// 当前 MVP 不强行引入第三方依赖，避免在无网络或版本未知时破坏编译。
// 本包先提供 GraphAgent、Tool、Session State、Memory、Artifact、Evaluation、
// Telemetry 的映射结构和 deterministic runner。后续接入真实 tRPC-Agent-Go
// 时，只需要把这些接口背后的实现替换为实际 GraphAgent 执行器。
package agentkit
