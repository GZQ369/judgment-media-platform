package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vertical-intel-video-agent/internal/engine"
	"vertical-intel-video-agent/internal/store"
)

func TestRequestIDMiddlewarePropagatesValidIDAndReplacesInvalidID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		preserve bool
	}{
		{name: "valid", input: "caller-request-id", preserve: true},
		{name: "empty", input: ""},
		{name: "control character", input: "request\n-id"},
		{name: "non ascii", input: "请求-id"},
		{name: "too long", input: strings.Repeat("a", 129)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			req.Header.Set(requestIDHeader, tt.input)
			resp := httptest.NewRecorder()
			var contextID string

			handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contextID = RequestID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))
			handler.ServeHTTP(resp, req)

			requestID := resp.Header().Get(requestIDHeader)
			if requestID == "" || requestID != contextID {
				t.Fatalf("request ID header=%q context=%q", requestID, contextID)
			}
			if tt.preserve && requestID != tt.input {
				t.Fatalf("valid request ID was not preserved: got %q want %q", requestID, tt.input)
			}
			if !tt.preserve && requestID == tt.input {
				t.Fatalf("invalid request ID was not replaced: %q", requestID)
			}
			if !validRequestID(requestID) {
				t.Fatalf("generated request ID is invalid: %q", requestID)
			}
		})
	}
}

func TestRequestIDMiddlewareGeneratesDifferentIDs(t *testing.T) {
	handler := requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	second := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/health", nil))
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/health", nil))

	if first.Header().Get(requestIDHeader) == second.Header().Get(requestIDHeader) {
		t.Fatal("generated request IDs must be unique across requests")
	}
}

func TestAccessLogIncludesBasicFields(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(previous)

	handler := requestIDMiddleware(accessLogMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test?ignored=true", nil)
	req.Header.Set(requestIDHeader, "log-test-id")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	logLine := logs.String()
	for _, field := range []string{"method=POST", "path=/api/v1/test", "status=202", "request_id=log-test-id", "duration="} {
		if !strings.Contains(logLine, field) {
			t.Fatalf("access log missing %q: %s", field, logLine)
		}
	}
}

func TestRecoveryReturnsInternalServerErrorJSONAndLogsStack(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)

	handler := requestIDMiddleware(recoveryMiddleware(accessLogMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("test panic")
	}))))
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	req.Header.Set(requestIDHeader, "panic-test-id")
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] != "internal server error" {
		t.Fatalf("error body=%v", body)
	}
	logLine := logs.String()
	for _, field := range []string{"panic recovered", "request_id=panic-test-id", "stack=", "http request", "status=500"} {
		if !strings.Contains(logLine, field) {
			t.Fatalf("panic logs missing %q: %s", field, logLine)
		}
	}
}

func TestRecoveryDoesNotOverwriteAlreadySentHeaders(t *testing.T) {
	handler := recoveryMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		panic("after headers")
	}))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/panic-after-header", nil))

	if resp.Code != http.StatusCreated {
		t.Fatalf("status=%d, recovery overwrote an already sent header", resp.Code)
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("unexpected response body=%q", resp.Body.String())
	}
}

func TestHealthCheckKeepsJSONAndMiddlewareHeaders(t *testing.T) {
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	resp := httptest.NewRecorder()
	NewHandler(e).Routes().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/health", nil))

	if resp.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	if resp.Header().Get(requestIDHeader) == "" {
		t.Fatal("health response is missing request ID")
	}
	if resp.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("health response lost CORS header: %q", resp.Header().Get("Access-Control-Allow-Origin"))
	}
	if resp.Header().Get("Access-Control-Allow-Headers") != "Content-Type, X-Request-ID" {
		t.Fatalf("health response has unexpected allowed headers: %q", resp.Header().Get("Access-Control-Allow-Headers"))
	}
	if resp.Header().Get("Access-Control-Allow-Methods") != "GET,POST,OPTIONS" {
		t.Fatalf("health response has unexpected allowed methods: %q", resp.Header().Get("Access-Control-Allow-Methods"))
	}
	if resp.Header().Get("Access-Control-Expose-Headers") != requestIDHeader {
		t.Fatalf("health response does not expose request ID: %q", resp.Header().Get("Access-Control-Expose-Headers"))
	}
	var body struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if body.Status != "ok" || body.Service != "agentic-video-factory" {
		t.Fatalf("health body=%+v", body)
	}
}

func TestRoutesHandleCORSPreflightForRequestID(t *testing.T) {
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/projects", nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-request-id")
	resp := httptest.NewRecorder()

	NewHandler(e).Routes().ServeHTTP(resp, req)

	if resp.Code != http.StatusNoContent {
		t.Fatalf("preflight status=%d body=%q", resp.Code, resp.Body.String())
	}
	if resp.Body.Len() != 0 {
		t.Fatalf("preflight body=%q, want empty", resp.Body.String())
	}
	if resp.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight origin=%q, want *", resp.Header().Get("Access-Control-Allow-Origin"))
	}
	if resp.Header().Get("Access-Control-Allow-Headers") != "Content-Type, X-Request-ID" {
		t.Fatalf("preflight allowed headers=%q", resp.Header().Get("Access-Control-Allow-Headers"))
	}
	if resp.Header().Get("Access-Control-Allow-Methods") != "GET,POST,OPTIONS" {
		t.Fatalf("preflight allowed methods=%q", resp.Header().Get("Access-Control-Allow-Methods"))
	}
	if resp.Header().Get("Access-Control-Expose-Headers") != requestIDHeader {
		t.Fatalf("preflight exposed headers=%q", resp.Header().Get("Access-Control-Expose-Headers"))
	}
}

func TestErrorJSONCompatibility(t *testing.T) {
	e := engine.NewEngineWithDataDir(store.NewMemoryStore(), t.TempDir())
	resp := httptest.NewRecorder()
	NewHandler(e).Routes().ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/health", nil))

	if resp.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d body=%s", resp.Code, resp.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["error"] != "method not allowed" {
		t.Fatalf("error body=%v", body)
	}
}

func TestRequestIDContextNilIsEmpty(t *testing.T) {
	if got := RequestID(context.Background()); got != "" {
		t.Fatalf("request ID for context without ID=%q", got)
	}
}

func TestResponseRecorderDoesNotExposeUnsupportedOptionalInterfaces(t *testing.T) {
	tests := []struct {
		name       string
		writer     http.ResponseWriter
		flusher    bool
		hijacker   bool
		pusher     bool
		readerFrom bool
	}{
		{name: "none", writer: newMinimalResponseWriter()},
		{name: "flusher only", writer: &flusherOnlyResponseWriter{newMinimalResponseWriter()}, flusher: true},
		{name: "hijacker only", writer: &hijackerOnlyResponseWriter{newMinimalResponseWriter()}, hijacker: true},
		{name: "pusher only", writer: &pusherOnlyResponseWriter{newMinimalResponseWriter()}, pusher: true},
		{name: "reader from only", writer: &readerFromOnlyResponseWriter{newMinimalResponseWriter()}, readerFrom: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := newResponseRecorder(tt.writer)
			assertRecorderCapabilities(t, recorder, tt.flusher, tt.hijacker, tt.pusher, tt.readerFrom)
			if got := recorder.Unwrap(); got != tt.writer {
				t.Fatalf("Unwrap()=%T %p, want original writer %T %p", got, got, tt.writer, tt.writer)
			}
		})
	}
}

func TestResponseRecorderForwardsSupportedOptionalInterfaces(t *testing.T) {
	underlying := newOptionalResponseWriter()
	recorder := newResponseRecorder(underlying)
	assertRecorderCapabilities(t, recorder, true, true, true, true)

	if err := http.NewResponseController(recorder).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush() error: %v", err)
	}
	if !underlying.flushed {
		t.Fatal("ResponseController.Flush() did not reach the underlying flusher")
	}
	if underlying.status != http.StatusOK {
		t.Fatalf("Flush() status=%d, want %d", underlying.status, http.StatusOK)
	}

	pusher := recorder.(http.Pusher)
	if err := pusher.Push("/asset.js", nil); err != nil {
		t.Fatalf("Push() error: %v", err)
	}
	if underlying.pushed != "/asset.js" {
		t.Fatalf("Push() target=%q, want %q", underlying.pushed, "/asset.js")
	}

	count, err := io.Copy(recorder, io.LimitReader(strings.NewReader("reader-from body"), int64(len("reader-from body"))))
	if err != nil {
		t.Fatalf("io.Copy() error: %v", err)
	}
	if count != int64(len("reader-from body")) {
		t.Fatalf("io.Copy() count=%d, want %d", count, len("reader-from body"))
	}
	if !underlying.readFromCalled {
		t.Fatal("io.Copy() did not use the underlying ReaderFrom")
	}
	if got := underlying.body.String(); got != "reader-from body" {
		t.Fatalf("underlying body=%q, want %q", got, "reader-from body")
	}

	conn, _, err := recorder.(http.Hijacker).Hijack()
	if err != nil {
		t.Fatalf("Hijack() error: %v", err)
	}
	if conn == nil {
		t.Fatal("Hijack() returned a nil connection")
	}
	_ = conn.Close()
}

func TestResponseControllerReturnsNotSupportedThroughUnwrap(t *testing.T) {
	recorder := newResponseRecorder(newMinimalResponseWriter())
	controller := http.NewResponseController(recorder)

	if err := controller.Flush(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("ResponseController.Flush() error=%v, want %v", err, http.ErrNotSupported)
	}
	if _, _, err := controller.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("ResponseController.Hijack() error=%v, want %v", err, http.ErrNotSupported)
	}
}

func assertRecorderCapabilities(t *testing.T, recorder http.ResponseWriter, wantFlusher, wantHijacker, wantPusher, wantReaderFrom bool) {
	t.Helper()
	if _, ok := recorder.(http.Flusher); ok != wantFlusher {
		t.Fatalf("http.Flusher exposed=%v, want %v", ok, wantFlusher)
	}
	if _, ok := recorder.(http.Hijacker); ok != wantHijacker {
		t.Fatalf("http.Hijacker exposed=%v, want %v", ok, wantHijacker)
	}
	if _, ok := recorder.(http.Pusher); ok != wantPusher {
		t.Fatalf("http.Pusher exposed=%v, want %v", ok, wantPusher)
	}
	if _, ok := recorder.(io.ReaderFrom); ok != wantReaderFrom {
		t.Fatalf("io.ReaderFrom exposed=%v, want %v", ok, wantReaderFrom)
	}
}

type minimalResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newMinimalResponseWriter() *minimalResponseWriter {
	return &minimalResponseWriter{header: make(http.Header)}
}

func (w *minimalResponseWriter) Header() http.Header { return w.header }

func (w *minimalResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *minimalResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(body)
}

type flusherOnlyResponseWriter struct{ *minimalResponseWriter }

func (w *flusherOnlyResponseWriter) Flush() {}

type hijackerOnlyResponseWriter struct{ *minimalResponseWriter }

func (w *hijackerOnlyResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

type pusherOnlyResponseWriter struct{ *minimalResponseWriter }

func (w *pusherOnlyResponseWriter) Push(string, *http.PushOptions) error { return nil }

type readerFromOnlyResponseWriter struct{ *minimalResponseWriter }

func (w *readerFromOnlyResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	n, err := io.Copy(&w.body, src)
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return n, err
}

type optionalResponseWriter struct {
	*minimalResponseWriter
	flushed        bool
	pushed         string
	readFromCalled bool
}

func newOptionalResponseWriter() *optionalResponseWriter {
	return &optionalResponseWriter{minimalResponseWriter: newMinimalResponseWriter()}
}

func (w *optionalResponseWriter) Flush() { w.flushed = true }

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	server, client := net.Pipe()
	_ = client.Close()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func (w *optionalResponseWriter) Push(target string, _ *http.PushOptions) error {
	w.pushed = target
	return nil
}

func (w *optionalResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFromCalled = true
	return io.Copy(&w.body, src)
}
