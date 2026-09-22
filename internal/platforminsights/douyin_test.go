package platforminsights

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type countingHTTPClient struct{ calls int }

func (c *countingHTTPClient) Do(*http.Request) (*http.Response, error) {
	c.calls++
	return nil, errors.New("network should not be reached")
}

func TestDouyinProviderWithoutCredentialsDoesNotUseNetwork(t *testing.T) {
	client := &countingHTTPClient{}
	provider := NewDouyinMetricsProvider(DouyinMetricsConfig{HTTPClient: client})
	capability := provider.Capability(context.Background())
	if capability.State != CapabilityBlocked || capability.Reason == "" {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	_, err := provider.Fetch(context.Background(), MetricsRequest{ExternalID: "item-1"})
	if !errors.Is(err, ErrCapabilityBlocked) {
		t.Fatalf("fetch error = %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("provider made %d network calls without credentials", client.calls)
	}
}

func TestDouyinProviderFetchUsesOfficialRequestShapeAndMatchingItem(t *testing.T) {
	const itemID = "item-1"
	var gotMethod, gotContentType, gotAccessToken, gotScope, gotOpenID string
	var gotBody map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotAccessToken = r.Header.Get("access-token")
		gotScope = r.Header.Get("x-douyin-scope")
		gotOpenID = r.URL.Query().Get("open_id")
		if r.URL.Query().Get("item_id") != "" {
			t.Errorf("unexpected item_id query parameter")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		_, _ = io.WriteString(w, `{"data":{"error_code":0,"list":[{"item_id":"item-1","statistics":{"play_count":12,"digg_count":3}}]},"extra":{"error_code":0}}`)
	}))
	defer server.Close()

	provider := NewDouyinMetricsProvider(DouyinMetricsConfig{
		AccessToken: "token-1",
		OpenID:      "open-1",
		Scope:       "video.data",
		APIBaseURL:  server.URL,
	})
	observation, err := provider.Fetch(context.Background(), MetricsRequest{ExternalID: itemID})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
	if gotAccessToken != "token-1" {
		t.Fatalf("access-token = %q", gotAccessToken)
	}
	if gotScope != "video.data" {
		t.Fatalf("x-douyin-scope = %q", gotScope)
	}
	if gotOpenID != "open-1" {
		t.Fatalf("open_id = %q", gotOpenID)
	}
	if !equalStringSlices(gotBody["item_ids"], []string{itemID}) {
		t.Fatalf("item_ids = %#v", gotBody["item_ids"])
	}
	if observation.ExternalID != itemID || observation.Metrics["play_count"] != 12 || observation.Metrics["digg_count"] != 3 {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDouyinProviderFetchRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "mismatched item",
			body: `{"data":{"error_code":0,"list":[{"item_id":"other-item","statistics":{"play_count":12}}]},"extra":{"error_code":0}}`,
			want: "identity mismatch",
		},
		{
			name: "ambiguous items",
			body: `{"data":{"error_code":0,"list":[{"item_id":"item-1","statistics":{"play_count":12}},{"item_id":"item-1","statistics":{"play_count":13}}]},"extra":{"error_code":0}}`,
			want: "missing or ambiguous",
		},
		{
			name: "empty response",
			body: `{"data":{"error_code":0,"list":[]},"extra":{"error_code":0}}`,
			want: "missing or ambiguous",
		},
		{
			name: "empty counters",
			body: `{"data":{"error_code":0,"list":[{"item_id":"item-1","statistics":{}}]},"extra":{"error_code":0}}`,
			want: "no counters",
		},
		{
			name: "malformed counters",
			body: `{"data":{"error_code":0,"list":[{"item_id":"item-1","statistics":{"play_count":"twelve"}}]},"extra":{"error_code":0}}`,
			want: "decode douyin metrics response",
		},
		{
			name: "api error",
			body: `{"data":{"error_code":2100005,"description":"Parameter error"},"extra":{"error_code":2100005,"description":"Parameter error"}}`,
			want: "douyin metrics error: Parameter error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			defer server.Close()

			provider := NewDouyinMetricsProvider(DouyinMetricsConfig{
				AccessToken: "token-1",
				OpenID:      "open-1",
				Scope:       "video.data",
				APIBaseURL:  server.URL,
			})
			_, err := provider.Fetch(context.Background(), MetricsRequest{ExternalID: "item-1"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Fetch() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
