package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/analytics"
	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

func (h *Handler) publications(w http.ResponseWriter, r *http.Request) {
	if h.publication == nil {
		writeError(w, http.StatusServiceUnavailable, "publication service is not configured")
		return
	}
	if !h.requirePublicationAuth(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, err := queryLimit(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := h.publication.List(r.Context(), limit)
	if err != nil {
		writePublicationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *Handler) publicationByID(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/publications/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 2 && (parts[1] == "insights" || parts[1] == "sync") {
		if !h.requirePublicationAuth(w, r) {
			return
		}
		if h.analytics == nil {
			writeError(w, http.StatusServiceUnavailable, "analytics service is not configured")
			return
		}
		switch parts[1] {
		case "insights":
			h.publicationInsights(w, r, parts[0])
		case "sync":
			h.publicationSync(w, r, parts[0])
		}
		return
	}
	if h.publication == nil {
		writeError(w, http.StatusServiceUnavailable, "publication service is not configured")
		return
	}
	capabilitiesRequest := len(parts) == 1 && parts[0] == "capabilities" && r.Method == http.MethodGet
	if !capabilitiesRequest && !h.requirePublicationAuth(w, r) {
		return
	}
	if len(parts) == 1 && (parts[0] == "candidate" || parts[0] == "candidates") {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var req publication.CandidateRequest
		if decodeStrictJSON(w, r, &req) != nil {
			return
		}
		candidate, err := h.publication.Candidate(r.Context(), req)
		if err != nil {
			writePublicationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, candidate)
		return
	}
	if len(parts) == 1 && parts[0] == "authorize" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var body struct {
			Candidate publication.CandidateRequest `json:"candidate"`
			Confirm   bool                         `json:"confirm"`
			Policy    publication.Policy           `json:"policy"`
			ExpiresAt time.Time                    `json:"expires_at"`
		}
		if decodeStrictJSON(w, r, &body) != nil {
			return
		}
		auth, err := h.publication.Authorize(r.Context(), publication.AuthorizeCandidateRequest{CandidateRequest: body.Candidate, Confirm: body.Confirm, PolicyVersion: body.Policy.Version, Selected: body.Policy.Selected, ExpiresAt: body.ExpiresAt})
		if err != nil {
			writePublicationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, auth)
		return
	}
	if len(parts) == 1 && parts[0] == "prepare" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var body struct {
			Candidate       publication.CandidateRequest `json:"candidate"`
			AuthorizationID string                       `json:"authorization_id"`
			Fingerprint     string                       `json:"fingerprint"`
			Policy          publication.Policy           `json:"policy"`
			BusinessDay     string                       `json:"business_day,omitempty"`
		}
		if decodeStrictJSON(w, r, &body) != nil {
			return
		}
		p, err := h.publication.Prepare(r.Context(), publication.PrepareCandidateRequest{CandidateRequest: body.Candidate, AuthorizationID: body.AuthorizationID, Fingerprint: body.Fingerprint, PolicyVersion: body.Policy.Version, Selected: body.Policy.Selected, BusinessDay: body.BusinessDay})
		if err != nil {
			writePublicationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	if len(parts) == 1 && parts[0] == "capabilities" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		capabilities := h.publication.Capabilities(r.Context())
		if h.publicationAPIToken == "" {
			for i := range capabilities {
				capabilities[i].Configured = false
				capabilities[i].Reason = "PUBLICATION_API_TOKEN is not configured; publication mutations are disabled"
			}
		}
		writeJSON(w, http.StatusOK, capabilities)
		return
	}
	if len(parts) < 1 || parts[0] == "" {
		writeError(w, http.StatusNotFound, "publication not found")
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		p, err := h.publication.Get(r.Context(), id)
		if err != nil {
			writePublicationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "commit" || parts[1] == "reconcile") {
		var p publication.Publication
		var err error
		if parts[1] == "commit" {
			var body struct {
				Fingerprint string `json:"fingerprint"`
				Confirm     bool   `json:"confirm"`
			}
			if decodeStrictJSON(w, r, &body) != nil {
				return
			}
			p, err = h.publication.Commit(r.Context(), publication.CommitRequest{PublicationID: id, Fingerprint: body.Fingerprint, Confirm: body.Confirm})
		} else {
			// Reconcile is a read-only query and does not need a request body.
			p, err = h.publication.Reconcile(r.Context(), publication.ReconcileRequest{PublicationID: id})
		}
		if err != nil {
			writePublicationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
		return
	}
	methodNotAllowed(w)
}

func (h *Handler) publicationInsights(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusNotFound, "publication not found")
		return
	}
	insights, err := h.analytics.Insights(r.Context(), id)
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, insights)
}

func (h *Handler) publicationSync(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusNotFound, "publication not found")
		return
	}
	if err := h.analytics.Sync(r.Context(), id); err != nil {
		writeAnalyticsError(w, err)
		return
	}
	job, err := h.analyticsJob(r.Context(), id)
	if err != nil {
		writeAnalyticsError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) analyticsJob(ctx context.Context, id string) (analytics.SyncJob, error) {
	insights, err := h.analytics.Insights(ctx, id)
	if err != nil {
		return analytics.SyncJob{}, err
	}
	if insights.SyncJob == nil {
		return analytics.SyncJob{}, platforminsights.ErrSyncNotFound
	}
	return *insights.SyncJob, nil
}

func writeAnalyticsError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, publication.ErrNotFound), errors.Is(err, platforminsights.ErrSyncNotFound), errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
	case errors.Is(err, platforminsights.ErrCapabilityBlocked), errors.Is(err, platforminsights.ErrUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusServiceUnavailable
	case errors.Is(err, platforminsights.ErrLeaseLost):
		status = http.StatusConflict
	case errors.Is(err, analytics.ErrSyncInProgress):
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func writePublicationError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	var blocker *publication.CapabilityBlocker
	switch {
	case errors.As(err, &blocker):
		status = http.StatusServiceUnavailable
	case errors.Is(err, publication.ErrNotFound), errors.Is(err, sql.ErrNoRows), errors.Is(err, runstore.ErrRunNotFound):
		status = http.StatusNotFound
	case errors.Is(err, publication.ErrAuthorizationExpired), errors.Is(err, publication.ErrCandidateNotReady), errors.Is(err, publication.ErrArtifactMissing), errors.Is(err, publication.ErrCandidateInvalid):
		status = http.StatusPreconditionFailed
	case errors.Is(err, publication.ErrAuthorizationConsumed), errors.Is(err, publication.ErrAuthorizationMismatch), errors.Is(err, publication.ErrCandidateChanged), errors.Is(err, publication.ErrNeedsReconcile), errors.Is(err, publication.ErrUnknownOutcome), errors.Is(err, publication.ErrReconcilePending), errors.Is(err, publication.ErrOperationActive), errors.Is(err, publication.ErrStaleOperation), errors.Is(err, publication.ErrBusinessDayMismatch), errors.Is(err, publication.ErrCommitConfirmationRequired):
		status = http.StatusConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusServiceUnavailable
	}
	writeError(w, status, err.Error())
}
