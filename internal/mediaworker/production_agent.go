package mediaworker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
	"trpc.group/trpc-go/trpc-agent-go/agent/graphagent"
	"trpc.group/trpc-go/trpc-agent-go/graph"
	"trpc.group/trpc-go/trpc-agent-go/graph/checkpoint/sqlite"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session/noop"
)

// OpenMontageRenderAdapter runs the real registry composer inside a
// checkpointed tRPC GraphAgent; it is not a full autonomous production agent.
type OpenMontageRenderAdapter struct {
	Python, Bridge, OpenMontageRoot, SourcePath, FFprobe, DataRoot, NodeBinDir, TTSModel string
	Timeout                                                                              time.Duration
}

func (p OpenMontageRenderAdapter) SupportsTaskAssets() bool { return true }

func (p OpenMontageRenderAdapter) Validate(_ context.Context, spec ProductionSpec, outputDir string) (Result, error) {
	for name, value := range map[string]string{"Python": p.Python, "Bridge": p.Bridge, "OpenMontageRoot": p.OpenMontageRoot, "FFprobe": p.FFprobe, "DataRoot": p.DataRoot} {
		if strings.TrimSpace(value) == "" {
			return Result{}, fmt.Errorf("production agent %s is not configured", name)
		}
	}
	if spec.Render == nil || !spec.Render.Enabled {
		return Result{}, fmt.Errorf("OpenMontage renderer requires media.render=true")
	}
	if spec.HasTaskAssets() {
		if _, err := VerifyAssetSnapshots(spec, filepath.Dir(outputDir)); err != nil {
			return Result{}, err
		}
	} else if strings.TrimSpace(p.SourcePath) == "" {
		return Result{}, fmt.Errorf("production agent SourcePath is not configured")
	}
	model := strings.TrimSpace(p.TTSModel)
	if model == "" || strings.ToLower(filepath.Ext(model)) != ".onnx" {
		return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "narration", Message: "No user-configured Piper .onnx voice model is available.", Remediation: "Provide a licensed voice model path in OPENMONTAGE_PIPER_MODEL; the adapter does not download models."}, ExitCode: 0}
	}
	modelInfo, err := os.Stat(model)
	if err != nil || !modelInfo.Mode().IsRegular() {
		return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "narration", Message: "Configured Piper model file is unavailable or is not a regular file.", Remediation: "Check OPENMONTAGE_PIPER_MODEL and provide the licensed .onnx model file."}, ExitCode: 0}
	}
	sidecarInfo, err := os.Stat(model + ".json")
	if err != nil || !sidecarInfo.Mode().IsRegular() {
		return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "narration", Message: "Piper model .onnx.json sidecar is unavailable or is not a regular file.", Remediation: "Provide the matching model configuration next to the .onnx file."}, ExitCode: 0}
	}
	rate := 1.0
	if spec.Timeline != nil && spec.Timeline.PlaybackRate > 0 {
		rate = spec.Timeline.PlaybackRate
	}
	if rate < 0.5 || rate > 2 {
		return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "playback_rate", Message: fmt.Sprintf("OpenMontage narration atempo supports rates from 0.5x to 2x; requested %.2fx", rate), Remediation: "Set media.playback_rate between 0.5 and 2.0."}, ExitCode: 0}
	}
	if rate != 1 {
		ffmpeg := filepath.Join(filepath.Dir(p.FFprobe), "ffmpeg.exe")
		if runtime.GOOS != "windows" {
			ffmpeg = filepath.Join(filepath.Dir(p.FFprobe), "ffmpeg")
		}
		if info, err := os.Stat(ffmpeg); err != nil || !info.Mode().IsRegular() {
			return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "playback_rate", Message: "FFmpeg is required to apply the requested narration playback rate.", Remediation: "Install FFmpeg next to MEDIA_WORKER_FFPROBE or select 1x playback."}, ExitCode: 0}
		}
	}
	if !spec.HasTaskAssets() {
		if _, err := os.Stat(p.SourcePath); err != nil {
			return Result{}, fmt.Errorf("production source asset unavailable: %w", err)
		}
	}
	if _, err := os.Stat(p.Bridge); err != nil {
		return Result{}, fmt.Errorf("OpenMontage bridge unavailable: %w", err)
	}
	if !filepath.IsAbs(p.Bridge) {
		return Result{}, fmt.Errorf("OpenMontage bridge path must be absolute; configure OPENMONTAGE_RENDER_BRIDGE")
	}
	return Result{OK: true, Valid: true, JobID: spec.JobID, OutputDir: outputDir}, nil
}

func (p OpenMontageRenderAdapter) Run(ctx context.Context, spec ProductionSpec, outputDir string) (Result, error) {
	if _, err := p.Validate(ctx, spec, outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return Result{}, err
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	video := filepath.Join(outputDir, "rendered-video.mp4")
	cover := filepath.Join(outputDir, "cover.png")
	dbPath := filepath.Join(p.DataRoot, "production-agent-checkpoints.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return Result{}, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	saver, err := sqlite.NewSaver(db)
	if err != nil {
		return Result{}, err
	}
	defer saver.Close()
	requestPath := filepath.Join(outputDir, "openmontage-request.json")
	narration := filepath.Join(outputDir, "narration.wav")
	sources := map[string]string{"source": p.SourcePath}
	assetRoutes := map[string]string{}
	if spec.HasTaskAssets() {
		paths, err := VerifyAssetSnapshots(spec, filepath.Dir(outputDir))
		if err != nil {
			return Result{}, err
		}
		sources = map[string]string{}
		for i, a := range spec.Assets {
			route := fmt.Sprintf("asset-%d", i)
			sources[route] = paths[a.ID]
			assetRoutes[a.ID] = route
		}
	}
	assets, err := newAllowlistedProductionAssetServer(sources, narration, outputDir)
	if err != nil {
		return Result{}, err
	}
	defer assets.Close()
	segments := make([]map[string]string, 0, len(spec.Segments))
	for _, segment := range spec.Segments {
		segments = append(segments, map[string]string{"text": segment.Text})
	}
	rate := 1.0
	if spec.Timeline != nil && spec.Timeline.PlaybackRate > 0 {
		rate = spec.Timeline.PlaybackRate
	}
	if rate < 0.5 || rate > 2 {
		return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "playback_rate", Message: fmt.Sprintf("OpenMontage narration atempo supports rates from 0.5x to 2x; requested %.2fx", rate), Remediation: "Set media.playback_rate between 0.5 and 2.0."}, ExitCode: 0}
	}
	ttsRequest, _ := json.Marshal(map[string]any{"segments": segments, "narration_path": narration, "tts_model": p.TTSModel, "playback_rate": rate, "ffprobe": p.FFprobe})
	if err := os.WriteFile(requestPath, ttsRequest, 0o600); err != nil {
		return Result{}, err
	}
	stateSchema := graph.NewStateSchema().AddField("result", graph.StateField{Type: reflect.TypeOf(""), Reducer: graph.DefaultReducer}).AddField("composition", graph.StateField{Type: reflect.TypeOf(map[string]any{}), Reducer: graph.DefaultReducer})
	g, err := graph.NewStateGraph(stateSchema).
		AddNode("MapCurrentProductionSpec", func(_ context.Context, _ graph.State) (any, error) {
			composition := composeFromSpec(spec, assets.URL("source"), p.SourcePath)
			if spec.HasTaskAssets() {
				urls := map[string]string{}
				for id, route := range assetRoutes {
					urls[id] = assets.URL(route)
				}
				var err error
				composition, err = composeBoundSpec(spec, urls)
				if err != nil {
					return nil, err
				}
			}
			if len(spec.Segments) == 0 || len(composition["cuts"].([]any)) == 0 {
				return nil, fmt.Errorf("current run has no script segments to render")
			}
			return graph.State{"result": "mapped current-run scripts, scenes, subtitles and estimated duration", "composition": composition}, nil
		}).
		AddNode("GenerateNarration", func(nodeCtx context.Context, state graph.State) (any, error) {
			composition, ok := state["composition"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("checkpoint is missing mapped composition state")
			}
			cmd := exec.CommandContext(nodeCtx, p.Python, p.Bridge, "--root", p.OpenMontageRoot, "--request", requestPath, "--stage", "narrate")
			cmd.Env = p.bridgeEnv()
			output, runErr := cmd.CombinedOutput()
			if runErr != nil {
				detail := strings.TrimSpace(string(output))
				for _, capability := range []string{"narration", "playback_rate"} {
					if strings.Contains(detail, "CAPABILITY_BLOCKER "+capability+":") {
						return nil, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: capability, Message: detail, Remediation: "Check the configured voice model and compositor ffmpeg, then retry."}, ExitCode: 2}
					}
				}
				return nil, fmt.Errorf("OpenMontage local narration failed: %s", detail)
			}
			var narrationResult struct {
				SegmentDurations []float64 `json:"segment_durations"`
			}
			if err := json.Unmarshal(output, &narrationResult); err != nil {
				return nil, fmt.Errorf("decode Piper segment timings: %w", err)
			}
			if err := applyNarrationDurations(composition, narrationResult.SegmentDurations); err != nil {
				return nil, err
			}
			if err := verifyNarration(nodeCtx, p.FFprobe, narration); err != nil {
				return nil, err
			}
			composition["audio"] = map[string]any{"narration": map[string]any{"src": assets.URL("narration"), "volume": 1}}
			composition["metadata"].(map[string]any)["narration_generated"] = true
			return graph.State{"result": "local Piper narration generated and concatenated", "composition": composition}, nil
		}).
		AddNode("OpenMontageVideoCompose", func(nodeCtx context.Context, state graph.State) (any, error) {
			composition, ok := state["composition"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("checkpoint is missing narrated composition state")
			}
			request, _ := json.Marshal(map[string]any{"composition_data": composition, "output_path": video, "profile": "tiktok"})
			if err := os.WriteFile(requestPath, request, 0o600); err != nil {
				return nil, err
			}
			cmd := exec.CommandContext(nodeCtx, p.Python, p.Bridge, "--root", p.OpenMontageRoot, "--request", requestPath, "--stage", "render")
			cmd.Env = p.bridgeEnv()
			output, runErr := cmd.CombinedOutput()
			if runErr != nil {
				return nil, fmt.Errorf("OpenMontage video_compose.remotion_render failed: %s", strings.TrimSpace(string(output)))
			}
			return graph.State{"result": "rendered"}, nil
		}).
		AddNode("RenderedMediaQA", func(nodeCtx context.Context, _ graph.State) (any, error) {
			if err := extractCover(nodeCtx, p.FFprobe, video, narration, cover); err != nil {
				return nil, err
			}
			return graph.State{"result": "ffprobe video QA and cover extraction passed"}, nil
		}).
		AddEdge("MapCurrentProductionSpec", "GenerateNarration").
		AddEdge("GenerateNarration", "OpenMontageVideoCompose").
		AddEdge("OpenMontageVideoCompose", "RenderedMediaQA").
		SetEntryPoint("MapCurrentProductionSpec").SetFinishPoint("RenderedMediaQA").Compile()
	if err != nil {
		return Result{}, err
	}
	ga, err := graphagent.New("OpenMontageRenderAdapter", g, graphagent.WithExecutorOptions(graph.WithCheckpointSaver(saver)))
	if err != nil {
		return Result{}, err
	}
	r := runner.NewRunner("vertical-intel-video-agent", ga, runner.WithSessionService(noop.NewService()))
	// Checkpoints are internal execution snapshots only. A retry gets a fresh
	// graph session; runstore remains the durable run/retry authority.
	sessionID := spec.JobID + ":" + uuid.NewString()
	events, err := r.Run(ctx, "distribution", sessionID, model.NewUserMessage("Render the requested OpenMontage production asset."))
	if err != nil {
		return Result{}, err
	}
	for evt := range events {
		if evt.Error != nil {
			for _, capability := range []string{"narration", "playback_rate"} {
				if strings.Contains(evt.Error.Message, capability) {
					return Result{}, &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: capability, Message: evt.Error.Message, Remediation: "Check the configured voice model and compositor ffmpeg, then retry."}, ExitCode: 2}
				}
			}
			return Result{}, fmt.Errorf("production graph failed: %s", evt.Error.Message)
		}
	}
	manifest := ArtifactManifest{SchemaVersion: ArtifactManifestVersion, Kind: "artifact-manifest", JobID: spec.JobID, ProductionSpecVersion: ProductionSpecVersion}
	for _, item := range []struct{ id, kind, path, mime string }{{"rendered-video", "video", video, "video/mp4"}, {"cover", "cover", cover, "image/png"}, {"narration", "audio", narration, "audio/wav"}} {
		info, err := os.Stat(item.path)
		if err != nil {
			return Result{}, err
		}
		if info.Size() == 0 {
			return Result{}, fmt.Errorf("empty production artifact: %s", item.path)
		}
		file, err := os.Open(item.path)
		if err != nil {
			return Result{}, err
		}
		digest := sha256.New()
		_, hashErr := io.Copy(digest, file)
		file.Close()
		if hashErr != nil {
			return Result{}, hashErr
		}
		artifact := Artifact{ID: item.id, Kind: item.kind, Path: item.path, RelativePath: filepath.Base(item.path), Bytes: info.Size(), SHA256: hex.EncodeToString(digest.Sum(nil)), MediaType: item.mime}
		probe := exec.CommandContext(ctx, p.FFprobe, "-v", "error", "-show_entries", "stream=codec_type,width,height,sample_rate,channels", "-show_entries", "format=duration", "-of", "json", item.path)
		data, err := probe.Output()
		if err != nil {
			return Result{}, fmt.Errorf("probe artifact %s: %w", item.id, err)
		}
		if err := applyArtifactProbeMetadata(&artifact, data); err != nil {
			return Result{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	manifestPath := filepath.Join(outputDir, "artifact-manifest.json")
	encoded, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, encoded, 0o600); err != nil {
		return Result{}, err
	}
	return Result{OK: true, JobID: spec.JobID, OutputDir: outputDir, ArtifactManifestPath: manifestPath, Manifest: &manifest}, nil
}

func applyArtifactProbeMetadata(artifact *Artifact, data []byte) error {
	var probe struct {
		Streams []struct {
			CodecType               string `json:"codec_type"`
			Width, Height, Channels int
			SampleRate              string `json:"sample_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" {
			artifact.Width, artifact.Height = stream.Width, stream.Height
		}
		if stream.CodecType == "audio" {
			artifact.Channels = stream.Channels
			artifact.SampleRate, _ = strconv.Atoi(stream.SampleRate)
		}
	}
	if artifact.Kind == "video" || artifact.Kind == "audio" {
		duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
		if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
			return fmt.Errorf("artifact %s has invalid duration", artifact.ID)
		}
		artifact.DurationSeconds = duration
	}
	if (artifact.Kind == "video" || artifact.Kind == "cover") && (artifact.Width <= 0 || artifact.Height <= 0) {
		return fmt.Errorf("artifact %s has no image dimensions", artifact.ID)
	}
	if artifact.Kind == "audio" && (artifact.SampleRate <= 0 || artifact.Channels <= 0) {
		return fmt.Errorf("artifact %s has no audio format metadata", artifact.ID)
	}
	return nil
}

const maxServedProductionAsset = int64(2 << 30)

type productionAssetServer struct {
	listener net.Listener
	server   *http.Server
	root     string
	assets   map[string]string
	token    string
	requests atomic.Int64
}

func newProductionAssetServer(source, narration, outputDir string) (*productionAssetServer, error) {
	return newAllowlistedProductionAssetServer(map[string]string{"source": source}, narration, outputDir)
}

func newAllowlistedProductionAssetServer(sources map[string]string, narration, outputDir string) (*productionAssetServer, error) {
	root, err := resolveAbsoluteAssetPath(outputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve production output directory: %w", err)
	}
	allowlist := map[string]string{}
	for route, source := range sources {
		if !assetIDPattern.MatchString(route) || route == "narration" {
			return nil, fmt.Errorf("invalid asset route %q", route)
		}
		sourcePath, err := resolveAbsoluteAssetPath(source)
		if err != nil {
			return nil, fmt.Errorf("resolve production source asset: %w", err)
		}
		if info, err := os.Stat(sourcePath); err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxServedProductionAsset {
			return nil, fmt.Errorf("production source must be a non-empty regular file no larger than 2 GiB")
		}
		allowlist[route] = sourcePath
	}
	narrationPath, err := filepath.Abs(narration)
	if err != nil {
		return nil, err
	}
	narrationParent, err := resolveAbsoluteAssetPath(filepath.Dir(narrationPath))
	if err != nil || filepath.Clean(narrationParent) != filepath.Clean(root) {
		return nil, fmt.Errorf("narration output must be directly inside the production output directory")
	}
	narrationPath = filepath.Join(root, filepath.Base(narrationPath))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start loopback asset server: %w", err)
	}
	allowlist["narration"] = narrationPath
	assets := &productionAssetServer{listener: listener, root: root, assets: allowlist, token: uuid.NewString()}
	assets.server = &http.Server{Handler: http.HandlerFunc(assets.serveHTTP), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = assets.server.Serve(listener) }()
	return assets, nil
}

func (s *productionAssetServer) URL(name string) string {
	return "http://" + s.listener.Addr().String() + "/" + s.token + "/" + name
}

func (s *productionAssetServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	prefix := "/" + s.token + "/"
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, prefix)
	path, allowed := s.assets[name]
	if !allowed {
		http.NotFound(w, r)
		return
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(path) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxServedProductionAsset {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(resolved)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	s.requests.Add(1)
	contentType := productionAssetMIME(name, resolved)
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(resolved))
	}
	if contentType == "" && name == "source" {
		contentType = "video/mp4"
	} else if contentType == "" {
		contentType = "audio/wav"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, filepath.Base(resolved), info.ModTime(), file)
}

func productionAssetMIME(name, path string) string {
	if name == "narration" {
		return ""
	}
	if _, mediaType := assetFileType(path); mediaType != "" {
		return mediaType
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}

func (s *productionAssetServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.server.Shutdown(ctx)
}

func (p OpenMontageRenderAdapter) bridgeEnv() []string {
	env := os.Environ()
	extra := []string{filepath.Dir(p.Python)}
	if p.NodeBinDir != "" {
		extra = append(extra, p.NodeBinDir)
	}
	path := strings.Join(extra, string(os.PathListSeparator)) + string(os.PathListSeparator) + os.Getenv("PATH")
	return append(env, "PATH="+path)
}

type compositionSource struct{ URL, Path string }

func composeBoundSpec(spec ProductionSpec, urls map[string]string) (map[string]any, error) {
	if err := validateAssetDefinitions(spec.Assets); err != nil {
		return nil, err
	}
	if err := validateSceneAssetRefs(spec); err != nil {
		return nil, err
	}
	assets := map[string]AssetSpec{}
	for _, a := range spec.Assets {
		assets[a.ID] = a
	}
	sources := map[string]compositionSource{}
	for _, scene := range spec.Scenes {
		a := assets[scene.AssetRef]
		url := urls[a.ID]
		if url == "" {
			return nil, fmt.Errorf("asset %s has no allowed URL", a.ID)
		}
		for _, id := range scene.SegmentIDs {
			sources[id] = compositionSource{URL: url, Path: a.Ref}
		}
	}
	return composeWithSources(spec, sources, compositionSource{}), nil
}

func composeFromSpec(spec ProductionSpec, sourceURL, sourcePath string) map[string]any {
	return composeWithSources(spec, nil, compositionSource{URL: sourceURL, Path: sourcePath})
}

func composeWithSources(spec ProductionSpec, sources map[string]compositionSource, fallback compositionSource) map[string]any {
	segments := spec.Segments
	if len(segments) == 0 {
		segments = []SegmentSpec{{Text: "本次内容暂无脚本", CaptionLines: []string{"本次内容暂无脚本"}}}
	}
	cuts := make([]any, 0, len(segments))
	captions := make([]any, 0)
	overlays := make([]any, 0, len(segments))
	cursor := 0.0
	for i, seg := range segments {
		source := fallback
		if sources != nil {
			source = sources[seg.ID]
		}
		text := strings.TrimSpace(seg.Text)
		if text == "" {
			text = strings.Join(seg.CaptionLines, " ")
		}
		seconds := float64(len([]rune(text))) / 4
		if seconds < 3 {
			seconds = 3
		}
		if seconds > 9 {
			seconds = 9
		}
		visual, purpose := "", ""
		var props map[string]any
		if sources == nil && i < len(spec.Scenes) {
			props = spec.Scenes[i].Props
		}
		if sources != nil {
			for _, scene := range spec.Scenes {
				for _, id := range scene.SegmentIDs {
					if id == seg.ID {
						props = scene.Props
					}
				}
			}
		}
		visual, purpose = propertyText(props, "visual"), propertyText(props, "purpose")
		caption := strings.Join(seg.CaptionLines, " ")
		if caption == "" {
			caption = text
		}
		title := sceneTitle(i, seg, visual, purpose)
		cut := map[string]any{"id": seg.ID, "type": "callout", "in_seconds": cursor, "out_seconds": cursor + seconds, "text": title, "subtitle": caption, "title": title, "backgroundColor": "#13202A", "color": "#FFFFFF"}
		if isStillImage(source.Path) {
			cut["type"] = "anime_scene"
			cut["images"] = []string{source.URL}
			cut["animation"] = "ken-burns"
			overlays = append(overlays, map[string]any{"type": "section_title", "text": title, "in_seconds": cursor, "out_seconds": cursor + seconds})
		} else {
			cut["backgroundVideo"] = source.URL
		}
		if visual != "" {
			cut["visual_description"] = visual
		}
		cuts = append(cuts, cut)
		captions = append(captions, captionWordTimings(caption, cursor, cursor+seconds)...)
		cursor += seconds
	}
	return map[string]any{"renderer_family": "explainer-data", "fps": 30, "duration_in_frames": int(cursor * 30), "cuts": cuts, "captions": captions, "overlays": overlays, "metadata": map[string]any{"production_mode": "asset_driven", "narration_generated": false}}
}

func propertyText(props map[string]any, key string) string {
	if props == nil || props[key] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(props[key]))
}

func sceneTitle(index int, segment SegmentSpec, visual, purpose string) string {
	if index == 0 {
		for _, candidate := range []string{segment.EditorialLabel, segment.Text, purpose, visual} {
			if title := conciseTitle(candidate, 34); title != "" {
				return title
			}
		}
		return "本期看点"
	}
	for _, candidate := range []string{purpose, visual, segment.EditorialLabel, segment.Text} {
		if title := conciseTitle(candidate, 26); title != "" {
			return title
		}
	}
	return "重点内容"
}

func conciseTitle(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return value
}

func isStillImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	default:
		return false
	}
}

func applyNarrationDurations(composition map[string]any, durations []float64) error {
	cuts, ok := composition["cuts"].([]any)
	if !ok || len(cuts) == 0 || len(cuts) != len(durations) {
		return fmt.Errorf("Piper returned %d segment durations for %d composition cuts", len(durations), len(cuts))
	}
	oldWindows := make([][2]float64, len(cuts))
	for i, raw := range cuts {
		cut, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("composition cut %d has invalid shape", i+1)
		}
		oldWindows[i] = [2]float64{numberValue(cut["in_seconds"]), numberValue(cut["out_seconds"])}
	}
	cursor := 0.0
	for i, value := range durations {
		if value <= 0 {
			return fmt.Errorf("Piper segment %d has invalid duration %.3f seconds", i+1, value)
		}
		cut, ok := cuts[i].(map[string]any)
		if !ok {
			return fmt.Errorf("composition cut %d has invalid shape", i+1)
		}
		cut["in_seconds"] = cursor
		cut["out_seconds"] = cursor + value
		cursor += value
	}
	if overlays, ok := composition["overlays"].([]any); ok {
		for _, raw := range overlays {
			overlay, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("composition overlay has invalid shape")
			}
			start, end := numberValue(overlay["in_seconds"]), numberValue(overlay["out_seconds"])
			matched := -1
			for i, window := range oldWindows {
				if start >= window[0] && start < window[1] && end <= window[1] {
					matched = i
					break
				}
			}
			if matched < 0 {
				return fmt.Errorf("composition overlay timing %.3f-%.3f does not match a cut", start, end)
			}
			cut := cuts[matched].(map[string]any)
			overlay["in_seconds"] = cut["in_seconds"]
			overlay["out_seconds"] = cut["out_seconds"]
		}
	}
	captions := make([]any, 0)
	cursor = 0
	for i, value := range durations {
		cut := cuts[i].(map[string]any)
		captions = append(captions, captionWordTimings(fmt.Sprint(cut["subtitle"]), cursor, cursor+value)...)
		cursor += value
	}
	composition["captions"] = captions
	composition["duration_in_frames"] = int(math.Ceil(cursor * 30))
	return nil
}

func numberValue(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case float32:
		return float64(number)
	case int:
		return float64(number)
	case int64:
		return float64(number)
	default:
		return math.NaN()
	}
}

func captionWordTimings(text string, startSeconds, endSeconds float64) []any {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) == 0 {
		return nil
	}
	const chunkSize = 8
	chunks := (len(runes) + chunkSize - 1) / chunkSize
	result := make([]any, 0, chunks)
	for i, offset := 0, 0; offset < len(runes); i, offset = i+1, offset+chunkSize {
		end := offset + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		startMs := int64((startSeconds + (endSeconds-startSeconds)*float64(i)/float64(chunks)) * 1000)
		endMs := int64((startSeconds + (endSeconds-startSeconds)*float64(i+1)/float64(chunks)) * 1000)
		result = append(result, map[string]any{"word": string(runes[offset:end]), "startMs": startMs, "endMs": endMs})
	}
	return result
}

func extractCover(ctx context.Context, ffprobe, video, narration, cover string) error {
	probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type,width,height", "-show_entries", "format=duration", "-of", "json", video)
	data, err := probe.Output()
	if err != nil {
		return fmt.Errorf("ffprobe rejected rendered video: %w", err)
	}
	var parsed struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width, Height int
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	validVideo, validAudio := false, false
	for _, stream := range parsed.Streams {
		if stream.CodecType == "video" && stream.Width > 0 && stream.Height > 0 {
			validVideo = true
		}
		if stream.CodecType == "audio" {
			validAudio = true
		}
	}
	if !validVideo || !validAudio || parsed.Format.Duration == "" || parsed.Format.Duration == "0" {
		return fmt.Errorf("rendered MP4 lacks valid video dimensions, audio stream, or duration")
	}
	videoDuration, parseErr := strconv.ParseFloat(parsed.Format.Duration, 64)
	if parseErr != nil {
		return fmt.Errorf("invalid rendered video duration: %w", parseErr)
	}
	audioProbe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", narration)
	audioData, err := audioProbe.Output()
	if err != nil {
		return fmt.Errorf("ffprobe rejected narration WAV: %w", err)
	}
	var audioInfo struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(audioData, &audioInfo); err != nil {
		return err
	}
	audioDuration, parseErr := strconv.ParseFloat(audioInfo.Format.Duration, 64)
	if parseErr != nil || videoDuration+0.05 < audioDuration {
		return fmt.Errorf("rendered video duration %.3fs is shorter than narration %.3fs", videoDuration, audioDuration)
	}
	ffmpeg := filepath.Join(filepath.Dir(ffprobe), "ffmpeg.exe")
	seek := coverTimestamp(videoDuration)
	cmd := exec.CommandContext(ctx, ffmpeg, "-y", "-ss", strconv.FormatFloat(seek, 'f', 3, 64), "-i", video, "-frames:v", "1", cover)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cover extraction failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func coverTimestamp(duration float64) float64 {
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return 0
	}
	return math.Min(0.5, duration*0.25)
}

func verifyNarration(ctx context.Context, ffprobe, narration string) error {
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-select_streams", "a:0", "-show_entries", "stream=codec_type", "-of", "json", narration)
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe rejected generated narration: %w", err)
	}
	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if len(result.Streams) == 0 || result.Streams[0].CodecType != "audio" {
		return fmt.Errorf("generated narration has no decodable audio stream")
	}
	return nil
}
