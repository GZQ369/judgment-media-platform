# Media Worker Go adapter

`internal/mediaworker` is the Go boundary around
`tools/media-worker/media_worker.py`. It exposes `Worker.Validate` and
`Worker.Run`, a small `ProductionSpec`, the worker result, validated artifact
manifests, and capability blockers. Python response fields that are
implementation details are not returned to callers.

`CommandWorker` writes a job file below `DataRoot/.mediaworker/jobs` and
requires the requested output directory to be below `DataRoot`. It invokes
Python with an argument array through `exec.CommandContext`; no shell is used.
Timeouts and stdout/stderr limits are configurable. `Validate` passes
`--validate-only` and does not create the output directory itself.

`Run` accepts only a successful worker response whose manifest has schema
version `1.0`. The manifest path and every artifact path must remain inside the
requested output directory, and declared artifact files must exist. Missing
capabilities are returned as `CapabilityBlocker` values with capability,
message, and remediation intact.

The worker emits Remotion-compatible props for OpenMontage's `Explainer`
composition. Each cut always has a stable `id`, a supported component `type`,
and readable `text` derived from its referenced segment; the first cut uses
`hero_title` and later incomplete scene metadata falls back to `text_card` or
`callout`. This prevents the Explainer fallback from displaying placeholders
such as `scene-1`. The emitted `format` is canonicalized to positive bounded
dimensions and defaults to the vertical `1080x1920` target.
Visual descriptions remain metadata/display copy; they are never treated as
image URLs. A cut `source` is emitted only for an existing media file or an
explicit HTTP(S) media URL.

`props.captions` is the strict OpenMontage `WordCaption` shape:
`{word,startMs,endMs}`. Chinese text is split into short phrases and
punctuation, with each word interval distributed inside its source cue and
timeline playback rate. The worker separately retains segment/text/start/end
fields while writing phrase-level VTT and ASS files, so the subtitle artifacts
remain backward compatible.

Distribution Run integration is opt-in. The server constructs `CommandWorker`
only when both `MEDIA_WORKER_PYTHON` and `MEDIA_WORKER_SCRIPT` are configured;
otherwise API-only startup remains package-only. Runtime paths are optional
(`MEDIA_WORKER_FFMPEG`, `MEDIA_WORKER_FFPROBE`, `MEDIA_WORKER_REMOTION`,
`MEDIA_WORKER_REMOTION_CLI`, and `MEDIA_WORKER_EDGE_TTS_ROOT`) and
`MEDIA_WORKER_TIMEOUT` defaults to 20 minutes.
`MEDIA_WORKER_REMOTION` is the Remotion executable. For a Node.js installation,
set it to `node.exe` and set the optional `MEDIA_WORKER_REMOTION_CLI` to the
Remotion CLI JavaScript entry; the server passes that value as the one-element
`paths.remotionArgsPrefix` array. If it is empty, no prefix is emitted, which
preserves compatibility with a real `remotion` executable. The CLI path is not
checked during configuration loading; an unavailable command is reported by
the worker's capability handling when rendering is attempted. For example on
Windows:

```text
MEDIA_WORKER_REMOTION=node.exe
MEDIA_WORKER_REMOTION_CLI=C:/path/to/node_modules/@remotion/cli/remotion-cli.js
```

Optional Remotion configuration is provided by `MEDIA_WORKER_REMOTION_ENTRY` and
`MEDIA_WORKER_REMOTION_COMPOSITION`. When `render` is true, they default to
`src/index.tsx` and `Explainer` respectively and are passed as `render.entry`
and `render.composition`; when rendering is disabled, these defaults are not
emitted.
When `MEDIA_WORKER_PYTHON` is configured, the same path is written to
`paths.python` so the worker can start `edge_tts` with the selected interpreter.
When `render` is true, `MEDIA_WORKER_REMOTION_CWD` is required and is passed as
`render.cwd`; without it the run stops with a `remotion` capability blocker.
Rendering remains optional when `render` is false.

When rendering is enabled, the Python worker temporarily serves the narration
asset from a random-prefix URL on `127.0.0.1` and an ephemeral port. The static
root is exactly the resolved run output directory. URL decoding, canonical
path resolution, and a root containment check reject traversal and symlink
escapes; only files below that directory are served. This is required because
the OpenMontage `Explainer` resolves local asset strings into `file://` URLs,
which Remotion refuses for this render path. The URL exists only while the
Remotion subprocess is running. The worker closes the server and joins its
serve thread in a `finally` path, then restores the durable props file to its
local artifact path.

The Remotion command receives the canonical `--width` and `--height` values;
dimensions are positive integers bounded by the worker and default to
`--width 1080 --height 1920`. After Remotion completes, configured FFmpeg
extracts frame `2.4s` into the output-local `cover.png` using the portable
single-frame image command, falling back to the end of a shorter video. It does
not require `pad` or `crop` filters because the rendered video already has the
requested dimensions. It then emits validated `cover-portrait-3x4.png`
(`1080x1440`) and `cover-landscape-4x3.png` (`1440x1080`) variants using Pillow
when available or the configured FFmpeg capability. `rendered-video`, `cover`
and both cover variants are added to the manifest only after their files exist,
are non-empty, and have the expected PNG dimensions. A cover extraction or
validation failure aborts the run before the media artifacts are declared.

Run output is fixed at `<data>/runs/<run>/media`. The Go service records the
package before validation, passes the ProductSeedRunner's storyboard,
voiceover, and subtitle output as a `ProductionSpec`, and records manifest
artifacts with the actual file path and SHA-256. The default playback rate is
2x and is applied once in the timeline spec. `rendered-video` is the only
artifact kind that makes `video_ready` true. Voice defaults are
`zh-CN-XiaoxiaoNeural` with `+0%/+0Hz/+0%`; the first segment uses a `hook`
profile with a mild `+4Hz/+8%` lift, while later segments use `normal`. Edge
TTS never receives the 2x timeline rate.
