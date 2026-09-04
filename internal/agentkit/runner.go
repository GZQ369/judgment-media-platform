package agentkit

import "context"

// AgentRunner 是 workflow 调用 agent 执行器的最小接口。
type AgentRunner interface {
	ExecuteNode(ctx context.Context, nodeName string, state any) error
}

// DeterministicRunner 是无 API key 时使用的本地确定性执行器。
type DeterministicRunner struct {
	Graph GraphSpec
}

// NewDeterministicRunner 创建默认的确定性 agent runner。
func NewDeterministicRunner() *DeterministicRunner {
	return &DeterministicRunner{Graph: ProductSeedGraphSpec()}
}

// ExecuteNode 记录节点通过 deterministic fallback 执行，真实业务由 workflow 节点完成。
func (r *DeterministicRunner) ExecuteNode(_ context.Context, nodeName string, state any) error {
	if recorder, ok := state.(interface {
		RecordAgentExecution(nodeName string, mode string)
	}); ok {
		recorder.RecordAgentExecution(nodeName, "deterministic_fallback")
	}
	return nil
}
