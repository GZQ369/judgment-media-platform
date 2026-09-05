package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/GZQ369/judgment-media-platform/internal/api"
	"github.com/GZQ369/judgment-media-platform/internal/config"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/observability"
	"github.com/GZQ369/judgment-media-platform/internal/store"
)

func main() {
	os.Exit(run())
}

func run() int {
	bootstrapLogger, err := observability.NewJSONLogger("info", os.Stderr)
	if err != nil {
		return 1
	}
	slog.SetDefault(bootstrapLogger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration load failed", "error", err)
		return 1
	}
	logger, err := observability.NewJSONLogger(cfg.LogLevel, os.Stdout)
	if err != nil {
		slog.Error("logger initialization failed", "error", err)
		return 1
	}
	slog.SetDefault(logger)

	s := store.NewMemoryStore()
	e := engine.NewEngineWithDataDir(s, cfg.DataDir)
	h := api.NewHandler(e)
	srv := newServer(cfg, h.Routes())

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("server starting", "addr", cfg.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", "error", err)
			return 1
		}
	case sig := <-signals:
		logger.Info("shutdown signal received", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown failed", "error", err)
			return 1
		}
		logger.Info("server stopped")
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped with error", "error", err)
			return 1
		}
	}
	return 0
}

func newServer(cfg config.Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
}
