package distribution

import (
	"context"
	"errors"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

// RetryOnce durably consumes a parent retry operation before waking the worker.
// Startup scanning also recovers queued work if the process stops before enqueue.
func (s *Service) RetryOnce(ctx context.Context, parentID string, parentAttempt int, childID string) (domain.Run, error) {
	run, err := s.store.RetryOnce(ctx, parentID, parentAttempt, childID)
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunStatusQueued {
		if err := s.enqueue(childID); err != nil && !errors.Is(err, ErrServiceStopped) {
			return domain.Run{}, err
		}
	}
	return run, nil
}
