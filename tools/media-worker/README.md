# Media worker contract

Run `python media_worker.py --job JOB.json --output OUT` to build measured narration, captions, scene props, and the artifact manifest. Add `--validate-only` to inspect the ProductionSpec and required capabilities without starting a subprocess or writing output.

`timeline.playbackRate` defaults to `2`. `timeline.inputPlaybackRate` defaults to `1` and documents a pre-accelerated input; the worker applies only `playbackRate / inputPlaybackRate` to audio. Audio is physically transformed with FFmpeg `atempo`, while emitted Remotion narration props always use `playbackRate: 1`, so the rate cannot be applied twice. Captions, scene windows, pauses, and timeline metadata use the same measured source durations and effective rate.

FFmpeg, FFprobe, TTS, and Remotion are capabilities: if a required capability is unavailable, the worker reports a capability blocker rather than claiming success. Commands have bounded timeouts. Existing Remotion projects are read-only inputs; the worker does not edit them. No network TTS, platform publishing, or large-video fixtures are part of the offline test contract.

When Remotion rendering is enabled, the worker starts a temporary HTTP server on
`127.0.0.1` with an ephemeral port. Its root is the resolved job output
directory, and every request is checked against that root after URL decoding;
path traversal and symlink escapes return `404`. The render-only copy of
`audio.narration.src` is a percent-encoded loopback URL, because Remotion's
`resolveAsset` accepts `http`/`https` but rejects `file://`. The server is
stopped and the persisted `remotion-props.json` is restored before the worker
returns, so no dead port or short-lived URL is recorded as a durable artifact.
