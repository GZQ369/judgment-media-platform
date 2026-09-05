package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/store"
)

func sampleProduct(title string) domain.ProductInput {
	return domain.ProductInput{
		Title:          title,
		Price:          "89",
		Category:       "生活好物",
		SellingPoints:  []string{"静音", "过滤", "可拆洗"},
		PainPoints:     []string{"不知道是否真的适合自己"},
		TargetAudience: "新手用户",
		UsageScenarios: []string{"日常使用", "租房小空间"},
		Assets:         []string{"media/smoke/pet_water.png"},
	}
}

func TestProductSeedWorkflow(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	project := e.CreateProject(domain.CreateProjectRequest{
		Name:            "宠物饮水机小红书种草",
		Platform:        domain.PlatformXiaohongshu,
		Template:        "xiaohongshu_product_seed",
		DurationSeconds: 45,
		Product:         sampleProduct("宠物自动饮水机"),
	})
	if project.ID == "" || project.Status != "draft" {
		t.Fatalf("unexpected project: %+v", project)
	}

	project, err := e.GenerateBrief(project.ID)
	if err != nil {
		t.Fatalf("GenerateBrief failed: %v", err)
	}
	if project.Brief == nil {
		t.Fatal("expected brief")
	}
	if len(project.Brief.TitleOptions) < 5 || len(project.Brief.CoverTexts) < 3 || len(project.Brief.Scripts) < 3 {
		t.Fatalf("brief missing options: %+v", project.Brief)
	}
	if len(project.Brief.Storyboard.Shots) == 0 || len(project.Brief.MaterialPlan.NeededAssets) == 0 {
		t.Fatalf("brief missing storyboard/material plan: %+v", project.Brief)
	}

	project, err = e.GenerateVariants(project.ID, 10)
	if err != nil {
		t.Fatalf("GenerateVariants failed: %v", err)
	}
	if len(project.Variants) != 10 {
		t.Fatalf("expected 10 variants, got %d", len(project.Variants))
	}
	if project.Variants[0].Title == project.Variants[1].Title && project.Variants[0].Hook == project.Variants[1].Hook {
		t.Fatalf("variants are not differentiated: %+v", project.Variants[:2])
	}

	exportPkg, err := e.ExportProject(project.ID, "markdown")
	if err != nil {
		t.Fatalf("ExportProject failed: %v", err)
	}
	if !strings.Contains(exportPkg.Markdown, "宠物自动饮水机") || !strings.Contains(exportPkg.Markdown, "A/B 测试变体") {
		t.Fatalf("unexpected markdown export: %s", exportPkg.Markdown)
	}
}

func TestRunBatchCreatesProjectsBriefsVariantsAndExports(t *testing.T) {
	dataDir := t.TempDir()
	e := NewEngineWithDataDir(store.NewMemoryStore(), dataDir)
	job, err := e.RunBatch(context.Background(), domain.BatchRunRequest{
		Name:          "三商品小红书发布包",
		Platform:      domain.PlatformXiaohongshu,
		Template:      "xiaohongshu_product_seed",
		VariantCount:  10,
		ExportFormats: []string{"markdown", "json"},
		Render:        false,
		Products: []domain.ProductInput{
			sampleProduct("宠物自动饮水机"),
			sampleProduct("桌面氛围灯"),
			sampleProduct("便携筋膜枪"),
		},
	})
	if err != nil {
		t.Fatalf("RunBatch failed: %v", err)
	}
	if job.Status != domain.BatchStatusCompleted {
		t.Fatalf("expected completed batch, got %+v", job)
	}
	if len(job.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(job.Items))
	}
	for _, item := range job.Items {
		if item.ProjectID == "" || item.Status != domain.BatchStatusCompleted {
			t.Fatalf("bad item: %+v", item)
		}
		project, ok := e.GetProject(item.ProjectID)
		if !ok || project.Brief == nil || len(project.Variants) != 10 {
			t.Fatalf("project not fully generated: ok=%t project=%+v", ok, project)
		}
		assertFileContains(t, item.PublishPackagePath, project.Product.Title)
		assertFileContains(t, item.PublishPackageJSONPath, project.Product.Title)
	}
	assertFileContains(t, job.ReportPath, "下一步")
	assertFileContains(t, job.ReportJSONPath, "三商品小红书发布包")
}

func TestRunBatchRejectsEmptyProducts(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	_, err := e.RunBatch(context.Background(), domain.BatchRunRequest{})
	if !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch, got %v", err)
	}
}

func TestRunBatchLimitsProductCount(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	products := make([]domain.ProductInput, 51)
	for i := range products {
		products[i] = sampleProduct("商品")
	}
	_, err := e.RunBatch(context.Background(), domain.BatchRunRequest{Products: products})
	if !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch, got %v", err)
	}
}

func TestRunBatchRenderTrueKeepsPublishPackagesOnRenderFailure(t *testing.T) {
	t.Setenv("OPENMONTAGE_DIR", filepath.Join(t.TempDir(), "missing-openmontage"))
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	job, err := e.RunBatch(context.Background(), domain.BatchRunRequest{
		Name:         "render failure keeps package",
		Platform:     domain.PlatformXiaohongshu,
		VariantCount: 10,
		Render:       true,
		Products:     []domain.ProductInput{sampleProduct("宠物自动饮水机")},
	})
	if err != nil {
		t.Fatalf("RunBatch should not fail when optional render fails: %v", err)
	}
	if job.Status != domain.BatchStatusCompletedWithErrors {
		t.Fatalf("expected completed_with_errors, got %+v", job)
	}
	if len(job.Items) != 1 || job.Items[0].Status != domain.BatchStatusCompletedWithErrors {
		t.Fatalf("bad render failure item: %+v", job.Items)
	}
	if job.Items[0].Error == "" {
		t.Fatalf("render failure should be recorded on item")
	}
	assertFileContains(t, job.Items[0].PublishPackagePath, "宠物自动饮水机")
	assertFileContains(t, job.Items[0].PublishPackageJSONPath, "宠物自动饮水机")
}

func TestPlatformSpecificBriefDiffers(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	product := sampleProduct("桌面氛围灯")
	xhs := e.CreateProject(domain.CreateProjectRequest{Platform: domain.PlatformXiaohongshu, Product: product})
	douyin := e.CreateProject(domain.CreateProjectRequest{Platform: domain.PlatformDouyin, Product: product})
	xhs, err := e.GenerateBrief(xhs.ID)
	if err != nil {
		t.Fatalf("xhs brief failed: %v", err)
	}
	douyin, err = e.GenerateBrief(douyin.ID)
	if err != nil {
		t.Fatalf("douyin brief failed: %v", err)
	}
	if xhs.Brief.TitleOptions[0] == douyin.Brief.TitleOptions[0] {
		t.Fatalf("titles should differ: %q", xhs.Brief.TitleOptions[0])
	}
	if xhs.Brief.Scripts[0].Hook == douyin.Brief.Scripts[0].Hook {
		t.Fatalf("hooks should differ: %q", xhs.Brief.Scripts[0].Hook)
	}
	if !strings.Contains(xhs.Brief.Caption, "小红书") || !strings.Contains(douyin.Brief.Caption, "抖音") {
		t.Fatalf("captions should be platform specific:\nxhs=%s\ndouyin=%s", xhs.Brief.Caption, douyin.Brief.Caption)
	}
}

func TestServiceAndCapabilityUseOfferingLanguage(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	tests := []domain.OfferingInput{
		{
			Kind:           domain.OfferingKindService,
			Name:           "短视频剪辑服务",
			ValuePoints:    []string{"48小时交付", "统一品牌风格", "支持两轮修改"},
			Problems:       []string{"团队没有稳定的剪辑产能"},
			TargetAudience: "内容团队",
		},
		{
			Kind:           domain.OfferingKindCapability,
			Name:           "多平台视频发布能力",
			ValuePoints:    []string{"一次提交", "平台状态可追踪", "保留人工确认"},
			Problems:       []string{"重复上传三个平台耗时"},
			TargetAudience: "运营人员",
		},
	}
	for _, offering := range tests {
		project := e.CreateProject(domain.CreateProjectRequest{Platform: domain.PlatformDouyin, Offering: offering})
		project, err := e.GenerateBrief(project.ID)
		if err != nil {
			t.Fatalf("generate %s brief: %v", offering.Kind, err)
		}
		if project.Offering.Kind != offering.Kind || project.Product.Title != offering.Name {
			t.Fatalf("offering compatibility fields not synchronized: %+v", project)
		}
		combined := strings.Join(project.Brief.TitleOptions, " ") + " " + project.Brief.Caption + " " + project.Brief.Scripts[0].FullScript
		for _, forbidden := range []string{"别再乱买", "值不值得买", "买前先看", "商品全貌"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("%s content contains product-only phrase %q: %s", offering.Kind, forbidden, combined)
			}
		}
	}
}

func TestRunBatchAcceptsOfferings(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	job, err := e.RunBatch(context.Background(), domain.BatchRunRequest{
		Platform: domain.PlatformXiaohongshu,
		Offerings: []domain.OfferingInput{{
			Kind:        domain.OfferingKindCapability,
			Name:        "自动剪辑能力",
			ValuePoints: []string{"自动切片", "自动字幕", "自动导出"},
		}},
	})
	if err != nil {
		t.Fatalf("RunBatch offerings: %v", err)
	}
	if len(job.Items) != 1 || job.Items[0].OfferingName != "自动剪辑能力" {
		t.Fatalf("unexpected offering batch: %+v", job)
	}
}

func TestTemplates(t *testing.T) {
	e := NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	templates := e.Templates()
	if len(templates) < 3 {
		t.Fatalf("expected platform templates, got %+v", templates)
	}
	if templates[0].ID != "xiaohongshu_product_seed" {
		t.Fatalf("first MVP template should be xiaohongshu_product_seed, got %+v", templates[0])
	}
}

func TestModuleDocsExist(t *testing.T) {
	docs := []string{
		"WORKFLOW.md",
		"AGENTKIT_TRPC_AGENT_GO.md",
		"TOOLS.md",
		"EXPORTER.md",
		"BATCH_ENGINE.md",
		"API_BATCH.md",
		"OPENMONTAGE_RENDER.md",
		"PUBLISHING.md",
	}
	for _, doc := range docs {
		path := filepath.Join("..", "..", "docs", "modules", doc)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("module doc missing %s: %v", path, err)
		}
	}
}

func assertFileContains(t *testing.T, path string, want string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(payload), want) {
		t.Fatalf("%s does not contain %q:\n%s", path, want, string(payload))
	}
}
