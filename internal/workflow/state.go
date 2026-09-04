package workflow

import (
	"context"
	"time"

	"vertical-intel-video-agent/internal/domain"
)

// AgentExecutor 是 workflow 调用 agentkit runner 的最小接口。
type AgentExecutor interface {
	ExecuteNode(ctx context.Context, nodeName string, state any) error
}

// NodeExecution 记录一个 workflow 节点的执行摘要。
type NodeExecution struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Mode      string    `json:"mode"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// WorkflowState 承载单个项目在 workflow 中逐步生成的结构化产物。
type WorkflowState struct {
	Project  domain.VideoProject  `json:"project"`
	Offering domain.OfferingInput `json:"offering"`
	// Product mirrors Offering while legacy nodes and clients migrate.
	Product       domain.ProductInput `json:"product"`
	Analysis      domain.ProductAnalysis
	Angles        []domain.ContentAngle
	TitleOptions  []string
	CoverTexts    []string
	Scripts       []domain.Script
	Storyboard    domain.Storyboard
	MaterialPlan  domain.MaterialPlan
	VoiceoverText string
	SubtitleText  string
	Caption       string
	Tags          []string
	QA            domain.QAReport
	Variants      []domain.Variant
	Artifacts     map[string]string
	NodeLog       []NodeExecution
	VariantCount  int
}

// NewWorkflowState 创建 workflow 初始状态。
func NewWorkflowState(project domain.VideoProject, variantCount int) *WorkflowState {
	offering := project.Offering
	if offering.Kind == "" && offering.Name == "" && offering.Title == "" {
		offering = project.Product
	}
	return &WorkflowState{
		Project:      project,
		Offering:     offering,
		Product:      offering,
		Artifacts:    map[string]string{},
		VariantCount: variantCount,
	}
}

// RecordAgentExecution 记录 agentkit runner 的执行模式。
func (s *WorkflowState) RecordAgentExecution(nodeName string, mode string) {
	for i := range s.NodeLog {
		if s.NodeLog[i].Name == nodeName && s.NodeLog[i].Mode == "" {
			s.NodeLog[i].Mode = mode
			return
		}
	}
}

// Brief 把 workflow state 汇总为可持久化的发布 brief。
func (s *WorkflowState) Brief() domain.PublishingBrief {
	return domain.PublishingBrief{
		ID:              "brief_" + s.Project.ID,
		ProjectID:       s.Project.ID,
		GeneratedAt:     time.Now().UTC(),
		ProductAnalysis: s.Analysis,
		Angles:          s.Angles,
		TitleOptions:    s.TitleOptions,
		CoverTexts:      s.CoverTexts,
		Scripts:         s.Scripts,
		Storyboard:      s.Storyboard,
		MaterialPlan:    s.MaterialPlan,
		VoiceoverText:   s.VoiceoverText,
		SubtitleText:    s.SubtitleText,
		Caption:         s.Caption,
		Tags:            s.Tags,
		QA:              s.QA,
	}
}

// ProjectWithOutputs 返回带 brief 和 variants 的项目副本。
func (s *WorkflowState) ProjectWithOutputs() domain.VideoProject {
	project := s.Project
	brief := s.Brief()
	project.Offering = s.Offering
	project.Product = s.Product
	project.Brief = &brief
	if len(s.Variants) > 0 {
		project.Variants = s.Variants
	}
	return project
}
