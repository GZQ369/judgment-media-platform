package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
)

func TestRunnerFetchesSourcesInBoundedParallelismAndRestoresInputOrder(t *testing.T) {
	urls := []string{"https://source-1.example/a", "https://source-2.example/b", "https://source-3.example/c"}
	reader := &fakeSourceFetcher{snapshots: map[string]SourceSnapshot{
		urls[0]: NewSourceSnapshot(urls[0], "text/plain", []byte("first source"), time.Now()),
		urls[1]: NewSourceSnapshot(urls[1], "text/plain", []byte("second source"), time.Now()),
		urls[2]: NewSourceSnapshot(urls[2], "text/plain", []byte("third source"), time.Now()),
	}, delays: map[string]time.Duration{urls[0]: 35 * time.Millisecond, urls[1]: 5 * time.Millisecond, urls[2]: 15 * time.Millisecond}}
	gateway := &bundleGateway{sources: reader.snapshots, responseFor: urls[0]}
	runner := NewRunner(reader, gateway, RunnerConfig{MaxConcurrentSources: 2})

	bundle, err := runner.Research(context.Background(), domain.ResearchTopic{
		Topic:         "compare the sources",
		SourceURLs:    urls,
		DesiredAction: "explain",
	})
	if err != nil {
		t.Fatalf("Research() error = %v", err)
	}
	if got := sourceURLs(bundle.Sources); !equalStrings(got, urls) {
		t.Fatalf("source order = %v, want %v", got, urls)
	}
	if reader.maxActive > 2 {
		t.Fatalf("max active fetches = %d, want <= 2", reader.maxActive)
	}
	if !strings.Contains(gateway.userPrompt, urls[0]) || !strings.Contains(gateway.userPrompt, urls[2]) {
		t.Fatalf("gateway prompt did not include fetched sources: %s", gateway.userPrompt)
	}
}

func TestRunnerReportsTheFailedSourceURLAndDoesNotReturnPartialBundle(t *testing.T) {
	okURL := "https://ok.example/article"
	failedURL := "https://failed.example/article"
	reader := &fakeSourceFetcher{
		snapshots: map[string]SourceSnapshot{okURL: NewSourceSnapshot(okURL, "text/plain", []byte("ok"), time.Now())},
		errByURL:  map[string]error{failedURL: errors.New("upstream unavailable")},
	}
	runner := NewRunner(reader, &bundleGateway{}, RunnerConfig{MaxConcurrentSources: 2})

	_, err := runner.Research(context.Background(), domain.ResearchTopic{
		Topic:         "question",
		SourceURLs:    []string{okURL, failedURL},
		DesiredAction: "inspect",
	})
	if err == nil || !strings.Contains(err.Error(), failedURL) || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("error = %v, want audited URL and cause", err)
	}
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) || sourceErr.URL != failedURL {
		t.Fatalf("error type = %T, value = %#v", err, err)
	}
}

func TestRunnerBlocksWithoutGatewayAndAcceptsStructuredConductOutput(t *testing.T) {
	topic := domain.ResearchTopic{Topic: "question", SourceURLs: []string{"https://source.example/article"}, DesiredAction: "inspect"}
	reader := &fakeSourceFetcher{snapshots: map[string]SourceSnapshot{
		topic.SourceURLs[0]: NewSourceSnapshot(topic.SourceURLs[0], "text/plain", []byte("quoted fact"), time.Now()),
	}}
	if _, err := NewRunner(reader, nil, RunnerConfig{}).Research(context.Background(), topic); err == nil || !strings.Contains(err.Error(), "text gateway is required") {
		t.Fatalf("missing gateway error = %v", err)
	}

	snapshot := reader.snapshots[topic.SourceURLs[0]]
	gateway := &bundleGateway{sources: reader.snapshots, responseFor: snapshot.SourceURL}
	bundle, err := NewRunner(reader, gateway, RunnerConfig{}).Research(context.Background(), topic)
	if err != nil {
		t.Fatalf("structured Research() error = %v", err)
	}
	if bundle.Topic != topic.Topic || len(bundle.Sources) != 1 || len(bundle.Evidence) != 1 {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestRunnerHonorsContextCancellationWithAuditableSource(t *testing.T) {
	url := "https://slow.example/article"
	reader := &fakeSourceFetcher{waitForContext: true}
	runner := NewRunner(reader, &bundleGateway{}, RunnerConfig{MaxConcurrentSources: 1})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	reader.started = started
	result := make(chan error, 1)
	go func() {
		_, err := runner.Research(ctx, domain.ResearchTopic{Topic: "question", SourceURLs: []string{url}, DesiredAction: "inspect"})
		result <- err
	}()
	<-started
	cancel()
	err := <-result
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), url) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type fakeSourceFetcher struct {
	snapshots      map[string]SourceSnapshot
	errByURL       map[string]error
	delays         map[string]time.Duration
	waitForContext bool
	started        chan struct{}
	mu             sync.Mutex
	active         int
	maxActive      int
}

func (f *fakeSourceFetcher) Fetch(ctx context.Context, rawURL string) (SourceSnapshot, error) {
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.waitForContext {
		<-ctx.Done()
		return SourceSnapshot{}, ctx.Err()
	}
	if delay := f.delays[rawURL]; delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return SourceSnapshot{}, ctx.Err()
		}
	}
	if err := f.errByURL[rawURL]; err != nil {
		return SourceSnapshot{}, err
	}
	if snapshot, ok := f.snapshots[rawURL]; ok {
		return snapshot, nil
	}
	return SourceSnapshot{}, fmt.Errorf("unknown source")
}

type bundleGateway struct {
	sources     map[string]SourceSnapshot
	responseFor string
	userPrompt  string
}

func (g *bundleGateway) Complete(_ context.Context, _, userPrompt string) (string, error) {
	g.userPrompt = userPrompt
	snapshot, ok := g.sources[g.responseFor]
	if !ok {
		return "", fmt.Errorf("missing response source")
	}
	result := EvidenceBundle{Evidence: []Evidence{{
		Statement: "The source contains the quoted fact.",
		Citations: []Citation{{SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: string(snapshot.Original)}},
	}}}
	encoded, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func sourceURLs(sources []SourceSnapshot) []string {
	urls := make([]string, 0, len(sources))
	for _, source := range sources {
		urls = append(urls, source.SourceURL)
	}
	return urls
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
