package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/GZQ369/judgment-media-platform/internal/analytics"
	"github.com/GZQ369/judgment-media-platform/internal/api"
	"github.com/GZQ369/judgment-media-platform/internal/config"
	"github.com/GZQ369/judgment-media-platform/internal/contentatom"
	"github.com/GZQ369/judgment-media-platform/internal/distribution"
	"github.com/GZQ369/judgment-media-platform/internal/egopublisher"
	"github.com/GZQ369/judgment-media-platform/internal/engine"
	"github.com/GZQ369/judgment-media-platform/internal/judgment"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
	"github.com/GZQ369/judgment-media-platform/internal/observability"
	"github.com/GZQ369/judgment-media-platform/internal/orchestration"
	"github.com/GZQ369/judgment-media-platform/internal/pipeline"
	"github.com/GZQ369/judgment-media-platform/internal/platforminsights"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
	"github.com/GZQ369/judgment-media-platform/internal/publisher"
	"github.com/GZQ369/judgment-media-platform/internal/research"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
	"github.com/GZQ369/judgment-media-platform/internal/store"
	_ "modernc.org/sqlite"
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
	runtime, err := openRuntime(cfg)
	if err != nil {
		logger.Error("service startup failed", "error", err)
		return 1
	}
	h := api.NewHandlerWithServicesAndSecurityAndAnalytics(e, runtime.distribution, runtime.pipeline, runtime.publication, runtime.analytics, api.SecurityOptions{
		PublicationAPIToken: cfg.PublicationAPIToken,
		AllowedOrigins:      cfg.AllowedOrigins,
	})
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
			shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if closeErr := runtime.close(shutdownCtx); closeErr != nil {
				logger.Error("service shutdown failed", "error", closeErr)
			}
			return 1
		}
	case sig := <-signals:
		logger.Info("shutdown signal received", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		shutdownErr := srv.Shutdown(shutdownCtx)
		serviceShutdownErr := runtime.close(shutdownCtx)
		if shutdownErr != nil || serviceShutdownErr != nil {
			logger.Error("server shutdown failed", "error", errors.Join(shutdownErr, serviceShutdownErr))
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

type serviceRuntime struct {
	pipeline      *pipeline.Service
	distribution  *distribution.Service
	pipelineStore *pipeline.Store
	runLedger     *runstore.Store
	publication   *publication.Service
	publicationDB *sql.DB
	analytics     *analytics.Service
	analyticsDB   *analytics.Store
	egoAdapters   []*egopublisher.Adapter
}

func openRuntime(cfg config.Config) (*serviceRuntime, error) {
	for _, dir := range []string{
		filepath.Join(cfg.DataDir, "runs"),
		filepath.Join(cfg.DataDir, "pipelines"),
		filepath.Join(cfg.DataDir, "publications"),
		filepath.Join(cfg.DataDir, "analytics"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("service data directory creation failed: %w", err)
		}
	}

	ledger, err := runstore.Open(filepath.Join(cfg.DataDir, "runs", "runs.sqlite"))
	if err != nil {
		return nil, fmt.Errorf("run ledger open failed: %w", err)
	}
	pipelineStore, err := pipeline.Open(filepath.Join(cfg.DataDir, "pipelines", "pipelines.sqlite"))
	if err != nil {
		_ = ledger.Close()
		return nil, fmt.Errorf("pipeline store open failed: %w", err)
	}
	runtime := &serviceRuntime{pipelineStore: pipelineStore, runLedger: ledger}
	publicationDB, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "publications", "publications.sqlite"))
	if err != nil {
		_ = runtime.close(context.Background())
		return nil, fmt.Errorf("publication database open failed: %w", err)
	}
	publicationDB.SetMaxOpenConns(1)
	publicationDB.SetMaxIdleConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA busy_timeout=5000"} {
		if _, err := publicationDB.Exec(pragma); err != nil {
			_ = publicationDB.Close()
			_ = runtime.close(context.Background())
			return nil, fmt.Errorf("publication database configure failed: %w", err)
		}
	}
	runtime.publicationDB = publicationDB
	publicationService, egoAdapters, err := newPublicationService(cfg, ledger, publicationDB)
	if err != nil {
		_ = runtime.close(context.Background())
		return nil, err
	}
	runtime.publication = publicationService
	runtime.egoAdapters = egoAdapters
	analyticsPath := cfg.AnalyticsDBPath
	if analyticsPath == "" {
		analyticsPath = filepath.Join(cfg.DataDir, "analytics", "analytics.sqlite")
	} else if dir := filepath.Dir(analyticsPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = runtime.close(context.Background())
			return nil, fmt.Errorf("analytics data directory creation failed: %w", err)
		}
	}
	analyticsStore, err := analytics.OpenStore(analyticsPath)
	if err != nil {
		_ = runtime.close(context.Background())
		return nil, fmt.Errorf("analytics store open failed: %w", err)
	}
	runtime.analyticsDB = analyticsStore
	runtime.analytics = analytics.NewService(analyticsStore, platforminsights.NewPublicationServiceGateway(publicationService),
		platforminsights.NewDouyinMetricsProvider(platforminsights.DouyinMetricsConfig{
			AccessToken: cfg.DouyinAccessToken, OpenID: cfg.DouyinOpenID, Scope: cfg.DouyinMetricsScope, APIBaseURL: cfg.DouyinAPIBaseURL,
		}),
		platforminsights.NewUnavailableProvider("xiaohongshu", "xiaohongshu", "no approved read-only metrics API is configured"),
		platforminsights.NewUnavailableProvider("bilibili", "bilibili", "no approved read-only metrics API is configured"),
	)

	var worker mediaworker.Worker
	if cfg.MediaWorkerPython != "" && cfg.MediaWorkerScript != "" {
		worker = mediaworker.CommandWorker{
			PythonPath: cfg.MediaWorkerPython,
			ScriptPath: cfg.MediaWorkerScript,
			DataRoot:   cfg.DataDir,
			Timeout:    cfg.MediaWorkerTimeout,
		}
	}
	runtime.distribution = distribution.NewServiceWithOptions(ledger, cfg.DataDir, distribution.Options{
		MediaWorker: worker,
		MediaPaths: distribution.MediaRuntimePaths{
			Python: cfg.MediaWorkerPython,
			FFmpeg: cfg.MediaWorkerFFmpeg, FFprobe: cfg.MediaWorkerFFprobe,
			Remotion: cfg.MediaWorkerRemotion, RemotionCLI: cfg.MediaWorkerRemotionCLI, RemotionCWD: cfg.MediaWorkerRemotionCWD,
			RemotionEntry: cfg.MediaWorkerRemotionEntry, RemotionComposition: cfg.MediaWorkerRemotionComposition,
			EdgeTTSRoot: cfg.MediaWorkerEdgeTTSRoot,
		},
	})
	runner, err := newJudgmentRunner(cfg)
	if err != nil {
		_ = runtime.close(context.Background())
		return nil, err
	}
	runtime.pipeline = pipeline.NewService(pipelineStore, runner, runtime.distribution)
	if err := runtime.distribution.Start(); err != nil {
		_ = runtime.close(context.Background())
		return nil, fmt.Errorf("run worker start failed: %w", err)
	}
	if err := runtime.pipeline.Start(); err != nil {
		_ = runtime.close(context.Background())
		return nil, fmt.Errorf("pipeline worker start failed: %w", err)
	}
	return runtime, nil
}

func newJudgmentRunner(cfg config.Config) (pipeline.JudgmentRunner, error) {
	if cfg.AIGatewayBaseURL == "" && cfg.AIGatewayModel == "" {
		return nil, nil
	}
	client, err := research.NewOpenAICompatibleClient(research.GatewayConfig{
		BaseURL:    cfg.AIGatewayBaseURL,
		Model:      cfg.AIGatewayModel,
		APIKey:     cfg.AIGatewayAPIKey,
		AllowLocal: cfg.AIGatewayAllowLocal,
		Timeout:    cfg.AIGatewayTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("AI gateway initialization failed: %w", err)
	}
	reader := research.NewSourceReader(research.FetchConfig{})
	researcher := research.NewRunner(reader, client, research.RunnerConfig{})
	judge := judgment.NewService(client)
	compiler := contentatom.NewCompiler(contentatom.Options{})
	return orchestration.NewJudgmentWorkflow(researcher, judge, compiler, nil), nil
}

func newPublicationService(cfg config.Config, runs *runstore.Store, db *sql.DB) (*publication.Service, []*egopublisher.Adapter, error) {
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve publication data directory: %w", err)
	}
	egoDataDir := filepath.Join(dataDir, "ego-packages")
	egoStateRoot := cfg.EgoStateRoot
	if egoStateRoot == "" {
		egoStateRoot = filepath.Join(dataDir, "ego-state")
	} else {
		egoStateRoot, err = filepath.Abs(egoStateRoot)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve Ego state directory: %w", err)
		}
	}
	for _, dir := range []string{egoDataDir, egoStateRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create Ego directory: %w", err)
		}
	}
	egoConfig := egopublisher.Config{
		NodePath:                cfg.EgoNodePath,
		PublisherScript:         cfg.EgoPublisherScript,
		EgoCommand:              cfg.EgoCommand,
		StateRoot:               egoStateRoot,
		DataDir:                 egoDataDir,
		Timeout:                 cfg.EgoTimeout,
		OriginalRightsConfirmed: cfg.EgoOriginalRightsConfirmed,
	}
	platforms := []publication.Platform{publication.PlatformDouyin, publication.PlatformXiaohongshu, publication.PlatformBilibili}
	egoAdapters := make([]*egopublisher.Adapter, 0, len(platforms))
	byPlatform := make(map[publication.Platform]publication.Adapter, len(platforms))
	for _, platform := range platforms {
		adapter, err := egopublisher.New(egoConfig, platform)
		if err != nil {
			for _, opened := range egoAdapters {
				_ = opened.Close()
			}
			return nil, nil, fmt.Errorf("Ego adapter %s: %w", platform, err)
		}
		egoAdapters = append(egoAdapters, adapter)
		byPlatform[platform] = adapter
	}

	// Prefer the configured official Douyin adapter. XHS's official publisher
	// only creates a client handoff, which cannot fulfill a one-use commit.
	official, err := publication.NewPublisherAdapters(
		publisher.NewDouyinPublisher(publisher.DouyinConfig{AccessToken: cfg.DouyinAccessToken, OpenID: cfg.DouyinOpenID, APIBaseURL: cfg.DouyinAPIBaseURL}),
		publisher.NewXiaohongshuPublisher(cfg.XHSShareAppKey),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("official publisher adapter: %w", err)
	}
	for _, adapter := range official {
		platform := adapter.Platform()
		byPlatform[platform] = selectPublicationAdapter(platform, byPlatform[platform], adapter)
	}
	adapters := make([]publication.Adapter, 0, len(platforms))
	for _, platform := range platforms {
		adapters = append(adapters, byPlatform[platform])
	}
	orchestrator, err := publication.New(db, adapters...)
	if err != nil {
		for _, adapter := range egoAdapters {
			_ = adapter.Close()
		}
		return nil, nil, fmt.Errorf("publication orchestrator: %w", err)
	}
	service, err := publication.NewService(runs, orchestrator, publication.CoordinationOptions{})
	if err != nil {
		for _, adapter := range egoAdapters {
			_ = adapter.Close()
		}
		return nil, nil, fmt.Errorf("publication service: %w", err)
	}
	return service, egoAdapters, nil
}

// XHS's official adapter only creates a client handoff. Keep Ego selected even
// when unconfigured so publication blocks before consuming authorization.
func selectPublicationAdapter(platform publication.Platform, ego, official publication.Adapter) publication.Adapter {
	if platform == publication.PlatformXiaohongshu {
		return ego
	}
	if official == nil {
		return ego
	}
	if !official.Capability(context.Background()).Configured {
		return ego
	}
	return official
}

func (r *serviceRuntime) close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var shutdownErrs []error
	if r.pipeline != nil {
		if err := r.pipeline.Stop(ctx); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("pipeline shutdown: %w", err))
		}
	}
	if r.distribution != nil {
		if err := r.distribution.Stop(ctx); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("distribution shutdown: %w", err))
		}
	}
	if r.analyticsDB != nil {
		if err := r.analyticsDB.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("analytics database close: %w", err))
		}
	}
	for _, adapter := range r.egoAdapters {
		if err := adapter.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("Ego adapter close: %w", err))
		}
	}
	if r.publicationDB != nil {
		if err := r.publicationDB.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("publication database close: %w", err))
		}
	}
	if r.pipelineStore != nil {
		if err := r.pipelineStore.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("pipeline store close: %w", err))
		}
	}
	if r.runLedger != nil {
		if err := r.runLedger.Close(); err != nil {
			shutdownErrs = append(shutdownErrs, fmt.Errorf("run ledger close: %w", err))
		}
	}
	return errors.Join(shutdownErrs...)
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
