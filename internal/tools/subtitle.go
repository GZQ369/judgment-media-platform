package tools

import "strings"

// SubtitleTextFromScript 从脚本文案生成稳定的字幕文本。
func SubtitleTextFromScript(fullScript string) string {
	lines := SplitSubtitleLines(fullScript, 18)
	return strings.Join(lines, "\n")
}

// SplitSubtitleLines 把长文案切成更适合短视频字幕的短行。
func SplitSubtitleLines(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		maxRunes = 18
	}
	separators := strings.NewReplacer("。", "\n", "！", "\n", "？", "\n", "；", "\n")
	chunks := strings.Split(separators.Replace(text), "\n")
	out := []string{}
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		runes := []rune(chunk)
		for len(runes) > maxRunes {
			out = append(out, string(runes[:maxRunes]))
			runes = runes[maxRunes:]
		}
		if len(runes) > 0 {
			out = append(out, string(runes))
		}
	}
	return out
}
