package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/analytics"
	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
)

type analyticsTestGateway struct {
	publication platforminsights.Publication
}

func (g analyticsTestGateway) Get(context.Context, string) (platforminsights.Publication, error) {
	return g.publication, nil
}

func (g analyticsTestGateway) Reconcile(context.Context, string) (platforminsights.Publication, error) {
	return g.publication, nil
}

type analyticsTestProvider struct{ calls int }

func (p *analyticsTestProvider) Name() string     { return "verified-fake" }
func (p *analyticsTestProvider) Platform() string { return "douyin" }
func (p *analyticsTestProvider) Capability(context.Context) platforminsights.Capability {
	return platforminsights.Capability{Provider: p.Name(), Platform: p.Platform(), State: platforminsights.CapabilityReady}
}
func (p *analyticsTestProvider) Fetch(_ context.Context, req platforminsights.MetricsRequest) (platforminsights.MetricsObservation, error) {
	p.calls++
	return platforminsights.MetricsObservation{
		Provider: p.Name(), Platform: p.Platform(), ExternalID: req.ExternalID,
		ObservedAt: time.Unix(1700000000, 0).UTC(), Metrics: map[string]float64{"views": 42},
	}, nil
}

func analyticsTestPublication() platforminsights.Publication {
	return platforminsights.Publication{
		ID: "pub-api-1", Platform: "douyin", Account: "acct", ExternalID: "item-api-1", ExternalURL: "https://example.test/item-api-1",
		Status:    platforminsights.StatusPublished,
		Evidence:  platforminsights.Evidence{Status: platforminsights.StatusPublished, ExternalID: "item-api-1", URL: "https://example.test/item-api-1", Verified: true, Proof: "verified-test-proof"},
		UpdatedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func TestAnalyticsRoutesRequirePublicationBearerToken(t *testing.T) {
	store := analyticsTestStore(t)
	service := analytics.NewService(store, analyticsTestGateway{publication: analyticsTestPublication()})
	handler := NewHandlerWithServicesAndSecurityAndAnalytics(nil, nil, nil, nil, service, SecurityOptions{PublicationAPIToken: "secret"}).Routes()
	paths := []string{
		"/api/v1/publications/pub-api-1/insights",
		"/api/v1/publications/pub-api-1/sync",
		"/api/v1/analytics/capabilities",
		"/api/v1/analytics/summary",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if path == paths[1] {
				req = httptest.NewRequest(http.MethodPost, path, nil)
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestAnalyticsSyncUsesVerifiedProviderAndIsIdempotent(t *testing.T) {
	store := analyticsTestStore(t)
	provider := &analyticsTestProvider{}
	service := analytics.NewService(store, analyticsTestGateway{publication: analyticsTestPublication()}, provider,
		platforminsights.NewUnavailableProvider("xhs", "xiaohongshu", "disabled in test"),
		platforminsights.NewUnavailableProvider("bilibili", "bilibili", "disabled in test"))
	handler := NewHandlerWithServicesAndSecurityAndAnalytics(nil, nil, nil, nil, service, SecurityOptions{PublicationAPIToken: "secret", AllowedOrigins: []string{"https://console.example"}}).Routes()

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/publications/pub-api-1/sync", nil)
		req.Header.Set("Authorization", "Bearer secret")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK {
			t.Fatalf("sync %d status=%d body=%s", i, resp.Code, resp.Body.String())
		}
	}
	if provider.calls != 2 {
		t.Fatalf("verified provider calls=%d, want two bounded manual sync calls", provider.calls)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/publications/pub-api-1/insights", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("insights status=%d body=%s", resp.Code, resp.Body.String())
	}
	var insights analytics.Insights
	decodeTestJSON(t, resp, &insights)
	if len(insights.Review) != 1 || len(insights.Metrics) != 1 || insights.Review[0].Verified != true {
		t.Fatalf("unexpected insights: %+v", insights)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/analytics/summary", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", resp.Code, resp.Body.String())
	}
	var summary analytics.Summary
	decodeTestJSON(t, resp, &summary)
	if summary.PublicationCount != 1 || summary.ReviewSnapshots != 1 || summary.MetricSnapshots != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestAnalyticsCapabilitiesAreTruthfulWhenUnconfigured(t *testing.T) {
	store := analyticsTestStore(t)
	service := analytics.NewService(store, analyticsTestGateway{publication: analyticsTestPublication()},
		platforminsights.NewDouyinMetricsProvider(platforminsights.DouyinMetricsConfig{}),
		platforminsights.NewUnavailableProvider("xhs", "xiaohongshu", "no approved read-only metrics API"),
		platforminsights.NewUnavailableProvider("bilibili", "bilibili", "no approved read-only metrics API"))
	handler := NewHandlerWithServicesAndSecurityAndAnalytics(nil, nil, nil, nil, service, SecurityOptions{PublicationAPIToken: "secret"}).Routes()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/analytics/capabilities", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("capabilities status=%d body=%s", resp.Code, resp.Body.String())
	}
	var capabilities []platforminsights.Capability
	decodeTestJSON(t, resp, &capabilities)
	if len(capabilities) != 3 || capabilities[0].State != platforminsights.CapabilityBlocked || capabilities[1].State != platforminsights.CapabilityUnavailable || capabilities[2].State != platforminsights.CapabilityUnavailable {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
}

func analyticsTestStore(t *testing.T) *analytics.Store {
	t.Helper()
	store, err := analytics.OpenStore(t.TempDir() + "/analytics.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func decodeTestJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
