package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/GZQ369/judgment-media-platform/internal/mediaworker"
)

const maxProductionManifestBytes = 8 << 20
const maxProductionArtifactBytes = int64(2 << 30)

func validateProductionRender(ctx context.Context, manifest *mediaworker.ArtifactManifest, manifestPath, outputDir, ffprobe string) error {
	if manifest == nil {
		return fmt.Errorf("artifact manifest is missing")
	}
	manifestFile, err := resolveArtifactPath(outputDir, manifestPath)
	if err != nil {
		return fmt.Errorf("artifact manifest path is invalid: %w", err)
	}
	manifestInfo, err := os.Stat(manifestFile)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() <= 0 || manifestInfo.Size() > maxProductionManifestBytes {
		return fmt.Errorf("artifact manifest must be a non-empty regular file no larger than 8 MiB")
	}
	manifestData, err := os.ReadFile(manifestFile)
	if err != nil {
		return err
	}
	var diskManifest mediaworker.ArtifactManifest
	if err := json.Unmarshal(manifestData, &diskManifest); err != nil {
		return fmt.Errorf("decode on-disk artifact manifest: %w", err)
	}
	if !reflect.DeepEqual(diskManifest, *manifest) {
		return fmt.Errorf("in-memory manifest does not match the persisted manifest")
	}
	var video, cover, narration string
	ids := make(map[string]struct{}, len(manifest.Artifacts))
	for _, item := range manifest.Artifacts {
		if item.ID == "" || item.SHA256 == "" {
			return fmt.Errorf("artifact manifest entries must have stable IDs and SHA-256 hashes")
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("duplicate artifact manifest ID %q", item.ID)
		}
		ids[item.ID] = struct{}{}
		path, err := resolveArtifactPath(outputDir, item.Path)
		if err != nil {
			return fmt.Errorf("artifact %s path is invalid: %w", item.ID, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("artifact %s is missing: %w", item.ID, err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxProductionArtifactBytes {
			return fmt.Errorf("artifact %s is empty, not regular, or exceeds the 2 GiB limit", item.ID)
		}
		if item.Bytes != info.Size() || item.RelativePath == "" {
			return fmt.Errorf("artifact %s declared size/relative path does not match the file", item.ID)
		}
		relativePath, err := resolveArtifactPath(outputDir, filepath.Join(outputDir, filepath.FromSlash(item.RelativePath)))
		if err != nil || filepath.Clean(relativePath) != filepath.Clean(path) {
			return fmt.Errorf("artifact %s relative path does not resolve to its declared file", item.ID)
		}
		digest, err := fileSHA256(path)
		if err != nil || !strings.EqualFold(digest, item.SHA256) {
			return fmt.Errorf("artifact %s SHA-256 does not match its manifest", item.ID)
		}
		if isRenderedVideoArtifact(item) {
			video = path
		}
		if item.ID == "cover" && item.Kind == "cover" {
			cover = path
		}
		if item.ID == "narration" && item.Kind == "audio" {
			narration = path
		}
	}
	if video == "" || cover == "" || narration == "" {
		return fmt.Errorf("rendered-video, generated narration, and cover artifacts are all required")
	}
	if strings.TrimSpace(ffprobe) == "" {
		return fmt.Errorf("ffprobe is not configured")
	}
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "stream=codec_type,width,height", "-show_entries", "format=duration", "-of", "json", video)
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("ffprobe rejected newly rendered video: %w", err)
	}
	var result struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode ffprobe result: %w", err)
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || duration <= 0 {
		return fmt.Errorf("rendered video has invalid duration %q", result.Format.Duration)
	}
	videoOK, audioOK := false, false
	for _, stream := range result.Streams {
		if stream.CodecType == "video" && stream.Width > 0 && stream.Height > 0 {
			videoOK = true
		}
		if stream.CodecType == "audio" {
			audioOK = true
		}
	}
	if !videoOK || !audioOK {
		return fmt.Errorf("rendered MP4 must contain valid video dimensions and an audio stream")
	}
	audioProbe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "json", narration)
	audioData, err := audioProbe.Output()
	if err != nil {
		return fmt.Errorf("ffprobe rejected generated narration: %w", err)
	}
	var narrationInfo struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(audioData, &narrationInfo); err != nil {
		return err
	}
	narrationDuration, err := strconv.ParseFloat(narrationInfo.Format.Duration, 64)
	if err != nil || duration+0.05 < narrationDuration {
		return fmt.Errorf("rendered MP4 duration %.3fs is shorter than narration %.3fs", duration, narrationDuration)
	}
	return nil
}

func resolveArtifactPath(root, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("empty path")
	}
	rootPath, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootPath, candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootPath, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path escapes media output directory")
	}
	return resolved, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
