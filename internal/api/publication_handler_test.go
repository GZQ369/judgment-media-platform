package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
	"github.com/GZQ369/judgment-media-platform/internal/store"
	_ "modernc.org/sqlite"
)

type publicationRunReader struct{ run domain.Run }

func (r publicationRunReader) GetRun(context.Context, string) (domain.Run, error) { return r.run, nil }

type publicationHTTPAdapter struct {
	platform publication.Platform
	commit   int
}

func (a *publicationHTTPAdapter) Platform() publication.Platform { return a.platform }
func (a *publicationHTTPAdapter) Capability(context.Context) publication.Capability {
	return publication.Capability{Platform: a.platform, Configured: true}
}
func (a *publicationHTTPAdapter) Commit(context.Context, publication.AdapterRequest) (publication.Evidence, error) {
	a.commit++
	return publication.Evidence{Status: publication.StatusSubmitted, ExternalID: "item-http-1", Verified: true, Proof: "httptest"}, nil
}
func (a *publicationHTTPAdapter) Reconcile(context.Context, publication.AdapterRequest) (publication.Evidence, error) {
	return publication.Evidence{Status: publication.StatusSubmitted, ExternalID: "item-http-1", Verified: true, Proof: "httptest reconcile"}, nil
}

func TestPublicationHTTPFullTransaction(t *testing.T) {
	const publicationToken = "test-publication-token"
	root := t.TempDir()
	video := filepath.Join(root, "rendered.mp4")
	cover := filepath.Join(root, "cover.png")
	packagePath := filepath.Join(root, "publish_package.json")
	for path, content := range map[string][]byte{video: []byte("video"), cover: []byte("cover"), packagePath: []byte(`{"variants":[{"id":"v1","title":"标题","caption":"完整正文"}],"brief":{"tags":["codex"]}}`)} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := domain.Run{ID: "run-http-1", Status: domain.RunStatusMediaReady, Request: domain.DistributionRequest{Platform: domain.PlatformDouyin, Targets: []domain.DistributionTarget{{Platform: domain.PlatformDouyin, AccountID: "acct"}}, Atom: &domain.ContentAtom{Revision: 2}}, Artifacts: []domain.Artifact{{Kind: "package", Path: packagePath}, {Kind: "video", ID: "rendered-video", Path: video}, {Kind: "cover", Path: cover}}}
	db, err := sql.Open("sqlite", filepath.Join(root, "publications.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &publicationHTTPAdapter{platform: publication.PlatformDouyin}
	orchestrator, err := publication.New(db, adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := publication.NewService(publicationRunReader{run: run}, orchestrator, publication.CoordinationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithServicesAndSecurity(engine.NewEngineWithDataDir(store.NewMemoryStore(), root), nil, nil, service, SecurityOptions{PublicationAPIToken: publicationToken}).Routes()
	server := httptest.NewServer(handler)
	defer server.Close()

	post := func(path string, body any, target any) int {
		raw, _ := json.Marshal(body)
		request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+publicationToken)
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if target != nil && response.StatusCode < 300 {
			if err := json.NewDecoder(response.Body).Decode(target); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode
	}
	candidateRequest := publication.CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: publication.PlatformDouyin, VariantID: "v1"}
	var candidate publication.Candidate
	if status := post("/api/v1/publications/candidate", candidateRequest, &candidate); status != http.StatusOK {
		t.Fatalf("candidate status = %d", status)
	}
	var authorization publication.Authorization
	if status := post("/api/v1/publications/authorize", map[string]any{"candidate": candidateRequest, "confirm": true, "policy": publication.Policy{Version: "v1", Selected: []publication.Platform{publication.PlatformDouyin}}, "expires_at": time.Now().Add(5 * time.Minute)}, &authorization); status != http.StatusOK {
		t.Fatalf("authorize status = %d", status)
	}
	var prepared publication.Publication
	if status := post("/api/v1/publications/prepare", map[string]any{"candidate": candidateRequest, "authorization_id": authorization.ID, "fingerprint": candidate.Fingerprint, "policy": publication.Policy{Version: "v1", Selected: []publication.Platform{publication.PlatformDouyin}}}, &prepared); status != http.StatusOK || prepared.Status != publication.StatusDraftReady {
		t.Fatalf("prepare status=%d publication=%+v", status, prepared)
	}
	if status := post("/api/v1/publications/"+prepared.ID+"/commit", map[string]any{"fingerprint": candidate.Fingerprint, "confirm": false}, nil); status != http.StatusConflict {
		t.Fatalf("commit without server confirmation status=%d, want conflict", status)
	}
	var committed publication.Publication
	if status := post("/api/v1/publications/"+prepared.ID+"/commit", map[string]any{"fingerprint": candidate.Fingerprint, "confirm": true}, &committed); status != http.StatusOK || committed.Status != publication.StatusSubmitted {
		t.Fatalf("commit status=%d publication=%+v", status, committed)
	}
	if adapter.commit != 1 || committed.ExternalID != "item-http-1" {
		t.Fatalf("adapter invocation/result mismatch: calls=%d publication=%+v", adapter.commit, committed)
	}
	if status := post("/api/v1/publications/"+prepared.ID+"/commit", map[string]any{"fingerprint": candidate.Fingerprint, "confirm": true}, nil); status != http.StatusConflict {
		t.Fatalf("second commit status = %d, want conflict", status)
	}
	reconcileRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/publications/"+prepared.ID+"/reconcile", nil)
	if err != nil {
		t.Fatal(err)
	}
	reconcileRequest.Header.Set("Authorization", "Bearer "+publicationToken)
	reconcileResponse, err := server.Client().Do(reconcileRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer reconcileResponse.Body.Close()
	if reconcileResponse.StatusCode != http.StatusOK {
		t.Fatalf("bodyless reconcile status = %d", reconcileResponse.StatusCode)
	}
}

func TestPublicationHTTPRequiresBearerTokenForMutations(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "publications.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &publicationHTTPAdapter{platform: publication.PlatformDouyin}
	orchestrator, err := publication.New(db, adapter)
	if err != nil {
		t.Fatal(err)
	}
	service, err := publication.NewService(publicationRunReader{run: domain.Run{ID: "queued", Status: domain.RunStatusRunning}}, orchestrator, publication.CoordinationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithServicesAndSecurity(engine.NewEngineWithDataDir(store.NewMemoryStore(), root), nil, nil, service, SecurityOptions{PublicationAPIToken: "secret"}).Routes()
	body, _ := json.Marshal(publication.CandidateRequest{RunID: "queued", Creator: "creator", Account: "acct", Platform: publication.PlatformDouyin})
	for name, token := range map[string]string{"missing": "", "wrong": "Bearer wrong"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/publications/candidate", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if token != "" {
				req.Header.Set("Authorization", token)
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestPublicationHTTPRejectsNonMediaReadyCandidate(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "publications.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &publicationHTTPAdapter{platform: publication.PlatformDouyin}
	orchestrator, _ := publication.New(db, adapter)
	service, _ := publication.NewService(publicationRunReader{run: domain.Run{ID: "queued", Status: domain.RunStatusRunning}}, orchestrator, publication.CoordinationOptions{})
	handler := NewHandlerWithServicesAndSecurity(engine.NewEngineWithDataDir(store.NewMemoryStore(), root), nil, nil, service, SecurityOptions{PublicationAPIToken: "test-publication-token"}).Routes()
	reqBody, _ := json.Marshal(publication.CandidateRequest{RunID: "queued", Creator: "creator", Account: "acct", Platform: publication.PlatformDouyin})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/publications/candidate", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-publication-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("candidate status = %d, want %d; body=%s", response.Code, http.StatusPreconditionFailed, response.Body.String())
	}
}
