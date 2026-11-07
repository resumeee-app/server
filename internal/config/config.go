// Package config parses process environment variables and initializes structured logging for Resumeee.
package config

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config encapsulates all runtime configuration for Resumeee.
type Config struct {
	Port                int
	DBPath              string
	DefaultUserID       string
	Admin               bool
	FirebaseProjectID   string
	LLMDailyRequestLimit int
	LLMMonthlyTokenLimit int
	WorkerConcurrency          int
	WorkerQueueSize            int
	MaxConcurrentJobsPerTenant int
	GeminiAPIKey        string
	OpenAIAPIKey        string
	AnthropicAPIKey     string
	OpenAICompatBaseURL string
	OpenAICompatAPIKey  string
	Environment         string
	LogLevel            slog.Level
	LLMDebugPrompts     bool
	LLMDebugDir         string
	RESTTimeout         time.Duration
	LLMTimeout          time.Duration
}

// Default configuration constants.
const (
	DefaultPort                 = 8080
	DefaultDBPath               = "./data/resumeee.db"
	DefaultUserIDVal            = "default"
	DefaultAdmin                = false
	DefaultLLMDailyRequestLimit = 50
	DefaultLLMMonthlyTokenLimit = 1000000
	DefaultWorkerConcurrency          = 2
	DefaultWorkerQueueSize            = 100
	DefaultMaxConcurrentJobsPerTenant = 2
	DefaultEnvironment       = "development"
	DefaultLLMDebugDir       = "data/llm"
	DefaultRESTTimeout       = 20 * time.Second
	DefaultLLMTimeout        = 10 * time.Minute
)

// Load reads configuration from process environment variables and a local .env file if present.
func Load() (*Config, error) {
	return LoadFromDir(".")
}

// LoadFrom parses configuration values using an injectable lookup function.
func LoadFrom(lookup func(string) string) (*Config, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	cfg := &Config{
		Port:                 DefaultPort,
		DBPath:               DefaultDBPath,
		DefaultUserID:        DefaultUserIDVal,
		Admin:                DefaultAdmin,
		LLMDailyRequestLimit: DefaultLLMDailyRequestLimit,
		LLMMonthlyTokenLimit: DefaultLLMMonthlyTokenLimit,
		WorkerConcurrency:          DefaultWorkerConcurrency,
		WorkerQueueSize:            DefaultWorkerQueueSize,
		MaxConcurrentJobsPerTenant: DefaultMaxConcurrentJobsPerTenant,
		Environment:          DefaultEnvironment,
		LogLevel:             slog.LevelInfo,
		LLMDebugPrompts:      false,
		LLMDebugDir:          DefaultLLMDebugDir,
		RESTTimeout:          DefaultRESTTimeout,
		LLMTimeout:           DefaultLLMTimeout,
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

	// Admin mode
	if val := strings.TrimSpace(lookup("ADMIN")); val != "" {
		a, err := strconv.ParseBool(val)
		if err != nil {
			return nil, fmt.Errorf("invalid ADMIN value %q: must be a boolean", val)
		}
		cfg.Admin = a
	}

	// FirebaseProjectID
	cfg.FirebaseProjectID = strings.TrimSpace(lookup("FIREBASE_PROJECT_ID"))

	// LLMDailyRequestLimit
	if val := strings.TrimSpace(lookup("LLM_DAILY_REQUEST_LIMIT")); val != "" {
		l, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid LLM_DAILY_REQUEST_LIMIT value %q: must be an integer", val)
		}
		if l <= 0 {
			return nil, fmt.Errorf("invalid LLM_DAILY_REQUEST_LIMIT value %d: must be greater than 0", l)
		}
		cfg.LLMDailyRequestLimit = l
	}

	// LLMMonthlyTokenLimit
	if val := strings.TrimSpace(lookup("LLM_MONTHLY_TOKEN_LIMIT")); val != "" {
		l, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid LLM_MONTHLY_TOKEN_LIMIT value %q: must be an integer", val)
		}
		if l <= 0 {
			return nil, fmt.Errorf("invalid LLM_MONTHLY_TOKEN_LIMIT value %d: must be greater than 0", l)
		}
		cfg.LLMMonthlyTokenLimit = l
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

	// MaxConcurrentJobsPerTenant
	if val := strings.TrimSpace(lookup("MAX_CONCURRENT_JOBS_PER_TENANT")); val != "" {
		m, err := strconv.Atoi(val)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_CONCURRENT_JOBS_PER_TENANT value %q: must be an integer", val)
		}
		if m <= 0 {
			return nil, fmt.Errorf("invalid MAX_CONCURRENT_JOBS_PER_TENANT value %d: must be greater than 0", m)
		}
		cfg.MaxConcurrentJobsPerTenant = m
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

	// LLM Debug Prompts: checks LLM_DEBUG_PROMPTS, falling back to DEBUG_LLM_PROMPTS
	debugPromptsVal := strings.TrimSpace(lookup("LLM_DEBUG_PROMPTS"))
	if debugPromptsVal == "" {
		debugPromptsVal = strings.TrimSpace(lookup("DEBUG_LLM_PROMPTS"))
	}
	if debugPromptsVal != "" {
		p, err := strconv.ParseBool(debugPromptsVal)
		if err != nil {
			return nil, fmt.Errorf("invalid LLM_DEBUG_PROMPTS value %q: must be a boolean", debugPromptsVal)
		}
		cfg.LLMDebugPrompts = p
	}

	// LLM Debug Directory: checks LLM_DEBUG_DIR, falling back to DEBUG_LLM_DIR
	debugDirVal := strings.TrimSpace(lookup("LLM_DEBUG_DIR"))
	if debugDirVal == "" {
		debugDirVal = strings.TrimSpace(lookup("DEBUG_LLM_DIR"))
	}
	if debugDirVal != "" {
		cfg.LLMDebugDir = debugDirVal
	}

	// REST Timeout: checks REST_TIMEOUT, falling back to HTTP_TIMEOUT and RESUMEEE_CLIENT_TIMEOUT
	restTimeoutVal := strings.TrimSpace(lookup("REST_TIMEOUT"))
	if restTimeoutVal == "" {
		restTimeoutVal = strings.TrimSpace(lookup("HTTP_TIMEOUT"))
	}
	if restTimeoutVal == "" {
		restTimeoutVal = strings.TrimSpace(lookup("RESUMEEE_CLIENT_TIMEOUT"))
	}
	if restTimeoutVal != "" {
		d, err := time.ParseDuration(restTimeoutVal)
		if err != nil {
			return nil, fmt.Errorf("invalid REST_TIMEOUT value %q: %w", restTimeoutVal, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("invalid REST_TIMEOUT value %v: must be greater than 0", d)
		}
		cfg.RESTTimeout = d
	}

	// LLM Timeout: checks LLM_TIMEOUT, falling back to LLM_CALL_TIMEOUT
	llmTimeoutVal := strings.TrimSpace(lookup("LLM_TIMEOUT"))
	if llmTimeoutVal == "" {
		llmTimeoutVal = strings.TrimSpace(lookup("LLM_CALL_TIMEOUT"))
	}
	if llmTimeoutVal != "" {
		d, err := time.ParseDuration(llmTimeoutVal)
		if err != nil {
			return nil, fmt.Errorf("invalid LLM_TIMEOUT value %q: %w", llmTimeoutVal, err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("invalid LLM_TIMEOUT value %v: must be greater than 0", d)
		}
		cfg.LLMTimeout = d
	}

	return cfg, nil
}

// ParseFlags parses command-line flags to override configuration fields.
func (c *Config) ParseFlags(args []string) error {
	if len(args) == 0 {
		return nil
	}

	fs := flag.NewFlagSet("resumeee", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	admin := fs.Bool("admin", c.Admin, "Run in local single-user admin mode")
	debugPrompts := fs.Bool("debug-llm-prompts", c.LLMDebugPrompts, "Store LLM prompts and responses in data/llm")
	fs.BoolVar(debugPrompts, "llm-debug-prompts", c.LLMDebugPrompts, "Alias for -debug-llm-prompts")
	debugDir := fs.String("llm-debug-dir", c.LLMDebugDir, "Directory to store LLM debug markdown files")
	fs.StringVar(debugDir, "debug-llm-dir", c.LLMDebugDir, "Alias for -llm-debug-dir")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	c.Admin = *admin
	c.LLMDebugPrompts = *debugPrompts
	c.LLMDebugDir = *debugDir
	return nil
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
