// Package config parses process environment variables and initializes structured logging for Resumeee.
package config

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Config encapsulates all runtime configuration for Resumeee.
type Config struct {
	Port                int
	DBPath              string
	DefaultUserID       string
	WorkerConcurrency   int
	WorkerQueueSize     int
	GeminiAPIKey        string
	OpenAIAPIKey        string
	AnthropicAPIKey     string
	OpenAICompatBaseURL string
	OpenAICompatAPIKey  string
	Environment         string
	LogLevel            slog.Level
}

// Default configuration constants.
const (
	DefaultPort              = 8080
	DefaultDBPath            = "./data/resumeee.db"
	DefaultUserIDVal         = "default"
	DefaultWorkerConcurrency = 2
	DefaultWorkerQueueSize   = 100
	DefaultEnvironment       = "development"
)

// Load reads configuration from process environment variables using os.Getenv.
func Load() (*Config, error) {
	return LoadFrom(os.Getenv)
}

// LoadFrom parses configuration values using an injectable lookup function.
func LoadFrom(lookup func(string) string) (*Config, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	cfg := &Config{
		Port:              DefaultPort,
		DBPath:            DefaultDBPath,
		DefaultUserID:     DefaultUserIDVal,
		WorkerConcurrency: DefaultWorkerConcurrency,
		WorkerQueueSize:   DefaultWorkerQueueSize,
		Environment:       DefaultEnvironment,
		LogLevel:          slog.LevelInfo,
	}

	// Port
	if val := strings.TrimSpace(lookup("PORT")); val != "" {
		p, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT value %q: must be an integer", val)
		}
		if p < 1 || p > 65535 {
			return nil, fmt.Errorf("invalid PORT value %d: must be between 1 and 65535", p)
		}
		cfg.Port = p
	}

	// DBPath
	if val := strings.TrimSpace(lookup("DB_PATH")); val != "" {
		cfg.DBPath = val
	}

	// DefaultUserID
	if val := strings.TrimSpace(lookup("DEFAULT_USER_ID")); val != "" {
		cfg.DefaultUserID = val
	}

	// WorkerConcurrency
	if val := strings.TrimSpace(lookup("WORKER_CONCURRENCY")); val != "" {
		c, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid WORKER_CONCURRENCY value %q: must be an integer", val)
		}
		if c <= 0 {
			return nil, fmt.Errorf("invalid WORKER_CONCURRENCY value %d: must be greater than 0", c)
		}
		cfg.WorkerConcurrency = c
	}

	// WorkerQueueSize
	if val := strings.TrimSpace(lookup("WORKER_QUEUE_SIZE")); val != "" {
		q, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid WORKER_QUEUE_SIZE value %q: must be an integer", val)
		}
		if q <= 0 {
			return nil, fmt.Errorf("invalid WORKER_QUEUE_SIZE value %d: must be greater than 0", q)
		}
		cfg.WorkerQueueSize = q
	}

	// API Keys and URLs
	cfg.GeminiAPIKey = strings.TrimSpace(lookup("GEMINI_API_KEY"))
	cfg.OpenAIAPIKey = strings.TrimSpace(lookup("OPENAI_API_KEY"))
	cfg.AnthropicAPIKey = strings.TrimSpace(lookup("ANTHROPIC_API_KEY"))
	cfg.OpenAICompatBaseURL = strings.TrimSpace(lookup("OPENAI_COMPAT_BASE_URL"))
	cfg.OpenAICompatAPIKey = strings.TrimSpace(lookup("OPENAI_COMPAT_API_KEY"))

	// Environment: check ENV first, then APP_ENV
	if envVal := strings.TrimSpace(lookup("ENV")); envVal != "" {
		cfg.Environment = envVal
	} else if appEnvVal := strings.TrimSpace(lookup("APP_ENV")); appEnvVal != "" {
		cfg.Environment = appEnvVal
	}

	// Log Level
	if levelVal := strings.TrimSpace(lookup("LOG_LEVEL")); levelVal != "" {
		switch strings.ToUpper(levelVal) {
		case "DEBUG":
			cfg.LogLevel = slog.LevelDebug
		case "INFO":
			cfg.LogLevel = slog.LevelInfo
		case "WARN", "WARNING":
			cfg.LogLevel = slog.LevelWarn
		case "ERROR":
			cfg.LogLevel = slog.LevelError
		default:
			cfg.LogLevel = slog.LevelInfo
		}
	}

	return cfg, nil
}

// NewLogger constructs a structured slog.Logger for the given configuration.
// If w is nil, os.Stdout is used as default.
func NewLogger(cfg *Config, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}

	opts := &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}

	var handler slog.Handler
	if strings.ToLower(cfg.Environment) == "production" {
		handler = slog.NewJSONHandler(w, opts)
	} else {
		handler = slog.NewTextHandler(w, opts)
	}

	return slog.New(handler)
}
