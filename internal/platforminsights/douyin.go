package platforminsights

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (p *DouyinMetricsProvider) Name() string     { return "douyin" }
func (p *DouyinMetricsProvider) Platform() string { return "douyin" }

func (p *DouyinMetricsProvider) Capability(context.Context) Capability {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(p.config.AccessToken) == "" {
		missing = append(missing, "access token")
	}
	if strings.TrimSpace(p.config.OpenID) == "" {
		missing = append(missing, "open id")
	}
	if strings.TrimSpace(p.config.Scope) == "" {
		missing = append(missing, "metrics scope")
	}
	if len(missing) != 0 {
		return blockedCapability(p.Name(), p.Platform(), "missing "+strings.Join(missing, ", "))
	}
	return Capability{Provider: p.Name(), Platform: p.Platform(), State: CapabilityReady}
}

type douyinMetricsResponse struct {
	Data struct {
		ErrorCode   int               `json:"error_code"`
		Description string            `json:"description"`
		List        []douyinVideoData `json:"list"`
	} `json:"data"`
	Extra struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"extra"`
}

type douyinVideoData struct {
	ItemID     string             `json:"item_id"`
	Statistics map[string]float64 `json:"statistics"`
}

// Fetch uses only the documented, read-only insight seam and accepts data only
// when the response identifies exactly the requested video.
func (p *DouyinMetricsProvider) Fetch(ctx context.Context, req MetricsRequest) (MetricsObservation, error) {
	capability := p.Capability(ctx)
	if capability.State != CapabilityReady {
		return MetricsObservation{}, fmt.Errorf("%w: %s", ErrCapabilityBlocked, capability.Reason)
	}
	if strings.TrimSpace(req.ExternalID) == "" {
		return MetricsObservation{}, fmt.Errorf("douyin metrics requires external id")
	}
	endpoint, err := url.Parse(strings.TrimRight(p.config.APIBaseURL, "/") + "/video/data/")
	if err != nil {
		return MetricsObservation{}, err
	}
	query := endpoint.Query()
	query.Set("open_id", p.config.OpenID)
	endpoint.RawQuery = query.Encode()
	body, err := json.Marshal(struct {
		ItemIDs []string `json:"item_ids"`
	}{ItemIDs: []string{req.ExternalID}})
	if err != nil {
		return MetricsObservation{}, fmt.Errorf("encode douyin metrics request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return MetricsObservation{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("access-token", p.config.AccessToken)
	httpReq.Header.Set("x-douyin-scope", p.config.Scope)
	response, err := p.config.HTTPClient.Do(httpReq)
	if err != nil {
		return MetricsObservation{}, fmt.Errorf("douyin metrics request: %w", err)
	}
	defer response.Body.Close()
	var decoded douyinMetricsResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&decoded); err != nil {
		return MetricsObservation{}, fmt.Errorf("decode douyin metrics response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return MetricsObservation{}, fmt.Errorf("douyin metrics http status %d: %s", response.StatusCode, douyinDescription(decoded))
	}
	if decoded.Data.ErrorCode != 0 || decoded.Extra.ErrorCode != 0 {
		return MetricsObservation{}, fmt.Errorf("douyin metrics error: %s", douyinDescription(decoded))
	}
	if len(decoded.Data.List) != 1 {
		return MetricsObservation{}, fmt.Errorf("douyin metrics response identity is missing or ambiguous")
	}
	item := decoded.Data.List[0]
	if item.ItemID == "" || item.ItemID != req.ExternalID {
		return MetricsObservation{}, fmt.Errorf("douyin metrics response identity mismatch")
	}
	metrics := copyMetrics(item.Statistics)
	if len(metrics) == 0 {
		return MetricsObservation{}, fmt.Errorf("douyin metrics response contained no counters")
	}
	raw := map[string]any{
		"item_id":    item.ItemID,
		"statistics": metrics,
	}
	return MetricsObservation{Provider: p.Name(), Platform: p.Platform(), ExternalID: item.ItemID, ObservedAt: time.Now().UTC(), Metrics: metrics, Raw: raw}, nil
}

func douyinDescription(response douyinMetricsResponse) string {
	if response.Data.Description != "" {
		return response.Data.Description
	}
	return response.Extra.Description
}

func copyMetrics(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
