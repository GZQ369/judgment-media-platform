package platforminsights

import (
	"context"
	"errors"
	"net/http"
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
