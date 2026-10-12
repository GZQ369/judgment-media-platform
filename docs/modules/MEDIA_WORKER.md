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

## OpenMontage Registry Render Adapter (Preview)

The opt-in Distribution request flag `media.openmontage_registry=true` selects
a checkpointed tRPC-Agent-Go Graph/Runner adapter that invokes OpenMontage's
real `video_compose` registry tool with `operation=remotion_render`. Configure
`OPENMONTAGE_RENDER_PYTHON`, `OPENMONTAGE_ROOT`,
`OPENMONTAGE_RENDER_BRIDGE` (optional absolute bridge script path; if omitted,
the server resolves the repository-relative default against its startup cwd),
`OPENMONTAGE_NODE_BIN` (directory containing `npx.cmd` on Windows), and
`OPENMONTAGE_PIPER_MODEL` (an installed Piper `.onnx` voice file with its
matching `.onnx.json` config), and
`MEDIA_WORKER_FFPROBE`. This preview maps this run's script segments, scene
descriptions, subtitles, and estimated duration into the composition. It is
asset-driven rendering, not an autonomous writer/director: it does not yet load
director YAML/skills or automatically discover source assets. It generates each current-run
script segment through the registered local `piper_tts` tool, concatenates the
WAV files, and supplies the result as top-level `audio.narration.src`. A real
`.onnx` voice model is required; unavailable Piper or a missing model blocks
the run, with no prerecorded-audio fallback. The fresh MP4 must pass ffprobe
with both video and audio streams, and the extracted cover must be non-empty
before `media_ready`; SHA-256 is recorded for all three media artifacts. The
existing Distribution runstore remains the
authority. tRPC SQLite stores internal execution snapshots only; each retry
starts with a fresh Graph session, and node-level restart/resume is not
implemented.
The Distribution default playback rate remains 2x. The bridge applies FFmpeg
`atempo` once to each generated speech segment before measuring WAV duration;
the composition then derives each cut and caption interval from those measured
durations. Rates outside 0.5x-2x are blocked. A matching FFmpeg executable is
required for non-1x playback. The voice model and its publication rights are
the operator's responsibility: no model is downloaded or bundled, and a
missing model/sidecar is reported as a narration capability blocker.
During rendering a tokenized HTTP server binds only to `127.0.0.1` and serves
only this run's allowlisted sources and generated narration by fixed route names. It
rejects other paths/methods, checks resolved regular files and a 2 GiB limit,
and shuts down after the Graph finishes. Adapter subprocess work is bounded by
`MEDIA_WORKER_TIMEOUT` (20 minutes by default).

For a local canary, point the source to `media/smoke/pet_demo.mp4`, configure an
installed Piper model, and use the OpenMontage venv Python. Without the model
the run is expected to stop as `blocked` at narration. The request must include
`{"media":{"enabled":true,"render":true,"openmontage_registry":true}}`.
This checks asset-driven composition with current-run copy and generated
narration, not unattended creative planning. Each run renders a new file under
`data/runs/<run-id>/media`; the existing sample MP4 is never registered as the
new output.

Composition overlays use a concise first-scene hook and short later scene
titles derived from scene purpose/visual properties; full narration and
subtitle text remain available unchanged for speech and captions. Local PNG,
JPEG, and WebP sources use the renderer's `anime_scene` image-array and
`ken-burns` animation fields, with title overlays collected at composition
level using `section_title`. MP4 sources retain the existing video-background
mapping. Overlay timing follows measured Piper segment durations. Cover
extraction seeks to 0.5 seconds, or one
quarter of shorter outputs, so
the frame is early enough to stay within short videos while allowing the
opening title overlay to appear within the first second.

## Task Asset Binding

Distribution accepts existing local images and MP4 files through optional
`media.assets`, `media.scene_assets`, and `media.default_asset_id`. For example,
with the following files already present under `<data>/assets`:

```json
{
  "media": {
    "enabled": true,
    "render": true,
    "openmontage_registry": true,
    "assets": [
      {"id": "fde", "ref": "fde-operations.png", "kind": "image"},
      {"id": "detail", "ref": "detail.png", "kind": "image"},
      {"id": "demo", "ref": "demo.mp4", "kind": "video"}
    ],
    "default_asset_id": "fde",
    "scene_assets": [
      {"scene_id": "scene-2", "asset_id": "detail"},
      {"scene_id": "scene-3", "asset_id": "demo"}
    ]
  }
}
```

Scenes use the generated `scene-1`, `scene-2`, etc. IDs. The explicit default
applies to scene-1 and all other unlisted scenes; individual bindings override
it. Without a default, every scene must be bound. Unknown scenes/assets,
duplicate asset IDs or scene bindings, and missing bindings fail production.
Assets require 1-64 character alphanumeric/underscore/hyphen IDs beginning
with an alphanumeric character. IDs must be unique under case folding (`A`
and `a` conflict), and Windows reserved device names such as `CON`, `NUL`,
`PRN`, `AUX`, `COM1`, and `LPT1` are rejected case-insensitively during request
normalization with HTTP 400. Valid IDs keep their original spelling for binding.
Supported inputs are PNG, JPEG, WebP (`image`)
and MP4 (`video`); extension, content MIME, and kind must agree. Optional
`sha256` supplies an expected 64-digit SHA-256 hash.

`ref` uses slash-separated paths relative to `<data>/assets`. Absolute paths,
drive letters, UNC paths, URLs, traversal components, Windows device paths,
and symlinks escaping the asset root are rejected. Files must be nonempty,
regular files no larger than 2 GiB. Explicit empty asset/binding requests without
valid bindings and requests with disabled media/render are rejected during
normalization, before `omitempty` serialization can discard their intent.

Before worker validation, sources are copied to `<run>/inputs/assets`. Their
hashes, byte sizes, MIME types, and request/binding fingerprint are committed
with `manifest.json` by an atomic directory rename. A retry uses the committed
copies and verifies their metadata and hashes; source changes or deletion do
not alter the run. Snapshot corruption or changed bindings fail without
rebuilding or falling back. Completed snapshots and their manifest must not be
symlinks. Content signatures are checked during intake; actual media decoding
is exercised by the renderer and output ffprobe QA.

The normalized ProductionSpec includes an `assets` table with refs such as
`inputs/assets/fde.png`, relative to the run directory, and `SceneSpec.assetRef`
IDs. The original optional `SceneSpec.asset` JSON field remains unchanged.
`<run>/inputs/production-spec.json` and the binding manifest are recorded in the
artifact ledger as `<run-id>:production_spec` and `<run-id>:input_assets`, with
hashes and sizes. Inputs live outside the media output directory.

OpenMontage maps each segment to its scene's asset: images use `anime_scene`
and `ken-burns` plus timed top-level titles, and videos retain the video cut
behavior. The HTTP routes need no filename extension because media selection
comes from the bound asset table. Only this run's asset allowlist is served.
`OPENMONTAGE_RENDER_SOURCE` may be empty for task-asset requests. Requests with
no new fields keep the existing global SourcePath fallback; that legacy source
is still read live and is not snapshotted. New asset requests never use this
fallback on failure, and workers without explicit task-asset support are
blocked. OpenMontage also blocks explicit `media.voice` selection because its
voice is determined by the configured local Piper model.

Output artifacts retain SHA-256, bytes, and MIME and now record measured
ffprobe metadata: video/cover width and height, audio sample rate/channels,
and video/audio duration. These values are copied into the artifact ledger.
The output remains the existing portrait profile; no unexecuted dimension,
model, or publication controls are introduced.
