package mediaworker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandWorkerRunValidatesManifest(t *testing.T) {
	worker, root := testWorker(t, `
import json, os, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
out = os.path.abspath(args["--output"])
os.makedirs(out, exist_ok=True)
artifact = os.path.join(out, "audio.wav")
open(artifact, "wb").write(b"audio")
manifest = os.path.join(out, "artifact-manifest.json")
json.dump({"schemaVersion":"1.0","kind":"artifact-manifest","jobId":"job-1","productionSpecVersion":"1.0","artifacts":[{"id":"audio","kind":"audio","path":artifact,"relativePath":"audio.wav","bytes":5,"sha256":"x"}]}, open(manifest, "w"))
print(json.dumps({"ok":True,"jobId":"job-1","output":out,"artifactManifest":manifest,"durationMs":12,"segments":1}))
`)

	result, err := worker.Run(context.Background(), ProductionSpec{JobID: "job-1", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "out"))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.OK || result.ArtifactManifestPath != filepath.Join(root, "out", "artifact-manifest.json") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCommandWorkerValidateDoesNotCreateOutput(t *testing.T) {
	worker, root := testWorker(t, `
import json
print(json.dumps({"productionSpecVersion":"1.0","jobId":"job-1","validateOnly":True,"valid":True,"blockers":[]}))
`)

	output := filepath.Join(root, "validation-output")
	result, err := worker.Validate(context.Background(), ProductionSpec{JobID: "job-1", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, output)
	if err != nil || !result.Valid {
		t.Fatalf("Validate() = %+v, %v", result, err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate created output directory: err=%v", err)
	}
}

func TestCommandWorkerRejectsValidationWrites(t *testing.T) {
	worker, root := testWorker(t, `
import json, os, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
os.makedirs(args["--output"], exist_ok=True)
print(json.dumps({"valid":True,"jobId":"job-1","blockers":[]}))
`)

	_, err := worker.Validate(context.Background(), ProductionSpec{JobID: "job-1", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "output"))
	if !errors.Is(err, ErrValidationWroteOutput) {
		t.Fatalf("expected validation write rejection, got %v", err)
	}
}

func TestCommandWorkerPreservesCapabilityBlocker(t *testing.T) {
	worker, _ := testWorker(t, `
import json, sys
print(json.dumps({"ok":False,"error":{"code":"capability_blocker","capability":"ffmpeg","message":"ffmpeg missing","remediation":"install ffmpeg"}}), file=sys.stderr)
sys.exit(2)
`)

	result, err := worker.Run(context.Background(), ProductionSpec{JobID: "blocked", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(worker.DataRoot, "out"))
	var blockerErr *CapabilityBlockerError
	if !errors.As(err, &blockerErr) || !errors.Is(err, ErrCapabilityBlocked) || !errors.Is(err, ErrNonZeroExit) {
		t.Fatalf("expected capability blocker/nonzero exit, got result=%+v err=%v", result, err)
	}
	if len(result.Blockers) != 1 || result.Blockers[0].Capability != "ffmpeg" || result.Blockers[0].Remediation != "install ffmpeg" {
		t.Fatalf("blocker was not preserved: %+v", result.Blockers)
	}
}

func TestCommandWorkerPreservesValidateBlockerFromStdout(t *testing.T) {
	worker, _ := testWorker(t, `
import json, sys
print(json.dumps({"valid":False,"jobId":"blocked","blockers":[{"capability":"edge-tts","message":"module missing","remediation":"install edge-tts"}]}))
sys.exit(2)
`)

	result, err := worker.Validate(context.Background(), ProductionSpec{JobID: "blocked", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(worker.DataRoot, "out"))
	var blockerErr *CapabilityBlockerError
	if !errors.As(err, &blockerErr) || len(result.Blockers) != 1 || result.Blockers[0].Capability != "edge-tts" {
		t.Fatalf("expected validate blocker, got result=%+v err=%v", result, err)
	}
}

func TestCommandWorkerReportsMissingPythonAsBlocker(t *testing.T) {
	worker, root := testWorker(t, `print("unused")`)
	worker.PythonPath = filepath.Join(root, "missing-python")
	worker.runner = nil
	result, err := worker.Validate(context.Background(), ProductionSpec{JobID: "missing-python", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "out"))
	var blockerErr *CapabilityBlockerError
	if !errors.As(err, &blockerErr) || len(result.Blockers) != 1 || result.Blockers[0].Capability != "python" {
		t.Fatalf("expected Python capability blocker, got result=%+v err=%v", result, err)
	}
}

func TestCommandWorkerDistinguishesTimeoutAndInvalidJSON(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		worker, _ := testWorker(t, `
import time
time.sleep(2)
`)
		worker.Timeout = 40 * time.Millisecond
		_, err := worker.Run(context.Background(), ProductionSpec{JobID: "slow", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(worker.DataRoot, "out"))
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("expected timeout, got %v", err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		worker, _ := testWorker(t, `
print("not json")
`)
		_, err := worker.Validate(context.Background(), ProductionSpec{JobID: "bad-json", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(worker.DataRoot, "out"))
		if !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("expected invalid JSON, got %v", err)
		}
	})

	t.Run("output limit", func(t *testing.T) {
		worker, _ := testWorker(t, `
print("x" * 1000)
`)
		worker.MaxStdoutBytes = 32
		_, err := worker.Validate(context.Background(), ProductionSpec{JobID: "large", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(worker.DataRoot, "out"))
		if !errors.Is(err, ErrOutputLimit) {
			t.Fatalf("expected output limit, got %v", err)
		}
	})
}

func TestCommandWorkerRejectsPathEscape(t *testing.T) {
	worker, root := testWorker(t, `print("should not run")`)
	_, err := worker.Run(context.Background(), ProductionSpec{JobID: "escape", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "..", "outside"))
	if !errors.Is(err, ErrPathEscape) {
		t.Fatalf("expected path escape, got %v", err)
	}
}

func TestCommandWorkerRejectsCorruptManifest(t *testing.T) {
	worker, root := testWorker(t, `
import json, os, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
out = os.path.abspath(args["--output"])
os.makedirs(out, exist_ok=True)
manifest = os.path.join(out, "artifact-manifest.json")
json.dump({"schemaVersion":"9.9","kind":"artifact-manifest","artifacts":[]}, open(manifest, "w"))
print(json.dumps({"ok":True,"artifactManifest":manifest}))
`)
	_, err := worker.Run(context.Background(), ProductionSpec{JobID: "corrupt", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "out"))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected invalid manifest, got %v", err)
	}
}

func TestCommandWorkerRejectsArtifactOutsideOutput(t *testing.T) {
	worker, root := testWorker(t, `
import json, os, sys
args = dict(zip(sys.argv[1::2], sys.argv[2::2]))
out = os.path.abspath(args["--output"])
os.makedirs(out, exist_ok=True)
outside = os.path.abspath(os.path.join(out, os.pardir, "outside.wav"))
open(outside, "wb").write(b"outside")
manifest = os.path.join(out, "artifact-manifest.json")
json.dump({"schemaVersion":"1.0","kind":"artifact-manifest","artifacts":[{"id":"outside","kind":"audio","path":outside,"relativePath":"../outside.wav"}]}, open(manifest, "w"))
print(json.dumps({"ok":True,"artifactManifest":manifest}))
`)
	_, err := worker.Run(context.Background(), ProductionSpec{JobID: "outside", Segments: []SegmentSpec{{ID: "one", Text: "hello"}}}, filepath.Join(root, "out"))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected artifact path rejection, got %v", err)
	}
}

func TestCommandWorkerRealPythonValidateOnly(t *testing.T) {
	python, ok := usablePython()
	if !ok {
		t.Skip("a usable Python interpreter is not available (Windows App Alias is not sufficient)")
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(workingDir, "..", "..", "tools", "media-worker", "media_worker.py")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("real media worker is not available: %v", err)
	}
	worker := CommandWorker{PythonPath: python, ScriptPath: script, DataRoot: t.TempDir(), Timeout: 5 * time.Second}
	result, err := worker.Validate(context.Background(), ProductionSpec{
		JobID:    "real-validation",
		Segments: []SegmentSpec{{ID: "one", AudioPath: "clip.wav"}},
		Timeline: &TimelineSpec{PlaybackRate: 1},
	}, filepath.Join(worker.DataRoot, "out"))
	if err != nil || !result.Valid {
		t.Fatalf("real worker Validate() = %+v, %v", result, err)
	}
}

func usablePython() (string, bool) {
	candidates := make([]string, 0, 4)
	if configured := strings.TrimSpace(os.Getenv("MEDIA_WORKER_PYTHON")); configured != "" {
		candidates = append(candidates, configured)
	}
	candidates = append(candidates, "python3", "python")
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, "-c", "import sys; print(sys.executable)")
		output, err := cmd.CombinedOutput()
		if err == nil && strings.TrimSpace(string(output)) != "" {
			return path, true
		}
	}
	return "", false
}

func testWorker(t *testing.T, body string) (CommandWorker, string) {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(root, "fake-worker.py")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env python3\n"+strings.TrimSpace(body)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return CommandWorker{PythonPath: "fake-python", ScriptPath: script, DataRoot: root, Timeout: 2 * time.Second, runner: fakeCommandRunner{body: body}}, root
}

type fakeCommandRunner struct {
	body string
}

func (r fakeCommandRunner) Run(ctx context.Context, args []string, stdoutLimit, stderrLimit int) (commandOutput, error) {
	output := argumentValue(args, "--output")
	if strings.Contains(r.body, "time.sleep") {
		<-ctx.Done()
		return commandOutput{}, ctx.Err()
	}
	if strings.Contains(r.body, "not json") {
		return commandOutput{stdout: []byte("not json"), exitCode: 0}, nil
	}
	if strings.Contains(r.body, `"x" * 1000`) {
		return commandOutput{stdout: []byte(strings.Repeat("x", 1000)), exitCode: 0}, ErrOutputLimit
	}
	if strings.Contains(r.body, `"capability":"ffmpeg"`) {
		return commandOutput{stderr: []byte(`{"ok":false,"error":{"code":"capability_blocker","capability":"ffmpeg","message":"ffmpeg missing","remediation":"install ffmpeg"}}`), exitCode: 2}, errors.New("fake nonzero exit")
	}
	if strings.Contains(r.body, `"capability":"edge-tts"`) {
		return commandOutput{stdout: []byte(`{"valid":false,"jobId":"blocked","blockers":[{"capability":"edge-tts","message":"module missing","remediation":"install edge-tts"}]}`), exitCode: 2}, errors.New("fake nonzero exit")
	}
	if strings.Contains(r.body, `"schemaVersion":"9.9"`) {
		manifest := filepath.Join(output, "artifact-manifest.json")
		if err := os.MkdirAll(output, 0o755); err != nil {
			return commandOutput{}, err
		}
		if err := os.WriteFile(manifest, []byte(`{"schemaVersion":"9.9","kind":"artifact-manifest","artifacts":[]}`), 0o600); err != nil {
			return commandOutput{}, err
		}
		return commandOutput{stdout: []byte(`{"ok":true,"artifactManifest":"` + filepath.ToSlash(manifest) + `"}`), exitCode: 0}, nil
	}
	if strings.Contains(r.body, `"outside.wav"`) {
		if err := os.MkdirAll(output, 0o755); err != nil {
			return commandOutput{}, err
		}
		outside := filepath.Join(filepath.Dir(output), "outside.wav")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			return commandOutput{}, err
		}
		manifest := filepath.Join(output, "artifact-manifest.json")
		payload := `{"schemaVersion":"1.0","kind":"artifact-manifest","artifacts":[{"id":"outside","kind":"audio","path":"` + filepath.ToSlash(outside) + `","relativePath":"../outside.wav"}]}`
		if err := os.WriteFile(manifest, []byte(payload), 0o600); err != nil {
			return commandOutput{}, err
		}
		return commandOutput{stdout: []byte(`{"ok":true,"artifactManifest":"` + filepath.ToSlash(manifest) + `"}`), exitCode: 0}, nil
	}
	if strings.Contains(r.body, "artifact-manifest") {
		if err := os.MkdirAll(output, 0o755); err != nil {
			return commandOutput{}, err
		}
		artifact := filepath.Join(output, "audio.wav")
		manifest := filepath.Join(output, "artifact-manifest.json")
		if err := os.WriteFile(artifact, []byte("audio"), 0o600); err != nil {
			return commandOutput{}, err
		}
		payload := `{"schemaVersion":"1.0","kind":"artifact-manifest","jobId":"job-1","productionSpecVersion":"1.0","artifacts":[{"id":"audio","kind":"audio","path":"` + filepath.ToSlash(artifact) + `","relativePath":"audio.wav","bytes":5,"sha256":"x"}]}`
		if err := os.WriteFile(manifest, []byte(payload), 0o600); err != nil {
			return commandOutput{}, err
		}
		response := `{"ok":true,"jobId":"job-1","output":"` + filepath.ToSlash(output) + `","artifactManifest":"` + filepath.ToSlash(manifest) + `","durationMs":12,"segments":1}`
		return commandOutput{stdout: []byte(response), exitCode: 0}, nil
	}
	if strings.Contains(r.body, "os.makedirs") {
		if err := os.MkdirAll(output, 0o755); err != nil {
			return commandOutput{}, err
		}
		return commandOutput{stdout: []byte(`{"valid":true,"jobId":"job-1","blockers":[]}`), exitCode: 0}, nil
	}
	return commandOutput{stdout: []byte(`{"valid":true,"jobId":"job-1","blockers":[]}`), exitCode: 0}, nil
}

func argumentValue(args []string, name string) string {
	for index, arg := range args {
		if arg == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}
