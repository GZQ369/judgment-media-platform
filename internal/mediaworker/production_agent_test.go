package mediaworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOpenMontageAssetURLRenderCanary(t *testing.T) {
	python := os.Getenv("OPENMONTAGE_RENDER_PYTHON")
	root := os.Getenv("OPENMONTAGE_ROOT")
	ffprobe := os.Getenv("MEDIA_WORKER_FFPROBE")
	if python == "" || root == "" || ffprobe == "" {
		t.Skip("set OPENMONTAGE_RENDER_PYTHON, OPENMONTAGE_ROOT and MEDIA_WORKER_FFPROBE for the real Remotion asset URL canary")
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root for canary")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	bridge := os.Getenv("OPENMONTAGE_RENDER_BRIDGE")
	if bridge == "" {
		bridge = filepath.Join(repoRoot, "tools", "openmontage-bridge", "bridge.py")
	}
	bridge, err := filepath.Abs(bridge)
	if err != nil {
		t.Fatal(err)
	}
	source := os.Getenv("OPENMONTAGE_RENDER_SOURCE")
	if source == "" {
		source = filepath.Join(repoRoot, "media", "smoke", "pet_demo.mp4")
	}
	audioSource := filepath.Join(repoRoot, "data", "projects", "proj_c801cccf", "openmontage", "narration.mp3")
	if _, err := os.Stat(audioSource); err != nil {
		t.Fatalf("approved supplied-audio smoke asset missing: %v", err)
	}
	out := t.TempDir()
	audio := filepath.Join(out, "supplied-smoke-audio.mp3")
	bytes, err := os.ReadFile(audioSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(audio, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newProductionAssetServer(source, audio, out)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	video := filepath.Join(out, "asset-url-canary.mp4")
	composition := composeFromSpec(ProductionSpec{Segments: []SegmentSpec{{ID: "canary", Text: "本次资产 URL 冒烟验证：使用仓库内已有音频，不代表自动配音。", CaptionLines: []string{"本地音频资产验证"}}}}, server.URL("source"), source)
	audioInfo := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", audio)
	data, err := audioInfo.Output()
	if err != nil {
		t.Fatalf("probe supplied smoke audio: %v", err)
	}
	var info struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	var duration float64
	if _, err := fmt.Sscanf(info.Format.Duration, "%f", &duration); err != nil || duration <= 0 {
		t.Fatalf("invalid supplied audio duration %q", info.Format.Duration)
	}
	if err := applyNarrationDurations(composition, []float64{duration}); err != nil {
		t.Fatal(err)
	}
	composition["audio"] = map[string]any{"narration": map[string]any{"src": server.URL("narration"), "volume": 1}}
	requestPath := filepath.Join(out, "render-request.json")
	request, _ := json.Marshal(map[string]any{"composition_data": composition, "output_path": video, "profile": "tiktok"})
	if err := os.WriteFile(requestPath, request, 0o600); err != nil {
		t.Fatal(err)
	}
	canaryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(canaryCtx, python, bridge, "--root", root, "--request", requestPath, "--stage", "render")
	path := filepath.Dir(python) + string(os.PathListSeparator) + os.Getenv("PATH")
	if node := os.Getenv("OPENMONTAGE_NODE_BIN"); node != "" {
		path = node + string(os.PathListSeparator) + path
	}
	cmd.Env = append(os.Environ(), "PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real OpenMontage registry render failed: %v\n%s", err, output)
	}
	if server.requests.Load() < 2 {
		t.Fatalf("Remotion did not fetch both local assets from loopback; requests=%d", server.requests.Load())
	}
	if err := extractCover(context.Background(), ffprobe, video, audio, filepath.Join(out, "cover.png")); err != nil {
		t.Fatalf("rendered canary failed video/audio ffprobe QA: %v", err)
	}
	t.Logf("asset URL render canary passed (existing supplied audio; not automatic TTS), output=%s", video)
}

func TestProductionAssetServerOnlyServesAllowlistedLocalFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	narration := filepath.Join(root, "narration.wav")
	if err := os.WriteFile(source, []byte("source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := newProductionAssetServer(source, narration, root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	response, err := http.Get(server.URL("source"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "source bytes" {
		t.Fatalf("source response: status=%d body=%q", response.StatusCode, body)
	}
	response, err = http.Get("http://" + server.listener.Addr().String() + "/" + server.token + "/../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("non-allowlisted route status=%d, want 404", response.StatusCode)
	}
	if err := os.WriteFile(narration, []byte("wav bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err = http.Get(server.URL("narration"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "wav bytes" {
		t.Fatalf("narration response: status=%d body=%q", response.StatusCode, body)
	}
}

func TestProductionAssetServerImageMIMEAndRequestGuards(t *testing.T) {
	for _, test := range []struct{ ext, want string }{{".png", "image/png"}, {".jpg", "image/jpeg"}, {".jpeg", "image/jpeg"}, {".webp", "image/webp"}} {
		t.Run(test.ext, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source"+test.ext)
			if err := os.WriteFile(source, []byte("image bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			server, err := newProductionAssetServer(source, filepath.Join(root, "narration.wav"), root)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			response, err := http.Get(server.URL("source"))
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != test.want {
				t.Fatalf("image response status=%d content-type=%q", response.StatusCode, response.Header.Get("Content-Type"))
			}
			request, _ := http.NewRequest(http.MethodPost, server.URL("source"), nil)
			response, err = http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("POST status=%d", response.StatusCode)
			}
			response, err = http.Get(server.URL("source") + "?x=1")
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("query route status=%d", response.StatusCode)
			}
		})
	}
}

func TestComposeFromSpecUsesCurrentRunScriptScenesAndDuration(t *testing.T) {
	spec := ProductionSpec{Segments: []SegmentSpec{{ID: "seg-1", Text: "本次运行的真实脚本内容，包含更长的口播与字幕信息，应该按字数估算出超过四秒的视频时长", CaptionLines: []string{"本次字幕"}}}, Scenes: []SceneSpec{{Props: map[string]any{"visual": "close-up", "purpose": "本次分镜"}}}}
	composition := composeFromSpec(spec, `D:\assets\source.mp4`, `D:\assets\source.mp4`)
	if composition["duration_in_frames"].(int) <= 120 {
		t.Fatal("duration should derive from script text, not a fixed four seconds")
	}
	cut := composition["cuts"].([]any)[0].(map[string]any)
	if cut["text"] != conciseTitle(spec.Segments[0].Text, 34) || cut["subtitle"] != "本次字幕" || cut["title"] != conciseTitle(spec.Segments[0].Text, 34) {
		t.Fatalf("spec content not mapped: %#v", cut)
	}
}

func TestComposeFromSpecTitlesAreConciseAndPreserveNarration(t *testing.T) {
	spec := ProductionSpec{
		Segments: []SegmentSpec{{ID: "hook", Text: "一段需要原样用于旁白和字幕的较长口播内容", CaptionLines: []string{"一段需要原样用于旁白和字幕的较长口播内容"}}, {ID: "later", Text: "后续完整旁白内容仍需保留", CaptionLines: []string{"后续字幕原样"}}},
		Scenes:   []SceneSpec{{Props: map[string]any{"purpose": "建立讨论问题", "visual": nil}}, {Props: map[string]any{"visual": "关键视觉特写", "purpose": ""}}},
	}
	composition := composeFromSpec(spec, "http://127.0.0.1/source", "source.mp4")
	cuts := composition["cuts"].([]any)
	first := cuts[0].(map[string]any)
	second := cuts[1].(map[string]any)
	if first["title"] != conciseTitle(spec.Segments[0].Text, 34) || first["title"] == "建立讨论问题" || first["subtitle"] != spec.Segments[0].CaptionLines[0] {
		t.Fatalf("hook or original subtitle lost: %#v", first)
	}
	if len([]rune(first["text"].(string))) > 34 || len([]rune(second["text"].(string))) > 26 || second["title"] != "关键视觉特写" {
		t.Fatalf("titles should be concise and use scene visual fallback: %#v %#v", first, second)
	}
	if _, ok := first["visual_description"]; ok {
		t.Fatal("nil visual must not stringify to <nil>")
	}
}

func TestComposeFromSpecSelectsImageOrVideoBackground(t *testing.T) {
	for _, test := range []struct {
		path, key, other string
		still            bool
	}{{"a.mp4", "backgroundVideo", "images", false}, {"a.png", "images", "backgroundVideo", true}, {"a.jpeg", "images", "backgroundVideo", true}, {"a.webp", "images", "backgroundVideo", true}} {
		const sourceURL = "http://127.0.0.1:12345/token/source"
		composition := composeFromSpec(ProductionSpec{Segments: []SegmentSpec{{ID: "s", Text: "标题"}}}, sourceURL, test.path)
		cut := composition["cuts"].([]any)[0].(map[string]any)
		if test.still {
			images, ok := cut[test.key].([]string)
			if !ok || len(images) != 1 || images[0] != sourceURL {
				t.Errorf("%s should map to renderer images array: %#v", test.path, cut[test.key])
			}
		} else if cut[test.key] != sourceURL {
			t.Errorf("%s should map to %s: %#v", test.path, test.key, cut)
		}
		if _, exists := cut[test.other]; exists {
			t.Errorf("%s unexpectedly maps to %s", test.path, test.other)
		}
		if test.still {
			if cut["type"] != "anime_scene" || cut["animation"] != "ken-burns" {
				t.Errorf("still image must use renderer motion path: %#v", cut)
			}
			if _, exists := cut["overlays"]; exists {
				t.Errorf("cut-local overlays are ignored by Explainer: %#v", cut)
			}
			rootOverlays, ok := composition["overlays"].([]any)
			if !ok || len(rootOverlays) != 1 {
				t.Fatalf("still image overlay must be at composition top level: %#v", rootOverlays)
			}
			overlay, ok := rootOverlays[0].(map[string]any)
			if !ok || overlay["type"] != "section_title" || overlay["text"] != "标题" || overlay["in_seconds"] != 0.0 || overlay["out_seconds"] == nil {
				t.Errorf("top-level title overlay needs renderer fields and scene timing: %#v", rootOverlays[0])
			}
		} else if overlays := composition["overlays"].([]any); len(overlays) != 0 {
			t.Errorf("video source should retain its original overlay behavior: %#v", overlays)
		}
	}
}

func TestCoverTimestampIsEarlyAndInsideShortVideo(t *testing.T) {
	for _, test := range []struct{ duration, want float64 }{{20, .5}, {1, .25}, {.1, .025}, {0, 0}} {
		got := coverTimestamp(test.duration)
		if got != test.want {
			t.Errorf("coverTimestamp(%v)=%v, want %v", test.duration, got, test.want)
		}
		if test.duration > 0 && (got >= test.duration || got > 1) {
			t.Errorf("cover time %v is not inside the first second of %.3fs video", got, test.duration)
		}
	}
}

func TestApplyNarrationDurationsSetsCutTimingAndTotalFrames(t *testing.T) {
	composition := map[string]any{
		"cuts": []any{
			map[string]any{"in_seconds": 0.0, "out_seconds": 3.0, "subtitle": "first"},
			map[string]any{"in_seconds": 3.0, "out_seconds": 6.0, "subtitle": "second"},
		},
		"overlays": []any{
			map[string]any{"type": "section_title", "text": "first title", "in_seconds": 0.0, "out_seconds": 3.0},
			map[string]any{"type": "section_title", "text": "second title", "in_seconds": 3.0, "out_seconds": 6.0},
		},
	}
	if err := applyNarrationDurations(composition, []float64{2.25, 3.5}); err != nil {
		t.Fatal(err)
	}
	cuts := composition["cuts"].([]any)
	if cuts[1].(map[string]any)["in_seconds"] != 2.25 || composition["duration_in_frames"] != 173 {
		t.Fatalf("incorrect duration mapping: %#v", composition)
	}
	overlays := composition["overlays"].([]any)
	if overlays[0].(map[string]any)["in_seconds"] != 0.0 || overlays[0].(map[string]any)["out_seconds"] != 2.25 || overlays[1].(map[string]any)["in_seconds"] != 2.25 || overlays[1].(map[string]any)["out_seconds"] != 5.75 {
		t.Fatalf("overlay times should follow actual Piper cut durations: %#v", overlays)
	}
	if err := applyNarrationDurations(composition, []float64{1}); err == nil {
		t.Fatal("duration count mismatch must fail")
	}
}

func TestOpenMontageValidateHandlesOptionalRenderSpec(t *testing.T) {
	root := t.TempDir()
	python, bridge, source, ffprobe := filepath.Join(root, "python.exe"), filepath.Join(root, "bridge.py"), filepath.Join(root, "source.mp4"), filepath.Join(root, "ffprobe.exe")
	for _, name := range []string{python, bridge, source, ffprobe} {
		if err := os.WriteFile(name, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	openMontage := filepath.Join(root, "openmontage")
	if err := os.MkdirAll(openMontage, 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(root, "voice.onnx")
	for _, name := range []string{model, model + ".json"} {
		if err := os.WriteFile(name, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	worker := OpenMontageRenderAdapter{Python: python, Bridge: bridge, OpenMontageRoot: openMontage, SourcePath: source, FFprobe: ffprobe, DataRoot: root, TTSModel: model}
	if _, err := worker.Validate(nil, ProductionSpec{}, t.TempDir()); err == nil {
		t.Fatal("nil render config must return an error, not panic")
	}
}

func TestOpenMontageValidateBlocksMissingPiperModel(t *testing.T) {
	root := t.TempDir()
	python, bridge, source, ffprobe := filepath.Join(root, "python.exe"), filepath.Join(root, "bridge.py"), filepath.Join(root, "source.mp4"), filepath.Join(root, "ffprobe.exe")
	for _, name := range []string{python, bridge, source, ffprobe} {
		if err := os.WriteFile(name, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	openMontage := filepath.Join(root, "openmontage")
	if err := os.MkdirAll(openMontage, 0o755); err != nil {
		t.Fatal(err)
	}
	worker := OpenMontageRenderAdapter{Python: python, Bridge: bridge, OpenMontageRoot: openMontage, SourcePath: source, FFprobe: ffprobe, DataRoot: root}
	_, err := worker.Validate(nil, ProductionSpec{Render: &RenderSpec{Enabled: true}}, t.TempDir())
	var blocker *CapabilityBlockerError
	if !errors.As(err, &blocker) || blocker.Blocker.Capability != "narration" {
		t.Fatalf("missing local voice model should produce a narration blocker, got %v", err)
	}
}
