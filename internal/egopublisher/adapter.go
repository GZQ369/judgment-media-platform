// Package egopublisher adapts the v2 video-publisher/Ego runtime to the
// publication transaction core. It is deliberately fail-closed: a draft is
// not a publication, and a click without platform evidence is not success.
package egopublisher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GZQ369/judgment-media-platform/internal/publication"
)

const (
	maxCommandOutput = 64 << 10
	maxDOMEvidence   = 4096
	maxExternalValue = 512
)

const (
	bridgeEvidenceSourceDOM    = "platform-dom"
	bridgeEvidenceSourceDetail = "platform-detail-url"
)

// Config contains every dependency needed by the adapter. In particular, the
// adapter never resolves paths or credentials from process-wide environment
// variables.
type Config struct {
	NodePath                string
	PublisherScript         string
	EgoCommand              string
	StateRoot               string
	DataDir                 string
	Timeout                 time.Duration
	OriginalRightsConfirmed bool
	Runner                  CommandRunner
}

// CommandOutput is the bounded result of one child process.
type CommandOutput struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// CommandRunner is injectable so tests can exercise safety decisions without
// starting Node, a browser bridge, or a real creator page.
type CommandRunner func(context.Context, string, []string, []byte) (CommandOutput, error)

// Adapter implements publication.Adapter for one configured platform.
type Adapter struct {
	config   Config
	platform publication.Platform
	runner   CommandRunner
	refs     *referenceStore
}

// New constructs an adapter for one platform. The variadic form also permits
// callers to construct an unbound adapter for capability inspection; such an
// adapter refuses Commit until a platform is supplied by New.
func New(config Config, platforms ...publication.Platform) (*Adapter, error) {
	if len(platforms) > 1 {
		return nil, errors.New("egopublisher: exactly one platform is allowed")
	}
	platform := publication.Platform("")
	if len(platforms) == 1 {
		platform = platforms[0]
	}
	if platform != "" && !supported(platform) {
		return nil, fmt.Errorf("egopublisher: unsupported platform %q", platform)
	}
	runner := config.Runner
	if runner == nil {
		runner = runCommand
	}
	var refs *referenceStore
	if strings.TrimSpace(config.StateRoot) != "" {
		var err error
		refs, err = openReferenceStore(config.StateRoot, platform)
		if err != nil {
			return nil, err
		}
	}
	return &Adapter{config: config, platform: platform, runner: runner, refs: refs}, nil
}

// Close releases the durable task-space reference database. It is safe to
// call on an unconfigured adapter and is used by the server shutdown path.
func (a *Adapter) Close() error {
	if a == nil || a.refs == nil {
		return nil
	}
	return a.refs.close()
}

// NewAdapter is an explicit constructor alias for callers that prefer a
// descriptive name.
func NewAdapter(config Config, platform publication.Platform) (*Adapter, error) {
	return New(config, platform)
}

// NewEgoAdapter is kept as a readable constructor for integration code.
func NewEgoAdapter(config Config, platform publication.Platform) (*Adapter, error) {
	return New(config, platform)
}

func (a *Adapter) Platform() publication.Platform { return a.platform }

func (a *Adapter) Capability(ctx context.Context) publication.Capability {
	capability := publication.Capability{Platform: a.platform}
	if a.platform == "" {
		capability.Reason = "platform is required"
		return capability
	}
	if err := ctx.Err(); err != nil {
		capability.Reason = "capability check cancelled"
		return capability
	}
	if err := validateConfig(a.config); err != nil {
		capability.Reason = err.Error()
		return capability
	}
	if !a.config.OriginalRightsConfirmed {
		capability.Reason = "original-rights confirmation is required"
		return capability
	}
	capability.Configured = true
	return capability
}

func (a *Adapter) Commit(ctx context.Context, req publication.AdapterRequest) (publication.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return publication.Evidence{}, err
	}
	if req.Package.Platform != a.platform {
		return publication.Evidence{}, fmt.Errorf("egopublisher: package platform %q does not match adapter %q", req.Package.Platform, a.platform)
	}
	if err := validateConfig(a.config); err != nil {
		return publication.Evidence{}, err
	}
	if !a.config.OriginalRightsConfirmed {
		return publication.Evidence{}, errors.New("egopublisher: original-rights confirmation is required")
	}
	if err := validatePackage(req.Package); err != nil {
		return publication.Evidence{}, err
	}

	packagePath, err := a.writeContentPackage(req)
	if err != nil {
		return publication.Evidence{}, err
	}
	defer os.Remove(packagePath)

	draftOutput, err := a.run(ctx, a.config.NodePath, publisherArgs(a.platform, a.config.PublisherScript, packagePath), nil)
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: prepare draft: %w", err)
	}
	draft, err := parseDraftSummary(draftOutput.Stdout)
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: prepare draft: %w", err)
	}
	if draft.Platform != "" && draft.Platform != string(a.platform) {
		return publication.Evidence{}, fmt.Errorf("egopublisher: draft platform %q does not match %q", draft.Platform, a.platform)
	}
	if !draft.Ready {
		return publication.Evidence{}, fmt.Errorf("egopublisher: draft is not ready: %s", draft.Status)
	}
	if draft.TaskSpaceID == "" {
		return publication.Evidence{}, errors.New("egopublisher: draft did not return taskSpaceId")
	}
	if a.refs == nil {
		return publication.Evidence{}, errors.New("egopublisher: durable reference store is not configured")
	}
	binding, err := packageBinding(req.Package)
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: package binding: %w", err)
	}
	if err := a.refs.put(req.PublicationID, req.Reservation, binding, draft.TaskSpaceID); err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: persist task-space reference: %w", err)
	}

	finalOutput, err := a.run(ctx, a.config.EgoCommand, nil, []byte(egoPublishProgram(a.platform, draft.TaskSpaceID, finalLabel(a.platform))))
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: final publish operation: %w", err)
	}
	return parseVerifiedEvidence(a.platform, finalOutput.Stdout, draft.TaskSpaceID)
}

// Reconcile only inspects the existing task space. It never calls
// publishOnce, and therefore cannot submit a second publication.
func (a *Adapter) Reconcile(ctx context.Context, req publication.AdapterRequest) (publication.Evidence, error) {
	if err := ctx.Err(); err != nil {
		return publication.Evidence{}, err
	}
	if err := validateConfig(a.config); err != nil {
		return publication.Evidence{}, err
	}
	if !a.config.OriginalRightsConfirmed {
		return publication.Evidence{}, errors.New("egopublisher: original-rights confirmation is required")
	}
	if a.refs == nil {
		return publication.Evidence{}, errors.New("egopublisher: durable reference store is not configured")
	}
	binding, err := packageBinding(req.Package)
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: package binding: %w", err)
	}
	ref, err := a.refs.get(req.PublicationID, req.Reservation, binding)
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: load task-space reference: %w", err)
	}
	output, err := a.run(ctx, a.config.EgoCommand, nil, []byte(egoInspectProgram(a.platform, ref)))
	if err != nil {
		return publication.Evidence{}, fmt.Errorf("egopublisher: reconcile: %w", err)
	}
	return parseReconcileEvidence(a.platform, output.Stdout, ref)
}

func supported(platform publication.Platform) bool {
	switch platform {
	case publication.PlatformDouyin, publication.PlatformXiaohongshu, publication.PlatformBilibili:
		return true
	default:
		return false
	}
}

func finalLabel(platform publication.Platform) string {
	switch platform {
	case publication.PlatformDouyin:
		return "发布"
	case publication.PlatformXiaohongshu:
		return "发布笔记"
	case publication.PlatformBilibili:
		return "立即投稿"
	default:
		return ""
	}
}

func publisherArgs(platform publication.Platform, script, packagePath string) []string {
	return []string{script, "--platform", string(platform), "--content-package", packagePath, "--draft-only"}
}

func validateConfig(config Config) error {
	if config.Timeout <= 0 {
		return errors.New("egopublisher: timeout must be positive")
	}
	for name, path := range map[string]string{
		"NodePath":        config.NodePath,
		"PublisherScript": config.PublisherScript,
		"EgoCommand":      config.EgoCommand,
	} {
		if err := validateFile(name, path); err != nil {
			return err
		}
	}
	for name, path := range map[string]string{"StateRoot": config.StateRoot, "DataDir": config.DataDir} {
		if err := validateDir(name, path); err != nil {
			return err
		}
	}
	return nil
}

func validateFile(name, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("egopublisher: %s is required", name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("egopublisher: %s must be an absolute path", name)
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("egopublisher: %s is unavailable: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("egopublisher: %s is not a regular file", name)
	}
	return nil
}

func validateDir(name, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("egopublisher: %s is required", name)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("egopublisher: %s must be an absolute path", name)
	}
	info, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("egopublisher: %s is unavailable: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("egopublisher: %s is not a directory", name)
	}
	return nil
}

func validatePackage(pkg publication.Package) error {
	if strings.TrimSpace(pkg.Video) == "" {
		return errors.New("egopublisher: video path is required")
	}
	if !filepath.IsAbs(pkg.Video) {
		return errors.New("egopublisher: video path must be absolute")
	}
	info, err := os.Stat(pkg.Video)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("egopublisher: video path is unavailable")
	}
	if strings.TrimSpace(pkg.Copy) == "" {
		return errors.New("egopublisher: copy is required")
	}
	if strings.TrimSpace(pkg.Title) == "" {
		return errors.New("egopublisher: title is required")
	}
	if strings.TrimSpace(pkg.Cover) == "" || !filepath.IsAbs(pkg.Cover) || !regularFile(pkg.Cover) {
		return errors.New("egopublisher: cover path is unavailable")
	}
	if pkg.CoverSHA256 != "" && !matchesSHA(pkg.Cover, pkg.CoverSHA256) {
		return errors.New("egopublisher: bound cover digest does not match")
	}
	for _, asset := range pkg.CoverAssets {
		if strings.TrimSpace(asset.Role) == "" || !filepath.IsAbs(asset.Path) || !regularFile(asset.Path) {
			return errors.New("egopublisher: bound cover asset is unavailable")
		}
		if !matchesSHA(asset.Path, asset.SHA256) {
			return fmt.Errorf("egopublisher: bound cover asset %q digest does not match", asset.Role)
		}
	}
	if pkg.VideoSHA256 != "" && !matchesSHA(pkg.Video, pkg.VideoSHA256) {
		return errors.New("egopublisher: bound video digest does not match")
	}
	return nil
}

type contentPackage struct {
	Platform          publication.Platform `json:"platform"`
	Video             string               `json:"video"`
	Cover             string               `json:"cover,omitempty"`
	CoverPortrait3x4  string               `json:"cover_portrait_3x4,omitempty"`
	CoverLandscape4x3 string               `json:"cover_landscape_4x3,omitempty"`
	Copy              string               `json:"copy"`
	Title             string               `json:"title,omitempty"`
	Tags              []string             `json:"tags,omitempty"`
}

func (a *Adapter) writeContentPackage(req publication.AdapterRequest) (string, error) {
	dir := filepath.Join(filepath.Clean(a.config.DataDir), "egopublisher")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("egopublisher: create content-package directory: %w", err)
	}
	portrait := boundCover(req.Package, "portrait_3x4")
	landscape := boundCover(req.Package, "landscape_4x3")
	cover := req.Package.Cover
	if a.platform == publication.PlatformBilibili && landscape != "" {
		cover = landscape
	} else if a.platform != publication.PlatformBilibili && portrait != "" {
		cover = portrait
	}
	value := contentPackage{
		Platform:          a.platform,
		Video:             req.Package.Video,
		Cover:             cover,
		CoverPortrait3x4:  portrait,
		CoverLandscape4x3: landscape,
		Copy:              req.Package.Copy,
		Title:             req.Package.Title,
		Tags:              append([]string(nil), req.Package.Tags...),
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("egopublisher: encode content package: %w", err)
	}
	f, err := os.CreateTemp(dir, "content-package-*.json")
	if err != nil {
		return "", fmt.Errorf("egopublisher: create content package: %w", err)
	}
	name := f.Name()
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("egopublisher: secure content package: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("egopublisher: write content package: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("egopublisher: close content package: %w", err)
	}
	return name, nil
}

func boundCover(pkg publication.Package, role string) string {
	for _, asset := range pkg.CoverAssets {
		if asset.Role == role {
			return asset.Path
		}
	}
	return ""
}

func matchesSHA(path, declared string) bool {
	if strings.TrimSpace(declared) == "" {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), declared)
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (a *Adapter) run(ctx context.Context, command string, args []string, stdin []byte) (CommandOutput, error) {
	if a.config.Timeout <= 0 {
		return CommandOutput{}, errors.New("egopublisher: timeout must be positive")
	}
	commandCtx, cancel := context.WithTimeout(ctx, a.config.Timeout)
	defer cancel()
	output, err := a.runner(commandCtx, command, args, stdin)
	if err != nil {
		return CommandOutput{}, err
	}
	if len(output.Stdout) > maxCommandOutput || len(output.Stderr) > maxCommandOutput {
		return CommandOutput{}, errors.New("command output exceeded safety limit")
	}
	if output.ExitCode != 0 {
		return CommandOutput{}, fmt.Errorf("command exited with code %d", output.ExitCode)
	}
	return output, nil
}

func runCommand(ctx context.Context, command string, args []string, stdin []byte) (CommandOutput, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	stdout := &boundedBuffer{}
	stderr := &boundedBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.truncated || stderr.truncated {
		return CommandOutput{}, errors.New("command output exceeded safety limit")
	}
	result := CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

type boundedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len() >= maxCommandOutput {
		b.truncated = true
		return len(p), nil
	}
	remaining := maxCommandOutput - b.Len()
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

type draftSummary struct {
	Ready       bool
	Status      string
	Platform    string
	TaskSpaceID string
}

func parseDraftSummary(output string) (draftSummary, error) {
	value, err := lastJSONObject(output)
	if err != nil {
		return draftSummary{}, err
	}
	value = unwrap(value)
	draft := draftSummary{Status: lowerString(value, "status", "state")}
	draft.Platform = lowerString(value, "platform")
	draft.TaskSpaceID = stringValue(value, "taskSpaceId", "task_space_id", "taskSpace")
	draft.Ready = boolValue(value, "ready", "draftReady") || readyStatus(draft.Status)
	if nested, ok := value["summary"].(map[string]any); ok {
		if draft.Status == "" {
			draft.Status = lowerString(nested, "status", "state")
		}
		if draft.Platform == "" {
			draft.Platform = lowerString(nested, "platform")
		}
		if draft.TaskSpaceID == "" {
			draft.TaskSpaceID = stringValue(nested, "taskSpaceId", "task_space_id", "taskSpace")
		}
		draft.Ready = draft.Ready || boolValue(nested, "ready", "draftReady") || readyStatus(draft.Status)
	}
	if !draft.Ready {
		return draft, nil
	}
	if draft.TaskSpaceID == "" {
		return draft, errors.New("draft summary is missing taskSpaceId")
	}
	return draft, nil
}

func readyStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ready", "draft_ready", "draft-ready", "prepared":
		return true
	default:
		return false
	}
}

func egoPublishProgram(platform publication.Platform, taskSpaceID, label string) string {
	ref := jsonStringOrNumber(taskSpaceID)
	encodedLabel, _ := json.Marshal(label)
	encodedPlatform, _ := json.Marshal(string(platform))
	return fmt.Sprintf(`const ref = %s;
const selected = await useOrCreateTaskSpace(ref, %s);
if (String(selected?.id) !== String(ref)) throw new Error("task space mismatch");
const claimed = await claimTaskSpace(ref, %s);
if (String(claimed?.id) !== String(ref)) throw new Error("task space claim mismatch");
const publish = await publishOnce(%s, { authorized: true });
const tabs = await listTabs();
const page = await pageInfo();
	cliLog(JSON.stringify({platform: %q, taskSpaceId: ref, publish, page, tabs}));
`, ref, encodedPlatform, encodedPlatform, encodedLabel, string(platform))
}

func egoInspectProgram(platform publication.Platform, taskSpaceID string) string {
	ref := jsonStringOrNumber(taskSpaceID)
	encodedPlatform, _ := json.Marshal(string(platform))
	return fmt.Sprintf(`const ref = %s;
const selected = await claimTaskSpace(ref, %s);
if (String(selected?.id) !== String(ref)) throw new Error("task space claim mismatch");
const tabs = await listTabs();
const page = await pageInfo();
const evidence = await inspectPublication();
	cliLog(JSON.stringify({platform: %q, taskSpaceId: ref, page, evidence, tabs}));
`, ref, encodedPlatform, string(platform))
}

func jsonStringOrNumber(value string) string {
	if n, err := strconv.Atoi(value); err == nil && n >= 0 {
		return strconv.Itoa(n)
	}
	b, _ := json.Marshal(value)
	return string(b)
}

func parseVerifiedEvidence(platform publication.Platform, output, expectedTaskSpaceID string) (publication.Evidence, error) {
	value, err := lastJSONObject(output)
	if err != nil {
		return publication.Evidence{}, err
	}
	publish := mapValue(value, "publish", "result")
	if len(publish) == 0 {
		return publication.Evidence{}, errors.New("egopublisher: final result is missing bridge publish evidence")
	}
	dom := mapValue(publish, "dom")
	if evidencePlatform := evidencePlatform(publish, value, dom); evidencePlatform != string(platform) {
		return publication.Evidence{}, errors.New("egopublisher: final evidence is not bound to the prepared platform")
	}
	if taskSpace := stringValue(publish, "taskSpaceId", "task_space_id", "taskSpace"); taskSpace == "" || taskSpace != expectedTaskSpaceID {
		return publication.Evidence{}, errors.New("egopublisher: final evidence is not bound to the prepared task space")
	}
	if err := requireSubmissionCorrelation(publish, expectedTaskSpaceID, "final"); err != nil {
		return publication.Evidence{}, err
	}
	if !trustedBridgeEvidence(publish) {
		return publication.Evidence{}, errors.New("egopublisher: final result has no trusted bridge evidence source")
	}
	status := publication.Status(statusFromValues(publish))
	if !acceptableStatus(status) {
		return publication.Evidence{}, errors.New("egopublisher: final result has no platform success, reviewing, or processing evidence")
	}
	clicked := boolValue(publish, "finalPublishClicked")
	if !clicked {
		return publication.Evidence{}, errors.New("egopublisher: final publish was not confirmed clicked")
	}
	externalID := stableExternalID(firstValue(publish, "externalID", "externalId", "external_id", "itemId", "item_id"))
	externalURL := platformWorkURL(platform, firstValue(publish, "externalURL", "externalUrl", "external_url"))
	if externalID == "" && externalURL == "" {
		externalID = stableExternalID(firstValue(dom, "externalID", "externalId", "external_id", "itemId", "item_id"))
		externalURL = platformWorkURL(platform, firstValue(dom, "externalURL", "externalUrl", "external_url"))
	}
	if externalURL == "" {
		externalURL = platformWorkURL(platform, firstValue(publish, "url", "href"))
	}
	if externalURL == "" {
		externalURL = platformWorkURL(platform, firstValue(dom, "url", "href"))
	}
	if externalID == "" && externalURL == "" {
		return publication.Evidence{}, errors.New("egopublisher: final result has no external URL or ID")
	}
	if status == publication.StatusReviewing || status == publication.StatusPublished || status == publication.StatusSubmitted {
		return publication.Evidence{Status: status, ExternalID: externalID, URL: externalURL, Verified: true, Proof: "final publish click and platform status evidence"}, nil
	}
	return publication.Evidence{}, errors.New("egopublisher: unsupported verified final status")
}

func parseReconcileEvidence(platform publication.Platform, output, expectedTaskSpaceID string) (publication.Evidence, error) {
	value, err := lastJSONObject(output)
	if err != nil {
		return publication.Evidence{}, err
	}
	evidence := mapValue(value, "evidence", "publication")
	if len(evidence) == 0 {
		return publication.Evidence{}, errors.New("egopublisher: reconcile result is missing bridge evidence")
	}
	if evidencePlatform := evidencePlatform(evidence, value); evidencePlatform != string(platform) {
		return publication.Evidence{}, errors.New("egopublisher: reconcile evidence is not bound to the prepared platform")
	}
	if taskSpace := stringValue(evidence, "taskSpaceId", "task_space_id", "taskSpace"); taskSpace == "" || taskSpace != expectedTaskSpaceID {
		return publication.Evidence{}, errors.New("egopublisher: reconcile evidence is not bound to the prepared task space")
	}
	if err := requireSubmissionCorrelation(evidence, expectedTaskSpaceID, "reconcile"); err != nil {
		return publication.Evidence{}, err
	}
	if !trustedBridgeEvidence(evidence) {
		return publication.Evidence{}, errors.New("egopublisher: reconcile found no trusted bridge evidence source")
	}
	status := publication.Status(statusFromValues(evidence))
	if !acceptableStatus(status) {
		return publication.Evidence{}, errors.New("egopublisher: reconcile found no platform status evidence")
	}
	externalID := stableExternalID(firstValue(evidence, "externalID", "externalId", "external_id", "itemId", "item_id"))
	externalURL := platformWorkURL(platform, firstValue(evidence, "externalURL", "externalUrl", "external_url"))
	if externalID == "" && externalURL == "" {
		return publication.Evidence{}, errors.New("egopublisher: reconcile found no external URL or ID")
	}
	return publication.Evidence{Status: status, ExternalID: externalID, URL: externalURL, Verified: true, Proof: "read-only task-space status evidence"}, nil
}

func requireSubmissionCorrelation(value map[string]any, expectedTaskSpaceID, phase string) error {
	correlation := mapValue(value, "submissionCorrelation")
	if len(correlation) == 0 {
		return fmt.Errorf("egopublisher: %s evidence is missing submission correlation", phase)
	}
	if version := stringValue(correlation, "version"); version != "1" {
		return fmt.Errorf("egopublisher: %s evidence has unsupported submission correlation version", phase)
	}
	if boundedValue(stringValue(correlation, "id", "attemptId", "attemptID")) == "" {
		return fmt.Errorf("egopublisher: %s evidence has no submission correlation id", phase)
	}
	if pageID := boundedValue(stringValue(correlation, "pageId", "pageID")); pageID == "" {
		return fmt.Errorf("egopublisher: %s evidence has no correlated page", phase)
	}
	if taskSpaceID := stringValue(correlation, "taskSpaceId", "task_space_id", "taskSpace"); taskSpaceID == "" || taskSpaceID != expectedTaskSpaceID {
		return fmt.Errorf("egopublisher: %s evidence correlation is not bound to the prepared task space", phase)
	}
	return nil
}

func trustedBridgeEvidence(value map[string]any) bool {
	source := stringValue(value, "evidenceSource", "source")
	return source == bridgeEvidenceSourceDOM || source == bridgeEvidenceSourceDetail
}

func evidencePlatform(values ...map[string]any) string {
	for _, value := range values {
		if platform := stringValue(value, "platform", "platformID", "platform_id"); platform != "" {
			return strings.ToLower(platform)
		}
	}
	return ""
}

func evidenceTaskSpaceID(values ...map[string]any) string {
	for _, value := range values {
		if id := stringValue(value, "taskSpaceId", "task_space_id", "taskSpace", "spaceId", "space_id"); id != "" {
			return id
		}
		for _, nestedKey := range []string{"publish", "result", "page", "dom", "evidence"} {
			if nested, ok := value[nestedKey].(map[string]any); ok {
				if id := stringValue(nested, "taskSpaceId", "task_space_id", "taskSpace", "spaceId", "space_id"); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func stableExternalID(value string) string {
	value = boundedValue(value)
	if len(value) < 3 {
		return ""
	}
	switch strings.ToLower(value) {
	case "success", "succeeded", "processing", "processed", "reviewing", "pending", "ready", "unknown":
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.:", character) {
			continue
		}
		return ""
	}
	return value
}

func platformWorkURL(platform publication.Platform, value string) string {
	value = boundedValue(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	path := strings.Trim(parsed.EscapedPath(), "/")
	if host == "" || path == "" {
		return ""
	}
	allowed := false
	switch platform {
	case publication.PlatformDouyin:
		allowed = (host == "www.douyin.com" || host == "m.douyin.com") && detailPath(path, "video/", "note/")
	case publication.PlatformXiaohongshu:
		allowed = (host == "www.xiaohongshu.com" || host == "xhslink.com") && detailPath(path, "explore/", "discovery/item/")
	case publication.PlatformBilibili:
		allowed = host == "www.bilibili.com" && detailPath(path, "video/")
	}
	if !allowed {
		return ""
	}
	return value
}

func detailPath(path string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) && strings.Trim(strings.TrimPrefix(path, prefix), "/") != "" {
			return true
		}
	}
	return false
}

func acceptableStatus(status publication.Status) bool {
	switch status {
	case publication.StatusSubmitted, publication.StatusReviewing, publication.StatusPublished:
		return true
	default:
		return false
	}
}

func statusFromValues(values ...map[string]any) string {
	for _, value := range values {
		for _, key := range []string{"status", "state", "publicationStatus", "publication_status"} {
			status := normalizeStatus(stringValue(value, key))
			if status != "" {
				return status
			}
		}
	}
	return ""
}

func normalizeStatus(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "submitted":
		return string(publication.StatusSubmitted)
	case "reviewing":
		return string(publication.StatusReviewing)
	case "published":
		return string(publication.StatusPublished)
	default:
		return ""
	}
}

func lastJSONObject(output string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	var last map[string]any
	for {
		var value any
		if err := decoder.Decode(&value); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// The child may have emitted human-readable lines before JSON.
			last = nil
			break
		}
		if object, ok := value.(map[string]any); ok {
			last = object
		}
	}
	if last == nil {
		lines := strings.Split(output, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			var value map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &value) == nil && value != nil {
				return value, nil
			}
		}
		return nil, errors.New("command output did not contain a JSON summary")
	}
	return last, nil
}

func unwrap(value map[string]any) map[string]any {
	for _, key := range []string{"summary", "result", "draft"} {
		if nested, ok := value[key].(map[string]any); ok && len(nested) > 0 {
			merged := make(map[string]any, len(value)+len(nested))
			for k, v := range value {
				merged[k] = v
			}
			for k, v := range nested {
				merged[k] = v
			}
			return merged
		}
	}
	return value
}

func mapValue(value map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if nested, ok := value[key].(map[string]any); ok {
			return nested
		}
	}
	return map[string]any{}
}

func stringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			switch item := raw.(type) {
			case string:
				return strings.TrimSpace(item)
			case float64:
				return strconv.FormatInt(int64(item), 10)
			case json.Number:
				return item.String()
			}
		}
	}
	return ""
}

func firstValue(value map[string]any, keys ...string) string { return stringValue(value, keys...) }

func lowerString(value map[string]any, keys ...string) string {
	return strings.ToLower(stringValue(value, keys...))
}

func boolValue(value map[string]any, keys ...string) bool {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			if parsed, ok := raw.(bool); ok {
				return parsed
			}
		}
	}
	return false
}

func boundedValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxExternalValue {
		return ""
	}
	if !utf8.ValidString(value) {
		return ""
	}
	return value
}

func boundedURL(value string) string {
	value = boundedValue(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	return value
}
