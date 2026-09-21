from __future__ import annotations

import json
import os
import shutil
import struct
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import unittest
import wave
import zlib
from pathlib import Path
from unittest.mock import patch


WORKER_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(WORKER_DIR))

import media_worker  # noqa: E402


def find_ffmpeg() -> str | None:
    configured = os.environ.get("MEDIA_WORKER_FFMPEG")
    if configured:
        configured_path = Path(configured).expanduser()
        if configured_path.is_file():
            return str(configured_path)
        discovered_configured = shutil.which(configured)
        if discovered_configured:
            return discovered_configured
    return shutil.which("ffmpeg")


FFMPEG = find_ffmpeg()


def make_wav(path: Path, milliseconds: int, sample_rate: int = 8000) -> None:
    frames = round(milliseconds * sample_rate / 1000)
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(2)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(b"\0\0" * 2 * frames)


def make_png(width: int, height: int) -> bytes:
    row = b"\0" + b"\0\0\0\xff" * width
    raw = row * height

    def chunk(kind: bytes, payload: bytes) -> bytes:
        return struct.pack(">I", len(payload)) + kind + payload + struct.pack(">I", zlib.crc32(kind + payload) & 0xffffffff)

    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


def png_size(path: Path) -> tuple[int, int]:
    data = path.read_bytes()
    return struct.unpack(">II", data[16:24])


class MediaWorkerTests(unittest.TestCase):
    def test_output_asset_server_serves_only_output_root_and_closes(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "output"
            root.mkdir()
            inside = root / "音频 clip.wav"
            inside.write_bytes(b"inside")
            outside = root.parent / "outside.wav"
            outside.write_bytes(b"outside")
            with media_worker._OutputAssetServer(root) as server:
                url = server.url_for(inside)
                self.assertEqual(urllib.request.urlopen(url, timeout=2).read(), b"inside")
                traversal = server.base_url + urllib.parse.quote("../outside.wav")
                with self.assertRaises(urllib.error.HTTPError) as raised:
                    urllib.request.urlopen(traversal, timeout=2)
                self.assertEqual(raised.exception.code, 404)
            with self.assertRaises(urllib.error.URLError):
                urllib.request.urlopen(url, timeout=2)

    def test_render_uses_loopback_asset_url_and_restores_props(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "out"
            output.mkdir()
            narration = output / "narration.wav"
            narration.write_bytes(b"audio")
            props_path = output / "remotion-props.json"
            original_src = str(narration.resolve())
            props_path.write_text(json.dumps({"audio": {"narration": {"src": original_src}}}), encoding="utf-8")
            captured: dict[str, object] = {}

            def fake_run(args, **kwargs):
                captured["args"] = list(args)
                props_arg = Path(args[args.index("--props") + 1])
                render_props = json.loads(props_arg.read_text(encoding="utf-8"))
                render_src = render_props["audio"]["narration"]["src"]
                parsed = urllib.parse.urlparse(render_src)
                self.assertEqual(parsed.scheme, "http")
                self.assertEqual(parsed.hostname, "127.0.0.1")
                captured["url"] = render_src
                self.assertEqual(urllib.request.urlopen(render_src, timeout=2).read(), b"audio")
                Path(args[args.index("--props") - 1]).write_bytes(b"video")
                return None

            spec = {
                "render": {"enabled": True, "output": "render.mp4", "cwd": str(root)},
                "paths": {"remotion": sys.executable},
            }
            with patch.object(media_worker, "_run", side_effect=fake_run):
                rendered = media_worker._render(spec, output, props_path, root)
            self.assertEqual(rendered, (output / "render.mp4").resolve())
            restored = json.loads(props_path.read_text(encoding="utf-8"))
            self.assertEqual(restored["audio"]["narration"]["src"], original_src)
            command = captured["args"]
            self.assertIn("render", command)
            self.assertEqual(command[command.index("--props") + 1], str(props_path))
            self.assertEqual(command[command.index("--codec") + 1], "h264")
            self.assertEqual(command[command.index("--width") + 1], "1080")
            self.assertEqual(command[command.index("--height") + 1], "1920")
            with self.assertRaises(urllib.error.URLError):
                urllib.request.urlopen(str(captured.get("url", "http://127.0.0.1:1/")), timeout=2)

    def test_props_use_readable_cuts_and_word_caption_schema(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "clip.wav", 1200)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "props-schema",
                "segments": [
                    {"id": "hook", "text": "这款产品到底值不值得买？", "audioPath": "clip.wav"},
                    {"id": "point", "text": "先看真实体验，再决定是否下单。", "audioPath": "clip.wav"},
                ],
                "timeline": {"playbackRate": 1},
                "audio": {"sampleRate": 8000, "channels": 2},
                "format": {"width": 1080, "height": 1920},
                "scenes": [
                    {"id": "scene-1", "segmentIds": ["hook"], "props": {"type": "arbitrary"}},
                    {"id": "scene-2", "segmentIds": ["point"], "props": {"label": "核心判断"}},
                ],
            }), encoding="utf-8")
            result = media_worker.run_job(job, root / "out")
            self.assertTrue(result["ok"])
            props = json.loads((root / "out" / "remotion-props.json").read_text(encoding="utf-8"))
            self.assertEqual(props["format"], {"width": 1080, "height": 1920})
            self.assertEqual(props["cuts"][0]["type"], "hero_title")
            self.assertIn("这款产品到底值不值得买", props["cuts"][0]["text"])
            self.assertEqual(props["cuts"][1]["type"], "text_card")
            self.assertNotIn("scene-1", props["cuts"][0]["text"])
            self.assertGreater(len(props["captions"]), 2)
            for caption in props["captions"]:
                self.assertEqual(set(caption), {"word", "startMs", "endMs"})
                self.assertIsInstance(caption["word"], str)
                self.assertLess(caption["startMs"], caption["endMs"])
                self.assertLessEqual(caption["endMs"], 2400)

    def test_rendered_cover_is_declared_only_after_successful_extraction(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "clip.wav", 500)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "cover-artifact",
                "segments": [{"id": "one", "text": "竖版封面测试", "audioPath": "clip.wav"}],
                "timeline": {"playbackRate": 1},
                "audio": {"sampleRate": 8000, "channels": 2},
                "render": {"enabled": True, "output": "render.mp4", "cwd": str(root)},
                "paths": {"ffmpeg": str(root / "ffmpeg.exe"), "remotion": str(root / "remotion.exe")},
            }), encoding="utf-8")
            output = root / "out"

            def fake_render(spec, output_dir, props_path, base):
                del spec, props_path, base
                video = output_dir / "render.mp4"
                video.write_bytes(b"video")
                return video

            def fake_cover(spec, output_dir, video_path, width, height, base):
                del spec, video_path, width, height, base
                cover = output_dir / "cover.png"
                cover.write_bytes(make_png(1080, 1920))
                return cover

            def fake_variants(spec, cover, output_dir, base):
                del spec, cover, base
                portrait = output_dir / "cover-portrait-3x4.png"
                landscape = output_dir / "cover-landscape-4x3.png"
                portrait.write_bytes(make_png(1080, 1440))
                landscape.write_bytes(make_png(1440, 1080))
                return [portrait, landscape]

            with patch.object(media_worker, "_render", side_effect=fake_render), patch.object(media_worker, "_extract_cover", side_effect=fake_cover), patch.object(media_worker, "_write_cover_variants", side_effect=fake_variants):
                result = media_worker.build_job(job, output, media_worker.normalize_spec(json.loads(job.read_text(encoding="utf-8"))))
            self.assertTrue(result["rendered"])
            manifest = json.loads((output / "artifact-manifest.json").read_text(encoding="utf-8"))
            artifacts = {item["id"]: item for item in manifest["artifacts"]}
            self.assertIn("rendered-video", artifacts)
            self.assertEqual(artifacts["cover"]["relativePath"], "cover.png")
            self.assertEqual(artifacts["cover-portrait-3x4"]["relativePath"], "cover-portrait-3x4.png")
            self.assertEqual(artifacts["cover-landscape-4x3"]["relativePath"], "cover-landscape-4x3.png")
            self.assertEqual(png_size(output / "cover-portrait-3x4.png"), (1080, 1440))
            self.assertEqual(png_size(output / "cover-landscape-4x3.png"), (1440, 1080))

    def test_cover_prefers_2_4_seconds_and_uses_safe_short_video_fallback(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "out"
            output.mkdir()
            video = output / "render.mp4"
            video.write_bytes(b"video")
            captured: list[list[str]] = []

            def fake_run(args, **kwargs):
                del kwargs
                command = list(args)
                captured.append(command)
                if "-show_entries" in command:
                    return type("Process", (), {"stdout": "4.0\n", "stderr": ""})()
                Path(command[-1]).write_bytes(make_png(1080, 1920))
                return type("Process", (), {"stdout": "", "stderr": ""})()

            with patch.object(media_worker, "_run", side_effect=fake_run):
                cover = media_worker._extract_cover({"paths": {"ffmpeg": sys.executable, "ffprobe": sys.executable}}, output, video, 1080, 1920, root)
            command = captured[-1]
            self.assertEqual(cover, (output / "cover.png").resolve())
            self.assertNotIn("-vf", command)
            self.assertNotIn("pad", " ".join(command))
            self.assertNotIn("crop", " ".join(command))
            self.assertEqual(command[command.index("-ss") + 1], "2.400")
            self.assertEqual(command[command.index("-frames:v") + 1], "1")
            self.assertEqual(command[command.index("-update") + 1], "1")

            captured.clear()

            def short_video_run(args, **kwargs):
                del kwargs
                command = list(args)
                captured.append(command)
                if "-show_entries" in command:
                    return type("Process", (), {"stdout": "1.0\n", "stderr": ""})()
                Path(command[-1]).write_bytes(make_png(1080, 1920))
                return type("Process", (), {"stdout": "", "stderr": ""})()

            with patch.object(media_worker, "_run", side_effect=short_video_run):
                media_worker._extract_cover({"paths": {"ffmpeg": sys.executable, "ffprobe": sys.executable}}, output, video, 1080, 1920, root)
            short_command = captured[-1]
            self.assertEqual(short_command[short_command.index("-ss") + 1], "0.950")

    def test_cover_variants_have_exact_dimensions_without_faking_missing_filters(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "out"
            output.mkdir()
            cover = output / "cover.png"
            cover.write_bytes(make_png(1080, 1920))
            calls: list[list[str]] = []

            def fake_run(args, **kwargs):
                del kwargs
                command = list(args)
                calls.append(command)
                if command[-1] == "-filters":
                    return type("Process", (), {"stdout": "scale crop pad", "stderr": ""})()
                Path(command[-1]).write_bytes(make_png(1080, 1440) if "portrait" in command[-1] else make_png(1440, 1080))
                return type("Process", (), {"stdout": "", "stderr": ""})()

            with patch.object(media_worker, "_write_cover_variants_with_pillow", return_value=None), patch.object(media_worker, "_run", side_effect=fake_run):
                variants = media_worker._write_cover_variants({"paths": {"ffmpeg": sys.executable}}, cover, output, root)
            self.assertEqual([path.name for path in variants], ["cover-portrait-3x4.png", "cover-landscape-4x3.png"])
            self.assertEqual(png_size(variants[0]), (1080, 1440))
            self.assertEqual(png_size(variants[1]), (1440, 1080))

            variants[0].unlink()
            variants[1].unlink()
            with patch.object(media_worker, "_write_cover_variants_with_pillow", return_value=None), patch.object(media_worker, "_run", return_value=type("Process", (), {"stdout": "scale only", "stderr": ""})()):
                with self.assertRaises(media_worker.CapabilityBlocker) as raised:
                    media_worker._write_cover_variants({"paths": {"ffmpeg": sys.executable}}, cover, output, root)
            self.assertEqual(raised.exception.capability, "ffmpeg-filters")
            self.assertFalse((output / "cover-portrait-3x4.png").exists())

    def test_pillow_cover_variants_are_real_exact_ratio_pngs(self) -> None:
        try:
            import PIL  # noqa: F401
        except ImportError:
            self.skipTest("Pillow is not installed")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "out"
            output.mkdir()
            cover = output / "cover.png"
            cover.write_bytes(make_png(1080, 1920))
            variants = media_worker._write_cover_variants({"paths": {}}, cover, output, root)
            self.assertEqual(png_size(variants[0]), (1080, 1440))
            self.assertEqual(png_size(variants[1]), (1440, 1080))

    def test_visual_description_never_becomes_source_but_media_source_is_kept(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            image = root / "visual.png"
            image.write_bytes(b"png")
            measured = [{"id": "one", "text": "产品真实体验", "sourceStartMs": 0}]
            described, _ = media_worker._scene_data({
                "scenes": [{"id": "described", "segmentIds": ["one"], "visual": "一张展示产品细节的特写画面"}],
            }, measured, 1000, 1, root)
            self.assertNotIn("source", described[0])
            self.assertEqual(described[0]["text"], "产品真实体验")
            sourced, _ = media_worker._scene_data({
                "scenes": [{"id": "sourced", "segmentIds": ["one"], "props": {"source": "visual.png"}}],
            }, measured, 1000, 1, root)
            self.assertEqual(sourced[0]["source"], str(image.resolve()))

    def test_cover_failure_does_not_write_a_complete_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "clip.wav", 500)
            output = root / "out"
            spec = media_worker.normalize_spec({
                "productionSpecVersion": "1.0",
                "jobId": "cover-failure",
                "segments": [{"id": "one", "text": "封面失败不能算完成", "audioPath": "clip.wav"}],
                "timeline": {"playbackRate": 1},
                "audio": {"sampleRate": 8000, "channels": 2},
                "render": {"enabled": True, "output": "render.mp4", "cwd": str(root)},
            })

            def fake_render(spec_value, output_dir, props_path, base):
                del spec_value, props_path, base
                video = output_dir / "render.mp4"
                video.write_bytes(b"video")
                return video

            with patch.object(media_worker, "_render", side_effect=fake_render), patch.object(media_worker, "_extract_cover", side_effect=media_worker.WorkerError("cover failed")):
                with self.assertRaises(media_worker.WorkerError):
                    media_worker.build_job(root / "job.json", output, spec)
            self.assertFalse((output / "artifact-manifest.json").exists())

    def test_invalid_format_and_render_output_paths_are_rejected(self) -> None:
        with self.assertRaises(media_worker.WorkerError):
            media_worker._timeline_config({"format": {"width": 0, "height": 1920}})
        with self.assertRaises(media_worker.WorkerError):
            media_worker._timeline_config({"format": {"width": 9000, "height": 1920}})
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            output = root / "out"
            output.mkdir()
            props = output / "props.json"
            props.write_text(json.dumps({"audio": {"narration": {"src": str(output / "audio.wav")}}}), encoding="utf-8")
            (output / "audio.wav").write_bytes(b"audio")
            with self.assertRaises(media_worker.WorkerError):
                media_worker._render({"render": {"enabled": True, "output": "../escape.mp4"}, "paths": {"remotion": sys.executable}}, output, props, root)

    def test_atempo_chain_is_pure_and_bounded(self) -> None:
        self.assertEqual(media_worker._atempo_chain(2), ["atempo=2"])
        self.assertEqual(media_worker._atempo_chain(1), ["atempo=1"])
        self.assertEqual(media_worker._atempo_chain(4), ["atempo=2.0", "atempo=2"])

    def test_validate_only_never_starts_external_process(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "clip.wav", 500)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "offline-validation",
                "segments": [{"id": "one", "audioPath": "clip.wav", "pauseAfterMs": 100}],
                "timeline": {"playbackRate": 1},
                "audio": {"sampleRate": 8000, "channels": 2},
                "outputs": {"audioFormat": "wav"},
            }), encoding="utf-8")
            with patch.object(media_worker.subprocess, "run", side_effect=AssertionError("subprocess called")) as mocked_run:
                result = media_worker.run_job(job, root / "out", validate_only=True)
            self.assertTrue(result["valid"])
            self.assertFalse((root / "out").exists())
            mocked_run.assert_not_called()

    @unittest.skipUnless(FFMPEG, "ffmpeg is not available")
    def test_fixture_measures_pauses_and_applies_2x_once(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "a.wav", 1000)
            make_wav(root / "b.wav", 500)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "offline-fixture",
                "segments": [
                    {"id": "a", "text": "first", "audioPath": "a.wav", "pauseAfterMs": 200},
                    {"id": "b", "text": "second", "audioPath": "b.wav"},
                ],
                "timeline": {"playbackRate": 2},
                "audio": {"sampleRate": 8000, "channels": 2},
                "scenes": [
                    {"id": "generic-a", "segmentIds": ["a"], "props": {"type": "arbitrary"}},
                    {"id": "generic-b", "segmentIds": ["b"], "props": {"label": "not editorial"}},
                ],
                "outputs": {"audioFormat": "wav"},
                "paths": {"ffmpeg": FFMPEG},
            }), encoding="utf-8")
            result = media_worker.run_job(job, root / "out")
            self.assertTrue(result["ok"])
            self.assertEqual(result["durationMs"], 850)
            with wave.open(str(root / "out" / "narration.wav"), "rb") as handle:
                actual_duration_ms = round(handle.getnframes() * 1000 / handle.getframerate())
            self.assertAlmostEqual(actual_duration_ms, 850, delta=100)
            manifest = json.loads((root / "out" / "artifact-manifest.json").read_text(encoding="utf-8"))
            self.assertTrue(manifest["timeline"]["rateAppliedOnce"])
            events = json.loads((root / "out" / "events.json").read_text(encoding="utf-8"))
            measured = [event for event in events["events"] if event["type"] == "segment.measured"]
            self.assertEqual(measured[0]["sourceEndMs"], 1000)
            self.assertEqual(measured[1]["sourceStartMs"], 1200)
            self.assertEqual(measured[1]["startMs"], 600)
            props = json.loads((root / "out" / "remotion-props.json").read_text(encoding="utf-8"))
            self.assertEqual([cut["id"] for cut in props["cuts"]], ["generic-a", "generic-b"])
            self.assertEqual(props["audio"]["narration"]["playbackRate"], 1)

    def test_already_2x_input_is_not_accelerated_again(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            make_wav(root / "already-2x.wav", 1000)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "already-2x",
                "segments": [{"id": "one", "audioPath": "already-2x.wav"}],
                "timeline": {"playbackRate": 2, "inputPlaybackRate": 2},
                "audio": {"sampleRate": 8000, "channels": 2},
                "outputs": {"audioFormat": "wav"},
            }), encoding="utf-8")
            with patch.object(media_worker.subprocess, "run", side_effect=AssertionError("subprocess called")) as mocked_run:
                result = media_worker.run_job(job, root / "out")
            self.assertTrue(result["ok"])
            mocked_run.assert_not_called()
            with wave.open(str(root / "out" / "narration.wav"), "rb") as handle:
                actual_duration_ms = round(handle.getnframes() * 1000 / handle.getframerate())
            self.assertAlmostEqual(actual_duration_ms, 1000, delta=20)

    def test_missing_tts_capabilities_are_explicit_blockers(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            job = root / "job.json"
            job.write_text(json.dumps({
                "productionSpecVersion": "1.0",
                "jobId": "blocked",
                "paths": {
                    "python": str(root / "missing-python.exe"),
                    "edgeTtsRoot": str(root / "missing-edge"),
                    "ffmpeg": str(root / "missing-ffmpeg.exe"),
                    "ffprobe": str(root / "missing-ffprobe.exe"),
                },
                "voice": {"voice": "en-US-AriaNeural", "default": {"rate": "+0%", "pitch": "+0Hz", "volume": "+0%"}},
                "segments": [{"id": "tts", "text": "offline should not call"}],
                "outputs": {"audioFormat": "mp3"},
            }), encoding="utf-8")
            with patch.object(media_worker.subprocess, "run", side_effect=AssertionError("subprocess called")):
                result = media_worker.run_job(job, root / "out", validate_only=True)
            self.assertFalse(result["valid"])
            capabilities = {item["capability"] for item in result["blockers"]}
            self.assertIn("python", capabilities)
            self.assertIn("edge-tts", capabilities)
            self.assertIn("ffmpeg", capabilities)
            self.assertIn("ffprobe", capabilities)


if __name__ == "__main__":
    unittest.main()
