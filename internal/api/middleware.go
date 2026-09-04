package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

const requestIDHeader = "X-Request-ID"

type requestIDContextKey struct{}

// RequestID returns the request ID stored in ctx, if any.
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get(requestIDHeader)
		if !validRequestID(requestID) {
			requestID = newRequestID()
		}

		w.Header().Set(requestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		// crypto/rand failure means the process cannot satisfy the ID contract.
		// Keep the failure explicit instead of emitting a predictable identifier.
		panic(fmt.Errorf("generate request id: %w", err))
	}
	return hex.EncodeToString(bytes[:])
}

func accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := newResponseRecorder(w)
		started := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				status := recorder.statusForLog(true)
				slog.Default().Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", status,
					"duration", time.Since(started),
					"request_id", RequestID(r.Context()),
				)
				panic(recovered)
			}

			status := recorder.statusForLog(false)
			slog.Default().Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", status,
				"duration", time.Since(started),
				"request_id", RequestID(r.Context()),
			)
		}()

		next.ServeHTTP(recorder, r)
	})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := newResponseRecorder(w)
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Default().Error("panic recovered",
					"request_id", RequestID(r.Context()),
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				if !recorder.headerWritten() {
					writeError(recorder, http.StatusInternalServerError, "internal server error")
				}
			}
		}()

		next.ServeHTTP(recorder, r)
	})
}

type responseRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bytes       int
}

type recorderState interface {
	http.ResponseWriter
	Unwrap() http.ResponseWriter
	statusForLog(bool) int
	headerWritten() bool
}

const (
	recorderFlusher uint8 = 1 << iota
	recorderHijacker
	recorderPusher
	recorderReaderFrom
)

func newResponseRecorder(w http.ResponseWriter) recorderState {
	recorder := &responseRecorder{ResponseWriter: w}
	var capabilities uint8
	if _, ok := w.(http.Flusher); ok {
		capabilities |= recorderFlusher
	}
	if _, ok := w.(http.Hijacker); ok {
		capabilities |= recorderHijacker
	}
	if _, ok := w.(http.Pusher); ok {
		capabilities |= recorderPusher
	}
	if _, ok := w.(io.ReaderFrom); ok {
		capabilities |= recorderReaderFrom
	}

	switch capabilities {
	case recorderFlusher:
		return &flusherRecorder{recorder}
	case recorderHijacker:
		return &hijackerRecorder{recorder}
	case recorderPusher:
		return &pusherRecorder{recorder}
	case recorderReaderFrom:
		return &readerFromRecorder{recorder}
	case recorderFlusher | recorderHijacker:
		return &flusherHijackerRecorder{recorder}
	case recorderFlusher | recorderPusher:
		return &flusherPusherRecorder{recorder}
	case recorderFlusher | recorderReaderFrom:
		return &flusherReaderFromRecorder{recorder}
	case recorderHijacker | recorderPusher:
		return &hijackerPusherRecorder{recorder}
	case recorderHijacker | recorderReaderFrom:
		return &hijackerReaderFromRecorder{recorder}
	case recorderPusher | recorderReaderFrom:
		return &pusherReaderFromRecorder{recorder}
	case recorderFlusher | recorderHijacker | recorderPusher:
		return &flusherHijackerPusherRecorder{recorder}
	case recorderFlusher | recorderHijacker | recorderReaderFrom:
		return &flusherHijackerReaderFromRecorder{recorder}
	case recorderFlusher | recorderPusher | recorderReaderFrom:
		return &flusherPusherReaderFromRecorder{recorder}
	case recorderHijacker | recorderPusher | recorderReaderFrom:
		return &hijackerPusherReaderFromRecorder{recorder}
	case recorderFlusher | recorderHijacker | recorderPusher | recorderReaderFrom:
		return &flusherHijackerPusherReaderFromRecorder{recorder}
	default:
		return recorder
	}
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *responseRecorder) statusForLog(panicked bool) int {
	if r.status != 0 {
		return r.status
	}
	if panicked {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func (r *responseRecorder) headerWritten() bool {
	return r.wroteHeader
}

func (r *responseRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(body)
	r.bytes += n
	return n, err
}

func (r *responseRecorder) flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	r.ResponseWriter.(http.Flusher).Flush()
}

func (r *responseRecorder) hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := r.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return conn, rw, err
}

func (r *responseRecorder) push(target string, opts *http.PushOptions) error {
	return r.ResponseWriter.(http.Pusher).Push(target, opts)
}

func (r *responseRecorder) readFrom(src io.Reader) (int64, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.(io.ReaderFrom).ReadFrom(src)
	r.bytes += int(n)
	return n, err
}

type flusherRecorder struct{ *responseRecorder }

func (r *flusherRecorder) Flush() { r.flush() }

type hijackerRecorder struct{ *responseRecorder }

func (r *hijackerRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) { return r.hijack() }

type pusherRecorder struct{ *responseRecorder }

func (r *pusherRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}

type readerFromRecorder struct{ *responseRecorder }

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) { return r.readFrom(src) }

type flusherHijackerRecorder struct{ *responseRecorder }

func (r *flusherHijackerRecorder) Flush() { r.flush() }
func (r *flusherHijackerRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}

type flusherPusherRecorder struct{ *responseRecorder }

func (r *flusherPusherRecorder) Flush() { r.flush() }
func (r *flusherPusherRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}

type flusherReaderFromRecorder struct{ *responseRecorder }

func (r *flusherReaderFromRecorder) Flush() { r.flush() }
func (r *flusherReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type hijackerPusherRecorder struct{ *responseRecorder }

func (r *hijackerPusherRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *hijackerPusherRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}

type hijackerReaderFromRecorder struct{ *responseRecorder }

func (r *hijackerReaderFromRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *hijackerReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type pusherReaderFromRecorder struct{ *responseRecorder }

func (r *pusherReaderFromRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}
func (r *pusherReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type flusherHijackerPusherRecorder struct{ *responseRecorder }

func (r *flusherHijackerPusherRecorder) Flush() { r.flush() }
func (r *flusherHijackerPusherRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *flusherHijackerPusherRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}

type flusherHijackerReaderFromRecorder struct{ *responseRecorder }

func (r *flusherHijackerReaderFromRecorder) Flush() { r.flush() }
func (r *flusherHijackerReaderFromRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *flusherHijackerReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type flusherPusherReaderFromRecorder struct{ *responseRecorder }

func (r *flusherPusherReaderFromRecorder) Flush() { r.flush() }
func (r *flusherPusherReaderFromRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}
func (r *flusherPusherReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type hijackerPusherReaderFromRecorder struct{ *responseRecorder }

func (r *hijackerPusherReaderFromRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *hijackerPusherReaderFromRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}
func (r *hijackerPusherReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}

type flusherHijackerPusherReaderFromRecorder struct{ *responseRecorder }

func (r *flusherHijackerPusherReaderFromRecorder) Flush() { r.flush() }
func (r *flusherHijackerPusherReaderFromRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.hijack()
}
func (r *flusherHijackerPusherReaderFromRecorder) Push(target string, opts *http.PushOptions) error {
	return r.push(target, opts)
}
func (r *flusherHijackerPusherReaderFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	return r.readFrom(src)
}
