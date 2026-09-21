package platforminsights

import (
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
		ErrorCode   int                `json:"error_code"`
		Description string             `json:"description"`
		Item        map[string]any     `json:"item"`
		Metrics     map[string]float64 `json:"metrics"`
	} `json:"data"`
	Extra struct {
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	} `json:"extra"`
}

// Fetch uses only the documented, read-only insight seam. The response parser
// accepts both a normalized metrics object and a flat item object so API
// response evolution does not silently discard counters.
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
	query.Set("item_id", req.ExternalID)
	endpoint.RawQuery = query.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return MetricsObservation{}, err
	}
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
	metrics := copyMetrics(decoded.Data.Metrics)
	if len(metrics) == 0 {
		metrics = numericFields(decoded.Data.Item)
	}
	if len(metrics) == 0 {
		return MetricsObservation{}, fmt.Errorf("douyin metrics response contained no counters")
	}
	raw := make(map[string]any, len(decoded.Data.Item))
	for key, value := range decoded.Data.Item {
		raw[key] = value
	}
	return MetricsObservation{Provider: p.Name(), Platform: p.Platform(), ExternalID: req.ExternalID, ObservedAt: time.Now().UTC(), Metrics: metrics, Raw: raw}, nil
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

func numericFields(in map[string]any) map[string]float64 {
	out := make(map[string]float64)
	for key, value := range in {
		switch number := value.(type) {
		case float64:
			out[key] = number
		case json.Number:
			if parsed, err := number.Float64(); err == nil {
				out[key] = parsed
			}
		}
	}
	return out
}
