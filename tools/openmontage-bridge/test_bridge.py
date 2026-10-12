import importlib.util
import unittest
import wave
from pathlib import Path


BRIDGE_PATH = Path(__file__).with_name("bridge.py")
SPEC = importlib.util.spec_from_file_location("openmontage_bridge", BRIDGE_PATH)
BRIDGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BRIDGE)


class WaveFormatSignatureTests(unittest.TestCase):
    def test_different_frame_counts_do_not_make_pcm_segments_incompatible(self):
        short_segment = wave._wave_params(1, 2, 22050, 22050, "NONE", "not compressed")
        long_segment = wave._wave_params(1, 2, 22050, 132300, "NONE", "not compressed")

        self.assertNotEqual(short_segment.nframes, long_segment.nframes)
        self.assertEqual(
            BRIDGE.wav_format_signature(short_segment),
            BRIDGE.wav_format_signature(long_segment),
        )

    def test_different_sample_rates_remain_incompatible(self):
        first = wave._wave_params(1, 2, 22050, 22050, "NONE", "not compressed")
        second = wave._wave_params(1, 2, 24000, 24000, "NONE", "not compressed")

        self.assertNotEqual(
            BRIDGE.wav_format_signature(first),
            BRIDGE.wav_format_signature(second),
        )


if __name__ == "__main__":
    unittest.main()
