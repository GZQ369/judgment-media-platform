package render

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"vertical-intel-video-agent/internal/domain"
)

type OpenMontageRenderer struct {
	RootDir string
	DataDir string
}

type explainerProps struct {
	Theme    string         `json:"theme"`
	Cuts     []explainerCut `json:"cuts"`
	Overlays []any          `json:"overlays"`
	Captions []wordCaption  `json:"captions"`
	Audio    audioConfig    `json:"audio"`
}

type audioConfig struct {
	Narration *audioLayer `json:"narration,omitempty"`
	Music     *musicLayer `json:"music,omitempty"`
}

type audioLayer struct {
	Src    string  `json:"src"`
	Volume float64 `json:"volume,omitempty"`
}

type musicLayer struct {
	Src            string  `json:"src"`
	Volume         float64 `json:"volume,omitempty"`
	FadeInSeconds  float64 `json:"fadeInSeconds,omitempty"`
	FadeOutSeconds float64 `json:"fadeOutSeconds,omitempty"`
	Loop           bool    `json:"loop,omitempty"`
}

type wordCaption struct {
	Word    string `json:"word"`
	StartMs int    `json:"startMs"`
	EndMs   int    `json:"endMs"`
}

type explainerCut struct {
	ID                   string         `json:"id"`
	Source               string         `json:"source"`
	Type                 string         `json:"type"`
	InSeconds            float64        `json:"in_seconds"`
	OutSeconds           float64        `json:"out_seconds"`
	SourceInSeconds      float64        `json:"source_in_seconds,omitempty"`
	Text                 string         `json:"text,omitempty"`
	Subtitle             string         `json:"subtitle,omitempty"`
	Title                string         `json:"title,omitempty"`
	CalloutType          string         `json:"callout_type,omitempty"`
	AccentColor          string         `json:"accentColor,omitempty"`
	BackgroundColor      string         `json:"backgroundColor,omitempty"`
	BackgroundImage      string         `json:"backgroundImage,omitempty"`
	BackgroundVideo      string         `json:"backgroundVideo,omitempty"`
	BackgroundVideoStart float64        `json:"backgroundVideoStart,omitempty"`
	BackgroundOverlay    float64        `json:"backgroundOverlay,omitempty"`
	Color                string         `json:"color,omitempty"`
	LeftLabel            string         `json:"leftLabel,omitempty"`
	LeftValue            string         `json:"leftValue,omitempty"`
	RightLabel           string         `json:"rightLabel,omitempty"`
	RightValue           string         `json:"rightValue,omitempty"`
	Animation            string         `json:"animation,omitempty"`
	Progress             float64        `json:"progress,omitempty"`
	ProgressLabel        string         `json:"progressLabel,omitempty"`
	ProgressColor        string         `json:"progressColor,omitempty"`
	ChartData            []kpiChartData `json:"chartData,omitempty"`
	Columns              int            `json:"columns,omitempty"`
	ChartColors          []string       `json:"chartColors,omitempty"`
	ChartAnimation       string         `json:"chartAnimation,omitempty"`
}

type kpiChartData struct {
	Label  string  `json:"label"`
	Value  float64 `json:"value"`
	Change float64 `json:"change"`
	Suffix string  `json:"suffix"`
}

type openMontageRequestManifest struct {
	ProjectID             string                          `json:"project_id"`
	Pipeline              string                          `json:"pipeline"`
	AspectRatio           string                          `json:"aspect_ratio"`
	OpenMontageRoot       string                          `json:"openmontage_root"`
	PipelineManifestPath  string                          `json:"pipeline_manifest_path,omitempty"`
	SourceImages          []string                        `json:"source_images,omitempty"`
	SourceVideos          []string                        `json:"source_videos,omitempty"`
	NarrationText         string                          `json:"narration_text"`
	ProviderCapabilities  map[string]bool                 `json:"provider_capabilities"`
	ProviderEnvNames      map[string]string               `json:"provider_env_names"`
	RequestedCapabilities domain.RenderOpenMontageRequest `json:"requested_capabilities"`
	GeneratedArtifacts    map[string]string               `json:"generated_artifacts"`
	Notes                 []string                        `json:"notes"`
}

func NewOpenMontageRenderer(rootDir, dataDir string) *OpenMontageRenderer {
	if rootDir == "" {
		rootDir = getenv("OPENMONTAGE_DIR", "D:/agent/video-workers/OpenMontage")
	}
	if dataDir == "" {
		dataDir = getenv("VIDEO_FACTORY_DATA_DIR", "D:/agent/vertical-intel-video-agent/data")
	}
	return &OpenMontageRenderer{RootDir: filepath.Clean(rootDir), DataDir: filepath.Clean(dataDir)}
}

func (r *OpenMontageRenderer) RenderProject(ctx context.Context, project domain.VideoProject, req domain.RenderOpenMontageRequest) (domain.RenderResult, error) {
	if project.Brief == nil {
		return domain.RenderResult{}, fmt.Errorf("project brief is required before rendering")
	}
	if req.Pipeline == "" {
		req.Pipeline = "openmontage-remotion-explainer"
	}
	if req.AspectRatio == "" {
		req.AspectRatio = "16:9"
	}
	composerDir := filepath.Join(r.RootDir, "remotion-composer")
	if _, err := os.Stat(filepath.Join(composerDir, "src", "index.tsx")); err != nil {
		return domain.RenderResult{}, fmt.Errorf("OpenMontage remotion composer not found under %s: %w", composerDir, err)
	}
	projectDir := filepath.Join(r.DataDir, "projects", project.ID)
	renderDir := filepath.Join(projectDir, "openmontage")
	exportDir := filepath.Join(projectDir, "exports")
	if err := os.MkdirAll(renderDir, 0o755); err != nil {
		return domain.RenderResult{}, err
	}
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return domain.RenderResult{}, err
	}

	propsPath := filepath.Join(renderDir, "openmontage_props.json")
	requestPath := filepath.Join(renderDir, "openmontage_request.json")
	videoPath := filepath.Join(exportDir, "openmontage_product_video.mp4")
	logPath := filepath.Join(renderDir, "openmontage_render.log")
	srtPath := filepath.Join(renderDir, "subtitles.srt")
	assPath := filepath.Join(renderDir, "subtitles.ass")
	narrationPath := filepath.Join(renderDir, "narration.mp3")
	musicPath := filepath.Join(renderDir, "background_music.mp3")
	pipelineManifest := r.pipelineManifestPath(req.Pipeline)
	caps := providerCapabilities()
	narrationText := narrationText(project)

	if req.GenerateSubtitles {
		if err := writeSRT(srtPath, project); err != nil {
			return domain.RenderResult{}, err
		}
		if err := writeASS(assPath, project); err != nil {
			return domain.RenderResult{}, err
		}
	}
	if req.GenerateTTS {
		voice := req.TTSVoice
		if voice == "" {
			voice = "zh-CN-XiaoxiaoNeural"
		}
		if err := generateEdgeTTS(ctx, narrationText, voice, narrationPath); err != nil {
			return domain.RenderResult{}, fmt.Errorf("generate edge-tts narration: %w", err)
		}
	}
	if req.BackgroundMusicPath != "" {
		musicPath = filepath.Clean(req.BackgroundMusicPath)
	} else if req.GenerateBackgroundMusic {
		if err := generateBackgroundMusic(ctx, musicPath, project.DurationSeconds); err != nil {
			return domain.RenderResult{}, fmt.Errorf("generate background music: %w", err)
		}
	}

	propsReq := req
	stagedImages, stagedVideos, stagedNarration, stagedMusic, err := r.stageRemotionAssets(project.ID, composerDir, req.ImagePaths, req.VideoPaths, narrationPath, musicPath)
	if err != nil {
		return domain.RenderResult{}, err
	}
	propsReq.ImagePaths = stagedImages
	propsReq.VideoPaths = stagedVideos
	props := buildExplainerProps(project, propsReq, stagedNarration, stagedMusic)
	if !fileExists(narrationPath) {
		props.Audio.Narration = nil
		narrationPath = ""
	}
	if !fileExists(musicPath) {
		props.Audio.Music = nil
		musicPath = ""
	}
	if req.GenerateSubtitles {
		props.Captions = buildWordCaptions(narrationText, project.DurationSeconds)
	}
	payload, err := json.MarshalIndent(props, "", "  ")
	if err != nil {
		return domain.RenderResult{}, err
	}
	if err := os.WriteFile(propsPath, payload, 0o644); err != nil {
		return domain.RenderResult{}, err
	}

	manifest := openMontageRequestManifest{
		ProjectID:             project.ID,
		Pipeline:              req.Pipeline,
		AspectRatio:           req.AspectRatio,
		OpenMontageRoot:       r.RootDir,
		PipelineManifestPath:  pipelineManifest,
		SourceImages:          cleanPaths(req.ImagePaths),
		SourceVideos:          cleanPaths(req.VideoPaths),
		NarrationText:         narrationText,
		ProviderCapabilities:  caps,
		ProviderEnvNames:      providerEnvNames(),
		RequestedCapabilities: req,
		GeneratedArtifacts: map[string]string{
			"props":            propsPath,
			"srt":              srtPath,
			"ass":              assPath,
			"narration":        narrationPath,
			"background_music": musicPath,
		},
		Notes: providerNotes(req, caps),
	}
	manifestPayload, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(requestPath, manifestPayload, 0o644)

	npx := "npx"
	if runtime.GOOS == "windows" {
		npx = "npx.cmd"
	}
	fullSummaryPath := ""
	openMontageProjectDir := ""
	if strings.EqualFold(req.ProviderMode, "full_openmontage_pipeline") || strings.EqualFold(req.ProviderMode, "strict_full_pipeline") {
		fullSummaryPath = filepath.Join(renderDir, "full_pipeline_summary.json")
		err = r.runFullPipeline(ctx, requestPath, propsPath, videoPath, fullSummaryPath, req.Pipeline)
		openMontageProjectDir = readFullPipelineProjectDir(fullSummaryPath)
	} else {
		cmd := exec.CommandContext(ctx, npx, "remotion", "render", "src/index.tsx", "Explainer", videoPath, "--props", propsPath, "--codec", "h264")
		cmd.Dir = composerDir
		cmd.Env = os.Environ()
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err = cmd.Run()
		logContent := out.String()
		_ = os.WriteFile(logPath, []byte(logContent), 0o644)
	}

	result := domain.RenderResult{
		ProjectID:             project.ID,
		Renderer:              "openmontage",
		Status:                "completed",
		Pipeline:              req.Pipeline,
		PipelineManifestPath:  pipelineManifest,
		PropsPath:             propsPath,
		RequestPath:           requestPath,
		VideoPath:             videoPath,
		LogPath:               logPath,
		NarrationPath:         narrationPath,
		BackgroundMusicPath:   musicPath,
		SRTPath:               valueIf(req.GenerateSubtitles, srtPath),
		ASSPath:               valueIf(req.GenerateSubtitles, assPath),
		ImagePaths:            cleanPaths(req.ImagePaths),
		VideoPaths:            cleanPaths(req.VideoPaths),
		ProviderCapabilities:  caps,
		FullPipelineSummary:   fullSummaryPath,
		OpenMontageProjectDir: openMontageProjectDir,
		DurationS:             project.DurationSeconds,
		GeneratedAt:           time.Now().UTC(),
		CommandSummary:        fmt.Sprintf("%s remotion render src/index.tsx Explainer %s --props %s --codec h264", npx, videoPath, propsPath),
	}
	if err != nil {
		result.Status = "failed"
		result.Error = err.Error()
		return result, fmt.Errorf("OpenMontage render failed: %w; see %s", err, logPath)
	}
	if info, statErr := os.Stat(videoPath); statErr != nil || info.Size() == 0 {
		result.Status = "failed"
		result.Error = "expected mp4 was not created"
		return result, fmt.Errorf("OpenMontage render finished but output missing: %s", videoPath)
	}
	return result, nil
}

func (r *OpenMontageRenderer) runFullPipeline(ctx context.Context, requestPath, propsPath, videoPath, summaryPath, pipeline string) error {
	pythonPath := filepath.Join(r.RootDir, ".venv", "Scripts", "python.exe")
	if runtime.GOOS != "windows" {
		pythonPath = filepath.Join(r.RootDir, ".venv", "bin", "python")
	}
	if !fileExists(pythonPath) {
		pythonPath = "python"
	}
	scriptPath := filepath.Join(r.RootDir, "video_factory_pipeline_runner.py")
	cmd := exec.CommandContext(ctx, pythonPath, scriptPath, "--request", requestPath, "--props", propsPath, "--output", videoPath, "--pipeline", pipeline, "--render-runtime", "remotion", "--summary", summaryPath)
	cmd.Dir = r.RootDir
	cmd.Env = os.Environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	_ = os.WriteFile(filepath.Join(filepath.Dir(requestPath), "full_pipeline_runner.log"), out.Bytes(), 0o644)
	if err != nil {
		return fmt.Errorf("full OpenMontage pipeline runner failed: %w", err)
	}
	return nil
}

func readFullPipelineProjectDir(summaryPath string) string {
	if summaryPath == "" {
		return ""
	}
	payload, err := os.ReadFile(summaryPath)
	if err != nil {
		return ""
	}
	var summary struct {
		ProjectDir string `json:"project_dir"`
	}
	if json.Unmarshal(payload, &summary) != nil {
		return ""
	}
	return summary.ProjectDir
}

func (r *OpenMontageRenderer) stageRemotionAssets(projectID, composerDir string, images, videos []string, narrationPath, musicPath string) ([]string, []string, string, string, error) {
	publicDir := filepath.Join(composerDir, "public", "video-factory", projectID)
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return nil, nil, "", "", err
	}
	stageOne := func(src string, prefix string) (string, error) {
		if strings.TrimSpace(src) == "" {
			return "", nil
		}
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "data:") {
			return src, nil
		}
		src = filepath.Clean(src)
		ext := filepath.Ext(src)
		if ext == "" {
			ext = ".bin"
		}
		dstName := fmt.Sprintf("%s_%d%s", prefix, time.Now().UnixNano(), ext)
		dst := filepath.Join(publicDir, dstName)
		if err := copyFile(src, dst); err != nil {
			return "", err
		}
		return strings.ReplaceAll("video-factory/"+projectID+"/"+dstName, "\\", "/"), nil
	}
	var stagedImages []string
	for i, src := range cleanPaths(images) {
		staged, err := stageOne(src, fmt.Sprintf("image_%02d", i+1))
		if err != nil {
			return nil, nil, "", "", err
		}
		stagedImages = append(stagedImages, staged)
	}
	var stagedVideos []string
	for i, src := range cleanPaths(videos) {
		staged, err := stageOne(src, fmt.Sprintf("video_%02d", i+1))
		if err != nil {
			return nil, nil, "", "", err
		}
		stagedVideos = append(stagedVideos, staged)
	}
	stagedNarration, err := stageOne(narrationPath, "narration")
	if err != nil {
		return nil, nil, "", "", err
	}
	stagedMusic, err := stageOne(musicPath, "music")
	if err != nil {
		return nil, nil, "", "", err
	}
	return stagedImages, stagedVideos, stagedNarration, stagedMusic, nil
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o644)
}

func (r *OpenMontageRenderer) pipelineManifestPath(pipeline string) string {
	pipeline = strings.TrimSpace(pipeline)
	aliases := map[string]string{
		"openmontage-remotion-explainer": "hybrid.yaml",
		"xiaohongshu-product-seed":       "hybrid.yaml",
		"douyin-commerce-hook":           "clip-factory.yaml",
		"talking-head":                   "talking-head.yaml",
		"hybrid":                         "hybrid.yaml",
		"clip-factory":                   "clip-factory.yaml",
		"documentary-montage":            "documentary-montage.yaml",
	}
	file := aliases[pipeline]
	if file == "" && strings.HasSuffix(pipeline, ".yaml") {
		file = pipeline
	}
	if file == "" {
		file = "hybrid.yaml"
	}
	path := filepath.Join(r.RootDir, "pipeline_defs", file)
	if fileExists(path) {
		return path
	}
	return ""
}

func buildExplainerProps(project domain.VideoProject, req domain.RenderOpenMontageRequest, narrationPath, musicPath string) explainerProps {
	brief := project.Brief
	title := project.Product.Title
	images := cleanPaths(req.ImagePaths)
	videos := cleanPaths(req.VideoPaths)
	mediaFor := func(i int) (source string, bgImage string, bgVideo string) {
		if len(videos) > 0 {
			return "", "", videos[i%len(videos)]
		}
		if len(images) > 0 {
			return images[i%len(images)], "", ""
		}
		return "", "", ""
	}
	s1, _, bgv1 := mediaFor(0)
	s2, _, bgv2 := mediaFor(1)
	s3, _, bgv3 := mediaFor(2)
	cuts := []explainerCut{
		mediaCut("hook", s1, bgv1, "hero_title", 0, 4, first(brief.TitleOptions, title), first(brief.CoverTexts, "真实体验，先看这3点")),
		{
			ID: "core-point", Type: "callout", Source: s2, InSeconds: 4, OutSeconds: 9,
			Title: "核心卖点", Text: brief.ProductAnalysis.CoreSellingPoint, CalloutType: "tip",
			AccentColor: "#EC4899", BackgroundColor: "#0F172A", BackgroundVideo: bgv2, BackgroundOverlay: 0.62, Color: "#F8FAFC",
		},
		{
			ID: "compare", Type: "comparison", Source: s3, InSeconds: 9, OutSeconds: 15,
			Title: "先看适不适合", LeftLabel: "用户痛点", LeftValue: short(first(brief.ProductAnalysis.AudiencePainPoints, "怕买错"), 16),
			RightLabel: "解决方向", RightValue: short(first(brief.ProductAnalysis.FunctionalPoints, "真实体验"), 16),
			AccentColor: "#A78BFA", BackgroundColor: "#0F172A", BackgroundVideo: bgv3, BackgroundOverlay: 0.62, Color: "#F8FAFC",
		},
		{
			ID: "script", Type: "callout", InSeconds: 15, OutSeconds: 23,
			Title: "脚本 Hook", Text: firstScriptHook(brief), CalloutType: "info", AccentColor: "#22D3EE", BackgroundColor: "#0F172A", Color: "#F8FAFC",
		},
		{
			ID: "kpis", Type: "kpi_grid", InSeconds: 23, OutSeconds: 32, Title: "发布包已生成",
			ChartData: []kpiChartData{{Label: "标题", Value: float64(len(brief.TitleOptions)), Change: 20, Suffix: "个"}, {Label: "脚本", Value: float64(len(brief.Scripts)), Change: 10, Suffix: "版"}, {Label: "分镜", Value: float64(len(brief.Storyboard.Shots)), Change: 15, Suffix: "个"}},
			Columns:   3, ChartColors: []string{"#22D3EE", "#34D399", "#F59E0B"}, ChartAnimation: "cascade", BackgroundColor: "#0F172A",
		},
		{
			ID: "cta", Type: "hero_title", InSeconds: 32, OutSeconds: float64(max(project.DurationSeconds, 36)),
			Text: "先收藏，再按场景判断", Subtitle: strings.Join(brief.Tags, " #"), BackgroundColor: "#0F172A",
		},
	}
	audio := audioConfig{}
	if narrationPath != "" {
		audio.Narration = &audioLayer{Src: narrationPath, Volume: 1}
	}
	if musicPath != "" {
		audio.Music = &musicLayer{Src: musicPath, Volume: 0.08, FadeInSeconds: 1.5, FadeOutSeconds: 2, Loop: true}
	}
	return explainerProps{Theme: "flat-motion-graphics", Cuts: cuts, Overlays: []any{}, Captions: []wordCaption{}, Audio: audio}
}

func mediaCut(id, source, bgVideo, typ string, inS, outS float64, text, subtitle string) explainerCut {
	cut := explainerCut{ID: id, Type: typ, Source: source, InSeconds: inS, OutSeconds: outS, Text: text, Subtitle: subtitle, BackgroundColor: "#0F172A", Animation: "ken-burns"}
	if bgVideo != "" {
		cut.Source = ""
		cut.BackgroundVideo = bgVideo
		cut.BackgroundOverlay = 0.62
	}
	return cut
}

func narrationText(project domain.VideoProject) string {
	brief := project.Brief
	if brief == nil {
		return project.Product.Title
	}
	parts := []string{first(brief.TitleOptions, project.Product.Title), brief.ProductAnalysis.CoreSellingPoint, firstScriptHook(brief), "先收藏，再按自己的使用场景判断。"}
	return strings.Join(parts, "。")
}

func writeSRT(path string, project domain.VideoProject) error {
	text := narrationText(project)
	parts := splitCaptionText(text)
	if len(parts) == 0 {
		return os.WriteFile(path, []byte(""), 0o644)
	}
	dur := max(project.DurationSeconds, 36)
	seg := float64(dur) / float64(len(parts))
	var b strings.Builder
	for i, p := range parts {
		start := float64(i) * seg
		end := float64(i+1) * seg
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(start), srtTime(end), p)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func writeASS(path string, project domain.VideoProject) error {
	text := `[Script Info]
ScriptType: v4.00+
PlayResX: 1080
PlayResY: 1920

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Microsoft YaHei,58,&H00FFFFFF,&H000000FF,&H99000000,&H66000000,1,0,0,0,100,100,0,0,1,4,1,2,60,60,120,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
`
	parts := splitCaptionText(narrationText(project))
	dur := max(project.DurationSeconds, 36)
	seg := float64(dur) / float64(max(len(parts), 1))
	var b strings.Builder
	b.WriteString(text)
	for i, p := range parts {
		fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n", assTime(float64(i)*seg), assTime(float64(i+1)*seg), strings.ReplaceAll(p, "\n", "\\N"))
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func generateEdgeTTS(ctx context.Context, text, voice, outPath string) error {
	cmd := exec.CommandContext(ctx, "python", "-m", "edge_tts", "--voice", voice, "--text", text, "--write-media", outPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, out.String())
	}
	return nil
}

func generateBackgroundMusic(ctx context.Context, outPath string, seconds int) error {
	seconds = max(seconds, 36)
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-f", "lavfi", "-i", "sine=frequency=220:duration="+fmt.Sprint(seconds), "-filter:a", "volume=0.07", outPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, out.String())
	}
	return nil
}

func buildWordCaptions(text string, durationSeconds int) []wordCaption {
	parts := splitWords(text)
	if len(parts) == 0 {
		return nil
	}
	totalMs := max(durationSeconds, 36) * 1000
	step := totalMs / len(parts)
	captions := make([]wordCaption, 0, len(parts))
	for i, w := range parts {
		captions = append(captions, wordCaption{Word: w, StartMs: i * step, EndMs: (i + 1) * step})
	}
	return captions
}

func providerCapabilities() map[string]bool {
	caps := map[string]bool{}
	for provider, env := range providerEnvNames() {
		caps[provider] = os.Getenv(env) != ""
	}
	caps["edge_tts"] = commandAvailable("python")
	caps["ffmpeg"] = commandAvailable("ffmpeg")
	caps["remotion"] = commandAvailable("npx") || commandAvailable("npx.cmd")
	return caps
}

func providerEnvNames() map[string]string {
	return map[string]string{"openai": "OPENAI_API_KEY", "fal": "FAL_KEY", "elevenlabs": "ELEVENLABS_API_KEY", "pexels": "PEXELS_API_KEY", "pixabay": "PIXABAY_API_KEY", "unsplash": "UNSPLASH_ACCESS_KEY"}
}

func providerNotes(req domain.RenderOpenMontageRequest, caps map[string]bool) []string {
	var notes []string
	if req.GenerateAIImages && !caps["openai"] && !caps["fal"] {
		notes = append(notes, "AI image generation requested but OPENAI_API_KEY/FAL_KEY are not configured; using supplied/local visuals only.")
	}
	for _, p := range req.StockProviders {
		if !caps[strings.ToLower(p)] {
			notes = append(notes, fmt.Sprintf("Stock provider %s requested but its API key is not configured.", p))
		}
	}
	if !caps["elevenlabs"] {
		notes = append(notes, "ElevenLabs API key is not configured; local Edge TTS is used when generate_tts=true.")
	}
	sort.Strings(notes)
	return notes
}

func splitCaptionText(text string) []string {
	replacer := strings.NewReplacer("？", "。", "?", "。", "！", "。", "!", "。", "；", "。", ";", "。")
	chunks := strings.Split(replacer.Replace(text), "。")
	out := []string{}
	for _, c := range chunks {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func splitWords(text string) []string {
	fields := strings.Fields(text)
	if len(fields) > 1 {
		return fields
	}
	runes := []rune(text)
	out := []string{}
	for i := 0; i < len(runes); i += 4 {
		end := i + 4
		if end > len(runes) {
			end = len(runes)
		}
		chunk := strings.TrimSpace(string(runes[i:end]))
		if chunk != "" {
			out = append(out, chunk)
		}
	}
	return out
}

func srtTime(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	ms := int((d - time.Duration(s)*time.Second) / time.Millisecond)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func assTime(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	cs := int((d - time.Duration(s)*time.Second) / (10 * time.Millisecond))
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

func cleanPaths(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	return out
}

func first(items []string, fallback string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return fallback
}

func firstScriptHook(brief *domain.PublishingBrief) string {
	if brief != nil && len(brief.Scripts) > 0 && strings.TrimSpace(brief.Scripts[0].Hook) != "" {
		return brief.Scripts[0].Hook
	}
	return "买之前，先看真实使用场景。"
}

func short(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() >= 0
}

func valueIf(ok bool, v string) string {
	if ok {
		return v
	}
	return ""
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
