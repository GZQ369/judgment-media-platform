package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/store"
)

func TestBatchEndpoints(t *testing.T) {
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	h := NewHandler(e).Routes()
	reqBody := domain.BatchRunRequest{
		Name:         "handler batch",
		Platform:     domain.PlatformDouyin,
		Template:     "douyin_product_hook",
		VariantCount: 10,
		Products: []domain.ProductInput{
			{Title: "宠物自动饮水机", SellingPoints: []string{"静音", "过滤", "可拆洗"}, TargetAudience: "养猫新手"},
			{Title: "桌面氛围灯", SellingPoints: []string{"柔光", "可调色", "不占地"}, TargetAudience: "租房用户"},
			{Title: "便携筋膜枪", SellingPoints: []string{"轻便", "多档位", "低噪"}, TargetAudience: "久坐办公族"},
		},
	}
	payload, _ := json.Marshal(reqBody)
	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/batches/run", bytes.NewReader(payload))
	runResp := httptest.NewRecorder()
	h.ServeHTTP(runResp, runReq)
	if runResp.Code != http.StatusOK {
		t.Fatalf("run batch status=%d body=%s", runResp.Code, runResp.Body.String())
	}
	var job domain.BatchJob
	if err := json.Unmarshal(runResp.Body.Bytes(), &job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if job.ID == "" || len(job.Items) != 3 {
		t.Fatalf("bad job response: %+v", job)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/batches/"+job.ID, nil)
	getResp := httptest.NewRecorder()
	h.ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get batch status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	var fetched domain.BatchJob
	if err := json.Unmarshal(getResp.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode fetched job: %v", err)
	}
	if fetched.ID != job.ID || fetched.Items[0].ProjectID == "" {
		t.Fatalf("bad fetched job: %+v", fetched)
	}
}

func TestPublishingPlatformCapabilitiesEndpoint(t *testing.T) {
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	h := NewHandler(e).Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/publishing/platforms", nil)
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var capabilities []domain.PlatformPublishCapability
	if err := json.Unmarshal(resp.Body.Bytes(), &capabilities); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if len(capabilities) != 3 {
		t.Fatalf("expected three publishing platforms, got %+v", capabilities)
	}
}

func TestPublishEndpointReturnsPerPlatformTruthfulStatus(t *testing.T) {
	t.Setenv("XHS_SHARE_APP_KEY", "app-key")
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	project := e.CreateProject(domain.CreateProjectRequest{
		Platform: domain.PlatformXiaohongshu,
		Offering: domain.OfferingInput{
			Kind:        domain.OfferingKindCapability,
			Name:        "自动剪辑能力",
			ValuePoints: []string{"自动切片", "自动字幕", "自动导出"},
		},
	})
	h := NewHandler(e).Routes()
	payload, _ := json.Marshal(domain.PublishProjectRequest{
		Platforms: []domain.Platform{domain.PlatformXiaohongshu, domain.PlatformWechatChannels},
		VideoURL:  "https://example.com/video.mp4",
		Confirm:   true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+project.ID+"/publish", bytes.NewReader(payload))
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var result domain.PublishProjectResult
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected two platform results: %+v", result)
	}
	for _, publication := range result.Results {
		if publication.Status != domain.PublicationStatusActionRequired {
			t.Fatalf("handoff platform must not be reported as published: %+v", publication)
		}
	}
	stored, ok := e.GetProject(project.ID)
	if !ok || len(stored.Publications) != 2 {
		t.Fatalf("publication history not persisted: ok=%t project=%+v", ok, stored)
	}
}
