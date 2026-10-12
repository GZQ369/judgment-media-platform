package mediaworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func taskAssetFixture(t *testing.T) (string, string, ProductionSpec) {
	t.Helper()
	return taskAssetFixtureAt(t, t.TempDir())
}

func taskAssetFixtureAt(t *testing.T, data string) (string, string, ProductionSpec) {
	t.Helper()
	run := filepath.Join(data, "runs", "asset-run")
	for _, dir := range []string{filepath.Join(data, "assets"), run} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first.png", "second.png"} {
		if err := os.WriteFile(filepath.Join(data, "assets", name), pngData.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Minimal ISO BMFF signature. Decodability is checked by the real renderer.
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(filepath.Join(data, "assets", "clip.mp4"), mp4, 0o600); err != nil {
		t.Fatal(err)
	}
	spec := ProductionSpec{JobID: "asset-run", Assets: []AssetSpec{{ID: "first", Ref: "first.png", Kind: "image"}, {ID: "second", Ref: "second.png", Kind: "image"}, {ID: "clip", Ref: "clip.mp4", Kind: "video"}}}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("s%d", i)
		spec.Segments = append(spec.Segments, SegmentSpec{ID: id, Text: "current hook", CaptionLines: []string{"complete caption"}})
		spec.Scenes = append(spec.Scenes, SceneSpec{ID: fmt.Sprintf("scene-%d", i), SegmentIDs: []string{id}, Asset: "legacy-field"})
	}
	return data, run, spec
}

func TestTaskAssetIDsArePortableAndCaseInsensitiveUnique(t *testing.T) {
	for _, id := range []string{"CON", "con", "NUL", "NuL", "PRN", "AUX", "COM1", "com9", "LPT1", "lPt9"} {
		t.Run(id, func(t *testing.T) {
			if err := ValidateAssetRequest([]AssetSpec{{ID: id, Ref: "safe.png", Kind: "image"}}, nil, id); err == nil {
				t.Fatalf("reserved device ID %q accepted", id)
			}
		})
	}
	for _, refs := range [][2]string{{"one.png", "two.png"}, {"one.png", "two.mp4"}} {
		kind, _ := assetFileType(refs[1])
		assets := []AssetSpec{{ID: "A", Ref: refs[0], Kind: "image"}, {ID: "a", Ref: refs[1], Kind: kind}}
		if err := ValidateAssetRequest(assets, nil, "A"); err == nil {
			t.Fatal("case-folded duplicate IDs accepted")
		}
	}
	assets := []AssetSpec{{ID: "Frame-A", Ref: "one.png", Kind: "image"}, {ID: "CON_image", Ref: "two.png", Kind: "image"}, {ID: "COM10", Ref: "three.png", Kind: "image"}}
	if err := ValidateAssetRequest(assets, nil, "Frame-A"); err != nil {
		t.Fatalf("valid IDs rejected: %v", err)
	}
	if assets[0].ID != "Frame-A" {
		t.Fatal("ID uniqueness check changed the public ID spelling")
	}
}

func TestTaskAssetServerSupportsRelativeDataAndOutputPaths(t *testing.T) {
	data, err := os.MkdirTemp(".", ".relative-task-assets-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(data); err != nil {
			t.Error(err)
		}
	})
	_, run, spec := taskAssetFixtureAt(t, data)
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	paths, err := VerifyAssetSnapshots(spec, run)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(run, "media")
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	narration := filepath.Join(output, "narration.wav")
	if err := os.WriteFile(narration, []byte("narration bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{paths["first"], filepath.Join(data, "assets", "first.png")} {
		server, err := newAllowlistedProductionAssetServer(map[string]string{"asset-0": source}, narration, output)
		if err != nil {
			t.Fatalf("relative data/output rejected: %v", err)
		}
		func() {
			defer server.Close()
			if !filepath.IsAbs(server.root) {
				t.Fatalf("server root is relative: %s", server.root)
			}
			for name, path := range server.assets {
				if !filepath.IsAbs(path) {
					t.Errorf("allowlist path %s is relative: %s", name, path)
				}
				response := httptest.NewRecorder()
				server.serveHTTP(response, httptest.NewRequest(http.MethodGet, server.URL(name), nil))
				if response.Code != http.StatusOK || response.Body.Len() == 0 {
					t.Errorf("asset %s status=%d, bytes=%d", name, response.Code, response.Body.Len())
				}
			}
		}()
	}
	if server, err := newAllowlistedProductionAssetServer(map[string]string{"asset-0": paths["first"]}, filepath.Join(run, "outside.wav"), output); err == nil {
		server.Close()
		t.Fatal("relative path normalization allowed narration outside output")
	}
}

func bindTaskFixture(t *testing.T, spec *ProductionSpec) {
	t.Helper()
	if err := BindSceneAssets(spec, []SceneAssetBinding{{SceneID: "scene-2", AssetID: "second"}, {SceneID: "scene-3", AssetID: "clip"}}, "first"); err != nil {
		t.Fatal(err)
	}
}

func TestTaskAssetsRejectUnsafeRefsAndInvalidBindings(t *testing.T) {
	for _, ref := range []string{"/tmp/a.png", `C:\a.png`, "C:a.png", `\\server\share\a.png`, "https://site/a.png", "../a.png", "dir/../a.png", "a/./b.png", "a//b.png", "NUL.png", "dir/a.png:stream", "file.png?x", "dir /a.png"} {
		t.Run(ref, func(t *testing.T) {
			if err := ValidateAssetRequest([]AssetSpec{{ID: "a", Ref: ref, Kind: "image"}}, nil, "a"); err == nil {
				t.Fatalf("unsafe path accepted: %q", ref)
			}
		})
	}
	for _, test := range []struct {
		name      string
		mutate    func(*ProductionSpec)
		bindings  []SceneAssetBinding
		defaultID string
	}{
		{name: "unknown scene", bindings: []SceneAssetBinding{{"missing", "first"}}, defaultID: "first"},
		{name: "duplicate binding", bindings: []SceneAssetBinding{{"scene-1", "first"}, {"scene-1", "second"}}, defaultID: "first"},
		{name: "dangling asset", bindings: []SceneAssetBinding{{"scene-1", "missing"}}, defaultID: "first"},
		{name: "missing default", bindings: []SceneAssetBinding{{"scene-1", "first"}}},
		{name: "unknown default", defaultID: "missing"},
		{name: "duplicate asset", defaultID: "first", mutate: func(s *ProductionSpec) { s.Assets = append(s.Assets, s.Assets[0]) }},
		{name: "duplicate scene", defaultID: "first", mutate: func(s *ProductionSpec) { s.Scenes = append(s.Scenes, s.Scenes[0]) }},
		{name: "unsupported kind", defaultID: "first", mutate: func(s *ProductionSpec) { s.Assets[0].Kind = "audio" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, spec := taskAssetFixture(t)
			if test.mutate != nil {
				test.mutate(&spec)
			}
			if err := BindSceneAssets(&spec, test.bindings, test.defaultID); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestTaskAssetSnapshotRetryUsesVerifiedCopy(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	paths, err := VerifyAssetSnapshots(spec, run)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range spec.Assets {
		if !strings.HasPrefix(a.Ref, "inputs/assets/") || len(a.SHA256) != 64 || a.Bytes <= 0 || a.MediaType == "" {
			t.Fatalf("incomplete snapshot metadata: %+v", a)
		}
	}
	manifestBefore, err := os.ReadFile(AssetSnapshotManifestPath(run))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "assets", "first.png"), []byte("changed original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(data, "assets", "second.png")); err != nil {
		t.Fatal(err)
	}
	var retry ProductionSpec
	if err := json.Unmarshal(raw, &retry); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotAssets(data, run, &retry); err != nil {
		t.Fatalf("retry should not require originals: %v", err)
	}
	if !reflect.DeepEqual(retry.Assets, spec.Assets) {
		t.Fatal("retry replaced input snapshots")
	}
	manifestAfter, _ := os.ReadFile(AssetSnapshotManifestPath(run))
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("retry changed manifest")
	}
	if err := os.WriteFile(paths["first"], []byte("tampered snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &retry); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotAssets(data, run, &retry); err == nil {
		t.Fatal("corrupt snapshot was silently rebuilt")
	}
}

func TestTaskAssetSnapshotRejectsChangedBindingsAndBadHash(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	raw, _ := json.Marshal(spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	var changed ProductionSpec
	if err := json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Scenes[0].AssetRef = "second"
	if err := SnapshotAssets(data, run, &changed); err == nil {
		t.Fatal("changed bindings reused snapshot")
	}
	data2, run2, bad := taskAssetFixture(t)
	bindTaskFixture(t, &bad)
	bad.Assets[0].SHA256 = strings.Repeat("0", 64)
	if err := SnapshotAssets(data2, run2, &bad); err == nil {
		t.Fatal("incorrect expected hash accepted")
	}
	if _, err := os.Stat(AssetSnapshotManifestPath(run2)); !os.IsNotExist(err) {
		t.Fatal("failed snapshot published a manifest")
	}
}

func TestTaskAssetsRejectSymlinkEscape(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.png")
	content, err := os.ReadFile(filepath.Join(data, "assets", "first.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(data, "assets", "escape.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	spec.Assets[0].Ref = "escape.png"
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err == nil {
		t.Fatal("source symlink escaped assets root")
	}
}

func TestTaskAssetSnapshotRejectsSymlinkReplacement(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "same-content.png")
	content, err := os.ReadFile(filepath.Join(data, "assets", "first.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run, filepath.FromSlash(spec.Assets[0].Ref))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := VerifyAssetSnapshots(spec, run); err == nil {
		t.Fatal("snapshot symlink replacement was accepted despite matching content")
	}
}

func TestTaskAssetSnapshotRejectsFalseImageMIME(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	if err := os.WriteFile(filepath.Join(data, "assets", "first.png"), []byte("not a PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotAssets(data, run, &spec); err == nil {
		t.Fatal("extension alone was trusted as MIME")
	}
	if _, err := os.Stat(AssetSnapshotManifestPath(run)); !os.IsNotExist(err) {
		t.Fatal("bad MIME published a snapshot")
	}
}

func TestTaskAssetCompositionSelectsEachSceneAndRetimesTitles(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	// A scene's asset is selected by segment ID, independent of scene ordering.
	spec.Scenes[0], spec.Scenes[3] = spec.Scenes[3], spec.Scenes[0]
	urls := map[string]string{"first": "http://127.0.0.1/token/asset-0", "second": "http://127.0.0.1/token/asset-1", "clip": "http://127.0.0.1/token/asset-2"}
	composition, err := composeBoundSpec(spec, urls)
	if err != nil {
		t.Fatal(err)
	}
	cuts := composition["cuts"].([]any)
	for i, id := range []string{"first", "second", "clip", "first"} {
		cut := cuts[i].(map[string]any)
		if id == "clip" {
			if cut["backgroundVideo"] != urls[id] || cut["type"] != "callout" {
				t.Fatalf("video mapping: %+v", cut)
			}
		} else if cut["type"] != "anime_scene" || cut["animation"] != "ken-burns" || !reflect.DeepEqual(cut["images"], []string{urls[id]}) {
			t.Fatalf("image mapping: %+v", cut)
		}
	}
	if err := applyNarrationDurations(composition, []float64{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	overlays := composition["overlays"].([]any)
	if len(overlays) != 3 {
		t.Fatalf("wrong title count: %d", len(overlays))
	}
	for i, want := range [][2]float64{{0, 1}, {1, 3}, {6, 10}} {
		overlay := overlays[i].(map[string]any)
		if overlay["type"] != "section_title" || overlay["in_seconds"] != want[0] || overlay["out_seconds"] != want[1] {
			t.Fatalf("title retiming: %+v", overlay)
		}
	}
	if _, err := composeBoundSpec(spec, map[string]string{}); err == nil {
		t.Fatal("missing allowlist must not fall back")
	}
	if spec.Scenes[0].Asset != "legacy-field" {
		t.Fatal("binding changed legacy scene asset")
	}
}

func TestTaskAssetServerOnlyServesThisRunAllowlist(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	paths, err := VerifyAssetSnapshots(spec, run)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newAllowlistedProductionAssetServer(map[string]string{"asset-0": paths["first"], "asset-1": paths["clip"]}, filepath.Join(run, "narration.wav"), run)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	for _, test := range []struct {
		route, mime string
		status      int
	}{{"asset-0", "image/png", 200}, {"asset-1", "video/mp4", 200}, {"source", "", 404}, {"second.png", "", 404}, {"../assets/first.png", "", 404}, {"asset-0?x=1", "", 404}} {
		r := httptest.NewRequest(http.MethodGet, server.URL(test.route), nil)
		w := httptest.NewRecorder()
		server.serveHTTP(w, r)
		if w.Code != test.status {
			t.Errorf("route %s status=%d", test.route, w.Code)
		}
		if test.mime != "" && w.Header().Get("Content-Type") != test.mime {
			t.Errorf("route %s MIME=%s", test.route, w.Header().Get("Content-Type"))
		}
	}
	wrong := httptest.NewRecorder()
	server.serveHTTP(wrong, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/wrong/asset-0", nil))
	if wrong.Code != 404 {
		t.Fatal("wrong token was accepted")
	}
}

func TestTaskAssetsPermitEmptyLegacySourcePath(t *testing.T) {
	data, run, spec := taskAssetFixture(t)
	bindTaskFixture(t, &spec)
	if err := SnapshotAssets(data, run, &spec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bridge.py", "voice.onnx", "voice.onnx.json"} {
		if err := os.WriteFile(filepath.Join(data, name), []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec.Render = &RenderSpec{Enabled: true}
	p := OpenMontageRenderAdapter{Python: "python", Bridge: filepath.Join(data, "bridge.py"), OpenMontageRoot: data, FFprobe: "ffprobe", DataRoot: data, TTSModel: filepath.Join(data, "voice.onnx")}
	if _, err := p.Validate(context.Background(), spec, filepath.Join(run, "media")); err != nil {
		t.Fatalf("task assets required global SourcePath: %v", err)
	}
	if err := RequireTaskAssetWorker(p); err != nil {
		t.Fatal(err)
	}
	if err := RequireTaskAssetWorker(CommandWorker{}); err == nil {
		t.Fatal("legacy worker accepted unsupported task assets")
	}
}

func TestArtifactMetadataUsesActualProbeValues(t *testing.T) {
	for _, test := range []struct {
		kind, probe                         string
		width, height, sampleRate, channels int
		duration                            float64
	}{
		{"video", `{"streams":[{"codec_type":"video","width":1080,"height":1920},{"codec_type":"audio","sample_rate":"48000","channels":2}],"format":{"duration":"12.125"}}`, 1080, 1920, 48000, 2, 12.125},
		{"cover", `{"streams":[{"codec_type":"video","width":1080,"height":1920}]}`, 1080, 1920, 0, 0, 0},
		{"audio", `{"streams":[{"codec_type":"audio","sample_rate":"22050","channels":1}],"format":{"duration":"12.0"}}`, 0, 0, 22050, 1, 12},
	} {
		a := Artifact{ID: test.kind, Kind: test.kind}
		if err := applyArtifactProbeMetadata(&a, []byte(test.probe)); err != nil {
			t.Fatal(err)
		}
		if a.Width != test.width || a.Height != test.height || a.SampleRate != test.sampleRate || a.Channels != test.channels || a.DurationSeconds != test.duration {
			t.Fatalf("incorrect probe metadata: %+v", a)
		}
	}
	if err := applyArtifactProbeMetadata(&Artifact{Kind: "audio"}, []byte(`{"format":{"duration":"NaN"}}`)); err == nil {
		t.Fatal("invalid measured duration accepted")
	}
}
