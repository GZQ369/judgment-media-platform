package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/distribution"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/GZQ369/judgment-media-platform/internal/pipeline"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
	"github.com/GZQ369/judgment-media-platform/internal/store"
)

type blockingPipelineJudgment struct {
	once    sync.Once
	started chan struct{}
}

func (r *blockingPipelineJudgment) Run(ctx context.Context, _ domain.ResearchTopic) (pipeline.JudgmentResult, error) {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	return orchestration.Result{}, ctx.Err()
}

func pipelineRequest(key string) pipeline.PipelineRequest {
	return pipeline.PipelineRequest{
		IdempotencyKey: key,
		Topic: domain.ResearchTopic{
			Topic:         "API pipeline topic",
			SourceURLs:    []string{"https://example.com/source"},
			DesiredAction: "inspect",
		},
		Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin}},
	}
}

func newPipelineHandler(t *testing.T, runner pipeline.JudgmentRunner) (*Handler, *pipeline.Service) {
	t.Helper()
	ledger, err := pipeline.Open(filepath.Join(t.TempDir(), "pipeline.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	service := pipeline.NewService(ledger, runner, nil, pipeline.Options{PollInterval: time.Millisecond})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	handler := NewHandlerWithPipeline(engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir()), service)
	return handler, service
}

func postPipeline(t *testing.T, handler http.Handler, request any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipeline-runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func decodePipelineRun(t *testing.T, resp *httptest.ResponseRecorder) pipeline.PipelineRun {
	t.Helper()
	var run pipeline.PipelineRun
	if err := json.Unmarshal(resp.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode pipeline run: %v; body=%s", err, resp.Body.String())
	}
	return run
}

func waitPipelineStatus(t *testing.T, handler http.Handler, id string, want pipeline.PipelineStatus) pipeline.PipelineRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline-runs/"+id, nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code == http.StatusOK {
			run := decodePipelineRun(t, resp)
			if run.Status == want {
				return run
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pipeline run %s did not reach %s", id, want)
	return pipeline.PipelineRun{}
}

func TestPipelineEndpointsCreateIdempotencyQueryEventsAndStrictJSON(t *testing.T) {
	h, _ := newPipelineHandler(t, nil)
	request := pipelineRequest("api-pipeline-1")
	createdResp := postPipeline(t, h.Routes(), request)
	if createdResp.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", createdResp.Code, createdResp.Body.String())
	}
	created := decodePipelineRun(t, createdResp)
	if created.ID == "" || created.Request.Topic.Topic != request.Topic.Topic {
		t.Fatalf("unexpected pipeline response: %+v", created)
	}

	replayResp := postPipeline(t, h.Routes(), request)
	if replayResp.Code != http.StatusOK || decodePipelineRun(t, replayResp).ID != created.ID {
		t.Fatalf("idempotent replay status=%d body=%s", replayResp.Code, replayResp.Body.String())
	}
	conflict := request
	conflict.Topic.Topic = "different topic"
	conflictResp := postPipeline(t, h.Routes(), conflict)
	if conflictResp.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflictResp.Code, conflictResp.Body.String())
	}

	unknown := postPipeline(t, h.Routes(), map[string]any{"topic": request.Topic, "targets": request.Targets, "unknown": true})
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline-runs?limit=10", nil)
	listResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(listResp, listReq)
	if listResp.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listResp.Code, listResp.Body.String())
	}
	var listed []pipeline.PipelineRun
	if err := json.Unmarshal(listResp.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list=%+v err=%v", listed, err)
	}

	eventsReq := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline-runs/"+created.ID+"/events", nil)
	eventsResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(eventsResp, eventsReq)
	if eventsResp.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", eventsResp.Code, eventsResp.Body.String())
	}
	var events []domain.RunEvent
	if err := json.Unmarshal(eventsResp.Body.Bytes(), &events); err != nil || len(events) == 0 || events[0].Type != "run_queued" {
		t.Fatalf("events=%+v err=%v", events, err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline-runs/"+created.ID, nil)
	getResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(getResp, getReq)
	if getResp.Code != http.StatusOK || decodePipelineRun(t, getResp).ID != created.ID {
		t.Fatalf("get status=%d body=%s", getResp.Code, getResp.Body.String())
	}
	missingReq := httptest.NewRequest(http.MethodGet, "/api/v1/pipeline-runs/missing", nil)
	missingResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(missingResp, missingReq)
	if missingResp.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missingResp.Code, missingResp.Body.String())
	}
}

func TestPipelineEndpointsCancelAndRetry(t *testing.T) {
	runner := &blockingPipelineJudgment{started: make(chan struct{})}
	h, service := newPipelineHandler(t, runner)
	createdResp := postPipeline(t, h.Routes(), pipelineRequest("api-pipeline-cancel"))
	created := decodePipelineRun(t, createdResp)
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline worker did not start")
	}
	waitPipelineStatus(t, h.Routes(), created.ID, pipeline.PipelineStatusRunning)

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/v1/pipeline-runs/"+created.ID+"/cancel", nil)
	cancelResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(cancelResp, cancelReq)
	if cancelResp.Code != http.StatusOK || decodePipelineRun(t, cancelResp).Status != pipeline.PipelineStatusCancelled {
		t.Fatalf("cancel status=%d body=%s", cancelResp.Code, cancelResp.Body.String())
	}

	if err := service.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	retryReq := httptest.NewRequest(http.MethodPost, "/api/v1/pipeline-runs/"+created.ID+"/retry", nil)
	retryResp := httptest.NewRecorder()
	h.Routes().ServeHTTP(retryResp, retryReq)
	if retryResp.Code != http.StatusAccepted || decodePipelineRun(t, retryResp).Status != pipeline.PipelineStatusQueued {
		t.Fatalf("retry status=%d body=%s", retryResp.Code, retryResp.Body.String())
	}
}

func TestPipelineEndpointsMissingService(t *testing.T) {
	h := NewHandler(engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())).Routes()
	resp := postPipeline(t, h, pipelineRequest("missing-service"))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing service status=%d body=%s", resp.Code, resp.Body.String())
	}
}

type readyMediaWorker struct{}

func (readyMediaWorker) Validate(context.Context, mediaworker.ProductionSpec, string) (mediaworker.Result, error) {
	return mediaworker.Result{OK: true, Valid: true}, nil
}

func (readyMediaWorker) Run(_ context.Context, spec mediaworker.ProductionSpec, outputDir string) (mediaworker.Result, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return mediaworker.Result{}, err
	}
	path := filepath.Join(outputDir, "video.mp4")
	if err := os.WriteFile(path, []byte("fake video"), 0o644); err != nil {
		return mediaworker.Result{}, err
	}
	return mediaworker.Result{OK: true, Valid: true, JobID: spec.JobID, ArtifactManifestPath: filepath.Join(outputDir, "artifact-manifest.json"), Manifest: &mediaworker.ArtifactManifest{
		SchemaVersion:         mediaworker.ArtifactManifestVersion,
		Kind:                  "artifact-manifest",
		JobID:                 spec.JobID,
		ProductionSpecVersion: mediaworker.ProductionSpecVersion,
		Artifacts:             []mediaworker.Artifact{{ID: "video-1", Kind: "rendered-video", Path: "video.mp4", RelativePath: "video.mp4", Bytes: 10}},
	}}, nil
}

func TestDistributionSSEEndsAtMediaReady(t *testing.T) {
	ledger, err := runstore.Open(filepath.Join(t.TempDir(), "runs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	runs := distribution.NewServiceWithOptions(ledger, t.TempDir(), distribution.Options{MediaWorker: readyMediaWorker{}})
	if err := runs.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Stop(context.Background()) })
	h := NewHandlerWithRuns(engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir()), runs).Routes()
	request := domain.DistributionRequest{IdempotencyKey: "media-ready-sse", Platform: domain.PlatformXiaohongshu, Offering: &domain.OfferingInput{
		Kind: domain.OfferingKindProduct, Name: "SSE test", Title: "SSE test", ValuePoints: []string{"fast"},
	}, Media: &domain.MediaRequest{Enabled: true}}
	postResp := postDistributionForTest(t, h, request)
	if postResp.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", postResp.Code, postResp.Body.String())
	}
	var created domain.Run
	if err := json.Unmarshal(postResp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	waitDistributionStatus(t, h, created.ID, domain.RunStatusMediaReady)

	sseReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+created.ID+"/events", nil)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseResp := httptest.NewRecorder()
	h.ServeHTTP(sseResp, sseReq)
	if sseResp.Code != http.StatusOK || !strings.Contains(sseResp.Body.String(), "media_ready") {
		t.Fatalf("SSE status=%d body=%s", sseResp.Code, sseResp.Body.String())
	}
}

func postDistributionForTest(t *testing.T, handler http.Handler, request domain.DistributionRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	return resp
}

func waitDistributionStatus(t *testing.T, handler http.Handler, id string, want domain.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/"+id, nil)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		var run domain.Run
		if resp.Code == http.StatusOK && json.Unmarshal(resp.Body.Bytes(), &run) == nil && run.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("distribution run %s did not reach %s", id, want)
}
