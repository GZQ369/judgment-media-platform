package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultAddr                     = "127.0.0.1:8088"
	defaultDataDir                  = "data"
	defaultLogLevel                 = "info"
	defaultReadHeaderTimeout        = 5 * time.Second
	defaultReadTimeout              = 15 * time.Second
	defaultWriteTimeout             = 20 * time.Minute
	defaultIdleTimeout              = 60 * time.Second
	defaultShutdownTimeout          = 30 * time.Second
	defaultMediaWorkerTimeout       = 20 * time.Minute
	defaultAIGatewayTimeout         = 2 * time.Minute
	defaultEgoTimeout               = 2 * time.Minute
	defaultAnalyticsReviewInterval  = 10 * time.Minute
	defaultAnalyticsMetricsInterval = time.Hour
	defaultAnalyticsMetricsWindow   = 168 * time.Hour
	defaultAnalyticsPollTick        = 15 * time.Second
	minAnalyticsReviewInterval      = time.Minute
	maxAnalyticsReviewInterval      = 24 * time.Hour
	minAnalyticsMetricsInterval     = time.Minute
	maxAnalyticsMetricsInterval     = 168 * time.Hour
	minAnalyticsMetricsWindow       = time.Hour
	maxAnalyticsMetricsWindow       = 720 * time.Hour
	minAnalyticsPollTick            = time.Second
	maxAnalyticsPollTick            = time.Hour
)

// Config contains process-level settings for the HTTP service.
type Config struct {
	Addr                           string
	DataDir                        string
	LogLevel                       string
	ReadHeaderTimeout              time.Duration
	ReadTimeout                    time.Duration
	WriteTimeout                   time.Duration
	IdleTimeout                    time.Duration
	ShutdownTimeout                time.Duration
	AnalyticsDBPath                string
	AnalyticsPollEnabled           bool
	AnalyticsReviewInterval        time.Duration
	AnalyticsMetricsInterval       time.Duration
	AnalyticsMetricsWindow         time.Duration
	AnalyticsPollTick              time.Duration
	MediaWorkerPython              string
	MediaWorkerScript              string
	MediaWorkerTimeout             time.Duration
	MediaWorkerFFmpeg              string
	MediaWorkerFFprobe             string
	MediaWorkerRemotion            string
	MediaWorkerRemotionCLI         string
	MediaWorkerRemotionCWD         string
	MediaWorkerRemotionEntry       string
	MediaWorkerRemotionComposition string
	MediaWorkerEdgeTTSRoot         string
	AIGatewayBaseURL               string
	AIGatewayModel                 string
	AIGatewayAPIKey                string `json:"-"`
	AIGatewayAllowLocal            bool
	AIGatewayTimeout               time.Duration
	EgoNodePath                    string
	EgoPublisherScript             string
	EgoCommand                     string
	EgoStateRoot                   string
	EgoTimeout                     time.Duration
	EgoOriginalRightsConfirmed     bool
	PublicationAPIToken            string `json:"-"`
	AllowedOrigins                 []string
	DouyinAccessToken              string `json:"-"`
	DouyinOpenID                   string `json:"-"`
	DouyinMetricsScope             string `json:"-"`
	DouyinAPIBaseURL               string
	XHSShareAppKey                 string `json:"-"`
}

// Load reads process configuration from the environment.
func Load() (Config, error) {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return LoadFrom(values)
}

// LoadFrom parses configuration from an environment-like map. Missing values
// use defaults; explicitly supplied values are always validated.
func LoadFrom(values map[string]string) (Config, error) {
	addr, err := requiredString(values, "ADDR", defaultAddr)
	if err != nil {
		return Config{}, err
	}
	dataDir, err := requiredString(values, "VIDEO_FACTORY_DATA_DIR", defaultDataDir)
	if err != nil {
		return Config{}, err
	}
	logLevel, err := requiredLogLevel(values, "LOG_LEVEL", defaultLogLevel)
	if err != nil {
		return Config{}, err
	}
	readHeaderTimeout, err := duration(values, "HTTP_READ_HEADER_TIMEOUT", defaultReadHeaderTimeout)
	if err != nil {
		return Config{}, err
	}
	readTimeout, err := duration(values, "HTTP_READ_TIMEOUT", defaultReadTimeout)
	if err != nil {
		return Config{}, err
	}
	writeTimeout, err := duration(values, "HTTP_WRITE_TIMEOUT", defaultWriteTimeout)
	if err != nil {
		return Config{}, err
	}
	idleTimeout, err := duration(values, "HTTP_IDLE_TIMEOUT", defaultIdleTimeout)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := duration(values, "SHUTDOWN_TIMEOUT", defaultShutdownTimeout)
	if err != nil {
		return Config{}, err
	}
	analyticsDBPath, err := optionalString(values, "ANALYTICS_DB_PATH")
	if err != nil {
		return Config{}, err
	}
	analyticsPollEnabled, err := optionalBool(values, "ANALYTICS_POLL_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	analyticsReviewInterval, err := boundedDuration(values, "ANALYTICS_REVIEW_INTERVAL", defaultAnalyticsReviewInterval, minAnalyticsReviewInterval, maxAnalyticsReviewInterval)
	if err != nil {
		return Config{}, err
	}
	analyticsMetricsInterval, err := boundedDuration(values, "ANALYTICS_METRICS_INTERVAL", defaultAnalyticsMetricsInterval, minAnalyticsMetricsInterval, maxAnalyticsMetricsInterval)
	if err != nil {
		return Config{}, err
	}
	analyticsMetricsWindow, err := boundedDuration(values, "ANALYTICS_METRICS_WINDOW", defaultAnalyticsMetricsWindow, minAnalyticsMetricsWindow, maxAnalyticsMetricsWindow)
	if err != nil {
		return Config{}, err
	}
	analyticsPollTick, err := boundedDuration(values, "ANALYTICS_POLL_TICK", defaultAnalyticsPollTick, minAnalyticsPollTick, maxAnalyticsPollTick)
	if err != nil {
		return Config{}, err
	}
	mediaWorkerPython, err := optionalString(values, "MEDIA_WORKER_PYTHON")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerScript, err := optionalString(values, "MEDIA_WORKER_SCRIPT")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerTimeout, err := duration(values, "MEDIA_WORKER_TIMEOUT", defaultMediaWorkerTimeout)
	if err != nil {
		return Config{}, err
	}
	mediaWorkerFFmpeg, err := optionalString(values, "MEDIA_WORKER_FFMPEG")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerFFprobe, err := optionalString(values, "MEDIA_WORKER_FFPROBE")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerRemotion, err := optionalString(values, "MEDIA_WORKER_REMOTION")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerRemotionCLI, err := optionalString(values, "MEDIA_WORKER_REMOTION_CLI")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerRemotionCWD, err := optionalString(values, "MEDIA_WORKER_REMOTION_CWD")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerRemotionEntry, err := optionalString(values, "MEDIA_WORKER_REMOTION_ENTRY")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerRemotionComposition, err := optionalString(values, "MEDIA_WORKER_REMOTION_COMPOSITION")
	if err != nil {
		return Config{}, err
	}
	mediaWorkerEdgeTTSRoot, err := optionalString(values, "MEDIA_WORKER_EDGE_TTS_ROOT")
	if err != nil {
		return Config{}, err
	}
	aiGatewayBaseURL, err := optionalString(values, "AI_GATEWAY_BASE_URL")
	if err != nil {
		return Config{}, err
	}
	aiGatewayModel, err := optionalString(values, "AI_GATEWAY_MODEL")
	if err != nil {
		return Config{}, err
	}
	if (aiGatewayBaseURL == "") != (aiGatewayModel == "") {
		return Config{}, fmt.Errorf("AI_GATEWAY_BASE_URL and AI_GATEWAY_MODEL must be configured together")
	}
	aiGatewayAPIKey, err := optionalString(values, "AI_GATEWAY_API_KEY")
	if err != nil {
		return Config{}, err
	}
	aiGatewayAllowLocal, err := optionalBool(values, "AI_GATEWAY_ALLOW_LOCAL", false)
	if err != nil {
		return Config{}, err
	}
	aiGatewayTimeout, err := duration(values, "AI_GATEWAY_TIMEOUT", defaultAIGatewayTimeout)
	if err != nil {
		return Config{}, err
	}
	egoNodePath, err := optionalString(values, "EGO_NODE_PATH")
	if err != nil {
		return Config{}, err
	}
	egoPublisherScript, err := optionalString(values, "EGO_PUBLISHER_SCRIPT")
	if err != nil {
		return Config{}, err
	}
	egoCommand, err := optionalString(values, "EGO_COMMAND")
	if err != nil {
		return Config{}, err
	}
	if egoCommand == "" {
		egoCommand, err = optionalString(values, "VIDEO_PUBLISHER_V2_EGO_COMMAND")
		if err != nil {
			return Config{}, err
		}
	}
	egoStateRoot, err := optionalString(values, "EGO_STATE_ROOT")
	if err != nil {
		return Config{}, err
	}
	egoTimeout, err := duration(values, "EGO_TIMEOUT", defaultEgoTimeout)
	if err != nil {
		return Config{}, err
	}
	egoRights, err := optionalBool(values, "EGO_ORIGINAL_RIGHTS_CONFIRMED", false)
	if err != nil {
		return Config{}, err
	}
	douyinAccessToken, err := optionalString(values, "DOUYIN_ACCESS_TOKEN")
	if err != nil {
		return Config{}, err
	}
	douyinOpenID, err := optionalString(values, "DOUYIN_OPEN_ID")
	if err != nil {
		return Config{}, err
	}
	douyinMetricsScope, err := optionalString(values, "DOUYIN_METRICS_SCOPE")
	if err != nil {
		return Config{}, err
	}
	douyinAPIBaseURL, err := optionalString(values, "DOUYIN_API_BASE_URL")
	if err != nil {
		return Config{}, err
	}
	xhsShareAppKey, err := optionalString(values, "XHS_SHARE_APP_KEY")
	if err != nil {
		return Config{}, err
	}
	publicationAPIToken, err := optionalString(values, "PUBLICATION_API_TOKEN")
	if err != nil {
		return Config{}, err
	}
	allowedOrigins, err := optionalOrigins(values, "CORS_ALLOWED_ORIGINS")
	if err != nil {
		return Config{}, err
	}

	return Config{
		Addr:                           addr,
		DataDir:                        dataDir,
		LogLevel:                       logLevel,
		ReadHeaderTimeout:              readHeaderTimeout,
		ReadTimeout:                    readTimeout,
		WriteTimeout:                   writeTimeout,
		IdleTimeout:                    idleTimeout,
		ShutdownTimeout:                shutdownTimeout,
		AnalyticsDBPath:                analyticsDBPath,
		AnalyticsPollEnabled:           analyticsPollEnabled,
		AnalyticsReviewInterval:        analyticsReviewInterval,
		AnalyticsMetricsInterval:       analyticsMetricsInterval,
		AnalyticsMetricsWindow:         analyticsMetricsWindow,
		AnalyticsPollTick:              analyticsPollTick,
		MediaWorkerPython:              mediaWorkerPython,
		MediaWorkerScript:              mediaWorkerScript,
		MediaWorkerTimeout:             mediaWorkerTimeout,
		MediaWorkerFFmpeg:              mediaWorkerFFmpeg,
		MediaWorkerFFprobe:             mediaWorkerFFprobe,
		MediaWorkerRemotion:            mediaWorkerRemotion,
		MediaWorkerRemotionCLI:         mediaWorkerRemotionCLI,
		MediaWorkerRemotionCWD:         mediaWorkerRemotionCWD,
		MediaWorkerRemotionEntry:       mediaWorkerRemotionEntry,
		MediaWorkerRemotionComposition: mediaWorkerRemotionComposition,
		MediaWorkerEdgeTTSRoot:         mediaWorkerEdgeTTSRoot,
		AIGatewayBaseURL:               aiGatewayBaseURL,
		AIGatewayModel:                 aiGatewayModel,
		AIGatewayAPIKey:                aiGatewayAPIKey,
		AIGatewayAllowLocal:            aiGatewayAllowLocal,
		AIGatewayTimeout:               aiGatewayTimeout,
		EgoNodePath:                    egoNodePath,
		EgoPublisherScript:             egoPublisherScript,
		EgoCommand:                     egoCommand,
		EgoStateRoot:                   egoStateRoot,
		EgoTimeout:                     egoTimeout,
		EgoOriginalRightsConfirmed:     egoRights,
		PublicationAPIToken:            publicationAPIToken,
		AllowedOrigins:                 allowedOrigins,
		DouyinAccessToken:              douyinAccessToken,
		DouyinOpenID:                   douyinOpenID,
		DouyinMetricsScope:             douyinMetricsScope,
		DouyinAPIBaseURL:               douyinAPIBaseURL,
		XHSShareAppKey:                 xhsShareAppKey,
	}, nil
}

func optionalOrigins(values map[string]string, name string) ([]string, error) {
	raw, err := optionalString(values, name)
	if err != nil || raw == "" {
		return nil, err
	}
	seen := make(map[string]struct{})
	origins := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		origin := strings.TrimSpace(value)
		if origin == "" {
			continue
		}
		if origin == "*" {
			return nil, fmt.Errorf("%s must not contain wildcard origin", name)
		}
		if _, ok := seen[origin]; ok {
			continue
		}
		seen[origin] = struct{}{}
		origins = append(origins, origin)
	}
	return origins, nil
}

func optionalString(values map[string]string, name string) (string, error) {
	value, ok := values[name]
	if !ok {
		return "", nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	return value, nil
}

func requiredString(values map[string]string, name, fallback string) (string, error) {
	value, ok := values[name]
	if !ok {
		return fallback, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return value, nil
}

func requiredLogLevel(values map[string]string, name, fallback string) (string, error) {
	value, ok := values[name]
	if !ok {
		return fallback, nil
	}
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "debug", "info", "warn", "error":
		return value, nil
	default:
		return "", fmt.Errorf("%s must be one of debug, info, warn, error", name)
	}
}

func duration(values map[string]string, name string, fallback time.Duration) (time.Duration, error) {
	value, ok := values[name]
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("%s must be a positive duration: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", name)
	}
	return parsed, nil
}

func boundedDuration(values map[string]string, name string, fallback, min, max time.Duration) (time.Duration, error) {
	parsed, err := duration(values, name, fallback)
	if err != nil {
		return 0, err
	}
	if parsed < min || parsed > max {
		return 0, fmt.Errorf("%s must be between %s and %s", name, min, max)
	}
	return parsed, nil
}

func optionalBool(values map[string]string, name string, fallback bool) (bool, error) {
	value, ok := values[name]
	if !ok || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", name)
	}
}
