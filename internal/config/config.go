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
	Port                       int
	DBPath                     string
	DefaultUserID              string
	Admin                      bool
	AuthProvider               string
	FirebaseProjectID          string
	FirebaseAPIKey             string
	FirebaseAuthDomain         string
	LLMDailyRequestLimit       int
	LLMMonthlyTokenLimit       int
	WorkerConcurrency          int
	WorkerQueueSize            int
	MaxConcurrentJobsPerTenant int
	GeminiAPIKey               string
	OpenAIAPIKey               string
	AnthropicAPIKey            string
	OpenAICompatBaseURL        string
	OpenAICompatAPIKey         string
	Environment                string
	LogLevel                   slog.Level
	LLMDebugPrompts            bool
	LLMDebugDir                string
	RESTTimeout                time.Duration
	LLMTimeout                 time.Duration
	PDFParseTimeout            time.Duration
	RenderPreviewTimeout       time.Duration
	HTTPReadHeaderTimeout      time.Duration
	HTTPReadTimeout            time.Duration
	HTTPMinWriteTimeout        time.Duration
	HTTPWriteTimeoutBuffer     time.Duration
	HTTPIdleTimeout            time.Duration
	HTTPShutdownTimeout        time.Duration
	MCPSSEKeepAliveInterval    time.Duration
}

// Default configuration constants applied when environment variables or flags are unset.
const (
	// DefaultPort is the default TCP port listened on by the unified HTTP server.
	DefaultPort = 8080

	// DefaultDBPath is the default filesystem path for the SQLite database.
	DefaultDBPath = "./data/resumeee.db"

	// DefaultUserID is the fallback tenant identifier used in local single-user admin mode.
	DefaultUserID = "default"

	// DefaultAdmin controls whether local single-user admin mode is enabled by default (multi-tenant safe).
	DefaultAdmin = false

	// DefaultLLMDailyRequestLimit bounds daily rolling LLM requests per tenant.
	DefaultLLMDailyRequestLimit = 50

	// DefaultLLMMonthlyTokenLimit bounds monthly rolling LLM tokens consumed per tenant.
	DefaultLLMMonthlyTokenLimit = 1000000

	// DefaultWorkerConcurrency sets the default number of concurrent background tailoring workers.
	DefaultWorkerConcurrency = 2

	// DefaultWorkerQueueSize sets the bounded in-memory task queue capacity across workers.
	DefaultWorkerQueueSize = 100

	// DefaultMaxConcurrentJobsPerTenant caps simultaneous in-flight LLM jobs for a single tenant.
	DefaultMaxConcurrentJobsPerTenant = 2

	// DefaultEnvironment identifies the deployment environment name for log formatting.
	DefaultEnvironment = "development"

	// DefaultLLMDebugDir specifies the directory where LLM prompt debug dumps are written when enabled.
	DefaultLLMDebugDir = "data/llm"

	// DefaultRESTTimeout bounds synchronous HTTP/MCP handler execution durations.
	DefaultRESTTimeout = 20 * time.Second

	// DefaultLLMTimeout bounds asynchronous LLM tailoring durations.
	DefaultLLMTimeout = 10 * time.Minute

	// DefaultPDFParseTimeout bounds upstream LLM PDF resume extraction durations.
	DefaultPDFParseTimeout = 10 * time.Minute

	// DefaultRenderPreviewTimeout bounds synchronous resume/template preview rendering durations.
	DefaultRenderPreviewTimeout = 10 * time.Second

	// DefaultHTTPReadHeaderTimeout bounds HTTP request header reading durations.
	DefaultHTTPReadHeaderTimeout = 5 * time.Second

	// DefaultHTTPReadTimeout bounds full HTTP request reading durations.
	DefaultHTTPReadTimeout = 60 * time.Second

	// DefaultHTTPMinWriteTimeout enforces a minimum HTTP response write timeout floor.
	DefaultHTTPMinWriteTimeout = 30 * time.Second

	// DefaultHTTPWriteTimeoutBuffer adds headroom above RESTTimeout for HTTP response writes.
	DefaultHTTPWriteTimeoutBuffer = 10 * time.Second

	// DefaultHTTPIdleTimeout bounds keep-alive idle durations between HTTP requests.
	DefaultHTTPIdleTimeout = 120 * time.Second

	// DefaultHTTPShutdownTimeout bounds graceful HTTP server shutdown draining.
	DefaultHTTPShutdownTimeout = 10 * time.Second

	// DefaultMCPSSEKeepAliveInterval sets the heartbeat cadence on MCP SSE streams.
	DefaultMCPSSEKeepAliveInterval = 15 * time.Second

	// DefaultMCPMaxConnsPerUser caps concurrent active MCP SSE streams per tenant.
	DefaultMCPMaxConnsPerUser = 5

	// DefaultMCPMaxTotalConns caps total concurrent active MCP SSE streams across all tenants.
	DefaultMCPMaxTotalConns = 100
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

	cfg := defaultConfig()
	if err := loadServerConfig(cfg, lookup); err != nil {
		return nil, err
	}
	if err := loadAuthConfig(cfg, lookup); err != nil {
		return nil, err
	}
	if err := loadQuotaAndWorkerConfig(cfg, lookup); err != nil {
		return nil, err
	}
	if err := loadTimeoutConfig(cfg, lookup); err != nil {
		return nil, err
	}
	if err := loadLLMConfig(cfg, lookup); err != nil {
		return nil, err
	}

	return cfg, nil
}

// defaultConfig returns a Config initialized with standard baseline defaults.
func defaultConfig() *Config {
	return &Config{
		Port:                       DefaultPort,
		DBPath:                     DefaultDBPath,
		DefaultUserID:              DefaultUserID,
		Admin:                      DefaultAdmin,
		LLMDailyRequestLimit:       DefaultLLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       DefaultLLMMonthlyTokenLimit,
		WorkerConcurrency:          DefaultWorkerConcurrency,
		WorkerQueueSize:            DefaultWorkerQueueSize,
		MaxConcurrentJobsPerTenant: DefaultMaxConcurrentJobsPerTenant,
		Environment:                DefaultEnvironment,
		LogLevel:                   slog.LevelInfo,
		LLMDebugPrompts:            false,
		LLMDebugDir:                DefaultLLMDebugDir,
		RESTTimeout:                DefaultRESTTimeout,
		LLMTimeout:                 DefaultLLMTimeout,
		PDFParseTimeout:            DefaultPDFParseTimeout,
		RenderPreviewTimeout:       DefaultRenderPreviewTimeout,
		HTTPReadHeaderTimeout:      DefaultHTTPReadHeaderTimeout,
		HTTPReadTimeout:            DefaultHTTPReadTimeout,
		HTTPMinWriteTimeout:        DefaultHTTPMinWriteTimeout,
		HTTPWriteTimeoutBuffer:     DefaultHTTPWriteTimeoutBuffer,
		HTTPIdleTimeout:            DefaultHTTPIdleTimeout,
		HTTPShutdownTimeout:        DefaultHTTPShutdownTimeout,
		MCPSSEKeepAliveInterval:    DefaultMCPSSEKeepAliveInterval,
	}
}

// loadServerConfig populates listener port, persistence path, environment, and log level from lookup.
func loadServerConfig(cfg *Config, lookup func(string) string) error {
	if val := strings.TrimSpace(lookup("PORT")); val != "" {
		p, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("invalid PORT value %q: must be an integer", val)
		}
		if p < 1 || p > 65535 {
			return fmt.Errorf("invalid PORT value %d: must be between 1 and 65535", p)
		}
		cfg.Port = p
	}
	if val := strings.TrimSpace(lookup("DB_PATH")); val != "" {
		cfg.DBPath = val
	}
	if val := firstNonEmpty(lookup, "ENV", "APP_ENV"); val != "" {
		cfg.Environment = val
	}
	if val := strings.TrimSpace(lookup("LOG_LEVEL")); val != "" {
		cfg.LogLevel = parseLogLevel(val)
	}
	return nil
}

// loadAuthConfig populates tenant defaults, admin mode, and Firebase settings from lookup.
func loadAuthConfig(cfg *Config, lookup func(string) string) error {
	if val := strings.TrimSpace(lookup("DEFAULT_USER_ID")); val != "" {
		cfg.DefaultUserID = val
	}
	if val, ok, err := parseBoolEnv(lookup, "ADMIN_MODE"); err != nil {
		return err
	} else if ok {
		cfg.Admin = val
	}

	cfg.AuthProvider = strings.TrimSpace(lookup("AUTH_PROVIDER"))
	cfg.FirebaseProjectID = strings.TrimSpace(lookup("FIREBASE_PROJECT_ID"))
	cfg.FirebaseAPIKey = strings.TrimSpace(lookup("FIREBASE_API_KEY"))
	cfg.FirebaseAuthDomain = strings.TrimSpace(lookup("FIREBASE_AUTH_DOMAIN"))
	if cfg.FirebaseAuthDomain == "" && cfg.FirebaseProjectID != "" {
		cfg.FirebaseAuthDomain = cfg.FirebaseProjectID + ".firebaseapp.com"
	}
	return nil
}

// loadQuotaAndWorkerConfig populates LLM quota ceilings and background worker pool parameters.
func loadQuotaAndWorkerConfig(cfg *Config, lookup func(string) string) error {
	intTargets := []struct {
		key string
		dst *int
	}{
		{key: "LLM_DAILY_REQUEST_LIMIT", dst: &cfg.LLMDailyRequestLimit},
		{key: "LLM_MONTHLY_TOKEN_LIMIT", dst: &cfg.LLMMonthlyTokenLimit},
		{key: "WORKER_CONCURRENCY", dst: &cfg.WorkerConcurrency},
		{key: "WORKER_QUEUE_SIZE", dst: &cfg.WorkerQueueSize},
		{key: "MAX_CONCURRENT_JOBS_PER_TENANT", dst: &cfg.MaxConcurrentJobsPerTenant},
	}
	for _, target := range intTargets {
		if err := parsePositiveInt(lookup, target.key, target.dst); err != nil {
			return err
		}
	}
	return nil
}

// loadTimeoutConfig parses synchronous REST, asynchronous LLM, HTTP server, and MCP timeout durations.
func loadTimeoutConfig(cfg *Config, lookup func(string) string) error {
	specs := []struct {
		target *time.Duration
		keys   []string
	}{
		{target: &cfg.RESTTimeout, keys: []string{"REST_TIMEOUT", "HTTP_TIMEOUT", "RESUMEEE_CLIENT_TIMEOUT"}},
		{target: &cfg.LLMTimeout, keys: []string{"LLM_TIMEOUT", "LLM_CALL_TIMEOUT"}},
		{target: &cfg.PDFParseTimeout, keys: []string{"PDF_PARSE_TIMEOUT"}},
		{target: &cfg.RenderPreviewTimeout, keys: []string{"RENDER_PREVIEW_TIMEOUT"}},
		{target: &cfg.HTTPReadHeaderTimeout, keys: []string{"HTTP_READ_HEADER_TIMEOUT"}},
		{target: &cfg.HTTPReadTimeout, keys: []string{"HTTP_READ_TIMEOUT"}},
		{target: &cfg.HTTPMinWriteTimeout, keys: []string{"HTTP_MIN_WRITE_TIMEOUT"}},
		{target: &cfg.HTTPWriteTimeoutBuffer, keys: []string{"HTTP_WRITE_TIMEOUT_BUFFER"}},
		{target: &cfg.HTTPIdleTimeout, keys: []string{"HTTP_IDLE_TIMEOUT"}},
		{target: &cfg.HTTPShutdownTimeout, keys: []string{"HTTP_SHUTDOWN_TIMEOUT"}},
		{target: &cfg.MCPSSEKeepAliveInterval, keys: []string{"MCP_SSE_KEEPALIVE_INTERVAL"}},
	}
	for _, spec := range specs {
		if err := parsePositiveDuration(lookup, spec.target, spec.keys...); err != nil {
			return err
		}
	}
	return nil
}

// loadLLMConfig reads LLM provider API credentials and debug prompt capture settings.
func loadLLMConfig(cfg *Config, lookup func(string) string) error {
	cfg.GeminiAPIKey = strings.TrimSpace(lookup("GEMINI_API_KEY"))
	cfg.OpenAIAPIKey = strings.TrimSpace(lookup("OPENAI_API_KEY"))
	cfg.AnthropicAPIKey = strings.TrimSpace(lookup("ANTHROPIC_API_KEY"))
	cfg.OpenAICompatBaseURL = strings.TrimSpace(lookup("OPENAI_COMPAT_BASE_URL"))
	cfg.OpenAICompatAPIKey = strings.TrimSpace(lookup("OPENAI_COMPAT_API_KEY"))

	if val, ok, err := parseBoolEnv(lookup, "LLM_DEBUG_PROMPTS", "DEBUG_LLM_PROMPTS"); err != nil {
		return err
	} else if ok {
		cfg.LLMDebugPrompts = val
	}
	if dirVal := firstNonEmpty(lookup, "LLM_DEBUG_DIR", "DEBUG_LLM_DIR"); dirVal != "" {
		cfg.LLMDebugDir = dirVal
	}
	return nil
}

// firstNonEmpty queries keys in order and returns the first non-empty trimmed value.
func firstNonEmpty(lookup func(string) string, keys ...string) string {
	for _, key := range keys {
		if val := strings.TrimSpace(lookup(key)); val != "" {
			return val
		}
	}
	return ""
}

// parseBoolEnv resolves the first set key from keys as a boolean, attributing parse errors to keys[0].
func parseBoolEnv(lookup func(string) string, keys ...string) (bool, bool, error) {
	val := firstNonEmpty(lookup, keys...)
	if val == "" {
		return false, false, nil
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, false, fmt.Errorf("invalid %s value %q: must be a boolean", keys[0], val)
	}
	return parsed, true, nil
}

// parsePositiveInt reads key from lookup and stores a strictly positive integer into dst.
func parsePositiveInt(lookup func(string) string, key string, dst *int) error {
	val := strings.TrimSpace(lookup(key))
	if val == "" {
		return nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fmt.Errorf("invalid %s value %q: must be an integer", key, val)
	}
	if n <= 0 {
		return fmt.Errorf("invalid %s value %d: must be greater than 0", key, n)
	}
	*dst = n
	return nil
}

// parsePositiveDuration reads the first set key from keys and stores a positive duration into dst.
func parsePositiveDuration(lookup func(string) string, dst *time.Duration, keys ...string) error {
	val := firstNonEmpty(lookup, keys...)
	if val == "" {
		return nil
	}
	d, err := time.ParseDuration(val)
	if err != nil {
		return fmt.Errorf("invalid %s value %q: %w", keys[0], val, err)
	}
	if d <= 0 {
		return fmt.Errorf("invalid %s value %v: must be greater than 0", keys[0], d)
	}
	*dst = d
	return nil
}

// parseLogLevel converts a case-insensitive log level label to slog.Level, defaulting to Info.
func parseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(raw) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// ParseFlags parses command-line flags to override configuration fields.
func (c *Config) ParseFlags(args []string) error {
	if len(args) == 0 {
		return nil
	}

	fs := flag.NewFlagSet("resumeee", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	admin := fs.Bool("admin-mode", c.Admin, "Run in local single-user admin mode")
	fs.BoolVar(admin, "admin", c.Admin, "Alias for -admin-mode")
	authProvider := fs.String("auth-provider", c.AuthProvider, "Authentication provider to use (e.g. admin, firebase)")
	debugPrompts := fs.Bool("debug-llm-prompts", c.LLMDebugPrompts, "Store LLM prompts and responses in data/llm")
	fs.BoolVar(debugPrompts, "llm-debug-prompts", c.LLMDebugPrompts, "Alias for -debug-llm-prompts")
	debugDir := fs.String("llm-debug-dir", c.LLMDebugDir, "Directory to store LLM debug markdown files")
	fs.StringVar(debugDir, "debug-llm-dir", c.LLMDebugDir, "Alias for -llm-debug-dir")

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	c.Admin = *admin
	c.AuthProvider = *authProvider
	c.LLMDebugPrompts = *debugPrompts
	c.LLMDebugDir = *debugDir
	return nil
}

// NewLogger constructs a structured slog.Logger for the given configuration.
// If cfg is nil, baseline defaults are used; if w is nil, os.Stdout is used.
func NewLogger(cfg *Config, w io.Writer) *slog.Logger {
	if cfg == nil {
		cfg = defaultConfig()
	}
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
