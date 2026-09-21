package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/analytics"
	"github.com/GZQ369/judgment-media-platform/internal/config"
	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/egopublisher"
	"github.com/GZQ369/judgment-media-platform/internal/pipeline"
	"github.com/GZQ369/judgment-media-platform/internal/publication"
	"github.com/GZQ369/judgment-media-platform/internal/publisher"
	"github.com/GZQ369/judgment-media-platform/internal/runstore"
)

type publicationAdapterDouble struct {
	platform   publication.Platform
	configured bool
}

func (a publicationAdapterDouble) Platform() publication.Platform { return a.platform }

func (a publicationAdapterDouble) Capability(context.Context) publication.Capability {
	return publication.Capability{Platform: a.platform, Configured: a.configured}
}

func (a publicationAdapterDouble) Commit(context.Context, publication.AdapterRequest) (publication.Evidence, error) {
	return publication.Evidence{}, nil
}

func (a publicationAdapterDouble) Reconcile(context.Context, publication.AdapterRequest) (publication.Evidence, error) {
	return publication.Evidence{}, nil
}

func TestSelectPublicationAdapterKeepsCapableEgoForConfiguredXHSHandoff(t *testing.T) {
	toolDir := t.TempDir()
	nodePath := filepath.Join(toolDir, "node")
	publisherScript := filepath.Join(toolDir, "publisher")
	egoCommand := filepath.Join(toolDir, "ego")
	for _, path := range []string{nodePath, publisherScript, egoCommand} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("create Ego test file %q: %v", path, err)
		}
	}
	ego, err := egopublisher.New(egopublisher.Config{
		NodePath:                nodePath,
		PublisherScript:         publisherScript,
		EgoCommand:              egoCommand,
		StateRoot:               t.TempDir(),
		DataDir:                 t.TempDir(),
		Timeout:                 time.Second,
		OriginalRightsConfirmed: true,
	}, publication.PlatformXiaohongshu)
	if err != nil {
		t.Fatalf("egopublisher.New returned error: %v", err)
	}
	defer ego.Close()
	official, err := publication.NewPublisherAdapter(publisher.NewXiaohongshuPublisher("app-key"))
	if err != nil {
		t.Fatalf("NewPublisherAdapter returned error: %v", err)
	}

	selected := selectPublicationAdapter(publication.PlatformXiaohongshu, ego, official)
	if selected != ego {
		t.Fatalf("selected adapter = %T, want capable Ego fallback to remain selected", selected)
	}
}

func TestSelectPublicationAdapterBlocksXHSHandoffWithoutEgo(t *testing.T) {
	ego, err := egopublisher.New(egopublisher.Config{}, publication.PlatformXiaohongshu)
	if err != nil {
		t.Fatalf("egopublisher.New returned error: %v", err)
	}
	defer ego.Close()
	official, err := publication.NewPublisherAdapter(publisher.NewXiaohongshuPublisher("app-key"))
	if err != nil {
		t.Fatalf("NewPublisherAdapter returned error: %v", err)
	}

	selected := selectPublicationAdapter(publication.PlatformXiaohongshu, ego, official)
	if selected != ego || selected.Capability(context.Background()).Configured {
		t.Fatalf("selected adapter = %T, capability = %+v, want unconfigured Ego", selected, selected.Capability(context.Background()))
	}
	if selected := selectPublicationAdapter(publication.PlatformXiaohongshu, nil, official); selected != nil {
		t.Fatalf("selected adapter = %T, want no adapter without Ego", selected)
	}
}

func TestXHSCommitDoesNotConsumeAuthorizationWhenEgoBecomesUnavailable(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	toolDir := t.TempDir()
	tools := []string{filepath.Join(toolDir, "node"), filepath.Join(toolDir, "publisher"), filepath.Join(toolDir, "ego")}
	for _, path := range tools {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := runstore.Open(filepath.Join(dataDir, "runs.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Close()
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "publications.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{
		DataDir: dataDir, XHSShareAppKey: "app-key", EgoNodePath: tools[0],
		EgoPublisherScript: tools[1], EgoCommand: tools[2], EgoTimeout: time.Second,
		EgoOriginalRightsConfirmed: true,
	}
	service, adapters, err := newPublicationService(cfg, runs, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, adapter := range adapters {
			_ = adapter.Close()
		}
	}()
	project := domain.VideoProject{Variants: []domain.Variant{{ID: "v1", Title: "title", Caption: "copy"}}}
	packageJSON, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	assets := []struct {
		id, kind, name string
		data           []byte
	}{
		{"package", "package", "publish_package.json", packageJSON},
		{"rendered-video", "video", "video.mp4", []byte("video")},
		{"cover", "cover", "cover.png", []byte("cover")},
	}
	run, _, err := runs.CreateRun(ctx, domain.DistributionRequest{Platform: domain.PlatformXiaohongshu})
	if err != nil {
		t.Fatal(err)
	}
	for _, asset := range assets {
		path := filepath.Join(dataDir, asset.name)
		if err := os.WriteFile(path, asset.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := runs.AddArtifact(ctx, domain.Artifact{ID: asset.id, RunID: run.ID, Kind: asset.kind, Path: path}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runs.UpdateRun(ctx, run.ID, domain.RunStatusMediaReady, "media_ready", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	request := publication.CandidateRequest{RunID: run.ID, Creator: "creator", Account: "acct", Platform: publication.PlatformXiaohongshu, VariantID: "v1"}
	candidate, err := service.Candidate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := service.Authorize(ctx, publication.AuthorizeCandidateRequest{
		CandidateRequest: request, Confirm: true, PolicyVersion: "v1",
		Selected: []publication.Platform{publication.PlatformXiaohongshu}, ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Prepare(ctx, publication.PrepareCandidateRequest{
		CandidateRequest: request, AuthorizationID: auth.ID, Fingerprint: candidate.Fingerprint,
		PolicyVersion: "v1", Selected: []publication.Platform{publication.PlatformXiaohongshu},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(tools[0]); err != nil {
		t.Fatal(err)
	}
	blockedService, blockedAdapters, err := newPublicationService(cfg, runs, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, adapter := range blockedAdapters {
			_ = adapter.Close()
		}
	}()
	for _, capability := range blockedService.Capabilities(ctx) {
		if capability.Platform == publication.PlatformXiaohongshu && capability.Configured {
			t.Fatalf("XHS capability with unavailable Ego = %+v, want unconfigured", capability)
		}
	}
	_, err = blockedService.Commit(ctx, publication.CommitRequest{PublicationID: prepared.ID, Fingerprint: candidate.Fingerprint, Confirm: true})
	var blocker *publication.CapabilityBlocker
	if !errors.As(err, &blocker) || blocker.Platform != publication.PlatformXiaohongshu {
		t.Fatalf("commit error = %v, want XHS capability blocker", err)
	}
	var consumedAt sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT consumed_at FROM pub_authorizations WHERE id=?", auth.ID).Scan(&consumedAt); err != nil {
		t.Fatal(err)
	}
	if consumedAt.Valid {
		t.Fatalf("authorization was consumed at %s", consumedAt.String)
	}
	stored, err := blockedService.Get(ctx, prepared.ID)
	if err != nil || stored.Status != publication.StatusDraftReady {
		t.Fatalf("publication after blocked commit = %+v, err = %v", stored, err)
	}
}

func TestSelectPublicationAdapterPreservesConfiguredOfficialDouyin(t *testing.T) {
	ego := publicationAdapterDouble{platform: publication.PlatformDouyin, configured: true}
	official := publicationAdapterDouble{platform: publication.PlatformDouyin, configured: true}

	selected := selectPublicationAdapter(publication.PlatformDouyin, ego, official)
	if selected != official {
		t.Fatalf("selected adapter = %T, want configured official Douyin adapter", selected)
	}
}

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

func TestOpenRuntimeWithoutGatewayStartsAndBlocksPipeline(t *testing.T) {
	runtime, err := openRuntime(config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("openRuntime returned error: %v", err)
	}
	defer runtime.close(context.Background())

	run, _, err := runtime.pipeline.StartRun(context.Background(), pipeline.PipelineRequest{
		Topic: domain.ResearchTopic{
			Topic:         "runtime capability test",
			SourceURLs:    []string{"https://example.com/source"},
			DesiredAction: "inform",
		},
		Targets: []domain.DistributionTarget{{Platform: domain.PlatformBilibili}},
	})
	if err != nil {
		t.Fatalf("StartRun returned error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, getErr := runtime.pipeline.GetRun(context.Background(), run.ID)
		if getErr != nil {
			t.Fatalf("GetRun returned error: %v", getErr)
		}
		if current.Status == pipeline.PipelineStatusBlocked {
			if current.CapabilityBlock == nil || current.CapabilityBlock.Capability != "judgment_workflow" {
				t.Fatalf("unexpected capability blocker: %#v", current.CapabilityBlock)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pipeline run did not become blocked: %s", run.ID)
}

func TestRuntimeCloseReleasesBothLedgers(t *testing.T) {
	dataDir := t.TempDir()
	runtime, err := openRuntime(config.Config{DataDir: dataDir})
	if err != nil {
		t.Fatalf("openRuntime returned error: %v", err)
	}
	if err := runtime.close(context.Background()); err != nil {
		t.Fatalf("runtime.close returned error: %v", err)
	}

	pipelineStore, err := pipeline.Open(filepath.Join(dataDir, "pipelines", "pipelines.sqlite"))
	if err != nil {
		t.Fatalf("pipeline ledger could not be reopened after close: %v", err)
	}
	if err := pipelineStore.Close(); err != nil {
		t.Fatalf("reopened pipeline ledger close returned error: %v", err)
	}
	runLedger, err := runstore.Open(filepath.Join(dataDir, "runs", "runs.sqlite"))
	if err != nil {
		t.Fatalf("distribution ledger could not be reopened after close: %v", err)
	}
	if err := runLedger.Close(); err != nil {
		t.Fatalf("reopened distribution ledger close returned error: %v", err)
	}
	analyticsStore, err := analytics.OpenStore(filepath.Join(dataDir, "analytics", "analytics.sqlite"))
	if err != nil {
		t.Fatalf("analytics database could not be reopened after close: %v", err)
	}
	if err := analyticsStore.Close(); err != nil {
		t.Fatalf("reopened analytics database close returned error: %v", err)
	}
	publicationDB, err := sql.Open("sqlite", filepath.Join(dataDir, "publications", "publications.sqlite"))
	if err != nil {
		t.Fatalf("publication database could not be reopened after close: %v", err)
	}
	if err := publicationDB.Close(); err != nil {
		t.Fatalf("reopened publication database close returned error: %v", err)
	}
}

func TestRuntimePublicationCapabilitiesAreExplicitWithoutConfiguration(t *testing.T) {
	runtime, err := openRuntime(config.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("openRuntime returned error: %v", err)
	}
	defer runtime.close(context.Background())
	capabilities := runtime.publication.Capabilities(context.Background())
	if len(capabilities) != 3 {
		t.Fatalf("capabilities = %#v, want three supported platforms", capabilities)
	}
	for _, capability := range capabilities {
		if capability.Configured || capability.Reason == "" {
			t.Fatalf("unconfigured capability was not explicit: %+v", capability)
		}
	}
}

func TestGatewaySecretIsNotReturnedByRuntimeConstruction(t *testing.T) {
	runner, err := newJudgmentRunner(config.Config{
		AIGatewayBaseURL:    "http://127.0.0.1:1/v1",
		AIGatewayModel:      "test-model",
		AIGatewayAPIKey:     "super-secret",
		AIGatewayAllowLocal: true,
	})
	if err != nil {
		t.Fatalf("newJudgmentRunner returned error: %v", err)
	}
	if runner == nil {
		t.Fatal("newJudgmentRunner returned nil for complete gateway configuration")
	}
}
