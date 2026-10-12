package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/GZQ369/judgment-media-platform/internal/domain"
	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
)

func TestNormalizeMediaTaskAssetsPreservesExplicitIntent(t *testing.T) {
	for _, input := range []string{
		`{"enabled":true,"render":true,"assets":[]}`,
		`{"enabled":true,"render":true,"scene_assets":[]}`,
		`{"enabled":false,"render":true,"assets":[{"id":"a","ref":"a.png","kind":"image"}],"default_asset_id":"a"}`,
		`{"enabled":true,"render":false,"assets":[{"id":"a","ref":"a.png","kind":"image"}],"default_asset_id":"a"}`,
		`{"enabled":true,"render":true,"assets":[{"id":"a","ref":"a.png","kind":"image"}],"scene_assets":[]}`,
		`{"enabled":true,"render":true,"assets":[{"id":"a","ref":"../a.png","kind":"image"}],"default_asset_id":"a"}`,
	} {
		request := offeringRequest("invalid-assets")
		if err := json.Unmarshal([]byte(input), &request.Media); err != nil {
			t.Fatal(err)
		}
		if _, err := normalizeRequest(request); err == nil {
			t.Errorf("invalid task asset intent accepted: %s", input)
		}
	}
	request := offeringRequest("valid-assets")
	request.Media = &domain.MediaRequest{Enabled: true, Render: true, Assets: []domain.MediaAssetRequest{{ID: "a", Ref: "a.png", Kind: "image"}}, DefaultAssetID: "a", SceneAssets: []domain.SceneAssetRequest{}}
	normalized, err := normalizeRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var restored domain.DistributionRequest
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Media.HasTaskAssets() || restored.Media.DefaultAssetID != "a" {
		t.Fatal("serialized request lost task asset intent")
	}
	legacy := offeringRequest("legacy")
	legacy.Media = &domain.MediaRequest{Enabled: true}
	if _, err := normalizeRequest(legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Media.HasTaskAssets() {
		t.Fatal("legacy request gained task asset mode")
	}
}

func TestNormalizeRejectsUnsafeTaskAssetIDs(t *testing.T) {
	for _, ids := range [][]string{{"A", "a"}, {"CON"}, {"con"}, {"NUL"}, {"PRN"}, {"AUX"}, {"COM1"}, {"lpt1"}} {
		request := offeringRequest("unsafe-asset-ids")
		request.Media = &domain.MediaRequest{Enabled: true, Render: true, DefaultAssetID: ids[0]}
		for _, id := range ids {
			request.Media.Assets = append(request.Media.Assets, domain.MediaAssetRequest{ID: id, Ref: "valid.png", Kind: "image"})
		}
		if _, err := normalizeRequest(request); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("IDs %v should be invalid request, got %v", ids, err)
		}
	}
}

func TestTaskAssetsBlockLegacyWorker(t *testing.T) {
	service := NewServiceWithOptions(testStore(t), t.TempDir(), Options{MediaWorker: &fakeMediaWorker{}, MediaPaths: MediaRuntimePaths{RemotionCWD: "configured"}})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("unsupported-worker")
	request.Media = &domain.MediaRequest{Enabled: true, Render: true, Assets: []domain.MediaAssetRequest{{ID: "a", Ref: "a.png", Kind: "image"}}, DefaultAssetID: "a"}
	run, _, err := service.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusBlocked)
	if got.CapabilityBlock == nil || got.CapabilityBlock.Capability != "task_assets" {
		t.Fatalf("expected explicit unsupported task assets blocker: %+v", got)
	}
}

type boundAssetTestWorker struct{ fakeMediaWorker }

func (*boundAssetTestWorker) SupportsTaskAssets() bool { return true }

func TestTaskAssetInputsPersistInRunLedgerBeforeWorker(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "assets", "first.png"), content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	worker := &boundAssetTestWorker{}
	service := NewServiceWithOptions(testStore(t), data, Options{MediaWorker: worker, MediaPaths: MediaRuntimePaths{RemotionCWD: "configured"}})
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	request := offeringRequest("bound-input-ledger")
	request.Media = &domain.MediaRequest{Enabled: true, Render: true, Assets: []domain.MediaAssetRequest{{ID: "first", Ref: "first.png", Kind: "image"}}, DefaultAssetID: "first"}
	run, _, err := service.StartRun(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := waitForRun(t, service, run.ID, domain.RunStatusMediaReady)
	wanted := map[string]string{run.ID + ":production_spec": "production_spec", run.ID + ":input_assets": "input_asset_manifest"}
	for _, a := range got.Artifacts {
		kind, exists := wanted[a.ID]
		if !exists {
			continue
		}
		if a.Kind != kind || len(a.SHA256) != 64 || a.Metadata["media_type"] != "application/json" {
			t.Fatalf("invalid input ledger metadata: %+v", a)
		}
		digest, err := fileSHA256(a.Path)
		if err != nil || digest != a.SHA256 {
			t.Fatalf("input ledger hash mismatch: %v", err)
		}
		if a.ID == run.ID+":production_spec" {
			encoded, err := os.ReadFile(a.Path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted mediaworker.ProductionSpec
			if err := json.Unmarshal(encoded, &persisted); err != nil {
				t.Fatal(err)
			}
			if len(persisted.Assets) != 1 || persisted.Assets[0].Ref != "inputs/assets/first.png" {
				t.Fatalf("spec does not contain normalized snapshot refs: %+v", persisted.Assets)
			}
		}
		delete(wanted, a.ID)
	}
	if len(wanted) != 0 {
		t.Fatalf("missing input audit artifacts: %+v", wanted)
	}
	if len(worker.spec.Scenes) == 0 {
		t.Fatal("worker did not receive bound scenes")
	}
	for _, scene := range worker.spec.Scenes {
		if scene.AssetRef != "first" {
			t.Fatalf("default not applied: %+v", scene)
		}
	}
	if _, err := mediaworker.VerifyAssetSnapshots(worker.spec, filepath.Join(data, "runs", run.ID)); err != nil {
		t.Fatal(err)
	}
}

func TestProductionSpecLegacySceneAssetJSONRemainsCompatible(t *testing.T) {
	spec := mediaworker.ProductionSpec{Scenes: []mediaworker.SceneSpec{{ID: "old", Asset: "legacy.png"}}}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"asset":"legacy.png"`)) || bytes.Contains(data, []byte(`"assetRef"`)) || bytes.Contains(data, []byte(`"assets"`)) {
		t.Fatalf("legacy JSON changed: %s", data)
	}
}
