// Package workflow 实现批量视频工厂的确定性 Agentic workflow。
//
// 本包按 tRPC-Agent-Go GraphAgent 的方式组织节点，但当前阶段不依赖外部
// LLM 或 API key。每个节点都可以独立测试，后续可逐步替换为 LLM Agent、
// Tool calling 或人工审核节点。
package workflow
