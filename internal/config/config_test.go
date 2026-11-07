package config_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if cfg.RESTTimeout != 20*time.Second {
		t.Errorf("expected default RESTTimeout 20s, got %v", cfg.RESTTimeout)
	}
	if cfg.LLMTimeout != 10*time.Minute {
		t.Errorf("expected default LLMTimeout 10m, got %v", cfg.LLMTimeout)
	}
	if cfg.Admin != false {
		t.Errorf("expected default Admin false, got %v", cfg.Admin)
	}
	if cfg.FirebaseProjectID != "" {
		t.Errorf("expected default FirebaseProjectID empty, got %q", cfg.FirebaseProjectID)
	}
	if cfg.LLMDailyRequestLimit != 50 {
		t.Errorf("expected default LLMDailyRequestLimit 50, got %d", cfg.LLMDailyRequestLimit)
	}
	if cfg.LLMMonthlyTokenLimit != 1000000 {
		t.Errorf("expected default LLMMonthlyTokenLimit 1000000, got %d", cfg.LLMMonthlyTokenLimit)
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

func TestLoadFrom_LLMDebugPrompts(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts to default to false, got true")
		}
		if cfg.LLMDebugDir != "data/llm" {
			t.Errorf("expected LLMDebugDir to default to %q, got %q", "data/llm", cfg.LLMDebugDir)
		}
	})

	t.Run("enabled via true", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_PROMPTS" {
				return "true"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts to be true")
		}
	})

	t.Run("enabled via 1", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_PROMPTS" {
				return "1"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts to be true")
		}
	})

	t.Run("explicitly disabled via false", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_PROMPTS" {
				return "false"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts to be false")
		}
	})

	t.Run("invalid boolean returns error", func(t *testing.T) {
		_, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_PROMPTS" {
				return "notabool"
			}
			return ""
		})
		if err == nil {
			t.Fatalf("expected error for invalid LLM_DEBUG_PROMPTS, got nil")
		}
	})

	t.Run("DEBUG_LLM_PROMPTS env alias enabled", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "DEBUG_LLM_PROMPTS" {
				return "true"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts true when DEBUG_LLM_PROMPTS is set")
		}
	})

	t.Run("LLM_DEBUG_PROMPTS takes precedence over DEBUG_LLM_PROMPTS", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_PROMPTS" {
				return "false"
			}
			if s == "DEBUG_LLM_PROMPTS" {
				return "true"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts false due to LLM_DEBUG_PROMPTS precedence")
		}
	})

	t.Run("invalid DEBUG_LLM_PROMPTS returns error", func(t *testing.T) {
		_, err := config.LoadFrom(func(s string) string {
			if s == "DEBUG_LLM_PROMPTS" {
				return "invalid-bool"
			}
			return ""
		})
		if err == nil {
			t.Fatalf("expected error for invalid DEBUG_LLM_PROMPTS, got nil")
		}
	})

	t.Run("custom debug directory", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_DIR" {
				return "/custom/debug/llm"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugDir != "/custom/debug/llm" {
			t.Errorf("expected LLMDebugDir %q, got %q", "/custom/debug/llm", cfg.LLMDebugDir)
		}
	})

	t.Run("DEBUG_LLM_DIR env alias", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "DEBUG_LLM_DIR" {
				return "/custom/alias/llm"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugDir != "/custom/alias/llm" {
			t.Errorf("expected LLMDebugDir %q, got %q", "/custom/alias/llm", cfg.LLMDebugDir)
		}
	})

	t.Run("LLM_DEBUG_DIR takes precedence over DEBUG_LLM_DIR", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string {
			if s == "LLM_DEBUG_DIR" {
				return "/primary/llm"
			}
			if s == "DEBUG_LLM_DIR" {
				return "/alias/llm"
			}
			return ""
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMDebugDir != "/primary/llm" {
			t.Errorf("expected LLMDebugDir '/primary/llm', got %q", cfg.LLMDebugDir)
		}
	})
}

func TestConfig_ParseFlags(t *testing.T) {
	t.Run("debug-llm-prompts flag", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags([]string{"-debug-llm-prompts"}); err != nil {
			t.Fatalf("ParseFlags failed: %v", err)
		}
		if !cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts true, got false")
		}
	})

	t.Run("llm-debug-prompts alias flag", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags([]string{"-llm-debug-prompts"}); err != nil {
			t.Fatalf("ParseFlags failed: %v", err)
		}
		if !cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts true, got false")
		}
	})

	t.Run("llm-debug-dir flag", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags([]string{"-llm-debug-dir", "/tmp/debug-llm"}); err != nil {
			t.Fatalf("ParseFlags failed: %v", err)
		}
		if cfg.LLMDebugDir != "/tmp/debug-llm" {
			t.Errorf("expected LLMDebugDir '/tmp/debug-llm', got %q", cfg.LLMDebugDir)
		}
	})

	t.Run("debug-llm-dir alias flag", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags([]string{"-debug-llm-dir", "/tmp/alias-llm"}); err != nil {
			t.Fatalf("ParseFlags failed: %v", err)
		}
		if cfg.LLMDebugDir != "/tmp/alias-llm" {
			t.Errorf("expected LLMDebugDir '/tmp/alias-llm', got %q", cfg.LLMDebugDir)
		}
	})

	t.Run("empty args does nothing", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags(nil); err != nil {
			t.Fatalf("ParseFlags failed: %v", err)
		}
		if cfg.LLMDebugPrompts {
			t.Errorf("expected LLMDebugPrompts false")
		}
	})

	t.Run("unknown flag returns error", func(t *testing.T) {
		cfg, err := config.LoadFrom(func(s string) string { return "" })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := cfg.ParseFlags([]string{"-unknown-flag"}); err == nil {
			t.Fatalf("expected error for unknown flag, got nil")
		}
	})
}

func TestLoad_DotEnv(t *testing.T) {
	tempDir := t.TempDir()
	envPath := filepath.Join(tempDir, ".env")
	envContent := `
# Resumeee test configuration
DEBUG_LLM_PROMPTS=true
DEBUG_LLM_DIR=/data/custom_debug
PORT=9095
DB_PATH=./data/dotenv.db
`
	if err := os.WriteFile(envPath, []byte(envContent), 0600); err != nil {
		t.Fatalf("failed to write test .env: %v", err)
	}

	// Verify process environment variable overrides value defined in .env
	t.Setenv("PORT", "9999")

	cfg, err := config.LoadFromDir(tempDir)
	if err != nil {
		t.Fatalf("LoadFromDir failed: %v", err)
	}

	if !cfg.LLMDebugPrompts {
		t.Errorf("expected LLMDebugPrompts true from .env DEBUG_LLM_PROMPTS, got false")
	}
	if cfg.LLMDebugDir != "/data/custom_debug" {
		t.Errorf("expected LLMDebugDir '/data/custom_debug', got %q", cfg.LLMDebugDir)
	}
	if cfg.Port != 9999 {
		t.Errorf("expected process env PORT 9999 to override .env 9095, got %d", cfg.Port)
	}
	if cfg.DBPath != "./data/dotenv.db" {
		t.Errorf("expected DBPath './data/dotenv.db' from .env, got %q", cfg.DBPath)
	}
}

func TestLoadDotEnv_TableDriven(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		fileContent string
		nonExistent bool
		expected    map[string]string
		expectError bool
	}{
		{
			name:        "non-existent file returns empty map without error",
			nonExistent: true,
			expected:    map[string]string{},
			expectError: false,
		},
		{
			name: "skips empty lines and comments",
			fileContent: `
# This is a comment
   # Indented comment

KEY1=value1

# Another comment
KEY2=value2
`,
			expected: map[string]string{
				"KEY1": "value1",
				"KEY2": "value2",
			},
			expectError: false,
		},
		{
			name: "strips export prefix",
			fileContent: `
export EXPORTED_VAR=exported_val
export   ANOTHER_VAR=another_val
NORMAL_VAR=normal_val
`,
			expected: map[string]string{
				"EXPORTED_VAR": "exported_val",
				"ANOTHER_VAR":  "another_val",
				"NORMAL_VAR":   "normal_val",
			},
			expectError: false,
		},
		{
			name: "unwraps single and double quotes",
			fileContent: `
DOUBLE_QUOTES="hello world"
SINGLE_QUOTES='single quote test'
UNQUOTED=no_quotes
EMPTY_QUOTES=""
SINGLE_CHAR_QUOTE="
MISMATCHED_QUOTE="mismatched'
`,
			expected: map[string]string{
				"DOUBLE_QUOTES":     "hello world",
				"SINGLE_QUOTES":     "single quote test",
				"UNQUOTED":          "no_quotes",
				"EMPTY_QUOTES":      "",
				"SINGLE_CHAR_QUOTE": "\"",
				"MISMATCHED_QUOTE":  "\"mismatched'",
			},
			expectError: false,
		},
		{
			name: "handles lines without equals sign gracefully",
			fileContent: `
VALID_KEY=valid_val
JUST_A_STRING_NO_EQUALS
ANOTHER_VALID=123
`,
			expected: map[string]string{
				"VALID_KEY":     "valid_val",
				"ANOTHER_VALID": "123",
			},
			expectError: false,
		},
		{
			name: "handles values with equals sign",
			fileContent: `
CONNECTION_STRING=postgres://user:pass@localhost:5432/db?sslmode=disable
EQUALS_VAL=foo=bar=baz
`,
			expected: map[string]string{
				"CONNECTION_STRING": "postgres://user:pass@localhost:5432/db?sslmode=disable",
				"EQUALS_VAL":        "foo=bar=baz",
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(tempDir, tc.name+".env")
			if !tc.nonExistent {
				if err := os.WriteFile(path, []byte(tc.fileContent), 0600); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
			}

			result, err := config.LoadDotEnv(path)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(result) != len(tc.expected) {
				t.Fatalf("expected %d entries, got %d: %+v", len(tc.expected), len(result), result)
			}

			for k, expectedVal := range tc.expected {
				gotVal, ok := result[k]
				if !ok {
					t.Errorf("missing key %q in result", k)
				} else if gotVal != expectedVal {
					t.Errorf("key %q: expected %q, got %q", k, expectedVal, gotVal)
				}
			}
		})
	}
}

func TestLoadTimeouts(t *testing.T) {
	t.Run("Custom REST_TIMEOUT and LLM_TIMEOUT", func(t *testing.T) {
		env := map[string]string{
			"REST_TIMEOUT": "45s",
			"LLM_TIMEOUT":  "15m",
		}
		cfg, err := config.LoadFrom(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RESTTimeout != 45*time.Second {
			t.Errorf("expected RESTTimeout 45s, got %v", cfg.RESTTimeout)
		}
		if cfg.LLMTimeout != 15*time.Minute {
			t.Errorf("expected LLMTimeout 15m, got %v", cfg.LLMTimeout)
		}
	})

	t.Run("Fallback to HTTP_TIMEOUT or RESUMEEE_CLIENT_TIMEOUT", func(t *testing.T) {
		env := map[string]string{
			"RESUMEEE_CLIENT_TIMEOUT": "30s",
		}
		cfg, err := config.LoadFrom(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.RESTTimeout != 30*time.Second {
			t.Errorf("expected RESTTimeout 30s from fallback, got %v", cfg.RESTTimeout)
		}
	})

	t.Run("Fallback to LLM_CALL_TIMEOUT", func(t *testing.T) {
		env := map[string]string{
			"LLM_CALL_TIMEOUT": "12m",
		}
		cfg, err := config.LoadFrom(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.LLMTimeout != 12*time.Minute {
			t.Errorf("expected LLMTimeout 12m from fallback, got %v", cfg.LLMTimeout)
		}
	})
}

func TestLoadInvalidTimeouts(t *testing.T) {
	invalidCases := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "Invalid REST_TIMEOUT string",
			env:  map[string]string{"REST_TIMEOUT": "not-a-duration"},
		},
		{
			name: "Negative REST_TIMEOUT",
			env:  map[string]string{"REST_TIMEOUT": "-5s"},
		},
		{
			name: "Zero REST_TIMEOUT",
			env:  map[string]string{"REST_TIMEOUT": "0s"},
		},
		{
			name: "Invalid LLM_TIMEOUT string",
			env:  map[string]string{"LLM_TIMEOUT": "invalid"},
		},
		{
			name: "Negative LLM_TIMEOUT",
			env:  map[string]string{"LLM_TIMEOUT": "-1m"},
		},
		{
			name: "Zero LLM_TIMEOUT",
			env:  map[string]string{"LLM_TIMEOUT": "0s"},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadFrom(func(k string) string { return tc.env[k] })
			if err == nil {
				t.Fatalf("expected error for invalid timeout config, got nil")
			}
		})
	}
}

func TestLoadMultiTenancyConfig(t *testing.T) {
	t.Run("Valid multi-tenancy env variables", func(t *testing.T) {
		env := map[string]string{
			"ADMIN":                   "true",
			"FIREBASE_PROJECT_ID":     "resumeee-prod-123",
			"LLM_DAILY_REQUEST_LIMIT": "100",
			"LLM_MONTHLY_TOKEN_LIMIT": "5000000",
		}
		cfg, err := config.LoadFrom(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.Admin {
			t.Errorf("expected Admin true, got false")
		}
		if cfg.FirebaseProjectID != "resumeee-prod-123" {
			t.Errorf("expected FirebaseProjectID 'resumeee-prod-123', got %q", cfg.FirebaseProjectID)
		}
		if cfg.LLMDailyRequestLimit != 100 {
			t.Errorf("expected LLMDailyRequestLimit 100, got %d", cfg.LLMDailyRequestLimit)
		}
		if cfg.LLMMonthlyTokenLimit != 5000000 {
			t.Errorf("expected LLMMonthlyTokenLimit 5000000, got %d", cfg.LLMMonthlyTokenLimit)
		}
	})

	t.Run("ADMIN boolean parsing error", func(t *testing.T) {
		env := map[string]string{
			"ADMIN": "not-a-bool",
		}
		_, err := config.LoadFrom(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("expected error for invalid ADMIN boolean, got nil")
		}
	})

	t.Run("LLM_DAILY_REQUEST_LIMIT integer parsing error", func(t *testing.T) {
		env := map[string]string{
			"LLM_DAILY_REQUEST_LIMIT": "invalid",
		}
		_, err := config.LoadFrom(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("expected error for invalid LLM_DAILY_REQUEST_LIMIT, got nil")
		}
	})

	t.Run("LLM_MONTHLY_TOKEN_LIMIT integer parsing error", func(t *testing.T) {
		env := map[string]string{
			"LLM_MONTHLY_TOKEN_LIMIT": "-10",
		}
		_, err := config.LoadFrom(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("expected error for negative LLM_MONTHLY_TOKEN_LIMIT, got nil")
		}
	})
}

func TestConfig_ParseFlags_Admin(t *testing.T) {
	cfg, err := config.LoadFrom(func(s string) string { return "" })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := cfg.ParseFlags([]string{"-admin"}); err != nil {
		t.Fatalf("ParseFlags failed: %v", err)
	}
	if !cfg.Admin {
		t.Errorf("expected Admin true after -admin flag, got false")
	}
}

