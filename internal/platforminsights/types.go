// Package platforminsights contains platform-neutral review and metrics
// contracts. Platform adapters are deliberately injected behind these small
// interfaces so the analytics core does not know about publishers or UI
// automation implementations.
package platforminsights

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type PublicationStatus string

const (
	StatusSubmitted  PublicationStatus = "submitted"
	StatusReviewing  PublicationStatus = "reviewing"
	StatusPublished  PublicationStatus = "published"
	StatusRejected   PublicationStatus = "rejected"
	StatusUnknown    PublicationStatus = "unknown"
	StatusCommitting PublicationStatus = "committing"
	StatusFailed     PublicationStatus = "failed"
)

// Publication is the minimum durable publication view needed by analytics.
// It is intentionally not the publication package's concrete type.
type Publication struct {
	ID          string
	Platform    string
	Account     string
	ExternalID  string
	ExternalURL string
	Status      PublicationStatus
	Evidence    Evidence
	UpdatedAt   time.Time
}

type Evidence struct {
	Status     PublicationStatus `json:"status"`
	ExternalID string            `json:"external_id,omitempty"`
	URL        string            `json:"url,omitempty"`
	Verified   bool              `json:"verified"`
	Proof      string            `json:"proof,omitempty"`
	Message    string            `json:"message,omitempty"`
}

// PublicationGateway is the only publication dependency of the sync service.
// Reconcile must return durable, verified platform evidence; an unknown result
// must remain unknown and must never be inferred as published.
type PublicationGateway interface {
	Get(context.Context, string) (Publication, error)
	Reconcile(context.Context, string) (Publication, error)
}

// PublicationDiscovery is an optional read-only contract for analytics
// discovery. It is separate from PublicationGateway so existing fakes remain
// valid.
type PublicationDiscovery interface {
	ListForAnalytics(context.Context, string, int) ([]Publication, error)
}

// PublicationSnapshotReader is an optional direct-read contract for scheduled
// analytics. Unlike PublicationGateway.Get, it must not recover leases or
// modify publication state.
type PublicationSnapshotReader interface {
	GetForAnalytics(context.Context, string) (Publication, error)
}

// PublicationGatewayFunc is useful for small adapters and tests.
type PublicationGatewayFunc struct {
	GetFunc       func(context.Context, string) (Publication, error)
	ReconcileFunc func(context.Context, string) (Publication, error)
}

func (f PublicationGatewayFunc) Get(ctx context.Context, id string) (Publication, error) {
	if f.GetFunc == nil {
		return Publication{}, errors.New("publication gateway get is not configured")
	}
	return f.GetFunc(ctx, id)
}

func (f PublicationGatewayFunc) Reconcile(ctx context.Context, id string) (Publication, error) {
	if f.ReconcileFunc == nil {
		return Publication{}, errors.New("publication gateway reconcile is not configured")
	}
	return f.ReconcileFunc(ctx, id)
}

type CapabilityState string

const (
	CapabilityReady       CapabilityState = "ready"
	CapabilityBlocked     CapabilityState = "blocked"
	CapabilityUnavailable CapabilityState = "unavailable"
)

type Capability struct {
	Provider string          `json:"provider"`
	Platform string          `json:"platform"`
	State    CapabilityState `json:"state"`
	Reason   string          `json:"reason,omitempty"`
}

// MetricsRequest contains only a verified external identity and platform.
type MetricsRequest struct {
	PublicationID string
	Platform      string
	Account       string
	ExternalID    string
	ExternalURL   string
}

// MetricsObservation is a normalized provider response. The map permits a
// provider to preserve platform-specific counters without changing the core.
type MetricsObservation struct {
	Provider   string             `json:"provider"`
	Platform   string             `json:"platform"`
	ExternalID string             `json:"external_id"`
	ObservedAt time.Time          `json:"observed_at"`
	Metrics    map[string]float64 `json:"metrics"`
	Raw        map[string]any     `json:"raw,omitempty"`
}

type MetricsProvider interface {
	Name() string
	Platform() string
	Capability(context.Context) Capability
	Fetch(context.Context, MetricsRequest) (MetricsObservation, error)
}

var (
	ErrCapabilityBlocked = errors.New("metrics capability blocked")
	ErrUnavailable       = errors.New("metrics provider unavailable")
	ErrLeaseLost         = errors.New("analytics sync lease lost")
	ErrSyncNotFound      = errors.New("analytics sync job not found")
)

func blockedCapability(name, platform, reason string) Capability {
	return Capability{Provider: name, Platform: platform, State: CapabilityBlocked, Reason: reason}
}

// UnavailableProvider is a truthful no-network provider for platforms without
// an approved metrics API. It never claims to have observed platform data.
type UnavailableProvider struct {
	ProviderName string
	PlatformName string
	Reason       string
}

func NewUnavailableProvider(provider, platform, reason string) *UnavailableProvider {
	return &UnavailableProvider{ProviderName: provider, PlatformName: platform, Reason: reason}
}
func (p *UnavailableProvider) Name() string     { return p.ProviderName }
func (p *UnavailableProvider) Platform() string { return p.PlatformName }
func (p *UnavailableProvider) Capability(context.Context) Capability {
	return Capability{Provider: p.Name(), Platform: p.Platform(), State: CapabilityUnavailable, Reason: p.Reason}
}
func (p *UnavailableProvider) Fetch(context.Context, MetricsRequest) (MetricsObservation, error) {
	return MetricsObservation{}, fmt.Errorf("%w: %s", ErrUnavailable, p.Reason)
}

// EgoInsights is an intentionally strict seam. No implementation may turn
// browser/UI signals into metrics without verified evidence.
type EgoInsights interface {
	Capability(context.Context) Capability
	Fetch(context.Context, MetricsRequest) (MetricsObservation, error)
}

type BlockedEgoInsights struct{}

func (BlockedEgoInsights) Capability(context.Context) Capability {
	return blockedCapability("ego", "", "verified insight evidence is not configured")
}
func (BlockedEgoInsights) Fetch(context.Context, MetricsRequest) (MetricsObservation, error) {
	return MetricsObservation{}, fmt.Errorf("%w: Ego insight requires verified evidence", ErrCapabilityBlocked)
}

// DouyinHTTPClient is injectable in tests and in callers that need custom
// transport behavior. It is intentionally the standard net/http contract.
type DouyinHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type DouyinMetricsConfig struct {
	AccessToken string
	OpenID      string
	Scope       string
	APIBaseURL  string
	HTTPClient  DouyinHTTPClient
}

type DouyinMetricsProvider struct {
	config DouyinMetricsConfig
}

func NewDouyinMetricsProvider(config DouyinMetricsConfig) *DouyinMetricsProvider {
	if strings.TrimSpace(config.APIBaseURL) == "" {
		config.APIBaseURL = "https://open.douyin.com"
	}
	if config.HTTPClient == nil {
		config.HTTPClient = http.DefaultClient
	}
	return &DouyinMetricsProvider{config: config}
}
