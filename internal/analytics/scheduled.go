package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
)

// RunScheduledOnce performs one bounded discovery page and one bounded batch
// of due jobs. The scheduled path only uses direct publication snapshots;
// it never invokes publication reconciliation or any publication mutation.
func (s *Service) RunScheduledOnce(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("analytics store is not configured")
	}
	if !s.pollEnabled {
		return nil
	}
	if s.gateway == nil {
		return ErrNoGateway
	}
	discovery, ok := s.gateway.(platforminsights.PublicationDiscovery)
	if !ok {
		return ErrNoDiscovery
	}
	reader, ok := s.gateway.(platforminsights.PublicationSnapshotReader)
	if !ok {
		return ErrNoSnapshotReader
	}
	if err := s.discover(ctx, discovery); err != nil {
		return err
	}

	var firstErr error
	for processed := 0; processed < s.maxJobs; processed++ {
		job, lease, err := s.store.ClaimDue(ctx, s.owner, s.leaseTTL, s.now().UTC())
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		if err := s.processScheduled(ctx, reader, job, lease); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				break
			}
		}
	}
	return firstErr
}

func (s *Service) discover(ctx context.Context, discovery platforminsights.PublicationDiscovery) error {
	cursor, err := s.store.DiscoveryCursor(ctx)
	if err != nil {
		return err
	}
	publications, err := discovery.ListForAnalytics(ctx, cursor, s.discoveryPageSize)
	if err != nil {
		return err
	}
	if len(publications) > s.discoveryPageSize {
		publications = publications[:s.discoveryPageSize]
	}
	if len(publications) == 0 {
		return s.store.SaveDiscoveryCursor(ctx, "")
	}
	now := s.now().UTC()
	seen := make(map[string]struct{}, len(publications))
	for _, publication := range publications {
		id := strings.TrimSpace(publication.ID)
		if id == "" {
			return errors.New("analytics discovery returned a publication without an id")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if err := s.store.EnsureScheduled(ctx, id); err != nil {
			return err
		}
		if scheduledReactivationEligible(publication, now, s.metricsWindow) {
			if _, err := s.store.ReactivateDone(ctx, id, now); err != nil {
				return err
			}
		}
	}
	lastID := strings.TrimSpace(publications[len(publications)-1].ID)
	if lastID == cursor {
		return ErrDiscoveryStalled
	}
	return s.store.SaveDiscoveryCursor(ctx, lastID)
}

func scheduledReactivationEligible(publication platforminsights.Publication, now time.Time, metricsWindow time.Duration) bool {
	switch publication.Status {
	case platforminsights.StatusSubmitted, platforminsights.StatusReviewing, platforminsights.StatusUnknown:
		return true
	case platforminsights.StatusPublished:
		publishedAt := publication.UpdatedAt
		if publishedAt.IsZero() {
			publishedAt = now
		}
		return publication.Evidence.Verified && now.Before(publishedAt.Add(metricsWindow))
	default:
		return false
	}
}

func (s *Service) processScheduled(ctx context.Context, reader platforminsights.PublicationSnapshotReader, job SyncJob, lease Lease) error {
	var publication platforminsights.Publication
	err := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		var getErr error
		publication, getErr = reader.GetForAnalytics(callCtx, job.PublicationID)
		return getErr
	})
	if err != nil {
		return s.finishScheduled(ctx, job, lease, JobRetry, err)
	}
	if publication.ID == "" || publication.ID != job.PublicationID {
		return s.finishScheduled(ctx, job, lease, JobRetry, errors.New("analytics gateway returned an invalid publication identity"))
	}

	review := reviewSnapshot(job.PublicationID, publication)
	if _, _, err := s.store.SaveReviewSnapshotFenced(ctx, review, lease); err != nil {
		return err
	}

	switch publication.Status {
	case platforminsights.StatusRejected, platforminsights.StatusFailed, platforminsights.PublicationStatus("expired"):
		return s.finishScheduled(ctx, job, lease, JobDone, nil)
	case platforminsights.StatusPublished:
		if review.Status != platforminsights.StatusPublished || !review.Verified || strings.TrimSpace(publication.ExternalID) == "" {
			return s.finishScheduled(ctx, job, lease, JobWaiting, nil)
		}
		return s.processPublished(ctx, job, lease, publication)
	case platforminsights.StatusSubmitted, platforminsights.StatusReviewing, platforminsights.StatusUnknown, platforminsights.StatusCommitting:
		return s.finishScheduled(ctx, job, lease, JobWaiting, nil)
	default:
		// Unknown states fail closed into review polling. They do not become
		// published merely because a provider happens to answer.
		return s.finishScheduled(ctx, job, lease, JobWaiting, nil)
	}
}

func (s *Service) processPublished(ctx context.Context, job SyncJob, lease Lease, publication platforminsights.Publication) error {
	now := s.now().UTC()
	publishedAt := publication.UpdatedAt
	if publishedAt.IsZero() {
		publishedAt = now
	}
	windowEnds := publishedAt.Add(s.metricsWindow)
	if !now.Before(windowEnds) {
		return s.finishScheduled(ctx, job, lease, JobDone, nil)
	}

	provider := s.providers[publication.Platform]
	if provider == nil {
		return s.finishScheduled(ctx, job, lease, JobBlocked, fmt.Errorf("%w: no metrics provider for %s", platforminsights.ErrCapabilityBlocked, publication.Platform))
	}
	var capability platforminsights.Capability
	if err := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		capability = provider.Capability(callCtx)
		return nil
	}); err != nil {
		return s.finishScheduled(ctx, job, lease, JobRetry, err)
	}
	if capability.State != platforminsights.CapabilityReady {
		reason := capability.Reason
		if reason == "" {
			reason = "read-only metrics capability is unavailable"
		}
		return s.finishScheduled(ctx, job, lease, JobBlocked, fmt.Errorf("%w: %s", platforminsights.ErrCapabilityBlocked, reason))
	}

	var observation platforminsights.MetricsObservation
	if err := s.callWithLease(ctx, job, lease, func(callCtx context.Context) error {
		var fetchErr error
		observation, fetchErr = provider.Fetch(callCtx, platforminsights.MetricsRequest{
			PublicationID: job.PublicationID,
			Platform:      publication.Platform,
			Account:       publication.Account,
			ExternalID:    publication.ExternalID,
			ExternalURL:   publication.ExternalURL,
		})
		return fetchErr
	}); err != nil {
		return s.finishScheduled(ctx, job, lease, JobRetry, err)
	}
	if strings.TrimSpace(observation.Provider) == "" {
		observation.Provider = provider.Name()
	}
	if strings.TrimSpace(observation.Platform) == "" {
		observation.Platform = provider.Platform()
	}
	if strings.TrimSpace(observation.ExternalID) == "" || observation.ExternalID != publication.ExternalID {
		return s.finishScheduled(ctx, job, lease, JobRetry, fmt.Errorf("%w: external identity does not match the publication", ErrInvalidObservation))
	}
	if observation.Platform != publication.Platform || observation.Provider != provider.Name() {
		return s.finishScheduled(ctx, job, lease, JobRetry, fmt.Errorf("%w: provider/platform identity does not match", ErrInvalidObservation))
	}
	if !s.now().UTC().Before(windowEnds) {
		return s.finishScheduled(ctx, job, lease, JobDone, nil)
	}
	if _, _, err := s.store.SaveMetricSnapshotFenced(ctx, MetricSnapshot{
		PublicationID: job.PublicationID,
		Provider:      observation.Provider,
		Platform:      observation.Platform,
		ExternalID:    observation.ExternalID,
		Metrics:       observation.Metrics,
		Raw:           observation.Raw,
		ObservedAt:    observation.ObservedAt,
		LastSeenAt:    observation.ObservedAt,
	}, lease); err != nil {
		return err
	}
	return s.finishScheduled(ctx, job, lease, JobWaiting, nil)
}

func (s *Service) finishScheduled(ctx context.Context, job SyncJob, lease Lease, status string, runErr error) error {
	next := s.now().UTC()
	switch status {
	case JobWaiting:
		if job.Status == JobRunning {
			// Published jobs are sampled less often than review-state jobs. The
			// caller chooses the interval by recording the next due time below.
			next = next.Add(s.reviewInterval)
		}
	case JobRetry:
		next = next.Add(s.retryDelay(job.Attempts))
	case JobBlocked:
		next = next.Add(s.retryDelay(job.Attempts))
	}
	// A metric observation is the only path that enters waiting from a
	// published state. Use the metric interval when the durable history says
	// this job has already reached a verified published snapshot.
	if status == JobWaiting {
		if snapshots, err := s.store.ListReviewSnapshots(ctx, job.PublicationID); err == nil && len(snapshots) > 0 && snapshots[len(snapshots)-1].Status == platforminsights.StatusPublished {
			next = s.now().UTC().Add(s.metricsInterval)
		}
	}
	if err := s.store.Finish(ctx, job.PublicationID, lease, status, next, runErr); err != nil {
		return err
	}
	return runErr
}

func (s *Service) retryDelay(attempts int) time.Duration {
	delay := s.retry
	if attempts < 1 {
		attempts = 1
	}
	for i := 1; i < attempts && delay < maxRetryDelay; i++ {
		if delay > maxRetryDelay/2 {
			return maxRetryDelay
		}
		delay *= 2
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

// callWithLease bounds a slow read-only provider call by renewing its lease.
// Lease loss cancels the provider context; any later durable write is also
// fenced by the store token.
func (s *Service) callWithLease(ctx context.Context, job SyncJob, lease Lease, fn func(context.Context) error) error {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- fn(callCtx) }()
	interval := s.leaseTTL / 3
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ticker.C:
			if err := s.store.Renew(ctx, job.PublicationID, lease, s.leaseTTL); err != nil {
				cancel()
				return err
			}
		case <-ctx.Done():
			cancel()
			return ctx.Err()
		}
	}
}
