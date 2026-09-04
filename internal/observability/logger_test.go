package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewJSONLogger(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewJSONLogger("debug", &output)
	if err != nil {
		t.Fatalf("NewJSONLogger returned error: %v", err)
	}
	logger.Info("service started", "component", "test")
	if !strings.Contains(output.String(), `"msg":"service started"`) || !strings.Contains(output.String(), `"component":"test"`) {
		t.Fatalf("logger output is not JSON structured output: %q", output.String())
	}
}

func TestNewJSONLoggerRejectsInvalidLevel(t *testing.T) {
	if _, err := NewJSONLogger("trace", nil); err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("NewJSONLogger error = %v, want LOG_LEVEL validation", err)
	}
}
