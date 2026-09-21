package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (h *Handler) requirePublicationAuth(w http.ResponseWriter, r *http.Request) bool {
	if h.publicationAPIToken == "" {
		writeError(w, http.StatusServiceUnavailable, "publication API is disabled: PUBLICATION_API_TOKEN is not configured")
		return false
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	prefix, token, ok := strings.Cut(value, " ")
	if !ok || !strings.EqualFold(prefix, "Bearer") || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(h.publicationAPIToken)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="publication"`)
		writeError(w, http.StatusUnauthorized, "publication API bearer token is required")
		return false
	}
	return true
}
