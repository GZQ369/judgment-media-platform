#!/usr/bin/env python3
"""Build measured audio, subtitle, scene props, and artifact events.

The worker intentionally has no third-party Python dependencies. External
commands are invoked only in normal mode and only when the ProductionSpec
requires them. ``--validate-only`` never starts a subprocess.
"""

from __future__ import annotations

import argparse
import contextlib
import hashlib
import importlib.util
import json
import math
import mimetypes
import os
import re
import secrets
import shutil
import struct
import subprocess
import sys
import threading
import urllib.parse
import wave
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Mapping, Sequence


PRODUCTION_SPEC_VERSION = "1.0"
ARTIFACT_MANIFEST_VERSION = "1.0"
EVENTS_VERSION = "1.0"
COMMAND_TIMEOUT_SECONDS = 120
DURATION_TOLERANCE_MS = 100
DEFAULT_VIDEO_WIDTH = 1080
DEFAULT_VIDEO_HEIGHT = 1920
MAX_VIDEO_DIMENSION = 8192
DEFAULT_COVER_TIMESTAMP_SECONDS = 2.4
COVER_SAFE_MARGIN_SECONDS = 0.05
COVER_PORTRAIT_SIZE = (1080, 1440)
COVER_LANDSCAPE_SIZE = (1440, 1080)


class WorkerError(Exception):
    """A user-actionable worker failure."""


class CapabilityBlocker(WorkerError):
    """A required executable or capability is unavailable."""

    def __init__(self, capability: str, message: str, remediation: str = "") -> None:
        super().__init__(message)
        self.capability = capability
        self.remediation = remediation

    def as_dict(self) -> dict[str, str]:
        result = {"code": "capability_blocker", "capability": self.capability, "message": str(self)}
        if self.remediation:
            result["remediation"] = self.remediation
        return result


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise WorkerError(f"missing JSON job: {path}") from exc
    except json.JSONDecodeError as exc:
        raise WorkerError(f"invalid JSON job {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise WorkerError(f"ProductionSpec must be a JSON object: {path}")
    return value


def write_json(path: Path, value: Mapping[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def normalize_spec(raw: dict[str, Any]) -> dict[str, Any]:
    """Accept the old integer field while emitting one canonical version."""
    spec = dict(raw)
    version = spec.get("productionSpecVersion", spec.get("schemaVersion"))
    if version is None:
        raise WorkerError("ProductionSpec requires productionSpecVersion (expected 1.0)")
    normalized_version = f"{version}.0" if isinstance(version, int) else str(version)
    if normalized_version != PRODUCTION_SPEC_VERSION:
        raise WorkerError(f"unsupported ProductionSpec version {version!r}; expected {PRODUCTION_SPEC_VERSION}")
    spec["productionSpecVersion"] = PRODUCTION_SPEC_VERSION
    job_id = spec.get("jobId", spec.get("projectId"))
    if not job_id:
        raise WorkerError("ProductionSpec requires jobId")
    spec["jobId"] = str(job_id)
    segments = spec.get("segments")
    if not isinstance(segments, list) or not segments:
        raise WorkerError("ProductionSpec requires a non-empty segments array")
    seen: set[str] = set()
    for index, segment in enumerate(segments):
        if not isinstance(segment, dict):
            raise WorkerError(f"segments[{index}] must be an object")
        if "id" not in segment:
            raise WorkerError(f"segments[{index}] requires id")
        segment_id = str(segment["id"])
        if segment_id in seen:
            raise WorkerError(f"duplicate segment id: {segment_id}")
        seen.add(segment_id)
        if not segment.get("text") and not _segment_audio_value(segment):
            raise WorkerError(f"segments[{index}] requires text or audioPath")
    return spec


def _segment_audio_value(segment: Mapping[str, Any]) -> Any:
    return segment.get("audioPath", segment.get("audio"))


def _job_root(job_path: Path, spec: Mapping[str, Any]) -> Path:
    source = spec.get("source")
    source_root = source.get("root") if isinstance(source, dict) else None
    root = Path(str(source_root)) if source_root else job_path.parent
    if not root.is_absolute():
        root = job_path.parent / root
    return root.resolve()


def resolve_path(value: Any, base: Path) -> Path | None:
    if not value:
        return None
    path = Path(str(value)).expanduser()
    if not path.is_absolute():
        path = base / path
    return path.resolve()


def _configured_command(value: Any, base: Path) -> str | None:
    if not value:
        return None
    raw = str(value)
    configured = Path(raw).expanduser()
    if configured.is_absolute() or any(mark in raw for mark in ("/", "\\")):
        if not configured.is_absolute():
            configured = base / configured
        return str(configured.resolve()) if configured.exists() else None
    return shutil.which(raw)


def command_path(spec: Mapping[str, Any], name: str, base: Path) -> str | None:
    paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
    configured = paths.get(name)
    return _configured_command(configured, base) if configured else shutil.which(name)


def _has_edge_tts(spec: Mapping[str, Any], base: Path) -> bool:
    paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
    edge_root = resolve_path(paths.get("edgeTtsRoot"), base)
    original = list(sys.path)
    try:
        if edge_root:
            sys.path.insert(0, str(edge_root))
        return importlib.util.find_spec("edge_tts") is not None
    finally:
        sys.path[:] = original


def _segment_has_audio(segment: Mapping[str, Any]) -> bool:
    return bool(_segment_audio_value(segment))


def _output_format(spec: Mapping[str, Any]) -> str:
    outputs = spec.get("outputs") if isinstance(spec.get("outputs"), dict) else {}
    value = str(outputs.get("audioFormat", spec.get("audioFormat", "wav"))).lower().lstrip(".")
    if value not in {"wav", "mp3"}:
        raise WorkerError(f"unsupported outputs.audioFormat: {value!r}; expected wav or mp3")
    return value


def _render_enabled(spec: Mapping[str, Any]) -> bool:
    render = spec.get("render")
    return bool(render.get("enabled")) if isinstance(render, dict) else False


def _ass_burn_enabled(spec: Mapping[str, Any]) -> bool:
    render = spec.get("render")
    if not isinstance(render, dict):
        return False
    return bool(render.get("burnInAss", render.get("assBurnIn", False)))


def _timeline_rates(spec: Mapping[str, Any]) -> tuple[float, float, float]:
    timeline = spec.get("timeline") if isinstance(spec.get("timeline"), dict) else {}
    try:
        target_rate = float(timeline.get("playbackRate", timeline.get("speed", 2.0)))
        input_rate = float(timeline.get("inputPlaybackRate", 1.0))
    except (TypeError, ValueError) as exc:
        raise WorkerError("timeline playbackRate and inputPlaybackRate must be numeric") from exc
    if not all(math.isfinite(value) and value > 0 for value in (target_rate, input_rate)):
        raise WorkerError("timeline playbackRate and inputPlaybackRate must be greater than zero")
    return target_rate, input_rate, target_rate / input_rate


def _audio_config(spec: Mapping[str, Any]) -> tuple[int, int]:
    audio = spec.get("audio") if isinstance(spec.get("audio"), dict) else {}
    try:
        sample_rate = int(audio.get("sampleRate", 48000))
        channels = int(audio.get("channels", 2))
    except (TypeError, ValueError) as exc:
        raise WorkerError("audio.sampleRate and audio.channels must be integers") from exc
    if sample_rate <= 0 or channels <= 0:
        raise WorkerError("audio.sampleRate and audio.channels must be greater than zero")
    return sample_rate, channels


def _validate_scene_refs(spec: Mapping[str, Any]) -> None:
    scenes = spec.get("scenes", spec.get("sceneSegments", [])) or []
    if not isinstance(scenes, list):
        raise WorkerError("scenes must be an array")
    segment_ids = [str(segment["id"]) for segment in spec["segments"]]
    positions = {segment_id: index for index, segment_id in enumerate(segment_ids)}
    seen: set[str] = set()
    starts: list[int] = []
    for scene in scenes:
        if not isinstance(scene, dict) or not scene.get("id"):
            raise WorkerError("each scene requires id")
        scene_id = str(scene["id"])
        if scene_id in seen:
            raise WorkerError(f"duplicate scene id: {scene_id}")
        seen.add(scene_id)
        ids = scene.get("segmentIds", scene.get("segments"))
        if not isinstance(ids, list) or not ids:
            raise WorkerError(f"scene {scene['id']} requires segmentIds")
        missing = [str(value) for value in ids if str(value) not in positions]
        if missing:
            raise WorkerError(f"scene {scene['id']} references unknown segment(s): {', '.join(missing)}")
        starts.append(min(positions[str(value)] for value in ids))
    if any(starts[index] > starts[index + 1] for index in range(len(starts) - 1)):
        raise WorkerError("scenes must be ordered by their referenced segment start")


def _validate_static_spec(spec: Mapping[str, Any], job_path: Path) -> None:
    _timeline_rates(spec)
    _video_dimensions(spec)
    _audio_config(spec)
    _validate_scene_refs(spec)
    base = _job_root(job_path, spec)
    for segment in spec["segments"]:
        value = _segment_audio_value(segment)
        if value is not None and not isinstance(value, (str, dict)):
            raise WorkerError(f"segment {segment['id']} audioPath must be a string or object")
        if isinstance(value, dict) and not value.get("src", value.get("path")):
            raise WorkerError(f"segment {segment['id']} audioPath object requires src or path")
        if "pauseAfterMs" in segment or "pauseMs" in segment:
            _pause_ms(segment)
    render = spec.get("render")
    if isinstance(render, dict) and render.get("fps") is not None:
        try:
            fps = float(render["fps"])
        except (TypeError, ValueError) as exc:
            raise WorkerError("render.fps must be numeric") from exc
        if not math.isfinite(fps) or fps <= 0:
            raise WorkerError("render.fps must be greater than zero")
    del base  # Keep path resolution in capability_report, where blockers are collected.


def _wav_needs_normalization(path: Path, sample_rate: int, channels: int) -> bool:
    try:
        return _wave_format(path) != (channels, 2, sample_rate)
    except (wave.Error, EOFError, OSError):
        return True


def capability_report(spec: Mapping[str, Any], job_path: Path) -> dict[str, Any]:
    """Inspect capabilities without starting any process or touching output."""
    _validate_static_spec(spec, job_path)
    base = _job_root(job_path, spec)
    segments = spec["segments"]
    needs_tts = any(not _segment_has_audio(segment) for segment in segments)
    timeline = spec.get("timeline") if isinstance(spec.get("timeline"), dict) else {}
    try:
        target_rate = float(timeline.get("playbackRate", timeline.get("speed", 2.0)))
        input_rate = float(timeline.get("inputPlaybackRate", 1.0))
    except (TypeError, ValueError) as exc:
        raise WorkerError("timeline playbackRate and inputPlaybackRate must be numeric") from exc
    # Rendered videos need FFmpeg/FFprobe for cover extraction and safe frame
    # selection. Variant generation checks the FFmpeg filters during a real run
    # (validate-only intentionally never starts an external process).
    needs_ffmpeg = needs_tts or _output_format(spec) == "mp3" or target_rate / input_rate != 1.0 or _render_enabled(spec)
    needs_ffprobe = needs_tts or any(
        Path(str(_segment_audio_value(segment))).suffix.lower() != ".wav"
        for segment in segments
        if _segment_has_audio(segment)
    ) or _render_enabled(spec)
    capabilities: dict[str, dict[str, Any]] = {}
    blockers: list[dict[str, str]] = []

    def check_command(name: str, required: bool, remediation: str) -> None:
        found = command_path(spec, name, base)
        capabilities[name] = {"required": required, "available": bool(found), "path": found}
        if required and not found:
            configured = (spec.get("paths") or {}).get(name) if isinstance(spec.get("paths"), dict) else None
            detail = f"{name} is not available"
            if configured:
                detail += f" at configured path {configured!r}"
            blockers.append(CapabilityBlocker(name, detail, remediation).as_dict())

    check_command("python", needs_tts, "Set paths.python to a Python executable that can run edge_tts.")
    if needs_tts:
        paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
        edge_root = resolve_path(paths.get("edgeTtsRoot"), base)
        edge_available = _has_edge_tts(spec, base)
        capabilities["edge-tts"] = {"required": True, "available": edge_available, "module": "edge_tts", "root": str(edge_root) if edge_root else None}
        if not edge_available:
            blockers.append(CapabilityBlocker(
                "edge-tts",
                "Python module edge_tts is unavailable; no TTS call can be made",
                "Set paths.edgeTtsRoot or install edge_tts in paths.python's environment.",
            ).as_dict())
    check_command("ffmpeg", needs_ffmpeg, "Set paths.ffmpeg to the FFmpeg binary used by the compositor.")
    check_command("ffprobe", needs_ffprobe, "Set paths.ffprobe to the matching FFprobe binary.")
    if _render_enabled(spec):
        render = spec["render"]
        paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
        found = _configured_command(paths.get("remotion"), base)
        capabilities["remotion"] = {"required": True, "available": bool(found), "path": found}
        if not found:
            blockers.append(CapabilityBlocker(
                "remotion",
                "Remotion rendering was requested but paths.remotion is unavailable",
                "Configure paths.remotion and keep render.cwd pointed at the existing Remotion project.",
            ).as_dict())
        cwd = resolve_path(render.get("cwd"), base)
        if not cwd or not cwd.is_dir():
            blockers.append(CapabilityBlocker(
                "remotion-cwd",
                "Remotion render.cwd must be an existing directory",
                "Point render.cwd at the existing Remotion project; the worker will not edit it.",
            ).as_dict())
    return {
        "productionSpecVersion": PRODUCTION_SPEC_VERSION,
        "jobId": spec["jobId"],
        "validateOnly": True,
        "capabilities": capabilities,
        "blockers": blockers,
        "valid": not blockers,
    }


def require_capabilities(report: Mapping[str, Any]) -> None:
    blockers = report.get("blockers") or []
    if blockers:
        first = blockers[0]
        raise CapabilityBlocker(str(first.get("capability", "unknown")), str(first.get("message", "required capability unavailable")), str(first.get("remediation", "")))


def _run(args: Sequence[str], *, env: Mapping[str, str] | None = None, cwd: Path | None = None, timeout: int = COMMAND_TIMEOUT_SECONDS) -> subprocess.CompletedProcess[str]:
    try:
        process = subprocess.run(list(args), env=dict(env) if env else None, cwd=str(cwd) if cwd else None, text=True, capture_output=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        raise WorkerError(f"command timed out after {timeout}s: {' '.join(args)}") from exc
    except OSError as exc:
        raise CapabilityBlocker("external-command", f"could not start {args[0]!r}: {exc}") from exc
    if process.returncode != 0:
        detail = process.stderr.strip() or process.stdout.strip()
        raise WorkerError(f"command failed ({process.returncode}): {' '.join(args)}\n{detail}")
    return process


def _edge_environment(spec: Mapping[str, Any], base: Path) -> dict[str, str]:
    env = os.environ.copy()
    paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
    root = resolve_path(paths.get("edgeTtsRoot"), base)
    if root:
        current = env.get("PYTHONPATH")
        env["PYTHONPATH"] = os.pathsep.join([str(root)] + ([current] if current else []))
    return env


def _voice_params(spec: Mapping[str, Any], segment: Mapping[str, Any]) -> dict[str, str]:
    config = spec.get("voice") if isinstance(spec.get("voice"), dict) else {}
    params: dict[str, str] = {}
    default = config.get("default") if isinstance(config.get("default"), dict) else {}
    params.update({str(k): str(v) for k, v in default.items()})
    profiles = config.get("profiles") if isinstance(config.get("profiles"), dict) else {}
    profile = profiles.get(segment.get("profile"), {})
    if isinstance(profile, dict):
        params.update({str(k): str(v) for k, v in profile.items()})
    overrides = segment.get("tts") if isinstance(segment.get("tts"), dict) else {}
    params.update({str(k): str(v) for k, v in overrides.items() if k != "enabled"})
    if config.get("voice"):
        params.setdefault("voice", str(config["voice"]))
    missing = [key for key in ("voice", "rate", "pitch", "volume") if not params.get(key)]
    if missing:
        raise WorkerError(f"segment {segment['id']} is missing Edge TTS parameters: {', '.join(missing)}")
    return params


def _safe_id(value: Any) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", str(value)).strip(".-") or "segment"


def synthesize_segment(spec: Mapping[str, Any], segment: Mapping[str, Any], output_dir: Path, base: Path) -> tuple[Path, Path]:
    paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
    python = command_path(spec, "python", base)
    if not python:
        raise CapabilityBlocker("python", "configured Python executable is unavailable")
    segment_dir = output_dir / "segments"
    segment_dir.mkdir(parents=True, exist_ok=True)
    stem = f"segment-{_safe_id(segment['id'])}"
    audio_path = segment_dir / f"{stem}.mp3"
    vtt_path = segment_dir / f"{stem}.vtt"
    params = _voice_params(spec, segment)
    args = [python, "-m", str(paths.get("edgeTtsModule", "edge_tts")), "--voice", params["voice"], "--rate", params["rate"], "--pitch", params["pitch"], "--volume", params["volume"], "--text", str(segment.get("text", "")), "--write-media", str(audio_path), "--write-subtitles", str(vtt_path)]
    _run(args, env=_edge_environment(spec, base))
    if not audio_path.exists() or not vtt_path.exists():
        raise WorkerError(f"edge_tts completed without expected artifacts for segment {segment['id']}")
    return audio_path, vtt_path


def _duration_from_wave(path: Path) -> int:
    try:
        with wave.open(str(path), "rb") as handle:
            rate = handle.getframerate()
            if not rate:
                raise WorkerError(f"WAV has no sample rate: {path}")
            return round(handle.getnframes() * 1000 / rate)
    except (wave.Error, EOFError) as exc:
        raise WorkerError(f"cannot measure WAV duration: {path}: {exc}") from exc


def probe_duration(path: Path, spec: Mapping[str, Any], base: Path) -> int:
    if path.suffix.lower() == ".wav":
        timeline = spec.get("timeline") if isinstance(spec.get("timeline"), dict) else {}
        if timeline.get("measurement", "auto") in {"stdlib", "auto"}:
            return _duration_from_wave(path)
    ffprobe = command_path(spec, "ffprobe", base)
    if not ffprobe:
        raise CapabilityBlocker("ffprobe", f"cannot measure non-WAV audio without ffprobe: {path}", "Set paths.ffprobe to the matching FFprobe binary.")
    process = _run([ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", str(path)])
    try:
        return round(float(process.stdout.strip()) * 1000)
    except ValueError as exc:
        raise WorkerError(f"ffprobe returned no numeric duration for {path}") from exc


def _wave_format(path: Path) -> tuple[int, int, int]:
    with wave.open(str(path), "rb") as handle:
        return handle.getnchannels(), handle.getsampwidth(), handle.getframerate()


def _write_silence(path: Path, milliseconds: int, sample_rate: int, channels: int) -> None:
    frames = round(milliseconds * sample_rate / 1000)
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(channels)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(b"\0\0" * channels * frames)


def _convert_to_wav(source: Path, target: Path, spec: Mapping[str, Any], base: Path, sample_rate: int, channels: int) -> None:
    if source.suffix.lower() == ".wav":
        try:
            source_format = _wave_format(source)
        except (wave.Error, EOFError) as exc:
            raise WorkerError(f"cannot read WAV input {source}: {exc}") from exc
        if source_format == (channels, 2, sample_rate):
            shutil.copyfile(source, target)
            return
    ffmpeg = command_path(spec, "ffmpeg", base)
    if not ffmpeg:
        raise CapabilityBlocker("ffmpeg", f"FFmpeg is required to normalize audio input: {source}", "Set paths.ffmpeg to the compositor FFmpeg binary.")
    _run([ffmpeg, "-y", "-i", str(source), "-ar", str(sample_rate), "-ac", str(channels), "-c:a", "pcm_s16le", str(target)])


def _concat_wav(parts: Sequence[Path], target: Path) -> None:
    if not parts:
        raise WorkerError("cannot concatenate an empty audio timeline")
    formats = [_wave_format(path) for path in parts]
    if len(set(formats)) != 1:
        raise WorkerError("standard-library WAV concatenation requires identical audio formats")
    channels, width, rate = formats[0]
    with wave.open(str(target), "wb") as output:
        output.setnchannels(channels)
        output.setsampwidth(width)
        output.setframerate(rate)
        for part in parts:
            with wave.open(str(part), "rb") as source:
                output.writeframes(source.readframes(source.getnframes()))


def _encode_mp3(source: Path, target: Path, spec: Mapping[str, Any], base: Path) -> None:
    ffmpeg = command_path(spec, "ffmpeg", base)
    if not ffmpeg:
        raise CapabilityBlocker("ffmpeg", "FFmpeg is required for MP3 output", "Set paths.ffmpeg to the compositor FFmpeg binary.")
    _run([ffmpeg, "-y", "-i", str(source), "-c:a", "libmp3lame", "-q:a", "2", str(target)])


_VTT_TIMESTAMP = re.compile(r"^(\d{2}:\d{2}:\d{2}[.,]\d{3})\s+-->\s+(\d{2}:\d{2}:\d{2}[.,]\d{3})")


def _parse_vtt_timestamp(value: str) -> int:
    hours, minutes, rest = value.replace(",", ".").split(":")
    seconds, millis = rest.split(".")
    return int(hours) * 3_600_000 + int(minutes) * 60_000 + int(seconds) * 1_000 + int(millis)


def parse_vtt(path: Path | None, duration_ms: int) -> list[dict[str, Any]]:
    if not path or not path.exists():
        return []
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    cues: list[dict[str, Any]] = []
    index = 0
    while index < len(lines):
        match = _VTT_TIMESTAMP.match(lines[index].strip())
        if not match:
            index += 1
            continue
        index += 1
        text_lines: list[str] = []
        while index < len(lines) and lines[index].strip():
            text_lines.append(lines[index].strip())
            index += 1
        start = max(0, min(duration_ms, _parse_vtt_timestamp(match.group(1))))
        end = max(start, min(duration_ms, _parse_vtt_timestamp(match.group(2))))
        text = " ".join(text_lines).replace("<c>", "").replace("</c>", "").strip()
        if text and end > start:
            cues.append({"startMs": start, "endMs": end, "text": text})
        index += 1
    return cues


def _scaled_ms(value: int, playback_rate: float) -> int:
    return round(value / playback_rate)


def _video_dimension(value: Any, name: str, default: int) -> int:
    if value is None:
        return default
    if isinstance(value, bool) or not isinstance(value, int):
        raise WorkerError(f"format.{name} must be a positive integer")
    if value <= 0 or value > MAX_VIDEO_DIMENSION:
        raise WorkerError(f"format.{name} must be between 1 and {MAX_VIDEO_DIMENSION}")
    return value


def _video_dimensions(spec: Mapping[str, Any]) -> tuple[int, int]:
    output_format = spec.get("format")
    if output_format is None:
        output_format = {}
    if not isinstance(output_format, dict):
        raise WorkerError("format must be an object with positive integer width and height")
    return (
        _video_dimension(output_format.get("width"), "width", DEFAULT_VIDEO_WIDTH),
        _video_dimension(output_format.get("height"), "height", DEFAULT_VIDEO_HEIGHT),
    )


def _timeline_config(spec: Mapping[str, Any]) -> tuple[float, int, int]:
    timeline = spec.get("timeline") if isinstance(spec.get("timeline"), dict) else {}
    try:
        playback_rate = float(timeline.get("playbackRate", timeline.get("speed", 2.0)))
        input_rate = float(timeline.get("inputPlaybackRate", 1.0))
    except (TypeError, ValueError) as exc:
        raise WorkerError("timeline playbackRate and inputPlaybackRate must be numeric") from exc
    if not math.isfinite(playback_rate) or not math.isfinite(input_rate) or playback_rate <= 0 or input_rate <= 0:
        raise WorkerError("timeline playbackRate and inputPlaybackRate must be greater than zero")
    width, height = _video_dimensions(spec)
    return playback_rate / input_rate, width, height


def _atempo_chain(rate: float) -> list[str]:
    if rate <= 0:
        raise WorkerError("effective playback rate must be greater than zero")
    filters: list[str] = []
    while rate > 2.0:
        filters.append("atempo=2.0")
        rate /= 2.0
    while rate < 0.5:
        filters.append("atempo=0.5")
        rate /= 0.5
    filters.append(f"atempo={rate:.10g}")
    return filters


def _format_vtt_time(milliseconds: int) -> str:
    hours, remainder = divmod(max(0, milliseconds), 3_600_000)
    minutes, remainder = divmod(remainder, 60_000)
    seconds, millis = divmod(remainder, 1_000)
    return f"{hours:02d}:{minutes:02d}:{seconds:02d}.{millis:03d}"


def _ass_time(milliseconds: int) -> str:
    total_cs = max(0, round(milliseconds / 10))
    hours, remainder = divmod(total_cs, 360_000)
    minutes, remainder = divmod(remainder, 6_000)
    seconds, centiseconds = divmod(remainder, 100)
    return f"{hours}:{minutes:02d}:{seconds:02d}.{centiseconds:02d}"


def _ass_escape(text: str) -> str:
    return text.replace("\\", r"\\").replace("{", r"\{").replace("}", r"\}").replace("\n", r"\N")


def write_subtitles(output_dir: Path, captions: Sequence[Mapping[str, Any]], width: int, height: int) -> tuple[Path, Path]:
    vtt = output_dir / "subtitles.vtt"
    lines = ["WEBVTT", ""]
    for index, caption in enumerate(captions, 1):
        lines.extend([str(index), f"{_format_vtt_time(int(caption['startMs']))} --> {_format_vtt_time(int(caption['endMs']))}", str(caption["text"]), ""])
    vtt.write_text("\n".join(lines), encoding="utf-8")
    ass = output_dir / "subtitles.ass"
    header = f"""[Script Info]
ScriptType: v4.00+
PlayResX: {width}
PlayResY: {height}
ScaledBorderAndShadow: yes

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,60,&H00FFFFFF,&H00FFFFFF,&H99000000,&H66000000,1,0,0,0,100,100,0,0,1,3,1,2,70,160,300,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
"""
    events = [header]
    for caption in captions:
        events.append(f"Dialogue: 0,{_ass_time(int(caption['startMs']))},{_ass_time(int(caption['endMs']))},Default,,0,0,0,,{_ass_escape(str(caption['text']))}\n")
    ass.write_text("".join(events), encoding="utf-8-sig")
    return vtt, ass


def _source_audio_path(segment: Mapping[str, Any], base: Path) -> Path | None:
    value = _segment_audio_value(segment)
    if isinstance(value, dict):
        value = value.get("src", value.get("path"))
    return resolve_path(value, base)


_CAPTION_PUNCTUATION = set("，。！？；：、,.!?;:!?…—-()（）【】[]{}\"'“”‘’")
_CJK_RANGE_START = "\u3400"
_CJK_RANGE_END = "\u9fff"


def _is_cjk(character: str) -> bool:
    return _CJK_RANGE_START <= character <= _CJK_RANGE_END


def _caption_tokens(text: str, max_cjk_phrase: int = 6) -> list[str]:
    """Split caption text into readable phrases while retaining punctuation."""
    normalized = re.sub(r"\s+", " ", text).strip()
    tokens: list[str] = []
    phrase: list[str] = []

    def flush() -> None:
        if phrase:
            tokens.append("".join(phrase))
            phrase.clear()

    index = 0
    while index < len(normalized):
        character = normalized[index]
        if character.isspace():
            flush()
        elif character in _CAPTION_PUNCTUATION:
            flush()
            tokens.append(character)
        elif _is_cjk(character):
            phrase.append(character)
            if len(phrase) >= max_cjk_phrase:
                flush()
        elif character.isalnum() or character in "_./%+":
            phrase.append(character)
        else:
            flush()
            tokens.append(character)
        index += 1
    flush()
    return [token for token in tokens if token]


def _caption_token_weight(token: str) -> float:
    if token and all(character in _CAPTION_PUNCTUATION for character in token):
        return 0.35
    return float(max(1, len(token)))


def _word_captions(cue: Mapping[str, Any]) -> list[dict[str, int | str]]:
    start = int(cue["startMs"])
    end = int(cue["endMs"])
    tokens = _caption_tokens(str(cue.get("text", "")))
    if not tokens or end <= start:
        return []
    duration = end - start
    if len(tokens) > duration:
        # A valid Remotion caption must have a non-empty interval. Extremely
        # short synthetic fixtures are made readable by merging adjacent
        # tokens until there is at most one token per millisecond.
        merged: list[str] = []
        for token in tokens:
            if len(merged) < duration:
                merged.append(token)
            else:
                merged[-1] += token
        tokens = merged
    weights = [_caption_token_weight(token) for token in tokens]
    total_weight = sum(weights)
    result: list[dict[str, int | str]] = []
    cursor = start
    for index, (token, weight) in enumerate(zip(tokens, weights)):
        if index == len(tokens) - 1:
            token_end = end
        else:
            token_end = start + round(duration * sum(weights[: index + 1]) / total_weight)
            token_end = max(cursor + 1, min(end - (len(tokens) - index - 1), token_end))
        result.append({"word": token, "startMs": cursor, "endMs": token_end})
        cursor = token_end
    result[-1]["endMs"] = end
    return result


def _pause_ms(segment: Mapping[str, Any]) -> int:
    pause = int(segment.get("pauseAfterMs", segment.get("pauseMs", 0)))
    if pause < 0:
        raise WorkerError(f"segment {segment['id']} pause cannot be negative")
    return pause


_MEDIA_EXTENSIONS = {
    ".bmp", ".gif", ".jpeg", ".jpg", ".m4v", ".mkv", ".mov", ".mp4",
    ".png", ".tif", ".tiff", ".webm", ".webp", ".avi",
}


def _validated_media_reference(value: Any, base: Path | None) -> str:
    """Return only an existing media path or a syntactically valid HTTP URL."""
    if not isinstance(value, (str, Path)):
        return ""
    raw = str(value).strip()
    if not raw:
        return ""
    parsed = urllib.parse.urlsplit(raw)
    if parsed.scheme in {"http", "https"} and parsed.netloc:
        return raw
    candidate = Path(raw).expanduser()
    if not candidate.is_absolute() and base is not None:
        candidate = base / candidate
    try:
        resolved = candidate.resolve()
    except OSError:
        return ""
    if resolved.is_file() and resolved.suffix.lower() in _MEDIA_EXTENSIONS:
        return str(resolved)
    return ""


def _scene_data(spec: Mapping[str, Any], measured: Sequence[Mapping[str, Any]], duration_ms: int, playback_rate: float, base: Path | None = None) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    scenes = spec.get("scenes", spec.get("sceneSegments", [])) or []
    if not isinstance(scenes, list):
        raise WorkerError("scenes must be an array")
    if not scenes:
        # A segment-only ProductionSpec is still renderable. Use stable segment
        # ids rather than scene-N placeholders so Explainer never displays an
        # implementation detail as the on-screen copy.
        scenes = [{"id": str(item["id"]), "segmentIds": [str(item["id"])]} for item in measured]
    by_id = {str(item["id"]): item for item in measured}
    windows: list[tuple[dict[str, Any], int]] = []
    seen: set[str] = set()
    for scene in scenes:
        if not isinstance(scene, dict) or not scene.get("id"):
            raise WorkerError("each scene requires id")
        scene_id = str(scene["id"])
        if scene_id in seen:
            raise WorkerError(f"duplicate scene id: {scene_id}")
        seen.add(scene_id)
        ids = scene.get("segmentIds", scene.get("segments"))
        if not isinstance(ids, list) or not ids:
            raise WorkerError(f"scene {scene['id']} requires segmentIds")
        selected = [by_id.get(str(value)) for value in ids]
        if any(item is None for item in selected):
            missing = [str(value) for value, item in zip(ids, selected) if item is None]
            raise WorkerError(f"scene {scene['id']} references unknown segment(s): {', '.join(missing)}")
        windows.append((scene, min(int(item["sourceStartMs"]) for item in selected if item)))
    if any(windows[index][1] > windows[index + 1][1] for index in range(len(windows) - 1)):
        raise WorkerError("scenes must be ordered by their referenced segment start")
    cuts: list[dict[str, Any]] = []
    overlays: list[dict[str, Any]] = []
    supported_types = {
        "text_card", "stat_card", "callout", "comparison", "hero_title",
        "terminal_scene", "screenshot_scene", "bar_chart", "line_chart",
        "pie_chart", "kpi_grid", "progress_bar", "anime_scene",
    }
    fallback_types = ("text_card", "callout")
    for index, (scene, source_start) in enumerate(windows):
        source_end = windows[index + 1][1] if index + 1 < len(windows) else duration_ms
        cut = dict(scene.get("props") or {})
        scene_id = str(scene["id"])
        ids = scene.get("segmentIds", scene.get("segments")) or []
        selected = [by_id[str(value)] for value in ids if str(value) in by_id]
        source_text = " ".join(str(item.get("text", "")).strip() for item in selected if str(item.get("text", "")).strip()).strip()
        label = next((str(item.get("editorialLabel", "")).strip() for item in selected if str(item.get("editorialLabel", "")).strip()), "")
        visual_description = str(cut.get("visual") or scene.get("visual") or "").strip()
        content = str(cut.get("text") or source_text or cut.get("title") or label or visual_description or "本段核心信息").strip()
        if not content:
            content = "本段核心信息"
        requested_type = str(scene.get("type") or cut.get("type") or "").strip()
        cut_type = requested_type if requested_type in supported_types else ("hero_title" if index == 0 else fallback_types[(index - 1) % len(fallback_types)])
        # Existing OpenMontage components need text-bearing props. Normalize
        # incomplete/arbitrary scene metadata instead of letting Explainer's
        # fallback render scene ids such as "scene-1".
        cut.update({
            "id": scene_id,
            "type": cut_type,
            "text": content,
            "in_seconds": _scaled_ms(source_start, playback_rate) / 1000,
            "out_seconds": _scaled_ms(source_end, playback_rate) / 1000,
        })
        if cut_type == "hero_title":
            cut.setdefault("heroSubtitle", label or "先看关键结论")
        elif cut_type == "callout":
            cut.setdefault("title", label or "重点信息")
            cut.setdefault("callout_type", "info")
        if scene.get("asset"):
            cut["asset"] = scene["asset"]
        source = _validated_media_reference(cut.get("source") or scene.get("source") or scene.get("asset"), base)
        if source:
            cut["source"] = source
        else:
            cut.pop("source", None)
        cuts.append(cut)
        if isinstance(scene.get("overlay"), dict):
            overlay = dict(scene["overlay"])
            overlay.update({"in_seconds": _scaled_ms(source_start, playback_rate) / 1000, "out_seconds": _scaled_ms(source_end, playback_rate) / 1000})
            overlays.append(overlay)
    return cuts, overlays


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _artifact(artifact_id: str, kind: str, path: Path, output_dir: Path, media_type: str = "") -> dict[str, Any]:
    item = {"id": artifact_id, "kind": kind, "path": str(path.resolve()), "relativePath": path.resolve().relative_to(output_dir.resolve()).as_posix(), "bytes": path.stat().st_size, "sha256": _sha256(path)}
    if media_type:
        item["mediaType"] = media_type
    return item


class _OutputAssetRequestHandler(BaseHTTPRequestHandler):
    """Serve only files below one worker output directory."""

    server_version = "MediaWorkerAssetServer/1.0"

    def _asset_server(self) -> "_OutputAssetServer":
        return self.server.asset_server  # type: ignore[attr-defined]

    def _send_file(self, head_only: bool) -> None:
        asset_server = self._asset_server()
        parsed = urllib.parse.urlsplit(self.path)
        path = asset_server.resolve_request_path(parsed.path)
        if path is None:
            self.send_error(404)
            return
        try:
            size = path.stat().st_size
            handle = path.open("rb")
        except (OSError, ValueError):
            self.send_error(404)
            return
        with handle:
            self.send_response(200)
            self.send_header("Content-Type", mimetypes.guess_type(path.name)[0] or "application/octet-stream")
            self.send_header("Content-Length", str(size))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            if not head_only:
                shutil.copyfileobj(handle, self.wfile)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        self._send_file(False)

    def do_HEAD(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler API
        self._send_file(True)

    def log_message(self, format: str, *args: Any) -> None:
        del format, args


class _OutputAssetServer:
    """Short-lived loopback server for Remotion's local asset fetches."""

    def __init__(self, output_dir: Path) -> None:
        self.root = output_dir.resolve()
        self.token = secrets.token_urlsafe(24)
        self._server: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None

    def __enter__(self) -> "_OutputAssetServer":
        self.root.mkdir(parents=True, exist_ok=True)
        server = ThreadingHTTPServer(("127.0.0.1", 0), _OutputAssetRequestHandler)
        server.daemon_threads = True
        server.asset_server = self  # type: ignore[attr-defined]
        self._server = server
        self._thread = threading.Thread(target=server.serve_forever, name="media-worker-assets", daemon=True)
        self._thread.start()
        return self

    @property
    def base_url(self) -> str:
        if self._server is None:
            raise WorkerError("local asset server is not running")
        return f"http://127.0.0.1:{self._server.server_port}/{self.token}/"

    def resolve_request_path(self, request_path: str) -> Path | None:
        prefix = f"/{self.token}/"
        if not request_path.startswith(prefix):
            return None
        encoded_relative = request_path[len(prefix):]
        try:
            relative = urllib.parse.unquote_to_bytes(encoded_relative).decode("utf-8")
        except UnicodeDecodeError:
            return None
        if not relative or "\x00" in relative:
            return None
        candidate = (self.root / relative.replace("/", os.sep)).resolve()
        if candidate != self.root and self.root not in candidate.parents:
            return None
        return candidate if candidate.is_file() else None

    def url_for(self, path: Path) -> str:
        candidate = path.resolve()
        if candidate != self.root and self.root not in candidate.parents:
            raise WorkerError(f"render asset must stay inside --output: {candidate}")
        if not candidate.is_file():
            raise WorkerError(f"render asset does not exist: {candidate}")
        relative = candidate.relative_to(self.root).as_posix()
        encoded = urllib.parse.quote(relative, safe="/")
        return f"{self.base_url}{encoded}"

    def __exit__(self, exc_type: Any, exc_value: Any, traceback: Any) -> None:
        del exc_type, exc_value, traceback
        server = self._server
        thread = self._thread
        self._server = None
        self._thread = None
        if server is None:
            return
        server.shutdown()
        server.server_close()
        if thread is not None:
            thread.join(timeout=COMMAND_TIMEOUT_SECONDS)
            if thread.is_alive():
                raise WorkerError("local asset server did not shut down cleanly")


@contextlib.contextmanager
def _render_props_with_local_asset_url(props_path: Path, output_dir: Path, asset_path: Path):
    """Temporarily expose the narration file as an HTTP URL for Remotion."""
    props = read_json(props_path)
    narration = props.get("audio", {}).get("narration") if isinstance(props.get("audio"), dict) else None
    if not isinstance(narration, dict):
        raise WorkerError("Remotion props require audio.narration")
    original_src = narration.get("src")
    with _OutputAssetServer(output_dir) as asset_server:
        narration["src"] = asset_server.url_for(asset_path)
        write_json(props_path, props)
        try:
            yield
        finally:
            narration["src"] = original_src
            write_json(props_path, props)


def _render(spec: Mapping[str, Any], output_dir: Path, props_path: Path, base: Path) -> Path | None:
    if not _render_enabled(spec):
        return None
    render = spec["render"]
    paths = spec.get("paths") if isinstance(spec.get("paths"), dict) else {}
    remotion = _configured_command(paths.get("remotion"), base)
    if not remotion:
        raise CapabilityBlocker("remotion", "configured Remotion executable is unavailable")
    target = resolve_path(render.get("output", "render.mp4"), output_dir)
    if target is None or (target != output_dir and output_dir.resolve() not in target.parents):
        raise WorkerError("render.output must stay inside --output")
    target.parent.mkdir(parents=True, exist_ok=True)
    prefix = paths.get("remotionArgsPrefix", [])
    if not isinstance(prefix, list):
        raise WorkerError("paths.remotionArgsPrefix must be an array")
    _, width, height = _timeline_config(spec)
    args = [
        remotion,
        *[str(item) for item in prefix],
        "render",
        str(render.get("entry", "src/index.tsx")),
        str(render.get("composition", "Explainer")),
        str(target),
        "--props", str(props_path),
        "--codec", str(render.get("codec", "h264")),
        "--width", str(width),
        "--height", str(height),
    ]
    extra = render.get("extraArgs", [])
    if not isinstance(extra, list):
        raise WorkerError("render.extraArgs must be an array")
    audio = read_json(props_path).get("audio", {})
    narration = audio.get("narration", {}) if isinstance(audio, dict) else {}
    audio_src = narration.get("src") if isinstance(narration, dict) else None
    if not audio_src:
        raise WorkerError("Remotion props require audio.narration.src")
    with _render_props_with_local_asset_url(props_path, output_dir, Path(str(audio_src))):
        _run(args + [str(item) for item in extra], cwd=resolve_path(render.get("cwd"), base))
    if not target.exists():
        raise WorkerError(f"Remotion completed without its declared output: {target}")
    return target


def _extract_cover(spec: Mapping[str, Any], output_dir: Path, video_path: Path, width: int, height: int, base: Path) -> Path:
    """Extract a deterministic cover only after video render succeeds."""
    ffmpeg = command_path(spec, "ffmpeg", base)
    if not ffmpeg:
        raise CapabilityBlocker("ffmpeg", "FFmpeg is required to extract the rendered cover", "Set paths.ffmpeg to the compositor FFmpeg binary.")
    ffprobe = command_path(spec, "ffprobe", base)
    if not ffprobe:
        raise CapabilityBlocker("ffprobe", "FFprobe is required to select a safe cover frame time", "Set paths.ffprobe to the matching FFprobe binary.")
    duration_process = _run([
        ffprobe, "-v", "error", "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1", str(video_path),
    ])
    try:
        duration = float(duration_process.stdout.strip())
    except (TypeError, ValueError) as exc:
        raise WorkerError(f"FFprobe returned no numeric video duration for {video_path}") from exc
    if not math.isfinite(duration) or duration <= 0:
        raise WorkerError(f"FFprobe returned an invalid video duration for {video_path}: {duration!r}")
    timestamp = max(0.0, min(DEFAULT_COVER_TIMESTAMP_SECONDS, duration - COVER_SAFE_MARGIN_SECONDS))
    cover = (output_dir / "cover.png").resolve()
    output_root = output_dir.resolve()
    if cover != output_root and output_root not in cover.parents:
        raise WorkerError("cover path must stay inside --output")
    _run([
        ffmpeg, "-y", "-ss", f"{timestamp:.3f}", "-i", str(video_path),
        "-frames:v", "1", "-update", "1", str(cover),
    ])
    if not cover.is_file() or cover.stat().st_size <= 0:
        raise WorkerError(f"FFmpeg completed without its declared cover output: {cover}")
    _assert_png_dimensions(cover, width, height)
    return cover


def _cover_variant_path(output_dir: Path, name: str) -> Path:
    path = (output_dir / name).resolve()
    output_root = output_dir.resolve()
    if path == output_root or output_root not in path.parents:
        raise WorkerError(f"cover variant path must stay inside --output: {path}")
    return path


def _center_crop_box(width: int, height: int, target_width: int, target_height: int) -> tuple[int, int, int, int]:
    source_ratio = width / height
    target_ratio = target_width / target_height
    if source_ratio > target_ratio:
        crop_width = max(1, round(height * target_ratio))
        left = max(0, (width - crop_width) // 2)
        return left, 0, left + crop_width, height
    crop_height = max(1, round(width / target_ratio))
    top = max(0, (height - crop_height) // 2)
    return 0, top, width, top + crop_height


def _assert_png_dimensions(path: Path, expected_width: int | None = None, expected_height: int | None = None) -> tuple[int, int]:
    try:
        header = path.read_bytes()[:24]
    except OSError as exc:
        raise WorkerError(f"could not read generated PNG {path}: {exc}") from exc
    if len(header) < 24 or header[:8] != b"\x89PNG\r\n\x1a\n" or header[12:16] != b"IHDR":
        raise WorkerError(f"generated cover is not a valid PNG: {path}")
    width, height = struct.unpack(">II", header[16:24])
    if expected_width is not None and expected_height is not None and (width, height) != (expected_width, expected_height):
        raise WorkerError(f"generated cover has dimensions {width}x{height}, expected {expected_width}x{expected_height}: {path}")
    return width, height


def _write_cover_variants_with_pillow(cover: Path, output_dir: Path) -> list[Path] | None:
    try:
        from PIL import Image  # type: ignore[import-not-found]
    except ImportError:
        return None
    try:
        with Image.open(cover) as source:
            source.load()
            source = source.convert("RGBA")
            variants = []
            for name, size in (("cover-portrait-3x4.png", COVER_PORTRAIT_SIZE), ("cover-landscape-4x3.png", COVER_LANDSCAPE_SIZE)):
                target = _cover_variant_path(output_dir, name)
                cropped = source.crop(_center_crop_box(source.width, source.height, *size))
                resampling = getattr(Image, "Resampling", Image).LANCZOS
                cropped.resize(size, resampling).save(target, format="PNG")
                _assert_png_dimensions(target, *size)
                variants.append(target)
            return variants
    except (OSError, ValueError) as exc:
        raise WorkerError(f"Pillow could not create cover variants from {cover}: {exc}") from exc


def _ffmpeg_filter_capability(spec: Mapping[str, Any], ffmpeg: str) -> None:
    try:
        process = _run([ffmpeg, "-hide_banner", "-filters"])
    except WorkerError as exc:
        raise CapabilityBlocker(
            "ffmpeg-filters",
            f"could not inspect FFmpeg filters at {ffmpeg!r}: {exc}",
            "Install an FFmpeg build that exposes scale, crop, and pad filters, or install Pillow in the worker Python environment.",
        ) from exc
    listing = f"{process.stdout}\n{process.stderr}"
    missing = [name for name in ("scale", "crop", "pad") if not re.search(rf"\b{re.escape(name)}\b", listing)]
    if missing:
        raise CapabilityBlocker(
            "ffmpeg-filters",
            f"FFmpeg at {ffmpeg!r} lacks required cover filters: {', '.join(missing)}",
            "Install an FFmpeg build with scale, crop, and pad filters, or install Pillow in the worker Python environment.",
        )


def _write_cover_variants_with_ffmpeg(spec: Mapping[str, Any], cover: Path, output_dir: Path, base: Path) -> list[Path]:
    del cover  # The rendered video is the source of truth; the frame is retained for compatibility.
    ffmpeg = command_path(spec, "ffmpeg", base)
    if not ffmpeg:
        raise CapabilityBlocker("ffmpeg", "FFmpeg is required to create cover variants", "Set paths.ffmpeg to the compositor FFmpeg binary.")
    _ffmpeg_filter_capability(spec, ffmpeg)
    portrait = _cover_variant_path(output_dir, "cover-portrait-3x4.png")
    landscape = _cover_variant_path(output_dir, "cover-landscape-4x3.png")
    # The source frame is already available as cover.png. Fit it inside each
    # target canvas, then crop/pad deterministically to exact dimensions.
    commands = [
        (portrait, f"scale={COVER_PORTRAIT_SIZE[0]}:{COVER_PORTRAIT_SIZE[1]}:force_original_aspect_ratio=increase,crop={COVER_PORTRAIT_SIZE[0]}:{COVER_PORTRAIT_SIZE[1]}"),
        (landscape, f"scale={COVER_LANDSCAPE_SIZE[0]}:{COVER_LANDSCAPE_SIZE[1]}:force_original_aspect_ratio=decrease,pad={COVER_LANDSCAPE_SIZE[0]}:{COVER_LANDSCAPE_SIZE[1]}:(ow-iw)/2:(oh-ih)/2"),
    ]
    for target, filter_graph in commands:
        _run([ffmpeg, "-y", "-i", str(output_dir / "cover.png"), "-vf", filter_graph, "-frames:v", "1", "-update", "1", str(target)])
        if not target.is_file() or target.stat().st_size <= 0:
            raise WorkerError(f"FFmpeg completed without its declared cover variant: {target}")
        _assert_png_dimensions(target, *({portrait: COVER_PORTRAIT_SIZE, landscape: COVER_LANDSCAPE_SIZE}[target]))
    return [portrait, landscape]


def _write_cover_variants(spec: Mapping[str, Any], cover: Path, output_dir: Path, base: Path) -> list[Path]:
    variants = _write_cover_variants_with_pillow(cover, output_dir)
    if variants is not None:
        return variants
    return _write_cover_variants_with_ffmpeg(spec, cover, output_dir, base)


def build_job(job_path: Path, output_dir: Path, spec: dict[str, Any]) -> dict[str, Any]:
    base = _job_root(job_path, spec)
    playback_rate, width, height = _timeline_config(spec)
    output_dir.mkdir(parents=True, exist_ok=True)
    audio_config = spec.get("audio") if isinstance(spec.get("audio"), dict) else {}
    sample_rate = int(audio_config.get("sampleRate", 48000))
    channels = int(audio_config.get("channels", 2))
    measured: list[dict[str, Any]] = []
    pcm_parts: list[Path] = []
    cursor_ms = 0
    for segment in spec["segments"]:
        source = _source_audio_path(segment, base)
        generated = source is None
        vtt_path: Path | None = None
        if generated:
            source, vtt_path = synthesize_segment(spec, segment, output_dir, base)
        if not source or not source.exists():
            raise WorkerError(f"audio input does not exist for segment {segment['id']}: {source}")
        source_duration = probe_duration(source, spec, base)
        normalized = output_dir / "pcm" / f"segment-{_safe_id(segment['id'])}.wav"
        normalized.parent.mkdir(parents=True, exist_ok=True)
        _convert_to_wav(source, normalized, spec, base, sample_rate, channels)
        pause_ms = _pause_ms(segment)
        item = {
            "id": str(segment["id"]), "text": str(segment.get("text", "")), "profile": segment.get("profile", "default"),
            "sourceAudioPath": str(source.resolve()), "sourceDurationMs": source_duration, "sourceStartMs": cursor_ms,
            "sourceEndMs": cursor_ms + source_duration, "pauseAfterMs": pause_ms, "sourceEndWithPauseMs": cursor_ms + source_duration + pause_ms,
            "startMs": _scaled_ms(cursor_ms, playback_rate), "endMs": _scaled_ms(cursor_ms + source_duration, playback_rate),
            "pauseAfterTimelineMs": _scaled_ms(pause_ms, playback_rate), "vttPath": str(vtt_path.resolve()) if vtt_path else "",
            "editorialLabel": segment.get("editorialLabel", ""),
        }
        if generated:
            item["tts"] = _voice_params(spec, segment)
        measured.append(item)
        pcm_parts.append(normalized)
        cursor_ms += source_duration
        if pause_ms:
            silence = output_dir / "pcm" / f"pause-after-{_safe_id(segment['id'])}.wav"
            _write_silence(silence, pause_ms, sample_rate, channels)
            pcm_parts.append(silence)
            cursor_ms += pause_ms
    source_duration_ms = cursor_ms
    concatenated_wav = output_dir / "pcm" / "concatenated.wav"
    _concat_wav(pcm_parts, concatenated_wav)
    narration_wav = output_dir / "narration.wav"
    if playback_rate != 1.0:
        ffmpeg = command_path(spec, "ffmpeg", base)
        if not ffmpeg:
            raise CapabilityBlocker("ffmpeg", "FFmpeg is required to apply timeline playback rate to narration", "Set paths.ffmpeg to the compositor FFmpeg binary.")
        _run([ffmpeg, "-y", "-i", str(concatenated_wav), "-filter:a", ",".join(_atempo_chain(playback_rate)), "-c:a", "pcm_s16le", str(narration_wav)])
    else:
        shutil.copyfile(concatenated_wav, narration_wav)
    output_format = _output_format(spec)
    narration_mp3: Path | None = None
    if output_format == "mp3":
        narration_mp3 = output_dir / "narration.mp3"
        _encode_mp3(narration_wav, narration_mp3, spec, base)
    subtitle_captions: list[dict[str, Any]] = []
    captions: list[dict[str, Any]] = []
    for item, segment in zip(measured, spec["segments"]):
        cues = parse_vtt(Path(item["vttPath"]) if item["vttPath"] else None, int(item["sourceDurationMs"]))
        caption_lines = segment.get("captionLines")
        if isinstance(caption_lines, list) and caption_lines:
            cues = [{"startMs": 0, "endMs": int(item["sourceDurationMs"]), "text": "\n".join(str(line) for line in caption_lines)}]
        if not cues:
            cues = [{"startMs": 0, "endMs": int(item["sourceDurationMs"]), "text": item["text"]}]
        for cue in cues:
            absolute_cue = {
                "segmentId": item["id"],
                "text": cue["text"],
                "startMs": _scaled_ms(int(item["sourceStartMs"]) + int(cue["startMs"]), playback_rate),
                "endMs": _scaled_ms(int(item["sourceStartMs"]) + int(cue["endMs"]), playback_rate),
                "editorialLabel": item["editorialLabel"],
            }
            subtitle_captions.append(absolute_cue)
            word_cue = {"startMs": absolute_cue["startMs"], "endMs": absolute_cue["endMs"], "text": absolute_cue["text"]}
            captions.extend(_word_captions(word_cue))
    subtitles_vtt, subtitles_ass = write_subtitles(output_dir, subtitle_captions, width, height)
    total_timeline_ms = _scaled_ms(source_duration_ms, playback_rate)
    cuts, overlays = _scene_data(spec, measured, source_duration_ms, playback_rate, base)
    audio_src = str((narration_mp3 or narration_wav).resolve())
    props = {
        "productionSpecVersion": PRODUCTION_SPEC_VERSION, "jobId": spec["jobId"], "format": {"width": width, "height": height},
        "cuts": cuts, "overlays": overlays, "captions": captions,
        "audio": {"narration": {"src": audio_src, "volume": 1, "playbackRate": 1}},
        "timeline": {"sourceDurationMs": source_duration_ms, "durationMs": total_timeline_ms, "playbackRate": playback_rate, "inputPlaybackRate": 1, "rateAppliedOnce": True},
        "subtitles": {"vtt": str(subtitles_vtt.resolve()), "ass": str(subtitles_ass.resolve())},
    }
    props_path = output_dir / "remotion-props.json"
    write_json(props_path, props)
    rendered = _render(spec, output_dir, props_path, base)
    cover: Path | None = None
    cover_variants: list[Path] = []
    if rendered:
        # A rendered video is not a complete media result until its required
        # cover succeeds. Let extraction errors abort before a video artifact
        # can be declared in the manifest.
        cover = _extract_cover(spec, output_dir, rendered, width, height, base)
        cover_variants = _write_cover_variants(spec, cover, output_dir, base)
    artifacts = [_artifact("narration-wav", "audio", narration_wav, output_dir, "audio/wav"), _artifact("subtitles-vtt", "subtitle", subtitles_vtt, output_dir, "text/vtt"), _artifact("subtitles-ass", "subtitle", subtitles_ass, output_dir, "text/x-ass"), _artifact("remotion-props", "props", props_path, output_dir, "application/json")]
    if narration_mp3:
        artifacts.append(_artifact("narration-mp3", "audio", narration_mp3, output_dir, "audio/mpeg"))
    if rendered:
        artifacts.append(_artifact("rendered-video", "video", rendered, output_dir, "video/mp4"))
    if cover:
        artifacts.append(_artifact("cover", "cover", cover, output_dir, "image/png"))
    if cover_variants:
        artifacts.extend([
            _artifact("cover-portrait-3x4", "cover", cover_variants[0], output_dir, "image/png"),
            _artifact("cover-landscape-4x3", "cover", cover_variants[1], output_dir, "image/png"),
        ])
    events = {
        "schemaVersion": EVENTS_VERSION, "kind": "events", "jobId": spec["jobId"],
        "timeline": {"sourceDurationMs": source_duration_ms, "durationMs": total_timeline_ms, "playbackRate": playback_rate, "rateAppliedOnce": True},
        "events": [{"type": "job.started", "jobId": spec["jobId"]}] + [
            {"type": "segment.measured", "segmentId": item["id"], "sourceStartMs": item["sourceStartMs"], "sourceEndMs": item["sourceEndMs"], "startMs": item["startMs"], "endMs": item["endMs"], "pauseAfterMs": item["pauseAfterMs"], "pauseAfterTimelineMs": item["pauseAfterTimelineMs"], "sourceDurationMs": item["sourceDurationMs"]}
            for item in measured
        ] + [{"type": "subtitles.generated", "cueCount": len(subtitle_captions), "wordCount": len(captions)}, {"type": "job.completed", "durationMs": total_timeline_ms}],
    }
    events_path = output_dir / "events.json"
    write_json(events_path, events)
    artifacts.append(_artifact("events", "events", events_path, output_dir, "application/json"))
    manifest = {
        "schemaVersion": ARTIFACT_MANIFEST_VERSION, "kind": "artifact-manifest", "jobId": spec["jobId"], "productionSpecVersion": PRODUCTION_SPEC_VERSION,
        "timeline": {"sourceDurationMs": source_duration_ms, "durationMs": total_timeline_ms, "playbackRate": playback_rate, "rateAppliedOnce": True}, "artifacts": artifacts,
    }
    manifest_path = output_dir / "artifact-manifest.json"
    write_json(manifest_path, manifest)
    return {"ok": True, "jobId": spec["jobId"], "output": str(output_dir.resolve()), "artifactManifest": str(manifest_path.resolve()), "events": str(events_path.resolve()), "durationMs": total_timeline_ms, "segments": len(measured), "rendered": bool(rendered)}


def run_job(job: str | Path, output: str | Path, validate_only: bool = False) -> dict[str, Any]:
    job_path = Path(job).expanduser().resolve()
    output_dir = Path(output).expanduser().resolve()
    spec = normalize_spec(read_json(job_path))
    report = capability_report(spec, job_path)
    if validate_only:
        return report
    require_capabilities(report)
    return build_job(job_path, output_dir, spec)


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--job", required=True, help="ProductionSpec JSON")
    parser.add_argument("--output", required=True, help="worker-owned output directory")
    parser.add_argument("--validate-only", action="store_true", help="check the contract and capabilities without executing commands")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    try:
        args = _parser().parse_args(argv)
        result = run_job(args.job, args.output, args.validate_only)
        print(json.dumps(result, ensure_ascii=False, indent=2))
        return 0 if result.get("valid", True) else 2
    except CapabilityBlocker as exc:
        print(json.dumps({"ok": False, "error": exc.as_dict()}, ensure_ascii=False, indent=2), file=sys.stderr)
        return 2
    except WorkerError as exc:
        print(json.dumps({"ok": False, "error": {"code": "worker_error", "message": str(exc)}}, ensure_ascii=False, indent=2), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
