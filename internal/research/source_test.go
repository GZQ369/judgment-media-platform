package research

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type rewriteTransport struct {
	transport http.RoundTripper
	target    string
}

func (t rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	target, err := cloned.URL.Parse(t.target + cloned.URL.Path)
	if err != nil {
		return nil, err
	}
	cloned.URL = target
	cloned.Host = target.Host
	return t.transport.RoundTrip(cloned)
}

func TestFetchFeedRetainsSourceAndParsesRSS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(`<rss><channel><item><title>Release</title><link>https://example.test/release</link><description>New text</description></item></channel></rss>`))
	}))
	defer server.Close()
	serverClient := server.Client()
	reader := NewSourceReader(FetchConfig{
		Client:   &http.Client{Transport: rewriteTransport{transport: serverClient.Transport, target: server.URL}},
		MaxBytes: 1024,
	})
	snapshot, entries, err := reader.FetchFeed(context.Background(), "http://1.1.1.1/feed.xml")
	if err != nil {
		t.Fatalf("FetchFeed() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "Release" || entries[0].Summary != "New text" {
		t.Fatalf("unexpected entries: %#v", entries)
	}
	if snapshot.OriginalSize == 0 || snapshot.ContentHash == "" || !strings.Contains(string(snapshot.Original), "Release") {
		t.Fatalf("snapshot did not retain original response: %#v", snapshot)
	}
	if !strings.Contains(snapshot.Text, "New text") {
		t.Fatalf("snapshot text = %q", snapshot.Text)
	}
}

func TestSourceFetchRejectsSSRFAndUnsafeRedirect(t *testing.T) {
	reader := NewSourceReader(FetchConfig{Client: http.DefaultClient})
	for _, rawURL := range []string{
		"http://127.0.0.1/feed",
		"http://10.0.0.1/feed",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := reader.Fetch(context.Background(), rawURL); err == nil {
			t.Errorf("Fetch(%q) unexpectedly succeeded", rawURL)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "http://127.0.0.1/private", http.StatusFound)
	}))
	defer server.Close()
	serverClient := server.Client()
	reader = NewSourceReader(FetchConfig{Client: &http.Client{
		Transport: rewriteTransport{transport: serverClient.Transport, target: server.URL},
	}})
	if _, err := reader.Fetch(context.Background(), "http://1.1.1.1/redirect"); err == nil || !strings.Contains(err.Error(), "unsafe redirect") {
		t.Fatalf("unsafe redirect error = %v", err)
	}
}

func TestSourceFetchCancellationAndSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			_, _ = writer.Write([]byte("late"))
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	serverClient := server.Client()
	reader := NewSourceReader(FetchConfig{
		Client:  &http.Client{Transport: rewriteTransport{transport: serverClient.Transport, target: server.URL}},
		Timeout: 5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Fetch(ctx, "http://1.1.1.1/slow"); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	largeServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("0123456789"))
	}))
	defer largeServer.Close()
	largeClient := largeServer.Client()
	reader = NewSourceReader(FetchConfig{
		Client:   &http.Client{Transport: rewriteTransport{transport: largeClient.Transport, target: largeServer.URL}},
		MaxBytes: 5,
	})
	if _, err := reader.Fetch(context.Background(), "http://1.1.1.1/large"); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("size error = %v", err)
	}
}

func TestExtractTextIsConservativeAcrossFormats(t *testing.T) {
	if got := ExtractText("text/html", []byte(`<html><script>ignore()</script><p>Hello &amp; world</p></html>`)); got != "Hello & world" {
		t.Fatalf("HTML text = %q", got)
	}
	if got := ExtractText("application/json", []byte(`{"title":"Hello","ignored":42,"description":"World"}`)); got != "Hello\nWorld" {
		t.Fatalf("JSON text = %q", got)
	}
}

func TestExtractTextJSONIsDeterministic(t *testing.T) {
	const body = `{"description":"World","body":"Body","title":"Hello","ignored":{"text":"Nested"}}`
	const want = "Hello\nWorld\nBody\nNested"
	for attempt := 0; attempt < 100; attempt++ {
		if got := ExtractText("application/json", []byte(body)); got != want {
			t.Fatalf("attempt %d: JSON text = %q, want %q", attempt, got, want)
		}
	}
}
