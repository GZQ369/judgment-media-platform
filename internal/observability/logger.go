package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewJSONLogger creates a structured JSON logger for the supplied level.
func NewJSONLogger(level string, output io.Writer) (*slog.Logger, error) {
	parsed, err := ParseLevel(level)
	if err != nil {
		return nil, err
	}
	if output == nil {
		output = os.Stdout
	}
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{Level: parsed})), nil
}

// ParseLevel validates and converts a supported log level.
func ParseLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error")
	}
}
