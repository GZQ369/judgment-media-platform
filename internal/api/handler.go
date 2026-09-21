package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/analytics"
	"github.com/GZQ369/judgment-media-platform/internal/distribution"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/pipeline"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

type Handler struct {
	engine              *engine.Engine
	runService          *distribution.Service
	pipeline            *pipeline.Service
	publication         *publication.Service
	analytics           *analytics.Service
	publicationAPIToken string
	allowedOrigins      []string
}

// SecurityOptions controls the local publication control plane. An empty
// publication token intentionally disables publication mutations.
type SecurityOptions struct {
	PublicationAPIToken string
	AllowedOrigins      []string
}

func NewHandler(e *engine.Engine) *Handler { return &Handler{engine: e} }

// NewHandlerWithRuns keeps the legacy handler constructor while enabling durable runs.
func NewHandlerWithRuns(e *engine.Engine, runs *distribution.Service) *Handler {
	return &Handler{engine: e, runService: runs}
}

// NewHandlerWithPipeline keeps the legacy handler constructor while enabling
// the durable pipeline API.
func NewHandlerWithPipeline(e *engine.Engine, pipelines *pipeline.Service) *Handler {
	return &Handler{engine: e, pipeline: pipelines}
}

// NewHandlerWithRunsAndPipeline enables both durable run APIs while retaining
// the existing constructors for callers that only need one of them.
func NewHandlerWithRunsAndPipeline(e *engine.Engine, runs *distribution.Service, pipelines *pipeline.Service) *Handler {
	return &Handler{engine: e, runService: runs, pipeline: pipelines}
}

// NewHandlerWithServices is the aggregate constructor for durable run,
// pipeline, and publication APIs. Older constructors remain source-compatible.
func NewHandlerWithServices(e *engine.Engine, runs *distribution.Service, pipelines *pipeline.Service, publications *publication.Service) *Handler {
	return NewHandlerWithServicesAndSecurity(e, runs, pipelines, publications, SecurityOptions{})
}

// NewHandlerWithServicesAndSecurity is the production constructor. The
// publication API is fail-closed unless a bearer token is configured.
func NewHandlerWithServicesAndSecurity(e *engine.Engine, runs *distribution.Service, pipelines *pipeline.Service, publications *publication.Service, security SecurityOptions) *Handler {
	return NewHandlerWithServicesAndSecurityAndAnalytics(e, runs, pipelines, publications, nil, security)
}

// NewHandlerWithServicesAndSecurityAndAnalytics enables the analytics read
// and manual-sync APIs alongside the existing publication control plane.
func NewHandlerWithServicesAndSecurityAndAnalytics(e *engine.Engine, runs *distribution.Service, pipelines *pipeline.Service, publications *publication.Service, insights *analytics.Service, security SecurityOptions) *Handler {
	return &Handler{
		engine: e, runService: runs, pipeline: pipelines, publication: publications, analytics: insights,
		publicationAPIToken: strings.TrimSpace(security.PublicationAPIToken),
		allowedOrigins:      append([]string(nil), security.AllowedOrigins...),
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/api/v1/templates", h.templates)
	mux.HandleFunc("/api/v1/publishing/platforms", h.publishingPlatforms)
	mux.HandleFunc("/api/v1/batches", h.batches)
	mux.HandleFunc("/api/v1/batches/", h.batchByID)
	mux.HandleFunc("/api/v1/runs", h.runs)
	mux.HandleFunc("/api/v1/runs/", h.runByID)
	mux.HandleFunc("/api/v1/pipeline-runs", h.pipelineRuns)
	mux.HandleFunc("/api/v1/pipeline-runs/", h.pipelineRunByID)
	mux.HandleFunc("/api/v1/publications", h.publications)
	mux.HandleFunc("/api/v1/publications/", h.publicationByID)
	mux.HandleFunc("/api/v1/analytics/capabilities", h.analyticsCapabilities)
	mux.HandleFunc("/api/v1/analytics/summary", h.analyticsSummary)
	mux.HandleFunc("/api/v1/projects", h.projects)
	mux.HandleFunc("/api/v1/projects/", h.projectByID)
	return requestIDMiddleware(recoveryMiddleware(accessLogMiddleware(cors(mux, h.allowedOrigins))))
}

func (h *Handler) analyticsCapabilities(w http.ResponseWriter, r *http.Request) {
	if !h.requirePublicationAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.analytics == nil {
		writeError(w, http.StatusServiceUnavailable, "analytics service is not configured")
		return
	}
	writeJSON(w, http.StatusOK, h.analytics.Capabilities(r.Context()))
}

func (h *Handler) analyticsSummary(w http.ResponseWriter, r *http.Request) {
	if !h.requirePublicationAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if h.analytics == nil {
		writeError(w, http.StatusServiceUnavailable, "analytics service is not configured")
		return
	}
	summary, err := h.analytics.Summary(r.Context())
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) pipelineRuns(w http.ResponseWriter, r *http.Request) {
	if h.pipeline == nil {
		writeError(w, http.StatusServiceUnavailable, "pipeline runs are not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, err := queryLimit(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		items, err := h.pipeline.ListRuns(r.Context(), limit)
		if err != nil {
			writePipelineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !runWriteAllowed(w, r) {
			return
		}
		var request pipeline.PipelineRequest
		if err := decodeStrictJSON(w, r, &request); err != nil {
			return
		}
		run, created, err := h.pipeline.StartRun(r.Context(), request)
		if err != nil {
			writePipelineError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		writeJSON(w, status, run)
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) pipelineRunByID(w http.ResponseWriter, r *http.Request) {
	if h.pipeline == nil {
		writeError(w, http.StatusServiceUnavailable, "pipeline runs are not configured")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/pipeline-runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "pipeline run id required")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		run, err := h.pipeline.GetRun(r.Context(), id)
		if err != nil {
			writePipelineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[1] {
	case "events":
		h.pipelineRunEvents(w, r, id)
	case "cancel":
		h.pipelineRunAction(w, r, id, "cancel")
	case "retry":
		h.pipelineRunAction(w, r, id, "retry")
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) pipelineRunAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !runWriteAllowed(w, r) {
		return
	}
	var (
		run pipeline.PipelineRun
		err error
	)
	switch action {
	case "cancel":
		run, err = h.pipeline.Cancel(r.Context(), id)
	case "retry":
		run, err = h.pipeline.Retry(r.Context(), id)
	}
	if err != nil {
		writePipelineError(w, err)
		return
	}
	status := http.StatusOK
	if action == "retry" && run.Status == pipeline.PipelineStatusQueued {
		status = http.StatusAccepted
	}
	writeJSON(w, status, run)
}

func (h *Handler) pipelineRunEvents(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	afterID, err := eventCursor(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		events, err := h.pipeline.Events(r.Context(), id, afterID, 200)
		if err != nil {
			writePipelineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := h.pipeline.Events(r.Context(), id, afterID, 200)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, _ := json.Marshal(event)
			_, _ = w.Write([]byte("id: " + strconv.FormatInt(event.ID, 10) + "\n"))
			_, _ = w.Write([]byte("event: " + event.Type + "\n"))
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
			afterID = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		run, err := h.pipeline.GetRun(r.Context(), id)
		if err != nil || terminalPipelineRun(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) runs(w http.ResponseWriter, r *http.Request) {
	if h.runService == nil {
		writeError(w, http.StatusServiceUnavailable, "durable runs are not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := h.runService.ListRuns(r.Context(), limit)
		if err != nil {
			writeRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case http.MethodPost:
		if !runWriteAllowed(w, r) {
			return
		}
		var request domain.DistributionRequest
		if err := decodeJSON(w, r, &request); err != nil {
			return
		}
		run, created, err := h.runService.StartRun(r.Context(), request)
		if err != nil {
			writeRunError(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusAccepted
		}
		writeJSON(w, status, run)
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) runByID(w http.ResponseWriter, r *http.Request) {
	if h.runService == nil {
		writeError(w, http.StatusServiceUnavailable, "durable runs are not configured")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "run id required")
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		run, err := h.runService.GetRun(r.Context(), id)
		if err != nil {
			writeRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch parts[1] {
	case "events":
		h.runEvents(w, r, id)
	case "cancel":
		h.runAction(w, r, id, "cancel")
	case "resume":
		h.runAction(w, r, id, "resume")
	case "retry":
		h.runAction(w, r, id, "retry")
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) runAction(w http.ResponseWriter, r *http.Request, id, action string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !runWriteAllowed(w, r) {
		return
	}
	var run domain.Run
	var err error
	switch action {
	case "cancel":
		run, err = h.runService.Cancel(r.Context(), id)
	case "resume":
		run, err = h.runService.Resume(r.Context(), id)
	case "retry":
		run, err = h.runService.Retry(r.Context(), id)
	}
	if err != nil {
		writeRunError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) runEvents(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	afterID := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		afterID, _ = strconv.ParseInt(raw, 10, 64)
	}
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		afterID, _ = strconv.ParseInt(raw, 10, 64)
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		events, err := h.runService.Events(r.Context(), id, afterID, 200)
		if err != nil {
			writeRunError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, events)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := h.runService.Events(r.Context(), id, afterID, 200)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, _ := json.Marshal(event)
			_, _ = w.Write([]byte("id: " + strconv.FormatInt(event.ID, 10) + "\n"))
			_, _ = w.Write([]byte("event: " + event.Type + "\n"))
			_, _ = w.Write([]byte("data: " + string(payload) + "\n\n"))
			afterID = event.ID
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		run, err := h.runService.GetRun(r.Context(), id)
		if err != nil || terminalRun(run.Status) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return errors.New("content-type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return err
	}
	return nil
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
		return errors.New("content-type must be application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("request body must contain a single JSON value")
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return err
	}
	return nil
}

func queryLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		return 0, errors.New("limit must be a non-negative integer")
	}
	return limit, nil
}

func eventCursor(r *http.Request) (int64, error) {
	afterID := int64(0)
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return 0, errors.New("Last-Event-ID must be a non-negative integer")
		}
		afterID = parsed
	}
	if raw := r.URL.Query().Get("after_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			return 0, errors.New("after_id must be a non-negative integer")
		}
		afterID = parsed
	}
	return afterID, nil
}

func runWriteAllowed(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
			writeError(w, http.StatusForbidden, "cross-origin run write is not allowed")
			return false
		}
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "same-site" && site != "none" {
		writeError(w, http.StatusForbidden, "cross-site run write is not allowed")
		return false
	}
	return true
}

func writeRunError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, runstore.ErrRunNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, runstore.ErrIdempotencyConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, distribution.ErrTargetsNotImplemented) {
		status = http.StatusBadRequest
	}
	writeError(w, status, err.Error())
}

func writePipelineError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, pipeline.ErrRunNotFound):
		status = http.StatusNotFound
	case errors.Is(err, pipeline.ErrIdempotencyConflict):
		status = http.StatusConflict
	case errors.Is(err, pipeline.ErrServiceStopped), errors.Is(err, pipeline.ErrServiceStarting), errors.Is(err, pipeline.ErrServiceStopping):
		status = http.StatusServiceUnavailable
	case errors.Is(err, pipeline.ErrInvalidRequest):
		status = http.StatusBadRequest
	}
	writeError(w, status, err.Error())
}

func terminalRun(status domain.RunStatus) bool {
	return status == domain.RunStatusPackageReady || status == domain.RunStatusMediaReady || status == domain.RunStatusBlocked || status == domain.RunStatusFailed || status == domain.RunStatusCancelled
}

func terminalPipelineRun(status pipeline.PipelineStatus) bool {
	return status == pipeline.PipelineStatusBlocked || status == pipeline.PipelineStatusFailed || status == pipeline.PipelineStatusDistributionReady || status == pipeline.PipelineStatusCancelled
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "judgment-media-platform", "time": time.Now().UTC()})
}

func (h *Handler) templates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, h.engine.Templates())
}

func (h *Handler) publishingPlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, h.engine.PublishingCapabilities(r.Context()))
}

func (h *Handler) projects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.engine.ListProjects())
	case http.MethodPost:
		var req domain.CreateProjectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, h.engine.CreateProject(req))
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) batches(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.engine.ListBatches())
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) batchByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/batches/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "batch id required")
		return
	}
	if parts[0] == "run" {
		h.runBatch(w, r)
		return
	}
	if len(parts) == 1 {
		h.batchDetail(w, r, parts[0])
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

func (h *Handler) runBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req domain.BatchRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	job, err := h.engine.RunBatch(ctx, req)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) batchDetail(w http.ResponseWriter, r *http.Request, batchID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job, ok := h.engine.GetBatch(batchID)
	if !ok {
		writeError(w, http.StatusNotFound, "batch not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) projectByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/projects/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "project id required")
		return
	}
	projectID := parts[0]
	if len(parts) == 1 {
		h.projectDetail(w, r, projectID)
		return
	}
	switch parts[1] {
	case "brief":
		h.generateBrief(w, r, projectID)
	case "workflow":
		if len(parts) == 3 && parts[2] == "run" {
			h.generateBrief(w, r, projectID)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	case "variants":
		if len(parts) == 2 {
			h.generateVariants(w, r, projectID)
			return
		}
		if len(parts) == 4 && parts[3] == "metrics" {
			h.updateMetrics(w, r, projectID, parts[2])
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	case "export":
		h.exportProject(w, r, projectID)
	case "publish":
		h.publishProject(w, r, projectID)
	case "render":
		if len(parts) == 3 && parts[2] == "openmontage" {
			h.renderOpenMontage(w, r, projectID)
			return
		}
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) projectDetail(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	project, ok := h.engine.GetProject(projectID)
	if !ok {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) generateBrief(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	project, err := h.engine.GenerateBrief(projectID)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) generateVariants(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req domain.VariantRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	project, err := h.engine.GenerateVariants(projectID, req.Count)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) updateMetrics(w http.ResponseWriter, r *http.Request, projectID, variantID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var metrics domain.VariantMetric
	if err := json.NewDecoder(r.Body).Decode(&metrics); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	project, err := h.engine.UpdateVariantMetrics(projectID, variantID, metrics)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) exportProject(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Format string `json:"format"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	pkg, err := h.engine.ExportProject(projectID, req.Format)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pkg)
}

func (h *Handler) renderOpenMontage(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req domain.RenderOpenMontageRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	project, err := h.engine.RenderOpenMontage(ctx, projectID, req)
	if err != nil {
		if project.ID != "" {
			writeJSON(w, http.StatusBadRequest, project)
			return
		}
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *Handler) publishProject(w http.ResponseWriter, r *http.Request, projectID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req domain.PublishProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := h.engine.PublishProject(ctx, projectID, req)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeEngineError(w http.ResponseWriter, err error) {
	if errors.Is(err, engine.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, engine.ErrBatchNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func cors(next http.Handler, allowedOrigins []string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			allowed := false
			for _, candidate := range allowedOrigins {
				if origin == candidate {
					allowed = true
					break
				}
			}
			if !allowed {
				writeError(w, http.StatusForbidden, "origin is not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", requestIDHeader)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
