package mediaworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const (
	// ProductionSpecVersion is the input contract version emitted by this
	// adapter when callers leave the version empty.
	ProductionSpecVersion = "1.0"
	// ArtifactManifestVersion is the manifest version accepted by Run.
	ArtifactManifestVersion = "1.0"
	defaultTimeout          = 20 * time.Minute
	defaultMaxOutputBytes   = 1 << 20
)

var (
	// ErrTimeout indicates that the worker exceeded its configured deadline.
	ErrTimeout = errors.New("media worker timed out")
	// ErrNonZeroExit indicates that the worker process exited unsuccessfully.
	ErrNonZeroExit = errors.New("media worker exited unsuccessfully")
	// ErrInvalidJSON indicates that a successful worker process did not emit a
	// valid JSON response.
	ErrInvalidJSON = errors.New("media worker emitted invalid JSON")
	// ErrOutputLimit indicates that stdout or stderr exceeded its configured cap.
	ErrOutputLimit = errors.New("media worker output exceeded limit")
	// ErrCapabilityBlocked indicates an actionable missing capability.
	ErrCapabilityBlocked = errors.New("media worker capability blocked")
	// ErrPathEscape indicates that a job, output, manifest, or artifact escaped
	// the configured data root/output directory.
	ErrPathEscape = errors.New("media worker path escapes allowed directory")
	// ErrInvalidManifest indicates that the worker manifest failed validation.
	ErrInvalidManifest = errors.New("invalid media worker artifact manifest")
	// ErrValidationWroteOutput indicates that validate-only changed the output
	// directory, violating the no-artifacts validation contract.
	ErrValidationWroteOutput = errors.New("media worker validation wrote output")
)

// Worker is the narrow interface consumed by Go orchestration code.
type Worker interface {
	Validate(context.Context, ProductionSpec, string) (Result, error)
	Run(context.Context, ProductionSpec, string) (Result, error)
}

// CommandWorker invokes the repository's Python worker without a shell.
// PythonPath and ScriptPath may live outside DataRoot; generated job files and
// caller-provided output directories may not.
type CommandWorker struct {
	// PythonPath is the Python executable or command name.
	PythonPath string
	// ScriptPath is the media_worker.py entrypoint.
	ScriptPath string
	// DataRoot is the only allowed parent for generated jobs and output.
	DataRoot string
	// Timeout bounds the complete worker process. Zero selects the default.
	Timeout time.Duration

	// MaxStdoutBytes and MaxStderrBytes prevent an untrusted worker from
	// filling memory with diagnostics. Zero selects the one-megabyte default.
	MaxStdoutBytes int
	MaxStderrBytes int

	runner commandRunner
}

// Validate checks a production specification and required capabilities. The
// Python worker's validate-only mode must not create the output directory or
// artifacts.
func (w CommandWorker) Validate(ctx context.Context, spec ProductionSpec, outputDir string) (Result, error) {
	return w.execute(ctx, spec, outputDir, true)
}

// Run executes the media worker and validates the returned artifact manifest
// before exposing its path to callers.
func (w CommandWorker) Run(ctx context.Context, spec ProductionSpec, outputDir string) (Result, error) {
	return w.execute(ctx, spec, outputDir, false)
}

type commandOutput struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type commandRunner interface {
	Run(context.Context, []string, int, int) (commandOutput, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, args []string, stdoutLimit, stderrLimit int) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	stdout := &limitedBuffer{limit: stdoutLimit}
	stderr := &limitedBuffer{limit: stderrLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return commandOutput{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode(cmd)}, fmt.Errorf("%w: stdout=%t stderr=%t", ErrOutputLimit, stdout.exceeded, stderr.exceeded)
	}
	return commandOutput{stdout: stdout.Bytes(), stderr: stderr.Bytes(), exitCode: exitCode(cmd)}, err
}

func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return 0, nil
	}
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.exceeded = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.exceeded = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func (w CommandWorker) execute(parent context.Context, spec ProductionSpec, outputDir string, validateOnly bool) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	root, err := w.dataRoot()
	if err != nil {
		return Result{}, err
	}
	output, err := pathWithin(root, outputDir)
	if err != nil {
		return Result{}, fmt.Errorf("output: %w", err)
	}
	if err := w.validateConfig(); err != nil {
		return Result{}, err
	}
	var before outputSnapshot
	if validateOnly {
		before, err = snapshotOutput(output)
		if err != nil {
			return Result{}, err
		}
	}
	jobPath, err := w.writeJob(root, spec)
	if err != nil {
		return Result{}, err
	}
	args := []string{w.PythonPath, w.ScriptPath, "--job", jobPath, "--output", output}
	if validateOnly {
		args = append(args, "--validate-only")
	}

	timeout := w.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	runner := w.runner
	if runner == nil {
		if _, lookErr := exec.LookPath(w.PythonPath); lookErr != nil {
			blocker := CapabilityBlocker{
				Capability:  "python",
				Message:     fmt.Sprintf("Python executable %q is unavailable: %v", w.PythonPath, lookErr),
				Remediation: "Set PythonPath to an installed Python executable.",
			}
			return Result{Blockers: []CapabilityBlocker{blocker}}, &CapabilityBlockerError{Blocker: blocker, ExitCode: -1}
		}
		runner = execCommandRunner{}
	}
	result, err := runner.Run(ctx, args, w.outputLimit(w.MaxStdoutBytes), w.outputLimit(w.MaxStderrBytes))
	if validateOnly {
		after, snapshotErr := snapshotOutput(output)
		if snapshotErr != nil {
			return Result{}, snapshotErr
		}
		if !reflect.DeepEqual(before, after) {
			return Result{}, ErrValidationWroteOutput
		}
	}
	if err != nil {
		if errors.Is(err, ErrOutputLimit) {
			return Result{}, err
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("%w: after %s", ErrTimeout, timeout)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return Result{}, ctx.Err()
		}
		if validateOnly {
			if parsed, parseErr := parseValidationResult(result.stdout); parseErr == nil && len(parsed.Blockers) > 0 {
				return parsed, &CapabilityBlockerError{Blocker: parsed.Blockers[0], ExitCode: result.exitCode}
			}
		}
		if blocker, ok := parseBlocker(result.stderr); ok {
			return Result{Blockers: []CapabilityBlocker{blocker}}, &CapabilityBlockerError{Blocker: blocker, ExitCode: result.exitCode}
		}
		return Result{}, &ExitError{Code: result.exitCode, Stderr: strings.TrimSpace(string(result.stderr))}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("%w: after %s", ErrTimeout, timeout)
	}

	if validateOnly {
		parsed, err := parseValidationResult(result.stdout)
		if err != nil {
			return Result{}, err
		}
		if len(parsed.Blockers) > 0 {
			return parsed, &CapabilityBlockerError{Blocker: parsed.Blockers[0], ExitCode: result.exitCode}
		}
		return parsed, nil
	}
	parsed, err := parseRunResult(result.stdout)
	if err != nil {
		return Result{}, err
	}
	manifest, manifestPath, err := loadManifest(output, parsed.ArtifactManifestPath)
	if err != nil {
		return Result{}, err
	}
	if parsed.JobID == "" {
		parsed.JobID = manifest.JobID
	}
	parsed.ArtifactManifestPath = manifestPath
	parsed.OutputDir = output
	parsed.Manifest = &manifest
	return parsed, nil
}

type outputSnapshot struct {
	exists  bool
	entries map[string]string
}

func snapshotOutput(path string) (outputSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return outputSnapshot{}, nil
	}
	if err != nil {
		return outputSnapshot{}, fmt.Errorf("snapshot validation output: %w", err)
	}
	snapshot := outputSnapshot{exists: true, entries: map[string]string{".": fileFingerprint(info, "")}}
	if !info.IsDir() {
		return snapshot, nil
	}
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		fingerprint := fileFingerprint(entryInfo, "")
		if !entry.IsDir() {
			content, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(content)
			fingerprint += ":" + hex.EncodeToString(digest[:])
		}
		snapshot.entries[relative] = fingerprint
		return nil
	})
	if err != nil {
		return outputSnapshot{}, fmt.Errorf("snapshot validation output: %w", err)
	}
	return snapshot, nil
}

func fileFingerprint(info os.FileInfo, target string) string {
	return fmt.Sprintf("%s:%d:%d:%s", info.Mode().String(), info.Size(), info.ModTime().UnixNano(), target)
}

func (w CommandWorker) validateConfig() error {
	if strings.TrimSpace(w.PythonPath) == "" {
		return errors.New("media worker PythonPath must not be empty")
	}
	if strings.TrimSpace(w.ScriptPath) == "" {
		return errors.New("media worker ScriptPath must not be empty")
	}
	if _, err := os.Stat(w.ScriptPath); err != nil {
		return fmt.Errorf("media worker script: %w", err)
	}
	return nil
}

func (w CommandWorker) dataRoot() (string, error) {
	if strings.TrimSpace(w.DataRoot) == "" {
		return "", errors.New("media worker DataRoot must not be empty")
	}
	root, err := filepath.Abs(filepath.Clean(w.DataRoot))
	if err != nil {
		return "", fmt.Errorf("resolve DataRoot: %w", err)
	}
	return filepath.Clean(root), nil
}

func (w CommandWorker) outputLimit(value int) int {
	if value > 0 {
		return value
	}
	return defaultMaxOutputBytes
}

func (w CommandWorker) writeJob(root string, spec ProductionSpec) (string, error) {
	if strings.TrimSpace(spec.ProductionSpecVersion) == "" {
		spec.ProductionSpecVersion = ProductionSpecVersion
	}
	if strings.TrimSpace(spec.JobID) == "" {
		return "", errors.New("ProductionSpec JobID must not be empty")
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return "", fmt.Errorf("marshal ProductionSpec: %w", err)
	}
	jobDir := filepath.Join(root, ".mediaworker", "jobs")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", fmt.Errorf("create worker job directory: %w", err)
	}
	digest := sha256.Sum256(payload)
	name := fmt.Sprintf("%s-%s.json", safeName(spec.JobID), hex.EncodeToString(digest[:])[:12])
	jobPath := filepath.Join(jobDir, name)
	if _, err := pathWithin(root, jobPath); err != nil {
		return "", err
	}
	if err := os.WriteFile(jobPath, append(payload, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write worker job: %w", err)
	}
	return jobPath, nil
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), ".-")
	if name == "" {
		return "job"
	}
	return name
}

func pathWithin(root, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", fmt.Errorf("%w: empty path", ErrPathEscape)
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	candidateAbs, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", err
	}
	rootResolved, err := resolveExistingPrefix(rootAbs)
	if err != nil {
		return "", err
	}
	candidateResolved, err := resolveExistingPrefix(candidateAbs)
	if err != nil {
		return "", err
	}
	if !isWithin(rootAbs, candidateAbs) || !isWithin(rootResolved, candidateResolved) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, candidate)
	}
	return candidateAbs, nil
}

func isWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveExistingPrefix resolves the longest existing prefix of a path and
// then appends the non-existing suffix. This closes the symlink-parent gap for
// output directories that have not been created yet.
func resolveExistingPrefix(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		if info, err := os.Lstat(current); err == nil {
			resolved := current
			if info.Mode()&os.ModeSymlink != 0 {
				resolved, err = filepath.EvalSymlinks(current)
				if err != nil {
					return "", fmt.Errorf("resolve symlinks %s: %w", current, err)
				}
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("lstat %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("cannot resolve path prefix %s", path)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func parseValidationResult(data []byte) (Result, error) {
	var raw struct {
		JobID    string              `json:"jobId"`
		Valid    bool                `json:"valid"`
		Blockers []CapabilityBlocker `json:"blockers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	return Result{OK: raw.Valid, Valid: raw.Valid, JobID: raw.JobID, Blockers: raw.Blockers}, nil
}

func parseRunResult(data []byte) (Result, error) {
	var raw struct {
		OK         bool   `json:"ok"`
		JobID      string `json:"jobId"`
		Output     string `json:"output"`
		Manifest   string `json:"artifactManifest"`
		DurationMs int64  `json:"durationMs"`
		Segments   int    `json:"segments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if !raw.OK {
		return Result{}, fmt.Errorf("%w: worker response did not report success", ErrInvalidJSON)
	}
	return Result{OK: raw.OK, Valid: true, JobID: raw.JobID, OutputDir: raw.Output, ArtifactManifestPath: raw.Manifest, DurationMs: raw.DurationMs, Segments: raw.Segments}, nil
}

func parseBlocker(data []byte) (CapabilityBlocker, bool) {
	var raw struct {
		Error struct {
			Code        string `json:"code"`
			Capability  string `json:"capability"`
			Message     string `json:"message"`
			Remediation string `json:"remediation"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &raw) != nil || raw.Error.Code != "capability_blocker" || raw.Error.Capability == "" {
		return CapabilityBlocker{}, false
	}
	return CapabilityBlocker{Capability: raw.Error.Capability, Message: raw.Error.Message, Remediation: raw.Error.Remediation}, true
}

// ExitError records a worker process failure and preserves stderr for logs.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("%v: exit code %d", ErrNonZeroExit, e.Code)
	}
	return fmt.Sprintf("%v: exit code %d: %s", ErrNonZeroExit, e.Code, e.Stderr)
}

func (e *ExitError) Unwrap() error { return ErrNonZeroExit }

// CapabilityBlockerError carries the original capability, message, and
// remediation while still identifying the failed process exit.
type CapabilityBlockerError struct {
	Blocker  CapabilityBlocker
	ExitCode int
}

func (e *CapabilityBlockerError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrCapabilityBlocked, e.Blocker.Capability, e.Blocker.Message)
}

func (e *CapabilityBlockerError) Unwrap() error {
	return errors.Join(ErrCapabilityBlocked, ErrNonZeroExit)
}

func loadManifest(outputDir, manifestPath string) (ArtifactManifest, string, error) {
	if manifestPath == "" {
		return ArtifactManifest{}, "", fmt.Errorf("%w: missing artifact manifest path", ErrInvalidManifest)
	}
	manifestPath, err := pathWithin(outputDir, manifestPath)
	if err != nil {
		return ArtifactManifest{}, "", fmt.Errorf("%w: manifest path: %v", ErrInvalidManifest, err)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ArtifactManifest{}, "", fmt.Errorf("%w: read manifest: %v", ErrInvalidManifest, err)
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ArtifactManifest{}, "", fmt.Errorf("%w: decode manifest: %v", ErrInvalidManifest, err)
	}
	if manifest.SchemaVersion != ArtifactManifestVersion || manifest.Kind != "artifact-manifest" {
		return ArtifactManifest{}, "", fmt.Errorf("%w: schemaVersion=%q kind=%q", ErrInvalidManifest, manifest.SchemaVersion, manifest.Kind)
	}
	for index, artifact := range manifest.Artifacts {
		if artifact.Path == "" {
			return ArtifactManifest{}, "", fmt.Errorf("%w: artifacts[%d] has no path", ErrInvalidManifest, index)
		}
		artifactPath, err := pathWithin(outputDir, artifact.Path)
		if err != nil {
			return ArtifactManifest{}, "", fmt.Errorf("%w: artifacts[%d] path: %v", ErrInvalidManifest, index, err)
		}
		if _, err := os.Stat(artifactPath); err != nil {
			return ArtifactManifest{}, "", fmt.Errorf("%w: artifacts[%d] missing file: %v", ErrInvalidManifest, index, err)
		}
		if artifact.RelativePath != "" {
			relativePath, err := pathWithin(outputDir, filepath.Join(outputDir, filepath.FromSlash(artifact.RelativePath)))
			if err != nil {
				return ArtifactManifest{}, "", fmt.Errorf("%w: artifacts[%d] relativePath: %v", ErrInvalidManifest, index, err)
			}
			if filepath.Clean(relativePath) != filepath.Clean(artifactPath) {
				return ArtifactManifest{}, "", fmt.Errorf("%w: artifacts[%d] path and relativePath disagree", ErrInvalidManifest, index)
			}
		}
	}
	return manifest, manifestPath, nil
}
