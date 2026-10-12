package mediaworker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AssetSpec refs are relative to data/assets before binding and to the run
// directory after SnapshotAssets. Only the latter form is sent to a renderer.
type AssetSpec struct {
	ID        string `json:"id"`
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

type SceneAssetBinding struct{ SceneID, AssetID string }

type assetSnapshotManifest struct {
	Version       int         `json:"version"`
	JobID         string      `json:"jobId"`
	RequestSHA256 string      `json:"requestSHA256"`
	Assets        []AssetSpec `json:"assets"`
}

var assetIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var reservedAssetDevice = regexp.MustCompile(`^(COM|LPT)[0-9]$`)

func ValidateAssetRequest(assets []AssetSpec, bindings []SceneAssetBinding, defaultID string) error {
	if err := validateAssetDefinitions(assets); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, a := range assets {
		ids[a.ID] = true
	}
	if defaultID != "" && !ids[defaultID] {
		return fmt.Errorf("unknown default asset %q", defaultID)
	}
	if defaultID == "" && len(bindings) == 0 {
		return fmt.Errorf("task assets require scene bindings or default_asset_id")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if b.SceneID == "" || seen[b.SceneID] || !ids[b.AssetID] {
			return fmt.Errorf("invalid or duplicate scene binding %q", b.SceneID)
		}
		seen[b.SceneID] = true
	}
	return nil
}

func (s ProductionSpec) HasTaskAssets() bool {
	if s.Assets != nil {
		return true
	}
	for _, scene := range s.Scenes {
		if scene.AssetRef != "" {
			return true
		}
	}
	return false
}

func RequireTaskAssetWorker(worker Worker) error {
	support, ok := worker.(interface{ SupportsTaskAssets() bool })
	if !ok || !support.SupportsTaskAssets() {
		return &CapabilityBlockerError{Blocker: CapabilityBlocker{Capability: "task_assets", Message: "Selected media worker does not support task asset snapshots and scene bindings.", Remediation: "Select the OpenMontage registry renderer."}}
	}
	return nil
}

func validateAssetDefinitions(assets []AssetSpec) error {
	if len(assets) == 0 {
		return fmt.Errorf("task assets cannot be empty")
	}
	ids := map[string]bool{}
	for _, a := range assets {
		foldedID := strings.ToLower(a.ID)
		if !assetIDPattern.MatchString(a.ID) || isReservedAssetDevice(a.ID) || ids[foldedID] {
			return fmt.Errorf("invalid or duplicate asset ID %q", a.ID)
		}
		ids[foldedID] = true
		if err := validateAssetRef(a.Ref); err != nil {
			return fmt.Errorf("asset %s: %w", a.ID, err)
		}
		kind, mime := assetFileType(a.Ref)
		if mime == "" || kind != a.Kind {
			return fmt.Errorf("asset %s has unsupported kind/extension %q/%q", a.ID, a.Kind, filepath.Ext(a.Ref))
		}
		if a.SHA256 != "" {
			decoded, err := hex.DecodeString(a.SHA256)
			if err != nil || len(decoded) != sha256.Size {
				return fmt.Errorf("asset %s has invalid SHA-256", a.ID)
			}
		}
	}
	return nil
}

// BindSceneAssets never invents a default. Every scene and narration segment
// must have an unambiguous, explicitly requested asset binding.
func BindSceneAssets(spec *ProductionSpec, bindings []SceneAssetBinding, defaultID string) error {
	if err := validateAssetDefinitions(spec.Assets); err != nil {
		return err
	}
	assets := map[string]bool{}
	for _, a := range spec.Assets {
		assets[a.ID] = true
	}
	if defaultID != "" && !assets[defaultID] {
		return fmt.Errorf("unknown default asset %q", defaultID)
	}
	scenes := map[string]int{}
	for i, scene := range spec.Scenes {
		if scene.ID == "" {
			return fmt.Errorf("scene ID is empty")
		}
		if _, exists := scenes[scene.ID]; exists {
			return fmt.Errorf("duplicate scene ID %q", scene.ID)
		}
		scenes[scene.ID] = i
		spec.Scenes[i].AssetRef = defaultID
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		i, exists := scenes[b.SceneID]
		if !exists || seen[b.SceneID] {
			return fmt.Errorf("unknown or duplicate scene binding %q", b.SceneID)
		}
		if !assets[b.AssetID] {
			return fmt.Errorf("scene %s references unknown asset %q", b.SceneID, b.AssetID)
		}
		seen[b.SceneID] = true
		spec.Scenes[i].AssetRef = b.AssetID
	}
	return validateSceneAssetRefs(*spec)
}

func validateSceneAssetRefs(spec ProductionSpec) error {
	assets := map[string]bool{}
	for _, a := range spec.Assets {
		assets[a.ID] = true
	}
	segments := map[string]bool{}
	for _, seg := range spec.Segments {
		if seg.ID == "" || segments[seg.ID] {
			return fmt.Errorf("empty or duplicate segment ID %q", seg.ID)
		}
		segments[seg.ID] = true
	}
	bound := map[string]bool{}
	scenes := map[string]bool{}
	for _, scene := range spec.Scenes {
		if scene.ID == "" || scenes[scene.ID] {
			return fmt.Errorf("empty or duplicate scene ID %q", scene.ID)
		}
		scenes[scene.ID] = true
		if !assets[scene.AssetRef] {
			return fmt.Errorf("scene %s has no valid assetRef", scene.ID)
		}
		if len(scene.SegmentIDs) == 0 {
			return fmt.Errorf("scene %s has no segments", scene.ID)
		}
		for _, id := range scene.SegmentIDs {
			if !segments[id] || bound[id] {
				return fmt.Errorf("scene %s has unknown or duplicate segment binding %q", scene.ID, id)
			}
			bound[id] = true
		}
	}
	for id := range segments {
		if !bound[id] {
			return fmt.Errorf("segment %s has no scene asset binding", id)
		}
	}
	return nil
}

func validateAssetRef(ref string) error {
	if ref == "" || strings.ContainsAny(ref, "\\:\x00?#") || filepath.IsAbs(ref) {
		return fmt.Errorf("asset ref must be a relative slash-separated path: %q", ref)
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return fmt.Errorf("unsafe asset ref %q", ref)
		}
		if isReservedAssetDevice(part) {
			return fmt.Errorf("reserved asset path %q", ref)
		}
	}
	return nil
}

func isReservedAssetDevice(name string) bool {
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	return base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || reservedAssetDevice.MatchString(base)
}

func resolveAbsoluteAssetPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func assetFileType(path string) (string, string) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image", "image/png"
	case ".jpg", ".jpeg":
		return "image", "image/jpeg"
	case ".webp":
		return "image", "image/webp"
	case ".mp4":
		return "video", "video/mp4"
	default:
		return "", ""
	}
}

func withinAssetRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func resolvedAssetPath(root, ref string) (string, error) {
	if err := validateAssetRef(ref); err != nil {
		return "", err
	}
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	base, err = filepath.Abs(base)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(base, filepath.FromSlash(ref)))
	if err != nil {
		return "", err
	}
	if !withinAssetRoot(base, path) {
		return "", fmt.Errorf("asset symlink escapes allowed root")
	}
	return path, nil
}

func safeInputsDir(runDir string) (string, error) {
	root, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolved != root {
		return "", fmt.Errorf("run directory must not traverse symlinks")
	}
	inputs := filepath.Join(root, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	resolved, err = filepath.EvalSymlinks(inputs)
	if err != nil || resolved != inputs {
		return "", fmt.Errorf("inputs directory must not traverse symlinks")
	}
	return inputs, nil
}

func AssetSnapshotManifestPath(runDir string) string {
	return filepath.Join(runDir, "inputs", "assets", "manifest.json")
}

// SnapshotAssets commits a complete directory in one rename. An existing
// manifest is authoritative; retries never re-read or replace source assets.
func SnapshotAssets(dataDir, runDir string, spec *ProductionSpec) error {
	if err := validateAssetDefinitions(spec.Assets); err != nil {
		return err
	}
	if err := validateSceneAssetRefs(*spec); err != nil {
		return err
	}
	fingerprintData, err := json.Marshal(struct {
		JobID  string
		Assets []AssetSpec
		Scenes []SceneSpec
	}{spec.JobID, spec.Assets, spec.Scenes})
	if err != nil {
		return err
	}
	fingerprint := sha256.Sum256(fingerprintData)
	requestHash := hex.EncodeToString(fingerprint[:])
	inputs, err := safeInputsDir(runDir)
	if err != nil {
		return err
	}
	dest := filepath.Join(inputs, "assets")
	load := func() error {
		manifestPath, err := resolvedAssetPath(runDir, "inputs/assets/manifest.json")
		if err != nil {
			return err
		}
		if manifestPath != filepath.Join(dest, "manifest.json") {
			return fmt.Errorf("snapshot manifest must not be a symlink")
		}
		info, err := os.Stat(manifestPath)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return fmt.Errorf("invalid snapshot manifest")
		}
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return err
		}
		var manifest assetSnapshotManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return err
		}
		if manifest.Version != 1 || manifest.JobID != spec.JobID || manifest.RequestSHA256 != requestHash || len(manifest.Assets) != len(spec.Assets) {
			return fmt.Errorf("asset snapshot does not match this request's bindings")
		}
		for i, a := range manifest.Assets {
			requested := spec.Assets[i]
			if a.ID != requested.ID || a.Kind != requested.Kind || a.Ref != snapshotAssetRef(requested) || (requested.SHA256 != "" && !strings.EqualFold(a.SHA256, requested.SHA256)) {
				return fmt.Errorf("snapshot asset %s differs from request", a.ID)
			}
		}
		candidate := *spec
		candidate.Assets = manifest.Assets
		if _, err := VerifyAssetSnapshots(candidate, runDir); err != nil {
			return err
		}
		spec.Assets = manifest.Assets
		return nil
	}
	if _, err := os.Lstat(dest); err == nil {
		return load()
	} else if !os.IsNotExist(err) {
		return err
	}
	sourceRoot, err := resolvedAssetPath(dataDir, "assets")
	if err != nil {
		return fmt.Errorf("asset source root: %w", err)
	}
	stage, err := os.MkdirTemp(inputs, ".assets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	manifest := assetSnapshotManifest{Version: 1, JobID: spec.JobID, RequestSHA256: requestHash}
	for _, requested := range spec.Assets {
		path, err := resolvedAssetPath(sourceRoot, requested.Ref)
		if err != nil {
			return fmt.Errorf("asset %s: %w", requested.ID, err)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxServedProductionAsset {
			file.Close()
			return fmt.Errorf("asset %s must be a nonempty regular file within 2 GiB", requested.ID)
		}
		ref := snapshotAssetRef(requested)
		out, err := os.OpenFile(filepath.Join(stage, filepath.Base(ref)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			file.Close()
			return err
		}
		_, copyErr := io.Copy(out, io.LimitReader(file, maxServedProductionAsset+1))
		syncErr := out.Sync()
		closeErr := out.Close()
		file.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		actual, err := inspectAsset(filepath.Join(stage, filepath.Base(ref)), requested)
		if err != nil {
			return err
		}
		actual.Ref = ref
		manifest.Assets = append(manifest.Assets, actual)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomicAssetFile(stage, "manifest.json", encoded); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		if _, statErr := os.Lstat(dest); statErr == nil {
			return load()
		}
		return err
	}
	spec.Assets = manifest.Assets
	return nil
}

func snapshotAssetRef(a AssetSpec) string {
	return "inputs/assets/" + a.ID + strings.ToLower(filepath.Ext(a.Ref))
}

func inspectAsset(path string, expected AssetSpec) (AssetSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return AssetSpec{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxServedProductionAsset {
		return AssetSpec{}, fmt.Errorf("asset %s is empty, nonregular, or too large", expected.ID)
	}
	var prefix [512]byte
	n, err := file.Read(prefix[:])
	if err != nil && err != io.EOF {
		return AssetSpec{}, err
	}
	kind, mime := assetFileType(expected.Ref)
	if mime == "" || kind != expected.Kind || http.DetectContentType(prefix[:n]) != mime {
		return AssetSpec{}, fmt.Errorf("asset %s content does not match declared kind/extension", expected.ID)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return AssetSpec{}, err
	}
	hash := sha256.New()
	bytes, err := io.Copy(hash, io.LimitReader(file, maxServedProductionAsset+1))
	if err != nil {
		return AssetSpec{}, err
	}
	if bytes != info.Size() || bytes > maxServedProductionAsset {
		return AssetSpec{}, fmt.Errorf("asset %s changed during read", expected.ID)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expected.SHA256 != "" && !strings.EqualFold(expected.SHA256, digest) {
		return AssetSpec{}, fmt.Errorf("asset %s SHA-256 mismatch", expected.ID)
	}
	return AssetSpec{ID: expected.ID, Ref: expected.Ref, Kind: kind, SHA256: digest, Bytes: bytes, MediaType: mime}, nil
}

func VerifyAssetSnapshots(spec ProductionSpec, runDir string) (map[string]string, error) {
	if err := validateAssetDefinitions(spec.Assets); err != nil {
		return nil, err
	}
	if err := validateSceneAssetRefs(spec); err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, a := range spec.Assets {
		if a.Ref != snapshotAssetRef(a) || a.SHA256 == "" || a.Bytes <= 0 {
			return nil, fmt.Errorf("asset %s is not a bound snapshot", a.ID)
		}
		path, err := resolvedAssetPath(runDir, a.Ref)
		if err != nil {
			return nil, err
		}
		absolute, err := filepath.Abs(filepath.Join(runDir, filepath.FromSlash(a.Ref)))
		if err != nil || path != absolute {
			return nil, fmt.Errorf("snapshot asset %s must not be a symlink", a.ID)
		}
		actual, err := inspectAsset(path, a)
		if err != nil {
			return nil, err
		}
		if actual.Bytes != a.Bytes || actual.MediaType != a.MediaType {
			return nil, fmt.Errorf("snapshot asset %s metadata mismatch", a.ID)
		}
		paths[a.ID] = path
	}
	return paths, nil
}

func writeAtomicAssetFile(dir, name string, data []byte) error {
	file, err := os.CreateTemp(dir, ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), filepath.Join(dir, name))
}

func PersistProductionSpec(runDir string, spec ProductionSpec) (string, error) {
	inputs, err := safeInputsDir(runDir)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomicAssetFile(inputs, "production-spec.json", data); err != nil {
		return "", err
	}
	return filepath.Join(inputs, "production-spec.json"), nil
}
