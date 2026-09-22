package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
	"github.com/google/uuid"
)

const (
	defaultSyncLeaseTTL      = 30 * time.Second
	defaultRetryDelay        = time.Minute
	defaultReviewInterval    = 15 * time.Minute
	defaultMetricsInterval   = 6 * time.Hour
	defaultMetricsWindow     = 7 * 24 * time.Hour
	defaultDiscoveryPageSize = 100
	defaultMaxJobs           = 25
	maxDiscoveryPageSize     = 200
	maxJobs                  = 200
	maxRetryDelay            = 1 * time.Hour
	publicationProvider      = "publication"
)

var (
	ErrNoGateway          = errors.New("analytics publication gateway is not configured")
	ErrNoDiscovery        = errors.New("analytics publication discovery is not configured")
	ErrNoSnapshotReader   = errors.New("analytics publication snapshot reader is not configured")
	ErrDiscoveryStalled   = errors.New("analytics publication discovery cursor did not advance")
	ErrInvalidObservation = errors.New("analytics provider returned an invalid observation")
	ErrSyncInProgress     = errors.New("analytics sync is already in progress")
)

// Options controls one bounded analytics sync worker. The service does not
// start a background goroutine; callers can run one job at a time or attach
// RunOnce to an existing scheduler.
type Options struct {
	Owner             string
	LeaseTTL          time.Duration
	RetryDelay        time.Duration
	PollEnabled       bool
	ReviewInterval    time.Duration
	MetricsInterval   time.Duration
	MetricsWindow     time.Duration
	DiscoveryPageSize int
	MaxJobs           int
	Clock             func() time.Time
}

// Service reconciles a publication, stores its immutable review observations,
// and fetches metrics only after verified published evidence is present.
type Service struct {
	store             *Store
	gateway           platforminsights.PublicationGateway
	providers         map[string]platforminsights.MetricsProvider
	owner             string
	leaseTTL          time.Duration
	retry             time.Duration
	pollEnabled       bool
	reviewInterval    time.Duration
	metricsInterval   time.Duration
	metricsWindow     time.Duration
	discoveryPageSize int
	maxJobs           int
	now               func() time.Time
}

// Insights is the complete durable analytics view for one publication.
type Insights struct {
	PublicationID string           `json:"publication_id"`
	Review        []ReviewSnapshot `json:"review_snapshots"`
	Metrics       []MetricSnapshot `json:"metric_snapshots"`
	Events        []Event          `json:"events"`
	SyncJob       *SyncJob         `json:"sync_job,omitempty"`
}

// NewService constructs a bounded sync service. Providers are selected by
// their platform name; omitted providers simply leave metrics unavailable.
func NewService(store *Store, gateway platforminsights.PublicationGateway, providers ...platforminsights.MetricsProvider) *Service {
	return NewServiceWithOptions(store, gateway, providers, Options{})
}

func NewServiceWithOptions(store *Store, gateway platforminsights.PublicationGateway, providers []platforminsights.MetricsProvider, options Options) *Service {
	if options.Owner == "" {
		options.Owner = fmt.Sprintf("analytics-%d-%s", os.Getpid(), uuid.NewString())
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = defaultSyncLeaseTTL
	}
	if options.RetryDelay <= 0 {
		options.RetryDelay = defaultRetryDelay
	}
	if options.ReviewInterval <= 0 {
		options.ReviewInterval = defaultReviewInterval
	}
	if options.MetricsInterval <= 0 {
		options.MetricsInterval = defaultMetricsInterval
	}
	if options.MetricsWindow <= 0 {
		options.MetricsWindow = defaultMetricsWindow
	}
	if options.DiscoveryPageSize <= 0 {
		options.DiscoveryPageSize = defaultDiscoveryPageSize
	}
	if options.DiscoveryPageSize > maxDiscoveryPageSize {
		options.DiscoveryPageSize = maxDiscoveryPageSize
	}
	if options.MaxJobs <= 0 {
		options.MaxJobs = defaultMaxJobs
	}
	if options.MaxJobs > maxJobs {
		options.MaxJobs = maxJobs
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	providerMap := make(map[string]platforminsights.MetricsProvider, len(providers))
	for _, provider := range providers {
		if provider == nil || strings.TrimSpace(provider.Platform()) == "" {
			continue
		}
		providerMap[provider.Platform()] = provider
	}
	return &Service{store: store, gateway: gateway, providers: providerMap, owner: options.Owner, leaseTTL: options.LeaseTTL, retry: options.RetryDelay, pollEnabled: options.PollEnabled, reviewInterval: options.ReviewInterval, metricsInterval: options.MetricsInterval, metricsWindow: options.MetricsWindow, discoveryPageSize: options.DiscoveryPageSize, maxJobs: options.MaxJobs, now: options.Clock}
}

// Enqueue makes a publication eligible for one reconciliation pass.
func (s *Service) Enqueue(ctx context.Context, publicationID string) error {
	if s == nil || s.store == nil {
		return errors.New("analytics store is not configured")
	}
	return s.store.Enqueue(ctx, publicationID)
}

// Sync enqueues and immediately attempts one bounded reconciliation pass.
// A caller that wants a durable queue can call Enqueue and later RunOnce.
func (s *Service) Sync(ctx context.Context, publicationID string) error {
	if s == nil || s.store == nil {
		return errors.New("analytics store is not configured")
	}
	if strings.TrimSpace(publicationID) == "" {
		return errors.New("publication id is required")
	}
	if s.gateway != nil {
		if _, err := s.gateway.Get(ctx, publicationID); err != nil {
			return err
		}
	}
	if err := s.Enqueue(ctx, publicationID); err != nil {
		return err
	}
	return s.RunOnceForPublication(ctx, publicationID)
}

// Reconcile is a compatibility alias for Sync.
func (s *Service) Reconcile(ctx context.Context, publicationID string) error {
	return s.Sync(ctx, publicationID)
}

// RunOnce claims one due publication and fences every durable write with the
// claimed lease. It returns sql.ErrNoRows when no work is due.
func (s *Service) RunOnce(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("analytics store is not configured")
	}
	job, lease, err := s.store.ClaimDue(ctx, s.owner, s.leaseTTL, time.Now().UTC())
	if err != nil {
		return err
	}
	return s.processManual(ctx, job, lease)
}

// RunOnceForPublication claims only the requested publication. It preserves
// the same lease and fencing semantics as RunOnce while preventing a manual
// request from accidentally processing another queued publication.
func (s *Service) RunOnceForPublication(ctx context.Context, publicationID string) error {
	if s == nil || s.store == nil {
		return errors.New("analytics store is not configured")
	}
	job, lease, err := s.store.ClaimDueForPublication(ctx, publicationID, s.owner, s.leaseTTL, time.Now().UTC())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if current, getErr := s.store.GetJob(ctx, publicationID); getErr == nil && current.Status == JobRunning && current.LeaseUntil.After(time.Now().UTC()) {
				return ErrSyncInProgress
			}
		}
		return err
	}
	return s.processManual(ctx, job, lease)
}

// Insights returns only durable observations and job state. If a publication
// gateway is configured, it is also used to distinguish an unknown
// publication from a valid publication with no observations yet.
func (s *Service) Insights(ctx context.Context, publicationID string) (Insights, error) {
	if s == nil || s.store == nil {
		return Insights{}, errors.New("analytics store is not configured")
	}
	if strings.TrimSpace(publicationID) == "" {
		return Insights{}, errors.New("publication id is required")
	}
	if s.gateway != nil {
		if _, err := s.gateway.Get(ctx, publicationID); err != nil {
			return Insights{}, err
		}
	}
	review, err := s.store.ListReviewSnapshots(ctx, publicationID)
	if err != nil {
		return Insights{}, err
	}
	metrics, err := s.store.ListMetricSnapshots(ctx, publicationID)
	if err != nil {
		return Insights{}, err
	}
	events, err := s.store.ListEvents(ctx, publicationID)
	if err != nil {
		return Insights{}, err
	}
	result := Insights{PublicationID: publicationID, Review: review, Metrics: metrics, Events: events}
	if job, err := s.store.GetJob(ctx, publicationID); err == nil {
		result.SyncJob = &job
	} else if !errors.Is(err, platforminsights.ErrSyncNotFound) {
		return Insights{}, err
	}
	return result, nil
}

func (s *Service) Summary(ctx context.Context) (Summary, error) {
	if s == nil || s.store == nil {
		return Summary{}, errors.New("analytics store is not configured")
	}
	return s.store.Summary(ctx)
}

// Capabilities reports every configured provider, including explicit
// unavailable providers for platforms that have no approved read-only API.
func (s *Service) Capabilities(ctx context.Context) []platforminsights.Capability {
	if s == nil {
		return nil
	}
	platforms := []string{"douyin", "xiaohongshu", "bilibili"}
	result := make([]platforminsights.Capability, 0, len(platforms))
	for _, platform := range platforms {
		provider := s.providers[platform]
		if provider == nil {
			result = append(result, platforminsights.Capability{Platform: platform, State: platforminsights.CapabilityUnavailable, Reason: "no read-only metrics provider registered"})
			continue
		}
		capability := provider.Capability(ctx)
		if capability.Platform == "" {
			capability.Platform = platform
		}
		result = append(result, capability)
	}
	return result
}

func (s *Service) processManual(ctx context.Context, job SyncJob, lease Lease) error {
	if s.gateway == nil {
		return s.finish(ctx, job, lease, JobBlocked, ErrNoGateway)
	}

	type reconcileResult struct {
		publication platforminsights.Publication
	}
	result := make(chan reconcileResult, 1)
	reconcileErr := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		publication, err := s.gateway.Reconcile(callCtx, job.PublicationID)
		result <- reconcileResult{publication: publication}
		return err
	})
	var publication platforminsights.Publication
	select {
	case completed := <-result:
		publication = completed.publication
	default:
		// callWithLease may return after cancellation or lease loss while a
		// non-cooperative gateway is still unwinding. Do not wait for it.
	}
	if publication.ID != "" {
		review := reviewSnapshot(job.PublicationID, publication)
		if _, _, err := s.store.SaveReviewSnapshotFenced(ctx, review, lease); err != nil {
			return err
		}
		if reconcileErr == nil && review.Status == platforminsights.StatusPublished && review.Verified && review.ExternalID != "" {
			if err := s.collectMetrics(ctx, publication, job, lease); err != nil {
				return err
			}
		}
	}

	if reconcileErr != nil {
		return s.finish(ctx, job, lease, JobRetry, reconcileErr)
	}
	return s.finish(ctx, job, lease, JobDone, nil)
}

func (s *Service) collectMetrics(ctx context.Context, publication platforminsights.Publication, job SyncJob, lease Lease) error {
	provider := s.providers[publication.Platform]
	if provider == nil {
		return nil
	}
	var capability platforminsights.Capability
	if err := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		capability = provider.Capability(callCtx)
		return nil
	}); err != nil {
		return s.finish(ctx, job, lease, JobRetry, err)
	}
	if capability.State != platforminsights.CapabilityReady {
		err := fmt.Errorf("%w: %s", platforminsights.ErrCapabilityBlocked, capability.Reason)
		return s.finish(ctx, job, lease, JobBlocked, err)
	}
	var observation platforminsights.MetricsObservation
	err := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		var fetchErr error
		observation, fetchErr = provider.Fetch(callCtx, platforminsights.MetricsRequest{
			PublicationID: job.PublicationID,
			Platform:      publication.Platform,
			Account:       publication.Account,
			ExternalID:    publication.ExternalID,
			ExternalURL:   publication.ExternalURL,
		})
		return fetchErr
	})
	if err != nil {
		return s.finish(ctx, job, lease, JobRetry, err)
	}
	if strings.TrimSpace(observation.Provider) == "" {
		observation.Provider = provider.Name()
	}
	if strings.TrimSpace(observation.Platform) == "" {
		observation.Platform = provider.Platform()
	}
	if strings.TrimSpace(observation.ExternalID) == "" || observation.ExternalID != publication.ExternalID {
		return s.finish(ctx, job, lease, JobRetry, fmt.Errorf("%w: external identity does not match the publication", ErrInvalidObservation))
	}
	if observation.Platform != publication.Platform || observation.Provider != provider.Name() {
		return s.finish(ctx, job, lease, JobRetry, fmt.Errorf("%w: provider/platform identity does not match", ErrInvalidObservation))
	}
	_, _, err = s.store.SaveMetricSnapshotFenced(ctx, MetricSnapshot{
		PublicationID:   job.PublicationID,
		Provider:        observation.Provider,
		Platform:        observation.Platform,
		ExternalID:      observation.ExternalID,
		ObservationHash: "",
		Metrics:         observation.Metrics,
		Raw:             observation.Raw,
		ObservedAt:      observation.ObservedAt,
		LastSeenAt:      observation.ObservedAt,
	}, lease)
	return err
}

func (s *Service) finish(ctx context.Context, job SyncJob, lease Lease, status string, runErr error) error {
	next := time.Now().UTC()
	if status == JobRetry {
		next = next.Add(s.retry)
	}
	finishErr := s.store.Finish(ctx, job.PublicationID, lease, status, next, runErr)
	if finishErr != nil {
		return finishErr
	}
	return runErr
}

func reviewSnapshot(publicationID string, publication platforminsights.Publication) ReviewSnapshot {
	status := publication.Status
	if !validReviewStatus(status) || (status == platforminsights.StatusPublished && !verifiedExternalEvidence(publication)) {
		status = platforminsights.StatusUnknown
	}
	return ReviewSnapshot{
		PublicationID: publicationID,
		Provider:      publicationProvider,
		Platform:      publication.Platform,
		Status:        status,
		ExternalID:    publication.ExternalID,
		ExternalURL:   publication.ExternalURL,
		Verified:      publication.Evidence.Verified && status != platforminsights.StatusUnknown,
		Proof:         publication.Evidence.Proof,
		ObservedAt:    publication.UpdatedAt,
	}
}

func validReviewStatus(status platforminsights.PublicationStatus) bool {
	switch status {
	case platforminsights.StatusSubmitted, platforminsights.StatusReviewing, platforminsights.StatusPublished, platforminsights.StatusRejected, platforminsights.StatusUnknown:
		return true
	default:
		return false
	}
}

func verifiedExternalEvidence(publication platforminsights.Publication) bool {
	return publication.Evidence.Verified && (strings.TrimSpace(publication.ExternalID) != "" || strings.TrimSpace(publication.ExternalURL) != "")
}
