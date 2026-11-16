package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/core/worker"
	"github.com/resumeee-app/server/auth"
	"github.com/resumeee-app/server/internal/config"
)

// TemplateRenderer defines the template rendering and validation contract required by Server.
type TemplateRenderer interface {
	renderer.TemplateRenderer
	Validate(tmpl *core.TemplateSet) error
}

// ProvenanceValidator defines the provenance validation contract required by branch and worker services.
type ProvenanceValidator interface {
	branch.ProvenanceValidator
	worker.ProvenanceValidator
}

// PDFTextExtractor defines the PDF text extraction contract required by Server.
type PDFTextExtractor interface {
	ExtractText(ctx context.Context, r io.ReaderAt, size int64) (string, error)
}

// Option configures a Server instance during construction.
type Option func(*Server) error

// WithConfig validates, normalizes, and replaces the server's configuration with the provided Config struct.
func WithConfig(cfg *Config) Option {
	return func(s *Server) error {
		normalized, err := validateAndNormalizeConfig(cfg)
		if err != nil {
			return err
		}
		s.cfg = &normalized
		return nil
	}
}

// validateAndNormalizeConfig enforces Config bounds and normalizes optional string defaults.
func validateAndNormalizeConfig(cfg *Config) (Config, error) {
	if cfg == nil {
		return Config{}, errors.New("server: Config cannot be nil")
	}
	copied := *cfg
	if copied.Port < 1 || copied.Port > 65535 {
		return Config{}, fmt.Errorf("server: invalid port %d: must be between 1 and 65535", copied.Port)
	}
	copied.DefaultUserID = strings.TrimSpace(copied.DefaultUserID)
	if copied.DefaultUserID == "" {
		return Config{}, errors.New("server: defaultUserID cannot be empty")
	}
	if err := validateConfigLimits(&copied); err != nil {
		return Config{}, err
	}
	if err := validateConfigDurations(&copied); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(copied.Environment) == "" {
		copied.Environment = config.DefaultEnvironment
	} else {
		copied.Environment = strings.TrimSpace(copied.Environment)
	}
	if strings.TrimSpace(copied.LLMDebugDir) == "" {
		copied.LLMDebugDir = config.DefaultLLMDebugDir
	} else {
		copied.LLMDebugDir = strings.TrimSpace(copied.LLMDebugDir)
	}
	return copied, nil
}

// validateConfigLimits verifies non-duration numeric ceilings inside Config are positive.
func validateConfigLimits(cfg *Config) error {
	limits := []struct {
		name string
		val  int
	}{
		{"LLMDailyRequestLimit", cfg.LLMDailyRequestLimit},
		{"LLMMonthlyTokenLimit", cfg.LLMMonthlyTokenLimit},
		{"WorkerConcurrency", cfg.WorkerConcurrency},
		{"MaxConcurrentJobsPerTenant", cfg.MaxConcurrentJobsPerTenant},
	}
	for _, limit := range limits {
		if limit.val <= 0 {
			return fmt.Errorf("server: %s must be > 0, got %d", limit.name, limit.val)
		}
	}
	return nil
}

// validateConfigDurations verifies every configured timeout and interval inside Config is positive.
func validateConfigDurations(cfg *Config) error {
	durations := []struct {
		name string
		val  time.Duration
	}{
		{"RESTTimeout", cfg.RESTTimeout},
		{"LLMTimeout", cfg.LLMTimeout},
		{"PDFParseTimeout", cfg.PDFParseTimeout},
		{"RenderPreviewTimeout", cfg.RenderPreviewTimeout},
		{"HTTPReadHeaderTimeout", cfg.HTTPReadHeaderTimeout},
		{"HTTPReadTimeout", cfg.HTTPReadTimeout},
		{"HTTPMinWriteTimeout", cfg.HTTPMinWriteTimeout},
		{"HTTPWriteTimeoutBuffer", cfg.HTTPWriteTimeoutBuffer},
		{"HTTPIdleTimeout", cfg.HTTPIdleTimeout},
		{"HTTPShutdownTimeout", cfg.HTTPShutdownTimeout},
		{"MCPSSEKeepAliveInterval", cfg.MCPSSEKeepAliveInterval},
	}
	for _, d := range durations {
		if d.val <= 0 {
			return fmt.Errorf("server: %s must be > 0, got %v", d.name, d.val)
		}
	}
	return nil
}

// WithPort overrides the TCP port the HTTP server listens on.
func WithPort(port int) Option {
	return func(s *Server) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("server: invalid port %d: must be between 1 and 65535", port)
		}
		s.cfg.Port = port
		return nil
	}
}

// WithAdminMode enables or disables local single-user admin mode.
func WithAdminMode(enabled bool) Option {
	return func(s *Server) error {
		s.cfg.Admin = enabled
		return nil
	}
}

// WithDefaultUserID sets the default user ID used in single-user admin mode.
func WithDefaultUserID(userID string) Option {
	return func(s *Server) error {
		if strings.TrimSpace(userID) == "" {
			return errors.New("server: defaultUserID cannot be empty")
		}
		s.cfg.DefaultUserID = strings.TrimSpace(userID)
		return nil
	}
}

// WithQuotaLimits sets the rolling daily request limit and monthly token limit per tenant.
func WithQuotaLimits(dailyRequests, monthlyTokens int) Option {
	return func(s *Server) error {
		if dailyRequests <= 0 {
			return fmt.Errorf("server: dailyRequests must be > 0, got %d", dailyRequests)
		}
		if monthlyTokens <= 0 {
			return fmt.Errorf("server: monthlyTokens must be > 0, got %d", monthlyTokens)
		}
		s.cfg.LLMDailyRequestLimit = dailyRequests
		s.cfg.LLMMonthlyTokenLimit = monthlyTokens
		return nil
	}
}

// WithWorkerPoolConfig customizes the background generation worker pool concurrency
// and per-tenant concurrency ceiling.
func WithWorkerPoolConfig(concurrency, maxPerTenant int) Option {
	return func(s *Server) error {
		if concurrency <= 0 {
			return fmt.Errorf("server: worker concurrency must be > 0, got %d", concurrency)
		}
		if maxPerTenant <= 0 {
			return fmt.Errorf("server: maxPerTenant must be > 0, got %d", maxPerTenant)
		}
		s.cfg.WorkerConcurrency = concurrency
		s.cfg.MaxConcurrentJobsPerTenant = maxPerTenant
		return nil
	}
}

// WithTimeouts sets the synchronous REST request timeout and the asynchronous LLM operation timeout.
func WithTimeouts(restTimeout, llmTimeout time.Duration) Option {
	return func(s *Server) error {
		if restTimeout <= 0 {
			return fmt.Errorf("server: restTimeout must be > 0, got %v", restTimeout)
		}
		if llmTimeout <= 0 {
			return fmt.Errorf("server: llmTimeout must be > 0, got %v", llmTimeout)
		}
		s.cfg.RESTTimeout = restTimeout
		s.cfg.LLMTimeout = llmTimeout
		return nil
	}
}

// WithHandlerTimeouts configures execution timeouts for synchronous PDF extraction,
// template preview rendering, and MCP SSE heartbeat cadences.
func WithHandlerTimeouts(pdfParseTimeout, renderPreviewTimeout, mcpSSEKeepAliveInterval time.Duration) Option {
	return func(s *Server) error {
		if pdfParseTimeout <= 0 {
			return fmt.Errorf("server: pdfParseTimeout must be > 0, got %v", pdfParseTimeout)
		}
		if renderPreviewTimeout <= 0 {
			return fmt.Errorf("server: renderPreviewTimeout must be > 0, got %v", renderPreviewTimeout)
		}
		if mcpSSEKeepAliveInterval <= 0 {
			return fmt.Errorf("server: mcpSSEKeepAliveInterval must be > 0, got %v", mcpSSEKeepAliveInterval)
		}
		s.cfg.PDFParseTimeout = pdfParseTimeout
		s.cfg.RenderPreviewTimeout = renderPreviewTimeout
		s.cfg.MCPSSEKeepAliveInterval = mcpSSEKeepAliveInterval
		return nil
	}
}

// WithHTTPTimeouts configures socket-level HTTP server read, write, idle, and graceful shutdown bounds.
func WithHTTPTimeouts(readHeader, read, minWrite, writeBuffer, idle, shutdown time.Duration) Option {
	return func(s *Server) error {
		checks := []struct {
			name string
			val  time.Duration
		}{
			{"readHeader", readHeader},
			{"read", read},
			{"minWrite", minWrite},
			{"writeBuffer", writeBuffer},
			{"idle", idle},
			{"shutdown", shutdown},
		}
		for _, c := range checks {
			if c.val <= 0 {
				return fmt.Errorf("server: %s timeout must be > 0, got %v", c.name, c.val)
			}
		}
		s.cfg.HTTPReadHeaderTimeout = readHeader
		s.cfg.HTTPReadTimeout = read
		s.cfg.HTTPMinWriteTimeout = minWrite
		s.cfg.HTTPWriteTimeoutBuffer = writeBuffer
		s.cfg.HTTPIdleTimeout = idle
		s.cfg.HTTPShutdownTimeout = shutdown
		return nil
	}
}

// WithLLMDebug enables or disables persisting outbound LLM prompts and responses to disk.
func WithLLMDebug(enabled bool, dir string) Option {
	return func(s *Server) error {
		s.cfg.LLMDebugPrompts = enabled
		if strings.TrimSpace(dir) != "" {
			s.cfg.LLMDebugDir = strings.TrimSpace(dir)
		}
		return nil
	}
}

// WithEnvironment sets the runtime environment name (e.g. "development", "production").
func WithEnvironment(env string) Option {
	return func(s *Server) error {
		if strings.TrimSpace(env) != "" {
			s.cfg.Environment = strings.TrimSpace(env)
		}
		return nil
	}
}

// WithLogLevel sets the minimum structured logging severity level.
func WithLogLevel(level slog.Level) Option {
	return func(s *Server) error {
		s.cfg.LogLevel = level
		return nil
	}
}

// WithLogger injects a custom structured slog.Logger instance.
func WithLogger(logger *slog.Logger) Option {
	return func(s *Server) error {
		s.logger = logger
		return nil
	}
}

// WithStore injects a configured *store.Store (e.g., from resumeee-storage-sql or core/memory).
func WithStore(st *store.Store) Option {
	return func(s *Server) error {
		if st == nil {
			return errors.New("server: Store cannot be nil")
		}
		s.store = st
		return nil
	}
}

// WithAuthProvider injects a configured core.AuthProvider implementation (e.g., from resumeee-auth-firebase),
// wrapping it via auth.NewTokenVerifierProvider for HTTP Bearer token extraction. Callers supplying
// an HTTP-aware auth.Provider (such as auth.AdminProvider or a non-Bearer request authenticator) must
// use WithRequestAuthProvider instead; passing an "admin" provider is rejected so local admin auth is
// never silently replaced with Bearer-only parsing.
func WithAuthProvider(provider core.AuthProvider) Option {
	return func(s *Server) error {
		if provider == nil {
			return errors.New("server: AuthProvider cannot be nil")
		}
		name := strings.TrimSpace(provider.Name())
		if name == "" {
			return errors.New("server: AuthProvider name cannot be empty")
		}
		if strings.EqualFold(name, auth.ProviderAdmin) {
			return errors.New("server: admin AuthProvider requires WithRequestAuthProvider or WithAdminMode(true)")
		}
		s.authProvider = auth.NewTokenVerifierProvider(name, provider)
		return nil
	}
}

// WithRequestAuthProvider injects an HTTP-aware auth.Provider implementation directly.
func WithRequestAuthProvider(provider auth.Provider) Option {
	return func(s *Server) error {
		if provider == nil {
			return errors.New("server: RequestAuthProvider cannot be nil")
		}
		s.authProvider = provider
		return nil
	}
}

// WithTokenVerifier injects a configured core.TokenVerifier wrapped as an AuthProvider with the given name.
func WithTokenVerifier(name string, verifier core.TokenVerifier) Option {
	return func(s *Server) error {
		if verifier == nil {
			return errors.New("server: TokenVerifier cannot be nil")
		}
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return errors.New("server: TokenVerifier name cannot be empty")
		}
		if strings.EqualFold(trimmed, auth.ProviderAdmin) {
			return errors.New("server: provider name \"admin\" is reserved for AdminProvider")
		}
		s.authProvider = auth.NewTokenVerifierProvider(trimmed, verifier)
		return nil
	}
}

// WithLLMRegistry injects a pre-configured llm.Registry.
func WithLLMRegistry(reg *llm.Registry) Option {
	return func(s *Server) error {
		if reg == nil {
			return errors.New("server: LLMRegistry cannot be nil")
		}
		s.llmRegistry = reg
		return nil
	}
}

// WithLLMClient registers a configured llm.Client into the server's LLM registry.
// Caller-constructed clients are registered as-is and should configure their own request timeout
// (for example, via the provider's WithTimeout option).
func WithLLMClient(client llm.Client) Option {
	return func(s *Server) error {
		if client == nil {
			return errors.New("server: LLMClient cannot be nil")
		}
		s.llmClients = append(s.llmClients, client)
		return nil
	}
}

// WithTemplateRenderer injects a custom TemplateRenderer implementation.
func WithTemplateRenderer(r TemplateRenderer) Option {
	return func(s *Server) error {
		if r == nil {
			return errors.New("server: TemplateRenderer cannot be nil")
		}
		s.templateRenderer = r
		return nil
	}
}

// WithValidator injects a custom ProvenanceValidator implementation.
func WithValidator(v ProvenanceValidator) Option {
	return func(s *Server) error {
		if v == nil {
			return errors.New("server: ProvenanceValidator cannot be nil")
		}
		s.provValidator = v
		return nil
	}
}

// WithPDFExtractor injects a custom PDFTextExtractor implementation.
func WithPDFExtractor(extractor PDFTextExtractor) Option {
	return func(s *Server) error {
		if extractor == nil {
			return errors.New("server: PDFTextExtractor cannot be nil")
		}
		s.pdfExtractor = extractor
		return nil
	}
}

// WithResumeExtractor injects a custom worker.ResumeExtractor implementation.
func WithResumeExtractor(extractor worker.ResumeExtractor) Option {
	return func(s *Server) error {
		if extractor == nil {
			return errors.New("server: ResumeExtractor cannot be nil")
		}
		s.resumeExtractor = extractor
		return nil
	}
}

// WithPromptCompiler injects a custom branch.PromptCompiler implementation.
func WithPromptCompiler(compiler branch.PromptCompiler) Option {
	return func(s *Server) error {
		if compiler == nil {
			return errors.New("server: PromptCompiler cannot be nil")
		}
		s.promptCompiler = compiler
		return nil
	}
}

// WithMCPEnabled enables or disables mounting the HTTP Model Context Protocol (/mcp, /mcp/*) endpoints.
// Enabled by default.
func WithMCPEnabled(enabled bool) Option {
	return func(s *Server) error {
		s.mcpEnabled = enabled
		return nil
	}
}
