package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadFromDefaults(t *testing.T) {
	cfg, err := LoadFrom(nil)
	if err != nil {
		t.Fatalf("LoadFrom(nil) returned error: %v", err)
	}

	if cfg.Addr != "127.0.0.1:8088" || cfg.DataDir != "data" || cfg.LogLevel != "info" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.ReadHeaderTimeout != 5*time.Second || cfg.ReadTimeout != 15*time.Second ||
		cfg.WriteTimeout != 20*time.Minute || cfg.IdleTimeout != 60*time.Second ||
		cfg.ShutdownTimeout != 30*time.Second {
		t.Fatalf("unexpected timeout defaults: %#v", cfg)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{
		"ADDR":                     "0.0.0.0:9090",
		"VIDEO_FACTORY_DATA_DIR":   "var/data",
		"LOG_LEVEL":                "DEBUG",
		"HTTP_READ_HEADER_TIMEOUT": "2s",
		"HTTP_READ_TIMEOUT":        "3s",
		"HTTP_WRITE_TIMEOUT":       "4s",
		"HTTP_IDLE_TIMEOUT":        "5s",
		"SHUTDOWN_TIMEOUT":         "6s",
	})
	if err != nil {
		t.Fatalf("LoadFrom(overrides) returned error: %v", err)
	}

	if cfg.Addr != "0.0.0.0:9090" || cfg.DataDir != "var/data" || cfg.LogLevel != "debug" {
		t.Fatalf("unexpected overrides: %#v", cfg)
	}
	if cfg.ReadHeaderTimeout != 2*time.Second || cfg.ReadTimeout != 3*time.Second ||
		cfg.WriteTimeout != 4*time.Second || cfg.IdleTimeout != 5*time.Second ||
		cfg.ShutdownTimeout != 6*time.Second {
		t.Fatalf("unexpected timeout overrides: %#v", cfg)
	}
}

func TestLoadFromRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "log level", env: map[string]string{"LOG_LEVEL": "trace"}, want: "LOG_LEVEL"},
		{name: "read timeout", env: map[string]string{"HTTP_READ_TIMEOUT": "0s"}, want: "HTTP_READ_TIMEOUT"},
		{name: "duration syntax", env: map[string]string{"SHUTDOWN_TIMEOUT": "soon"}, want: "SHUTDOWN_TIMEOUT"},
		{name: "address", env: map[string]string{"ADDR": "   "}, want: "ADDR"},
		{name: "data dir", env: map[string]string{"VIDEO_FACTORY_DATA_DIR": ""}, want: "VIDEO_FACTORY_DATA_DIR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadFrom(tt.env)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadFrom(%v) error = %v, want variable %s", tt.env, err, tt.want)
			}
		})
	}
}
