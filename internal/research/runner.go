package research

import (
	"context"
	"fmt"
	"sync"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

const DefaultMaxConcurrentSources = 4

// SourceFetcher is the source-reading capability required by Runner. Keeping
// this as a small interface makes the runner injectable without coupling it to
// a transport or to orchestration.
type SourceFetcher interface {
	Fetch(context.Context, string) (SourceSnapshot, error)
}

// RunnerConfig controls source fan-out and the existing Conduct budget.
type RunnerConfig struct {
	MaxConcurrentSources int
	Conduct              ResearchConfig
}

// SourceError identifies the source that could not be fetched. The wrapped
// error remains available to errors.Is/errors.As callers for audit and retry
// decisions.
type SourceError struct {
	URL string
	Err error
}

func (e *SourceError) Error() string {
	if e == nil {
		return "research: source fetch failed"
	}
	return fmt.Sprintf("research: fetch source %q: %v", e.URL, e.Err)
}

func (e *SourceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Runner implements the Researcher shape consumed by
// internal/orchestration.JudgmentWorkflow without importing that package.
// It only fetches source snapshots and conducts source-grounded research.
type Runner struct {
	reader  SourceFetcher
	gateway TextGateway
	config  RunnerConfig
}

func NewRunner(reader SourceFetcher, gateway TextGateway, config RunnerConfig) *Runner {
	if config.MaxConcurrentSources <= 0 {
		config.MaxConcurrentSources = DefaultMaxConcurrentSources
	}
	return &Runner{reader: reader, gateway: gateway, config: config}
}

// Research fetches every topic source with bounded concurrency, restores the
// caller's input order, and only then asks Conduct to produce an EvidenceBundle.
// No partial bundle is returned when a source or the gateway fails.
func (r *Runner) Research(ctx context.Context, topic domain.ResearchTopic) (EvidenceBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := topic.Validate(); err != nil {
		return EvidenceBundle{}, fmt.Errorf("research: invalid topic: %w", err)
	}
	if r == nil || r.reader == nil {
		return EvidenceBundle{}, fmt.Errorf("research: source reader is required")
	}
	if r.gateway == nil {
		return EvidenceBundle{}, fmt.Errorf("research: text gateway is required")
	}

	sources, err := r.fetchSources(ctx, topic.SourceURLs)
	if err != nil {
		return EvidenceBundle{}, err
	}
	return ConductWithConfig(ctx, r.gateway, topic.Topic, sources, r.config.Conduct)
}

func (r *Runner) fetchSources(ctx context.Context, urls []string) ([]SourceSnapshot, error) {
	limit := r.config.MaxConcurrentSources
	if limit > len(urls) {
		limit = len(urls)
	}
	if limit == 0 {
		return nil, ErrNoSources
	}

	sources := make([]SourceSnapshot, len(urls))
	errs := make([]error, len(urls))
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(limit)
	for worker := 0; worker < limit; worker++ {
		go func() {
			defer wait.Done()
			for index := range jobs {
				snapshot, err := r.reader.Fetch(ctx, urls[index])
				if err != nil {
					errs[index] = &SourceError{URL: urls[index], Err: err}
					continue
				}
				sources[index] = snapshot
			}
		}()
	}
	for index := range urls {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wait.Wait()
			for _, err := range errs {
				if err != nil {
					return nil, err
				}
			}
			return nil, &SourceError{URL: urls[index], Err: ctx.Err()}
		}
	}
	close(jobs)
	wait.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return sources, nil
}

var _ interface {
	Research(context.Context, domain.ResearchTopic) (EvidenceBundle, error)
} = (*Runner)(nil)
