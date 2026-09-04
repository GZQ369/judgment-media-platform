package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultAddr              = "127.0.0.1:8088"
	defaultDataDir           = "data"
	defaultLogLevel          = "info"
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 20 * time.Minute
	defaultIdleTimeout       = 60 * time.Second
	defaultShutdownTimeout   = 30 * time.Second
)

// Config contains process-level settings for the HTTP service.
type Config struct {
	Addr              string
	DataDir           string
	LogLevel          string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
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

	return Config{
		Addr:              addr,
		DataDir:           dataDir,
		LogLevel:          logLevel,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ShutdownTimeout:   shutdownTimeout,
	}, nil
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
