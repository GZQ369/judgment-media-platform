package workflow

import (
	"context"
	"time"
)

// WorkflowNode 是确定性 workflow 节点接口，对齐 tRPC-Agent-Go graph node。
type WorkflowNode interface {
	Name() string
	Inputs() []string
	Outputs() []string
	Run(ctx context.Context, state *WorkflowState) error
}

// RunOptions 控制单次 workflow 是否生成变体。
type RunOptions struct {
	VariantCount int
}

func runNode(ctx context.Context, executor AgentExecutor, node WorkflowNode, state *WorkflowState) error {
	started := time.Now().UTC()
	state.NodeLog = append(state.NodeLog, NodeExecution{Name: node.Name(), Status: "running", StartedAt: started})
	if executor != nil {
		if err := executor.ExecuteNode(ctx, node.Name(), state); err != nil {
			state.NodeLog[len(state.NodeLog)-1].Status = "failed"
			state.NodeLog[len(state.NodeLog)-1].EndedAt = time.Now().UTC()
			return err
		}
	}
	if err := node.Run(ctx, state); err != nil {
		state.NodeLog[len(state.NodeLog)-1].Status = "failed"
		state.NodeLog[len(state.NodeLog)-1].EndedAt = time.Now().UTC()
		return err
	}
	if state.NodeLog[len(state.NodeLog)-1].Mode == "" {
		state.NodeLog[len(state.NodeLog)-1].Mode = "deterministic"
	}
	state.NodeLog[len(state.NodeLog)-1].Status = "completed"
	state.NodeLog[len(state.NodeLog)-1].EndedAt = time.Now().UTC()
	return nil
}
