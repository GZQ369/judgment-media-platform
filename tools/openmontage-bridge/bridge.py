"""Execute real OpenMontage registry tools for the ProductionAgent slice."""
import argparse
import json
import sys
import wave
import shutil
import os
import subprocess
from pathlib import Path


def wav_format_signature(params):
    """Return WAV encoding fields, excluding per-file frame count."""
    return (params.nchannels, params.sampwidth, params.framerate, params.comptype, params.compname)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    parser.add_argument("--request", required=True)
    parser.add_argument("--stage", choices=["narrate", "render"], required=True)
    args = parser.parse_args()
    scripts = str(Path(sys.executable).resolve().parent)
    os.environ["PATH"] = scripts + os.pathsep + os.environ.get("PATH", "")
    root = Path(args.root).resolve()
    sys.path.insert(0, str(root))
    request = json.loads(Path(args.request).read_text(encoding="utf-8"))
    from tools.tool_registry import registry
    from tools.base_tool import ToolStatus

    registry.discover()
    if args.stage == "narrate":
        tts = registry.get("piper_tts")
        segments = request.get("segments") or []
        if tts is None:
            raise RuntimeError("CAPABILITY_BLOCKER narration: OpenMontage piper_tts registry tool is not registered")
        if not shutil.which("piper"):
            raise RuntimeError("CAPABILITY_BLOCKER narration: piper.exe is not on the bridge PATH; add the OpenMontage venv Scripts directory to PATH")
        model = Path(str(request.get("tts_model") or "")).expanduser()
        if not model.is_file() or model.suffix.lower() != ".onnx" or not Path(str(model) + ".json").is_file():
            raise RuntimeError("CAPABILITY_BLOCKER narration: no Piper .onnx voice model and matching .onnx.json config are installed. Set OPENMONTAGE_PIPER_MODEL to a real model file; provider AVAILABLE status alone is insufficient.")
        narration_path = Path(request["narration_path"])
        narration_path.parent.mkdir(parents=True, exist_ok=True)
        wavs = []
        rate = float(request.get("playback_rate", 1.0))
        if rate < 0.5 or rate > 2.0:
            raise RuntimeError("CAPABILITY_BLOCKER playback_rate: Piper atempo rate must be between 0.5x and 2.0x")
        ffmpeg = Path(request["ffprobe"]).with_name("ffmpeg.exe")
        for index, segment in enumerate(segments):
            text = str(segment.get("text", "")).strip()
            if not text:
                raise RuntimeError(f"CAPABILITY_BLOCKER narration: segment {index + 1} has no speech text")
            wav_path = narration_path.with_name(f".voice-{index + 1}.wav")
            tts_result = tts.execute({"text": text, "model": str(model), "output_path": str(wav_path)})
            if not tts_result.success or not wav_path.is_file() or wav_path.stat().st_size == 0:
                detail = tts_result.error or "Piper returned success without a WAV file"
                raise RuntimeError(f"CAPABILITY_BLOCKER narration: local Piper generation failed for segment {index + 1}: {detail}. Configure OPENMONTAGE_PIPER_MODEL with an installed .onnx voice model; no prerecorded-audio fallback is used.")
            if rate != 1.0:
                if not ffmpeg.is_file():
                    raise RuntimeError("CAPABILITY_BLOCKER playback_rate: compositor ffmpeg.exe is unavailable for one-time narration atempo")
                adjusted = wav_path.with_name(f".tempo-{index + 1}.wav")
                result = subprocess.run([str(ffmpeg), "-y", "-i", str(wav_path), "-filter:a", f"atempo={rate:.6f}", "-c:a", "pcm_s16le", str(adjusted)], capture_output=True, text=True, timeout=300)
                if result.returncode != 0 or not adjusted.is_file() or adjusted.stat().st_size == 0:
                    raise RuntimeError(f"CAPABILITY_BLOCKER playback_rate: ffmpeg atempo failed for segment {index + 1}: {result.stderr[-1200:]}")
                wav_path.unlink(missing_ok=True)
                wav_path = adjusted
            wavs.append(wav_path)
        if not wavs:
            raise RuntimeError("CAPABILITY_BLOCKER narration: current production spec contains no speech segments")
        with wave.open(str(wavs[0]), "rb") as first:
            params = first.getparams()
            chunks = [first.readframes(first.getnframes())]
        for wav_path in wavs[1:]:
            with wave.open(str(wav_path), "rb") as current:
                if wav_format_signature(current.getparams()) != wav_format_signature(params):
                    raise RuntimeError("CAPABILITY_BLOCKER narration: Piper segment WAV formats differ and cannot be concatenated")
                chunks.append(current.readframes(current.getnframes()))
        if not any(any(chunk) for chunk in chunks):
            raise RuntimeError("CAPABILITY_BLOCKER narration: Piper returned silent audio samples")
        with wave.open(str(narration_path), "wb") as output:
            output.setparams(params)
            for chunk in chunks:
                output.writeframes(chunk)
        durations = []
        for wav_path in wavs:
            with wave.open(str(wav_path), "rb") as audio:
                durations.append(audio.getnframes() / audio.getframerate())
            wav_path.unlink(missing_ok=True)
        print(json.dumps({"ok": True, "narration": str(narration_path), "bytes": narration_path.stat().st_size, "segment_durations": durations}))
        return

    tool = registry.get("video_compose")
    if tool is None or tool.get_status() != ToolStatus.AVAILABLE:
        raise RuntimeError("OpenMontage video_compose registry tool is unavailable")
    result = tool.execute({
        "operation": "remotion_render",
        "composition_data": request["composition_data"],
        "output_path": request["output_path"],
        "profile": request.get("profile", "portrait_1080x1920"),
    })
    if not result.success:
        raise RuntimeError(result.error or "OpenMontage remotion_render failed")
    output = Path(request["output_path"])
    if not output.is_file() or output.stat().st_size <= 0:
        raise RuntimeError("OpenMontage reported success but produced no non-empty MP4")
    print(json.dumps({"ok": True, "output": str(output), "bytes": output.stat().st_size}))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(json.dumps({"ok": False, "error": str(exc)}))
        raise SystemExit(2)
