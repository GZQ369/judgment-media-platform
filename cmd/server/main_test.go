package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/config"
)

func TestNewServerConfiguresTimeouts(t *testing.T) {
	cfg := config.Config{
		Addr:              "127.0.0.1:8088",
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       4 * time.Second,
	}
	server := newServer(cfg, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	if server.Addr != cfg.Addr || server.ReadHeaderTimeout != cfg.ReadHeaderTimeout ||
		server.ReadTimeout != cfg.ReadTimeout || server.WriteTimeout != cfg.WriteTimeout ||
		server.IdleTimeout != cfg.IdleTimeout || server.MaxHeaderBytes <= 0 {
		t.Fatalf("server configuration mismatch: %#v", server)
	}
}

func TestServerShutdown(t *testing.T) {
	server := newServer(config.Config{Addr: "127.0.0.1:0"}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen returned error: %v", err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned error = %v, want ErrServerClosed", err)
	}
}
