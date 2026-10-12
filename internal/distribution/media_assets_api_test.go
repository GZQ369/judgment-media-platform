package distribution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/api"
	"github.com/GZQ369/judgment-media-platform/internal/distribution"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

func TestTaskAssetUnsafeIDsReturnHTTP400BeforeCreatingRun(t *testing.T) {
	data := t.TempDir()
	store, err := runstore.Open(filepath.Join(data, "runs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := distribution.NewService(store, data)
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer service.Stop(context.Background())
	handler := api.NewHandlerWithRuns(nil, service).Routes()
	for _, ids := range [][]string{{"A", "a"}, {"CON"}, {"con"}, {"NUL"}, {"PRN"}, {"AUX"}, {"COM1"}, {"lpt1"}} {
		request := domain.DistributionRequest{Platform: domain.PlatformXiaohongshu, Offering: &domain.OfferingInput{Kind: domain.OfferingKindProduct, Title: "test", Name: "test"}, Media: &domain.MediaRequest{Enabled: true, Render: true, DefaultAssetID: ids[0]}}
		for _, id := range ids {
			request.Media.Assets = append(request.Media.Assets, domain.MediaAssetRequest{ID: id, Ref: "valid.png", Kind: "image"})
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(encoded))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte("asset ID")) {
			t.Fatalf("IDs %v: expected asset-ID HTTP 400, got %d: %s", ids, w.Code, w.Body.String())
		}
	}
	runs, err := service.ListRuns(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("invalid IDs created runs: %+v", runs)
	}
}
