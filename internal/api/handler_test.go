package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/distribution"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
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
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode publish result: %v", err)
	}
	if result["error"] != "legacy project publish API is disabled; use the publication orchestrator transaction API" {
		t.Fatalf("unexpected legacy publish error: %+v", result)
	}
	stored, ok := e.GetProject(project.ID)
	if !ok || len(stored.Publications) != 0 {
		t.Fatalf("legacy publish must not persist publication history: ok=%t project=%+v", ok, stored)
	}
}

func TestRunEndpointsOfferingPost202ThenPackageReady(t *testing.T) {
	ledger, err := runstore.Open(t.TempDir() + "/runs.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	runs := distribution.NewService(ledger, t.TempDir())
	if err := runs.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Stop(context.Background()) })
	h := NewHandlerWithRuns(engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir()), runs).Routes()
	payload := domain.DistributionRequest{
		IdempotencyKey: "api-e2e-1",
		Platform:       domain.PlatformXiaohongshu,
		Offering: &domain.OfferingInput{
			Kind:        domain.OfferingKindProduct,
			Title:       "API 测试商品",
			Name:        "API 测试商品",
			ValuePoints: []string{"轻便", "耐用"},
		},
	}
	body, _ := json.Marshal(payload)
	post := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
	post.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	h.ServeHTTP(resp, post)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("POST status=%d body=%s", resp.Code, resp.Body.String())
	}
	var created domain.Run
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != domain.RunStatusQueued {
		t.Fatalf("unexpected accepted run: %+v", created)
	}

	deadline := time.Now().Add(5 * time.Second)
	var ready domain.Run
	for time.Now().Before(deadline) {
		get := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+created.ID, nil)
		getResp := httptest.NewRecorder()
		h.ServeHTTP(getResp, get)
		if getResp.Code != http.StatusOK {
			t.Fatalf("GET status=%d body=%s", getResp.Code, getResp.Body.String())
		}
		if err := json.Unmarshal(getResp.Body.Bytes(), &ready); err != nil {
			t.Fatal(err)
		}
		if ready.Status == domain.RunStatusPackageReady {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ready.Status != domain.RunStatusPackageReady || len(ready.Artifacts) != 1 || ready.Artifacts[0].Kind != "package" {
		t.Fatalf("run did not produce package artifact: %+v", ready)
	}

	// Same normalized payload reuses the durable run and is not accepted as a new run.
	replay := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
	replay.Header.Set("Content-Type", "application/json")
	replayResp := httptest.NewRecorder()
	h.ServeHTTP(replayResp, replay)
	if replayResp.Code != http.StatusOK {
		t.Fatalf("idempotent replay status=%d body=%s", replayResp.Code, replayResp.Body.String())
	}
	var reused domain.Run
	if err := json.Unmarshal(replayResp.Body.Bytes(), &reused); err != nil || reused.ID != created.ID {
		t.Fatalf("idempotent replay returned %+v err=%v", reused, err)
	}

	conflictPayload := payload
	conflictPayload.Offering = &domain.OfferingInput{Title: "different payload"}
	conflictBody, _ := json.Marshal(conflictPayload)
	conflict := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(conflictBody))
	conflict.Header.Set("Content-Type", "application/json")
	conflictResp := httptest.NewRecorder()
	h.ServeHTTP(conflictResp, conflict)
	if conflictResp.Code != http.StatusConflict {
		t.Fatalf("idempotency conflict status=%d body=%s", conflictResp.Code, conflictResp.Body.String())
	}
}
