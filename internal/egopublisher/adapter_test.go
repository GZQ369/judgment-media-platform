package egopublisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GZQ369/judgment-media-platform/internal/publication"
)

type recordedCommand struct {
	command string
	args    []string
	stdin   string
}

func testAdapter(t *testing.T, platform publication.Platform, runner CommandRunner) (*Adapter, publication.Package, string) {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	nodePath := filepath.Join(root, "node.exe")
	publisherScript := filepath.Join(root, "publisher.mjs")
	egoCommand := filepath.Join(root, "ego-browser.exe")
	for _, name := range []string{nodePath, publisherScript, egoCommand} {
		if err := os.WriteFile(name, []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := New(Config{
		NodePath:                nodePath,
		PublisherScript:         publisherScript,
		EgoCommand:              egoCommand,
		StateRoot:               stateRoot,
		DataDir:                 dataDir,
		Timeout:                 time.Second,
		OriginalRightsConfirmed: true,
		Runner:                  runner,
	}, platform)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	video := filepath.Join(root, "clip.mp4")
	cover := filepath.Join(root, "cover.jpg")
	if err := os.WriteFile(video, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cover, []byte("cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	pkg := publication.Package{Platform: platform, Video: video, Cover: cover, Copy: "copy", Title: "title", Tags: []string{"tag"}}
	return adapter, pkg, root
}

func TestCapabilityReportsMissingConfiguration(t *testing.T) {
	adapter, err := New(Config{}, publication.PlatformDouyin)
	if err != nil {
		t.Fatal(err)
	}
	capability := adapter.Capability(context.Background())
	if capability.Configured || !strings.Contains(capability.Reason, "timeout") {
		t.Fatalf("expected explicit missing configuration reason, got %+v", capability)
	}
}

func TestPlatformMappingsAndVerifiedSubmission(t *testing.T) {
	tests := []struct {
		platform publication.Platform
		label    string
	}{
		{publication.PlatformDouyin, "发布"},
		{publication.PlatformXiaohongshu, "发布笔记"},
		{publication.PlatformBilibili, "立即投稿"},
	}
	for _, tt := range tests {
		t.Run(string(tt.platform), func(t *testing.T) {
			var mu sync.Mutex
			var calls []recordedCommand
			adapter, pkg, _ := testAdapter(t, tt.platform, func(_ context.Context, command string, args []string, stdin []byte) (CommandOutput, error) {
				mu.Lock()
				calls = append(calls, recordedCommand{command: command, args: append([]string(nil), args...), stdin: string(stdin)})
				mu.Unlock()
				if len(args) > 0 {
					return CommandOutput{Stdout: `{"status":"ready","platform":"` + string(tt.platform) + `","taskSpaceId":7}`}, nil
				}
				return CommandOutput{Stdout: `{"platform":"` + string(tt.platform) + `","taskSpaceId":7,"publish":{"platform":"` + string(tt.platform) + `","taskSpaceId":7,"finalPublishClicked":true,"status":"reviewing","externalID":"item-7","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"7","pageId":"page-7"}}}`}, nil
			})
			evidence, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "pub-" + string(tt.platform), Reservation: "reservation-" + string(tt.platform), Package: pkg})
			if err != nil {
				t.Fatal(err)
			}
			if !evidence.Verified || evidence.Status != publication.StatusReviewing || evidence.ExternalID == "" {
				t.Fatalf("expected verified evidence, got %+v", evidence)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) != 2 || calls[0].command != adapter.config.NodePath || calls[1].command != adapter.config.EgoCommand {
				t.Fatalf("unexpected command sequence: %+v", calls)
			}
			if !containsArg(calls[0].args, "--draft-only") || !containsArg(calls[0].args, string(tt.platform)) {
				t.Fatalf("draft command did not target exact platform: %+v", calls[0].args)
			}
			if !strings.Contains(calls[1].stdin, "claimTaskSpace(ref,") || !strings.Contains(calls[1].stdin, `publishOnce("`+tt.label+`"`) || !strings.Contains(calls[1].stdin, "authorized: true") {
				t.Fatalf("final program did not use exact label/task space: %s", calls[1].stdin)
			}
		})
	}
}

func TestDraftBlockedAndClickOnlyAreRejected(t *testing.T) {
	t.Run("draft blocked", func(t *testing.T) {
		calls := 0
		adapter, pkg, _ := testAdapter(t, publication.PlatformDouyin, func(_ context.Context, _ string, _ []string, _ []byte) (CommandOutput, error) {
			calls++
			return CommandOutput{Stdout: `{"status":"blocked","taskSpaceId":7}`}, nil
		})
		if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "blocked", Reservation: "reservation-blocked", Package: pkg}); err == nil || calls != 1 {
			t.Fatalf("blocked draft must stop before Ego command: calls=%d err=%v", calls, err)
		}
	})

	t.Run("click only", func(t *testing.T) {
		adapter, pkg, _ := testAdapter(t, publication.PlatformDouyin, func(_ context.Context, _ string, args []string, _ []byte) (CommandOutput, error) {
			if len(args) > 0 {
				return CommandOutput{Stdout: `{"status":"ready","taskSpaceId":7}`}, nil
			}
			return CommandOutput{Stdout: `{"platform":"douyin","taskSpaceId":7,"publish":{"finalPublishClicked":true,"url":"https://creator.example/editor"}}`}, nil
		})
		if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "click-only", Reservation: "reservation-click-only", Package: pkg}); err == nil {
			t.Fatal("click-only result must not be verified")
		}
	})
}

func TestBridgeEvidenceContractIsFailClosed(t *testing.T) {
	good := `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"task-7","finalPublishClicked":true,"status":"reviewing","externalID":"work-7","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"task-7","pageId":"page-7"}}}`
	if evidence, err := parseVerifiedEvidence(publication.PlatformDouyin, good, "task-7"); err != nil || !evidence.Verified || evidence.Status != publication.StatusReviewing || evidence.ExternalID != "work-7" {
		t.Fatalf("valid bridge evidence = %+v, err=%v", evidence, err)
	}

	for name, output := range map[string]string{
		"unknown":                         `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"task-7","finalPublishClicked":true,"status":"unknown","evidenceSource":"unknown","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"task-7","pageId":"page-7"}}}`,
		"generic success":                 `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"task-7","finalPublishClicked":true,"status":"success","externalID":"work-7","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"task-7","pageId":"page-7"}}}`,
		"unbound task space":              `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"other","finalPublishClicked":true,"status":"submitted","externalID":"work-7","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"other","pageId":"page-7"}}}`,
		"missing correlation":             `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"task-7","finalPublishClicked":true,"status":"submitted","externalID":"work-7","evidenceSource":"platform-dom"}}`,
		"correlation task space mismatch": `{"platform":"douyin","taskSpaceId":"task-7","publish":{"platform":"douyin","taskSpaceId":"task-7","finalPublishClicked":true,"status":"submitted","externalID":"work-7","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-7","taskSpaceId":"other","pageId":"page-7"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseVerifiedEvidence(publication.PlatformDouyin, output, "task-7"); err == nil {
				t.Fatal("untrusted or unknown bridge evidence must not verify")
			}
		})
	}

	if _, err := parseReconcileEvidence(publication.PlatformDouyin, `{"platform":"douyin","taskSpaceId":"task-7","dom":{"status":"reviewing","externalID":"work-7"}}`, "task-7"); err == nil {
		t.Fatal("reconcile must reject legacy raw DOM evidence")
	}
}

func TestTimeoutAndReconcileNeverPublish(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		adapter, pkg, _ := testAdapter(t, publication.PlatformDouyin, func(ctx context.Context, _ string, _ []string, _ []byte) (CommandOutput, error) {
			<-ctx.Done()
			return CommandOutput{}, ctx.Err()
		})
		adapter.config.Timeout = time.Millisecond
		if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "timeout", Reservation: "reservation-timeout", Package: pkg}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected timeout, got %v", err)
		}
	})

	t.Run("reconcile", func(t *testing.T) {
		var mu sync.Mutex
		calls := make([]recordedCommand, 0, 2)
		adapter, pkg, _ := testAdapter(t, publication.PlatformDouyin, func(_ context.Context, command string, args []string, stdin []byte) (CommandOutput, error) {
			mu.Lock()
			calls = append(calls, recordedCommand{command: command, args: args, stdin: string(stdin)})
			mu.Unlock()
			if len(args) > 0 {
				return CommandOutput{Stdout: `{"status":"ready","taskSpaceId":9}`}, nil
			}
			if strings.Contains(string(stdin), "publishOnce") {
				return CommandOutput{Stdout: `{"platform":"douyin","taskSpaceId":9,"publish":{"platform":"douyin","taskSpaceId":9,"finalPublishClicked":true,"status":"submitted","externalID":"bad","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-9","taskSpaceId":"9","pageId":"page-9"}}}`}, nil
			}
			return CommandOutput{Stdout: `{"platform":"douyin","taskSpaceId":9,"evidence":{"platform":"douyin","taskSpaceId":9,"status":"reviewing","externalID":"item-9","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-9","taskSpaceId":"9","pageId":"page-9"}}}`}, nil
		})
		if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "reconcile", Reservation: "reservation-reconcile", Package: pkg}); err != nil {
			t.Fatal(err)
		}
		evidence, err := adapter.Reconcile(context.Background(), publication.AdapterRequest{PublicationID: "reconcile", Reservation: "reservation-reconcile", Package: pkg})
		if err != nil || !evidence.Verified || evidence.ExternalID == "" {
			t.Fatalf("unexpected reconcile result: %+v err=%v", evidence, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(calls) != 3 || strings.Contains(calls[2].stdin, "publishOnce") {
			t.Fatalf("reconcile must not publish: %+v", calls)
		}
	})
}

func TestTaskSpaceReferenceSurvivesAdapterRestart(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	stateRoot := filepath.Join(root, "state")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"node.exe", "publisher.mjs", "ego-browser.exe"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("test"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	video := filepath.Join(root, "clip.mp4")
	cover := filepath.Join(root, "cover.jpg")
	_ = os.WriteFile(video, []byte("video"), 0o600)
	_ = os.WriteFile(cover, []byte("cover"), 0o600)
	pkg := publication.Package{Platform: publication.PlatformDouyin, Video: video, Cover: cover, Copy: "copy", Title: "title"}
	config := Config{NodePath: filepath.Join(root, "node.exe"), PublisherScript: filepath.Join(root, "publisher.mjs"), EgoCommand: filepath.Join(root, "ego-browser.exe"), StateRoot: stateRoot, DataDir: dataDir, Timeout: time.Second, OriginalRightsConfirmed: true}
	reservation := "stable-reservation"
	adapter, err := New(config, publication.PlatformDouyin)
	if err != nil {
		t.Fatal(err)
	}
	var finalCalls int
	adapter.runner = func(_ context.Context, _ string, args []string, stdin []byte) (CommandOutput, error) {
		if len(args) > 0 {
			return CommandOutput{Stdout: `{"status":"ready","taskSpaceId":"task-42"}`}, nil
		}
		finalCalls++
		if !strings.Contains(string(stdin), "publishOnce") {
			t.Fatalf("expected final publish program")
		}
		return CommandOutput{Stdout: `{"platform":"douyin","taskSpaceId":"task-42","publish":{"platform":"douyin","taskSpaceId":"task-42","finalPublishClicked":true,"status":"submitted","externalID":"item-42","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-42","taskSpaceId":"task-42","pageId":"page-42"}}}`}, nil
	}
	if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "pub-restart", Reservation: reservation, Package: pkg}); err != nil {
		t.Fatal(err)
	}
	if finalCalls != 1 {
		t.Fatalf("final publish calls = %d, want 1", finalCalls)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(config, publication.PlatformDouyin)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restarted.runner = func(_ context.Context, _ string, _ []string, stdin []byte) (CommandOutput, error) {
		if strings.Contains(string(stdin), "publishOnce") {
			t.Fatal("reconcile must never call publishOnce after restart")
		}
		if !strings.Contains(string(stdin), "task-42") {
			t.Fatal("reconcile did not use the persisted task-space reference")
		}
		return CommandOutput{Stdout: `{"platform":"douyin","taskSpaceId":"task-42","evidence":{"platform":"douyin","taskSpaceId":"task-42","status":"reviewing","externalID":"item-42","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-42","taskSpaceId":"task-42","pageId":"page-42"}}}`}, nil
	}
	evidence, err := restarted.Reconcile(context.Background(), publication.AdapterRequest{PublicationID: "pub-restart", Reservation: reservation, Package: pkg})
	if err != nil || !evidence.Verified || evidence.ExternalID == "" {
		t.Fatalf("reconcile after restart = %+v, err=%v", evidence, err)
	}
}

func TestContentPackageMapsCustomCovers(t *testing.T) {
	for _, tt := range []struct {
		platform publication.Platform
		name     string
	}{
		{publication.PlatformXiaohongshu, "cover-portrait-3x4.png"},
		{publication.PlatformBilibili, "cover-landscape-4x3.png"},
	} {
		t.Run(string(tt.platform), func(t *testing.T) {
			var packageJSON map[string]any
			adapter, pkg, root := testAdapter(t, tt.platform, func(_ context.Context, _ string, args []string, _ []byte) (CommandOutput, error) {
				if len(args) > 0 {
					data, err := os.ReadFile(args[4])
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(data, &packageJSON); err != nil {
						t.Fatal(err)
					}
					return CommandOutput{Stdout: `{"status":"ready","taskSpaceId":11}`}, nil
				}
				return CommandOutput{Stdout: `{"platform":"` + string(tt.platform) + `","taskSpaceId":11,"publish":{"platform":"` + string(tt.platform) + `","taskSpaceId":11,"finalPublishClicked":true,"status":"submitted","externalID":"item-11","evidenceSource":"platform-dom","submissionCorrelation":{"version":1,"id":"attempt-11","taskSpaceId":"11","pageId":"page-11"}}}`}, nil
			})
			if err := os.WriteFile(filepath.Join(root, tt.name), []byte("custom"), 0o600); err != nil {
				t.Fatal(err)
			}
			customPath := filepath.Join(root, tt.name)
			customDigest := sha256.Sum256([]byte("custom"))
			role := "portrait_3x4"
			if tt.platform == publication.PlatformBilibili {
				role = "landscape_4x3"
			}
			pkg.CoverAssets = []publication.CoverAsset{{Role: role, Path: customPath, SHA256: hex.EncodeToString(customDigest[:])}}
			if _, err := adapter.Commit(context.Background(), publication.AdapterRequest{PublicationID: "cover-" + string(tt.platform), Reservation: "reservation-cover-" + string(tt.platform), Package: pkg}); err != nil {
				t.Fatal(err)
			}
			if got, _ := packageJSON["cover"].(string); filepath.Base(got) != tt.name {
				t.Fatalf("custom cover was not mapped: %+v", packageJSON)
			}
			if packageJSON["video"] != pkg.Video || packageJSON["copy"] != pkg.Copy || packageJSON["title"] != pkg.Title {
				t.Fatalf("content package fields were not preserved: %+v", packageJSON)
			}
		})
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
