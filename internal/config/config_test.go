package config

import (
	"encoding/json"
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
	if cfg.AnalyticsPollEnabled || cfg.AnalyticsReviewInterval != 10*time.Minute ||
		cfg.AnalyticsMetricsInterval != time.Hour || cfg.AnalyticsMetricsWindow != 168*time.Hour ||
		cfg.AnalyticsPollTick != 15*time.Second {
		t.Fatalf("unexpected analytics defaults: %#v", cfg)
	}
	if cfg.MediaWorkerPython != "" || cfg.MediaWorkerScript != "" || cfg.MediaWorkerTimeout != 20*time.Minute {
		t.Fatalf("unexpected media worker defaults: %#v", cfg)
	}
	if cfg.MediaWorkerRemotionCLI != "" || cfg.MediaWorkerRemotionEntry != "" || cfg.MediaWorkerRemotionComposition != "" {
		t.Fatalf("render entry/composition must be unset by default: %#v", cfg)
	}
	if cfg.AIGatewayBaseURL != "" || cfg.AIGatewayModel != "" || cfg.AIGatewayAPIKey != "" || cfg.AIGatewayAllowLocal || cfg.AIGatewayTimeout != 2*time.Minute {
		t.Fatalf("unexpected AI gateway defaults: %#v", cfg)
	}
	if cfg.EgoTimeout != 2*time.Minute || cfg.EgoNodePath != "" || cfg.EgoCommand != "" || cfg.EgoOriginalRightsConfirmed {
		t.Fatalf("unexpected Ego defaults: %#v", cfg)
	}
}

func TestLoadFromOverrides(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{
		"ADDR":                              "0.0.0.0:9090",
		"VIDEO_FACTORY_DATA_DIR":            "var/data",
		"LOG_LEVEL":                         "DEBUG",
		"HTTP_READ_HEADER_TIMEOUT":          "2s",
		"HTTP_READ_TIMEOUT":                 "3s",
		"HTTP_WRITE_TIMEOUT":                "4s",
		"HTTP_IDLE_TIMEOUT":                 "5s",
		"SHUTDOWN_TIMEOUT":                  "6s",
		"ANALYTICS_DB_PATH":                 "var/analytics.sqlite",
		"ANALYTICS_POLL_ENABLED":            "true",
		"ANALYTICS_REVIEW_INTERVAL":         "5m",
		"ANALYTICS_METRICS_INTERVAL":        "2h",
		"ANALYTICS_METRICS_WINDOW":          "336h",
		"ANALYTICS_POLL_TICK":               "30s",
		"MEDIA_WORKER_PYTHON":               "python3",
		"MEDIA_WORKER_SCRIPT":               "tools/media-worker/media_worker.py",
		"MEDIA_WORKER_TIMEOUT":              "7m",
		"MEDIA_WORKER_FFMPEG":               "ffmpeg",
		"MEDIA_WORKER_REMOTION":             "node.exe",
		"MEDIA_WORKER_REMOTION_CLI":         "C:/project/node_modules/@remotion/cli/remotion-cli.js",
		"MEDIA_WORKER_REMOTION_CWD":         "web/remotion",
		"MEDIA_WORKER_REMOTION_ENTRY":       "custom/index.tsx",
		"MEDIA_WORKER_REMOTION_COMPOSITION": "CustomExplainer",
		"AI_GATEWAY_BASE_URL":               "https://gateway.example.test/v1",
		"AI_GATEWAY_MODEL":                  "test-model",
		"AI_GATEWAY_API_KEY":                "super-secret",
		"AI_GATEWAY_ALLOW_LOCAL":            "true",
		"AI_GATEWAY_TIMEOUT":                "90s",
		"EGO_NODE_PATH":                     "C:/node/node.exe",
		"EGO_PUBLISHER_SCRIPT":              "C:/publisher/run-platform.mjs",
		"VIDEO_PUBLISHER_V2_EGO_COMMAND":    "C:/bridge/ego-browser.exe",
		"EGO_STATE_ROOT":                    "C:/state/ego",
		"EGO_TIMEOUT":                       "8m",
		"EGO_ORIGINAL_RIGHTS_CONFIRMED":     "true",
		"DOUYIN_ACCESS_TOKEN":               "token",
		"DOUYIN_OPEN_ID":                    "creator",
		"DOUYIN_METRICS_SCOPE":              "video.data",
		"DOUYIN_API_BASE_URL":               "https://open.example.test",
		"XHS_SHARE_APP_KEY":                 "xhs-key",
	})
	if err != nil {
		t.Fatalf("LoadFrom(overrides) returned error: %v", err)
	}

	if cfg.Addr != "0.0.0.0:9090" || cfg.DataDir != "var/data" || cfg.LogLevel != "debug" || cfg.AnalyticsDBPath != "var/analytics.sqlite" {
		t.Fatalf("unexpected overrides: %#v", cfg)
	}
	if cfg.ReadHeaderTimeout != 2*time.Second || cfg.ReadTimeout != 3*time.Second ||
		cfg.WriteTimeout != 4*time.Second || cfg.IdleTimeout != 5*time.Second ||
		cfg.ShutdownTimeout != 6*time.Second {
		t.Fatalf("unexpected timeout overrides: %#v", cfg)
	}
	if !cfg.AnalyticsPollEnabled || cfg.AnalyticsReviewInterval != 5*time.Minute ||
		cfg.AnalyticsMetricsInterval != 2*time.Hour || cfg.AnalyticsMetricsWindow != 336*time.Hour ||
		cfg.AnalyticsPollTick != 30*time.Second {
		t.Fatalf("unexpected analytics overrides: %#v", cfg)
	}
	if cfg.MediaWorkerPython != "python3" || cfg.MediaWorkerScript == "" || cfg.MediaWorkerTimeout != 7*time.Minute || cfg.MediaWorkerFFmpeg != "ffmpeg" || cfg.MediaWorkerRemotion != "node.exe" || cfg.MediaWorkerRemotionCLI != "C:/project/node_modules/@remotion/cli/remotion-cli.js" || cfg.MediaWorkerRemotionCWD != "web/remotion" || cfg.MediaWorkerRemotionEntry != "custom/index.tsx" || cfg.MediaWorkerRemotionComposition != "CustomExplainer" {
		t.Fatalf("unexpected media worker overrides: %#v", cfg)
	}
	if cfg.AIGatewayBaseURL != "https://gateway.example.test/v1" || cfg.AIGatewayModel != "test-model" || cfg.AIGatewayAPIKey != "super-secret" || !cfg.AIGatewayAllowLocal || cfg.AIGatewayTimeout != 90*time.Second {
		t.Fatalf("unexpected AI gateway overrides: %#v", cfg)
	}
	if cfg.EgoNodePath != "C:/node/node.exe" || cfg.EgoPublisherScript != "C:/publisher/run-platform.mjs" || cfg.EgoCommand != "C:/bridge/ego-browser.exe" || cfg.EgoStateRoot != "C:/state/ego" || cfg.EgoTimeout != 8*time.Minute || !cfg.EgoOriginalRightsConfirmed || cfg.DouyinAccessToken != "token" || cfg.DouyinOpenID != "creator" || cfg.DouyinMetricsScope != "video.data" || cfg.DouyinAPIBaseURL != "https://open.example.test" || cfg.XHSShareAppKey != "xhs-key" {
		t.Fatalf("unexpected publication overrides: %#v", cfg)
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
		{name: "gateway pair base only", env: map[string]string{"AI_GATEWAY_BASE_URL": "https://gateway.example.test/v1"}, want: "AI_GATEWAY_BASE_URL and AI_GATEWAY_MODEL"},
		{name: "gateway pair model only", env: map[string]string{"AI_GATEWAY_MODEL": "test-model"}, want: "AI_GATEWAY_BASE_URL and AI_GATEWAY_MODEL"},
		{name: "gateway timeout", env: map[string]string{"AI_GATEWAY_TIMEOUT": "0s"}, want: "AI_GATEWAY_TIMEOUT"},
		{name: "gateway local flag", env: map[string]string{"AI_GATEWAY_ALLOW_LOCAL": "sometimes"}, want: "AI_GATEWAY_ALLOW_LOCAL"},
		{name: "analytics enabled flag", env: map[string]string{"ANALYTICS_POLL_ENABLED": "sometimes"}, want: "ANALYTICS_POLL_ENABLED"},
		{name: "analytics review too short", env: map[string]string{"ANALYTICS_REVIEW_INTERVAL": "30s"}, want: "ANALYTICS_REVIEW_INTERVAL"},
		{name: "analytics review too long", env: map[string]string{"ANALYTICS_REVIEW_INTERVAL": "25h"}, want: "ANALYTICS_REVIEW_INTERVAL"},
		{name: "analytics metrics interval too short", env: map[string]string{"ANALYTICS_METRICS_INTERVAL": "30s"}, want: "ANALYTICS_METRICS_INTERVAL"},
		{name: "analytics metrics window too short", env: map[string]string{"ANALYTICS_METRICS_WINDOW": "30m"}, want: "ANALYTICS_METRICS_WINDOW"},
		{name: "analytics metrics window too long", env: map[string]string{"ANALYTICS_METRICS_WINDOW": "721h"}, want: "ANALYTICS_METRICS_WINDOW"},
		{name: "analytics poll tick too short", env: map[string]string{"ANALYTICS_POLL_TICK": "500ms"}, want: "ANALYTICS_POLL_TICK"},
		{name: "analytics poll tick too long", env: map[string]string{"ANALYTICS_POLL_TICK": "2h"}, want: "ANALYTICS_POLL_TICK"},
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

func TestLoadFromDoesNotExposeGatewayAPIKeyInJSON(t *testing.T) {
	cfg, err := LoadFrom(map[string]string{
		"AI_GATEWAY_BASE_URL": "https://gateway.example.test/v1",
		"AI_GATEWAY_MODEL":    "test-model",
		"AI_GATEWAY_API_KEY":  "super-secret",
	})
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	if strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("gateway API key entered JSON: %s", encoded)
	}
}
