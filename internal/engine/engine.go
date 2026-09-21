package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/agentkit"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/exporter"
	"github.com/GZQ369/judgment-media-platform/internal/publisher"
	"github.com/GZQ369/judgment-media-platform/internal/render"
	"github.com/GZQ369/judgment-media-platform/internal/store"
	"github.com/GZQ369/judgment-media-platform/internal/tools"
	"github.com/GZQ369/judgment-media-platform/internal/workflow"
)

var (
	ErrProjectNotFound                 = errors.New("project not found")
	ErrBatchNotFound                   = errors.New("batch not found")
	ErrInvalidBatch                    = errors.New("invalid batch request")
	ErrPublicationOrchestratorRequired = errors.New("legacy project publish API is disabled; use the publication orchestrator transaction API")
)

// Engine 是 HTTP API 调用的应用服务层，负责串联 workflow、exporter、render 和 store。
type Engine struct {
	store      *store.MemoryStore
	dataDir    string
	runner     workflow.AgentExecutor
	publishers *publisher.Registry
}

// NewEngine 使用默认 data 目录创建应用服务。
func NewEngine(s *store.MemoryStore) *Engine {
	return NewEngineWithDataDir(s, getenv("VIDEO_FACTORY_DATA_DIR", "data"))
}

// NewEngineWithDataDir 使用指定 data 目录创建应用服务，主要供测试隔离产物。
func NewEngineWithDataDir(s *store.MemoryStore, dataDir string) *Engine {
	return NewEngineWithPublishers(s, dataDir, publisher.NewDefaultRegistry())
}

// NewEngineWithPublishers 允许注入平台适配器，供部署配置和集成测试使用。
func NewEngineWithPublishers(s *store.MemoryStore, dataDir string, publishers *publisher.Registry) *Engine {
	if dataDir == "" {
		dataDir = "data"
	}
	if publishers == nil {
		publishers = publisher.NewDefaultRegistry()
	}
	return &Engine{store: s, dataDir: filepath.Clean(dataDir), runner: agentkit.NewDeterministicRunner(), publishers: publishers}
}

// Templates 返回当前支持的平台模板。
func (e *Engine) Templates() []domain.Template {
	return []domain.Template{
		{
			ID:          "xiaohongshu_product_seed",
			Name:        "小红书供给内容发布包",
			Platform:    domain.PlatformXiaohongshu,
			Description: "输入商品、服务或能力及其价值点和素材，生成标题、封面、脚本、分镜、素材计划、发布文案和测试变体。",
			Stages:      []string{"product_analyzer", "angle_planner", "script_writer", "director", "material_planner", "voice_planner", "subtitle_planner", "qa_reviewer", "variant_generator", "export_packager"},
		},
		{
			ID:          "douyin_product_hook",
			Name:        "抖音强钩子供给短视频",
			Platform:    domain.PlatformDouyin,
			Description: "更强前 3 秒钩子、更快节奏、更明确 CTA，适合商品、服务和能力的快速判断与短句转化。",
			Stages:      []string{"product_analyzer", "angle_planner", "script_writer", "director", "material_planner", "qa_reviewer", "variant_generator", "export_packager"},
		},
		{
			ID:          "wechat_channels_product_trust",
			Name:        "视频号生活化推荐",
			Platform:    domain.PlatformWechatChannels,
			Description: "后续模板：更生活化、更可信、适合私域转化。",
			Stages:      []string{"product_analyzer", "trust_angle", "script_writer", "director", "export_packager"},
		},
	}
}

// CreateProject 创建单个视频生产项目。
func (e *Engine) CreateProject(req domain.CreateProjectRequest) domain.VideoProject {
	now := time.Now().UTC()
	platform := tools.NormalizePlatform(req.Platform)
	template := tools.NormalizeTemplate(platform, req.Template)
	duration := req.DurationSeconds
	if duration <= 0 {
		duration = tools.ProfileForPlatform(platform).DefaultDuration
	}
	offering := tools.NormalizeOffering(tools.ResolveOffering(req.Offering, req.Product), platform)
	name := tools.DefaultString(req.Name, offering.Name+"发布包")
	project := domain.VideoProject{
		ID:              tools.NewID("proj"),
		Name:            name,
		Platform:        platform,
		Template:        template,
		DurationSeconds: duration,
		Status:          "draft",
		Offering:        offering,
		Product:         offering,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	e.store.SaveProject(project)
	return project
}

// ListProjects 返回所有项目。
func (e *Engine) ListProjects() []domain.VideoProject { return e.store.Projects() }

// GetProject 按 ID 返回项目。
func (e *Engine) GetProject(id string) (domain.VideoProject, bool) { return e.store.Project(id) }

// GenerateBrief 通过 workflow 生成单项目发布 brief。
func (e *Engine) GenerateBrief(projectID string) (domain.VideoProject, error) {
	project, ok := e.store.Project(projectID)
	if !ok {
		return domain.VideoProject{}, ErrProjectNotFound
	}
	state, err := workflow.NewProductSeedRunner(e.runner).Run(context.Background(), project, workflow.RunOptions{})
	if err != nil {
		return domain.VideoProject{}, err
	}
	project = state.ProjectWithOutputs()
	project.Status = "brief_generated"
	project.UpdatedAt = time.Now().UTC()
	e.store.SaveProject(project)
	return project, nil
}

// GenerateVariants 通过 workflow 生成 brief 和 A/B 测试变体。
func (e *Engine) GenerateVariants(projectID string, count int) (domain.VideoProject, error) {
	project, ok := e.store.Project(projectID)
	if !ok {
		return domain.VideoProject{}, ErrProjectNotFound
	}
	count = normalizeVariantCount(count)
	state, err := workflow.NewProductSeedRunner(e.runner).Run(context.Background(), project, workflow.RunOptions{VariantCount: count})
	if err != nil {
		return domain.VideoProject{}, err
	}
	project = state.ProjectWithOutputs()
	project.Status = "variants_generated"
	project.UpdatedAt = time.Now().UTC()
	e.store.SaveProject(project)
	return project, nil
}

// UpdateVariantMetrics 保存人工回填的变体指标。
func (e *Engine) UpdateVariantMetrics(projectID, variantID string, metrics domain.VariantMetric) (domain.VideoProject, error) {
	project, ok := e.store.Project(projectID)
	if !ok {
		return domain.VideoProject{}, ErrProjectNotFound
	}
	for i := range project.Variants {
		if project.Variants[i].ID == variantID {
			metrics.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			project.Variants[i].Metrics = metrics
			project.Status = "metrics_recorded"
			project.UpdatedAt = time.Now().UTC()
			e.store.SaveProject(project)
			return project, nil
		}
	}
	return domain.VideoProject{}, errors.New("variant not found")
}

// ExportProject 返回内存中的 Markdown 或 JSON 发布包。
func (e *Engine) ExportProject(projectID string, format string) (domain.ExportPackage, error) {
	project, ok := e.store.Project(projectID)
	if !ok {
		return domain.ExportPackage{}, ErrProjectNotFound
	}
	if project.Brief == nil {
		var err error
		project, err = e.GenerateBrief(project.ID)
		if err != nil {
			return domain.ExportPackage{}, err
		}
	}
	format = tools.DefaultString(format, "markdown")
	pkg := domain.ExportPackage{ProjectID: projectID, Format: format}
	if strings.EqualFold(format, "json") {
		pkg.JSON = project
		return pkg, nil
	}
	pkg.Markdown = exporter.RenderMarkdown(project)
	return pkg, nil
}

// ExportProjectFiles 把单项目发布包写出到指定目录。
func (e *Engine) ExportProjectFiles(project domain.VideoProject, dir string, formats []string) ([]string, error) {
	if project.Brief == nil {
		var err error
		project, err = e.GenerateBrief(project.ID)
		if err != nil {
			return nil, err
		}
	}
	return exporter.ExportProjectFiles(project, dir, formats)
}

// RunBatch 同步运行批量视频工厂，逐个供给生成项目、变体、发布包和 batch report。
func (e *Engine) RunBatch(ctx context.Context, req domain.BatchRunRequest) (domain.BatchJob, error) {
	offerings := req.Offerings
	if len(offerings) == 0 {
		offerings = req.Products
	}
	if len(offerings) == 0 {
		return domain.BatchJob{}, fmt.Errorf("%w: offerings is required", ErrInvalidBatch)
	}
	if len(offerings) > 50 {
		return domain.BatchJob{}, fmt.Errorf("%w: offerings must not exceed 50", ErrInvalidBatch)
	}
	platform := tools.NormalizePlatform(req.Platform)
	template := tools.NormalizeTemplate(platform, req.Template)
	duration := req.DurationSeconds
	if duration <= 0 {
		duration = tools.ProfileForPlatform(platform).DefaultDuration
	}
	variantCount := normalizeVariantCount(req.VariantCount)
	formats := req.ExportFormats
	if len(formats) == 0 {
		formats = []string{"markdown", "json"}
	}
	now := time.Now().UTC()
	batchID := tools.NewID("batch")
	artifactManager := agentkit.NewArtifactManager(e.dataDir)
	job := domain.BatchJob{
		ID:           batchID,
		Name:         tools.DefaultString(req.Name, "批量视频工厂 "+now.Format("20060102-150405")),
		Status:       domain.BatchStatusRunning,
		Platform:     platform,
		Template:     template,
		VariantCount: variantCount,
		Render:       req.Render,
		Items:        make([]domain.BatchItem, 0, len(offerings)),
		ExportDir:    artifactManager.BatchDir(batchID),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for i, offering := range offerings {
		name := tools.DefaultString(tools.DefaultString(offering.Name, offering.Title), fmt.Sprintf("供给%d", i+1))
		job.Items = append(job.Items, domain.BatchItem{Index: i + 1, OfferingName: name, ProductTitle: name, Status: domain.BatchStatusPending})
	}
	e.store.SaveBatch(job)

	for i := range offerings {
		job.Items[i].Status = domain.BatchStatusRunning
		job.UpdatedAt = time.Now().UTC()
		e.store.SaveBatch(job)
		item := &job.Items[i]
		project, paths, renderErr, err := e.runBatchItem(ctx, batchID, offerings[i], platform, template, duration, variantCount, formats, req.Render, req.RenderRequest)
		if project.ID != "" {
			item.ProjectID = project.ID
		}
		fillExportPathsFromList(item, paths)
		if err != nil {
			item.Status = domain.BatchStatusFailed
			item.Error = err.Error()
			job.UpdatedAt = time.Now().UTC()
			e.store.SaveBatch(job)
			continue
		}
		if renderErr != nil {
			item.Status = domain.BatchStatusCompletedWithErrors
			item.Error = renderErr.Error()
		} else {
			item.Status = domain.BatchStatusCompleted
		}
		if project.Render != nil && project.Render.VideoPath != "" {
			item.RenderVideoPath = project.Render.VideoPath
		}
		if item.PublishPackagePath == "" || item.PublishPackageJSONPath == "" {
			fillExportPaths(item, artifactManager.ProjectDir(batchID, project.ID))
		}
		job.UpdatedAt = time.Now().UTC()
		e.store.SaveBatch(job)
	}

	job.Status = summarizeBatchStatus(job.Items)
	job.UpdatedAt = time.Now().UTC()
	job.ReportPath = filepath.Join(job.ExportDir, "batch_report.md")
	job.ReportJSONPath = filepath.Join(job.ExportDir, "batch_report.json")
	reportPath, reportJSONPath, err := exporter.ExportBatchReportFiles(job, job.ExportDir)
	if err != nil {
		job.Status = domain.BatchStatusCompletedWithErrors
		job.UpdatedAt = time.Now().UTC()
		e.store.SaveBatch(job)
		return job, err
	}
	job.ReportPath = reportPath
	job.ReportJSONPath = reportJSONPath
	e.store.SaveBatch(job)
	return job, nil
}

// ListBatches 返回所有 batch job。
func (e *Engine) ListBatches() []domain.BatchJob { return e.store.Batches() }

// GetBatch 按 ID 返回 batch job。
func (e *Engine) GetBatch(id string) (domain.BatchJob, bool) { return e.store.Batch(id) }

// RenderOpenMontage 调用现有 OpenMontage 适配器渲染项目。
func (e *Engine) RenderOpenMontage(ctx context.Context, projectID string, req domain.RenderOpenMontageRequest) (domain.VideoProject, error) {
	project, ok := e.store.Project(projectID)
	if !ok {
		return domain.VideoProject{}, ErrProjectNotFound
	}
	if project.Brief == nil {
		var err error
		project, err = e.GenerateBrief(project.ID)
		if err != nil {
			return domain.VideoProject{}, err
		}
	}
	result, err := render.NewOpenMontageRenderer("", e.dataDir).RenderProject(ctx, project, req)
	project.Render = &result
	if err != nil {
		project.Status = "render_failed"
		project.UpdatedAt = time.Now().UTC()
		e.store.SaveProject(project)
		return project, err
	}
	project.Status = "rendered"
	project.UpdatedAt = time.Now().UTC()
	e.store.SaveProject(project)
	return project, nil
}

// PublishingCapabilities 返回各平台当前可用的官方发布模式和配置状态。
func (e *Engine) PublishingCapabilities(ctx context.Context) []domain.PlatformPublishCapability {
	return e.publishers.Capabilities(ctx)
}

// PublishProject 在一次显式确认后向一个或多个平台执行发布或官方客户端交接。
func (e *Engine) PublishProject(ctx context.Context, projectID string, req domain.PublishProjectRequest) (domain.PublishProjectResult, error) {
	// Keep the legacy method source-compatible for callers that still compile
	// against the engine, but make it impossible to reach a Publisher from the
	// old project endpoint. Only publication.Service may consume authorization,
	// reserve a package fingerprint, and call an adapter.
	_ = ctx
	_ = projectID
	_ = req
	return domain.PublishProjectResult{}, ErrPublicationOrchestratorRequired
}

func normalizePublishPlatforms(req domain.PublishProjectRequest, fallback domain.Platform) []domain.Platform {
	platforms := append([]domain.Platform(nil), req.Platforms...)
	if len(platforms) == 0 && req.Platform != "" {
		platforms = append(platforms, req.Platform)
	}
	if len(platforms) == 0 {
		platforms = append(platforms, fallback)
	}
	seen := map[domain.Platform]bool{}
	out := make([]domain.Platform, 0, len(platforms))
	for _, platform := range platforms {
		if platform != "" && !seen[platform] {
			seen[platform] = true
			out = append(out, platform)
		}
	}
	return out
}

func (e *Engine) runBatchItem(ctx context.Context, batchID string, offering domain.OfferingInput, platform domain.Platform, template string, duration int, variantCount int, formats []string, shouldRender bool, renderReq domain.RenderOpenMontageRequest) (domain.VideoProject, []string, error, error) {
	name := tools.DefaultString(offering.Name, offering.Title)
	project := e.CreateProject(domain.CreateProjectRequest{
		Name:            tools.DefaultString(name, "未命名供给") + "发布包",
		Platform:        platform,
		Template:        template,
		DurationSeconds: duration,
		Offering:        offering,
	})
	project, err := e.GenerateVariants(project.ID, variantCount)
	if err != nil {
		return project, nil, nil, err
	}
	projectDir := agentkit.NewArtifactManager(e.dataDir).ProjectDir(batchID, project.ID)
	paths, err := e.ExportProjectFiles(project, projectDir, formats)
	if err != nil {
		return project, paths, nil, err
	}
	if shouldRender {
		rendered, renderErr := e.RenderOpenMontage(ctx, project.ID, renderReq)
		if rendered.Render != nil && rendered.Render.VideoPath != "" {
			project.Render = rendered.Render
		}
		if renderErr != nil {
			return project, paths, renderErr, nil
		}
	}
	return project, paths, nil, nil
}

func normalizeVariantCount(count int) int {
	if count <= 0 {
		return 10
	}
	if count > 50 {
		return 50
	}
	return count
}

func summarizeBatchStatus(items []domain.BatchItem) domain.BatchStatus {
	success := 0
	failedOrWarn := 0
	for _, item := range items {
		switch item.Status {
		case domain.BatchStatusCompleted:
			success++
		case domain.BatchStatusCompletedWithErrors:
			success++
			failedOrWarn++
		default:
			failedOrWarn++
		}
	}
	if success == 0 {
		return domain.BatchStatusFailed
	}
	if failedOrWarn > 0 {
		return domain.BatchStatusCompletedWithErrors
	}
	return domain.BatchStatusCompleted
}

func fillExportPaths(item *domain.BatchItem, projectDir string) {
	item.PublishPackagePath = filepath.Join(projectDir, "publish_package.md")
	item.PublishPackageJSONPath = filepath.Join(projectDir, "publish_package.json")
}

func fillExportPathsFromList(item *domain.BatchItem, paths []string) {
	for _, path := range paths {
		switch strings.ToLower(filepath.Base(path)) {
		case "publish_package.md":
			item.PublishPackagePath = path
		case "publish_package.json":
			item.PublishPackageJSONPath = path
		}
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
