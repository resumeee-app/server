package config_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"resumeee/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	emptyLookup := func(key string) string {
		return ""
	}

	cfg, err := config.LoadFrom(emptyLookup)
	if err != nil {
		t.Fatalf("expected no error loading defaults, got: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected default Port 8080, got %d", cfg.Port)
	}
	if cfg.DBPath != "./data/resumeee.db" {
		t.Errorf("expected default DBPath './data/resumeee.db', got %q", cfg.DBPath)
	}
	if cfg.DefaultUserID != "default" {
		t.Errorf("expected default DefaultUserID 'default', got %q", cfg.DefaultUserID)
	}
	if cfg.WorkerConcurrency != 2 {
		t.Errorf("expected default WorkerConcurrency 2, got %d", cfg.WorkerConcurrency)
	}
	if cfg.WorkerQueueSize != 100 {
		t.Errorf("expected default WorkerQueueSize 100, got %d", cfg.WorkerQueueSize)
	}
	if cfg.Environment != "development" {
		t.Errorf("expected default Environment 'development', got %q", cfg.Environment)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("expected default LogLevel LevelInfo, got %v", cfg.LogLevel)
	}
	if cfg.GeminiAPIKey != "" || cfg.OpenAIAPIKey != "" || cfg.AnthropicAPIKey != "" {
		t.Errorf("expected empty API keys by default")
	}
}

func TestLoadOverrides(t *testing.T) {
	envMap := map[string]string{
		"PORT":                   "9090",
		"DB_PATH":                "/var/lib/resumeee/custom.db",
		"DEFAULT_USER_ID":        "user-123",
		"WORKER_CONCURRENCY":     "8",
		"WORKER_QUEUE_SIZE":      "500",
		"GEMINI_API_KEY":         "gem-secret",
		"OPENAI_API_KEY":         "sk-openai-secret",
		"ANTHROPIC_API_KEY":      "ant-secret",
		"OPENAI_COMPAT_BASE_URL": "http://localhost:11434/v1",
		"OPENAI_COMPAT_API_KEY":  "ollama-secret",
		"ENV":                    "production",
		"LOG_LEVEL":              "DEBUG",
	}

	lookup := func(key string) string {
		return envMap[key]
	}

	cfg, err := config.LoadFrom(lookup)
	if err != nil {
		t.Fatalf("unexpected error loading overrides: %v", err)
	}

	if cfg.Port != 9090 {
		t.Errorf("expected Port 9090, got %d", cfg.Port)
	}
	if cfg.DBPath != "/var/lib/resumeee/custom.db" {
		t.Errorf("expected DBPath '/var/lib/resumeee/custom.db', got %q", cfg.DBPath)
	}
	if cfg.DefaultUserID != "user-123" {
		t.Errorf("expected DefaultUserID 'user-123', got %q", cfg.DefaultUserID)
	}
	if cfg.WorkerConcurrency != 8 {
		t.Errorf("expected WorkerConcurrency 8, got %d", cfg.WorkerConcurrency)
	}
	if cfg.WorkerQueueSize != 500 {
		t.Errorf("expected WorkerQueueSize 500, got %d", cfg.WorkerQueueSize)
	}
	if cfg.GeminiAPIKey != "gem-secret" {
		t.Errorf("expected GeminiAPIKey 'gem-secret', got %q", cfg.GeminiAPIKey)
	}
	if cfg.OpenAIAPIKey != "sk-openai-secret" {
		t.Errorf("expected OpenAIAPIKey 'sk-openai-secret', got %q", cfg.OpenAIAPIKey)
	}
	if cfg.AnthropicAPIKey != "ant-secret" {
		t.Errorf("expected AnthropicAPIKey 'ant-secret', got %q", cfg.AnthropicAPIKey)
	}
	if cfg.OpenAICompatBaseURL != "http://localhost:11434/v1" {
		t.Errorf("expected OpenAICompatBaseURL 'http://localhost:11434/v1', got %q", cfg.OpenAICompatBaseURL)
	}
	if cfg.OpenAICompatAPIKey != "ollama-secret" {
		t.Errorf("expected OpenAICompatAPIKey 'ollama-secret', got %q", cfg.OpenAICompatAPIKey)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected Environment 'production', got %q", cfg.Environment)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("expected LogLevel LevelDebug, got %v", cfg.LogLevel)
	}
}

func TestLoadValidationAndFallbacks(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		expectError bool
		check       func(t *testing.T, cfg *config.Config)
	}{
		{
			name: "invalid non-numeric PORT returns error",
			env: map[string]string{
				"PORT": "invalid-port",
			},
			expectError: true,
		},
		{
			name: "port out of range returns error",
			env: map[string]string{
				"PORT": "70000",
			},
			expectError: true,
		},
		{
			name: "invalid non-numeric WORKER_CONCURRENCY returns error",
			env: map[string]string{
				"WORKER_CONCURRENCY": "abc",
			},
			expectError: true,
		},
		{
			name: "non-positive WORKER_CONCURRENCY returns error",
			env: map[string]string{
				"WORKER_CONCURRENCY": "0",
			},
			expectError: true,
		},
		{
			name: "invalid non-numeric WORKER_QUEUE_SIZE returns error",
			env: map[string]string{
				"WORKER_QUEUE_SIZE": "xyz",
			},
			expectError: true,
		},
		{
			name: "non-positive WORKER_QUEUE_SIZE returns error",
			env: map[string]string{
				"WORKER_QUEUE_SIZE": "-10",
			},
			expectError: true,
		},
		{
			name: "APP_ENV fallback when ENV unset",
			env: map[string]string{
				"APP_ENV": "staging",
			},
			expectError: false,
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.Environment != "staging" {
					t.Errorf("expected Environment 'staging', got %q", cfg.Environment)
				}
			},
		},
		{
			name: "unknown LOG_LEVEL falls back to LevelInfo",
			env: map[string]string{
				"LOG_LEVEL": "UNKNOWN_LEVEL",
			},
			expectError: false,
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.LogLevel != slog.LevelInfo {
					t.Errorf("expected default LevelInfo on unknown log level, got %v", cfg.LogLevel)
				}
			},
		},
		{
			name: "case-insensitive LOG_LEVEL values",
			env: map[string]string{
				"LOG_LEVEL": "warn",
			},
			expectError: false,
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.LogLevel != slog.LevelWarn {
					t.Errorf("expected LevelWarn, got %v", cfg.LogLevel)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(key string) string {
				return tc.env[key]
			}
			cfg, err := config.LoadFrom(lookup)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.check != nil {
					tc.check(t, cfg)
				}
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	t.Run("production outputs JSON", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config.Config{
			Environment: "production",
			LogLevel:    slog.LevelInfo,
		}
		logger := config.NewLogger(cfg, &buf)
		logger.Info("server starting", "port", 8080)

		line := strings.TrimSpace(buf.String())
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("expected valid JSON log entry, got: %s (err: %v)", line, err)
		}
		if parsed["msg"] != "server starting" {
			t.Errorf("expected msg 'server starting', got %v", parsed["msg"])
		}
		if parsed["level"] != "INFO" {
			t.Errorf("expected level 'INFO', got %v", parsed["level"])
		}
		if parsed["port"] != float64(8080) {
			t.Errorf("expected port 8080, got %v", parsed["port"])
		}
	})

	t.Run("development outputs text", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config.Config{
			Environment: "development",
			LogLevel:    slog.LevelDebug,
		}
		logger := config.NewLogger(cfg, &buf)
		logger.Debug("debug message", "key", "val")

		line := buf.String()
		if !strings.Contains(line, "level=DEBUG") && !strings.Contains(line, "DEBUG") {
			t.Errorf("expected text log to contain level DEBUG, got: %s", line)
		}
		if !strings.Contains(line, "msg=\"debug message\"") && !strings.Contains(line, "debug message") {
			t.Errorf("expected text log to contain message, got: %s", line)
		}
	})

	t.Run("respects log level filtering", func(t *testing.T) {
		var buf bytes.Buffer
		cfg := &config.Config{
			Environment: "development",
			LogLevel:    slog.LevelWarn,
		}
		logger := config.NewLogger(cfg, &buf)
		logger.Info("info message should be ignored")
		if buf.Len() != 0 {
			t.Errorf("expected no output for info message when level is Warn, got: %s", buf.String())
		}
		logger.Warn("warning message should be recorded")
		if buf.Len() == 0 {
			t.Errorf("expected output for warning message")
		}
	})
}

func TestLoad(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("expected Load() to succeed with environment defaults, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config from Load()")
	}
}
