package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
)

type Handler struct{ engine *engine.Engine }

func NewHandler(e *engine.Engine) *Handler { return &Handler{engine: e} }

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/api/v1/templates", h.templates)
	mux.HandleFunc("/api/v1/publishing/platforms", h.publishingPlatforms)
	mux.HandleFunc("/api/v1/batches", h.batches)
	mux.HandleFunc("/api/v1/batches/", h.batchByID)
	mux.HandleFunc("/api/v1/projects", h.projects)
	mux.HandleFunc("/api/v1/projects/", h.projectByID)
	return requestIDMiddleware(recoveryMiddleware(accessLogMiddleware(cors(mux))))
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

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", requestIDHeader)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
