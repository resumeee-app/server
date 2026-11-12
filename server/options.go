package server

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/validator"
)

// Option configures a Server instance during construction.
type Option func(*Server) error

// WithConfig replaces the server's configuration with the provided Config struct.
func WithConfig(cfg *Config) Option {
	return func(s *Server) error {
		if cfg == nil {
			return errors.New("server: Config cannot be nil")
		}
		copied := *cfg
		s.cfg = &copied
		return nil
	}
}

// WithEnvConfig loads configuration values from process environment variables and .env file if present.
func WithEnvConfig() Option {
	return func(s *Server) error {
		cfg, err := LoadConfig()
		if err != nil {
			return err
		}
		s.cfg = cfg
		s.loadEnv = true
		return nil
	}
}

// WithFlags parses CLI flag arguments (such as -admin-mode, -auth-provider, -debug-llm-prompts, -llm-debug-dir)
// onto the server configuration before startup.
func WithFlags(args ...string) Option {
	return func(s *Server) error {
		s.flagArgs = append([]string(nil), args...)
		return nil
	}
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

// WithDBPath overrides the default SQLite database file path.
func WithDBPath(dbPath string) Option {
	return func(s *Server) error {
		if strings.TrimSpace(dbPath) == "" {
			return errors.New("server: dbPath cannot be empty")
		}
		s.cfg.DBPath = strings.TrimSpace(dbPath)
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

// WithFirebaseProjectID sets the Firebase project ID for multi-tenant JWT verification.
func WithFirebaseProjectID(projectID string) Option {
	return func(s *Server) error {
		s.cfg.FirebaseProjectID = strings.TrimSpace(projectID)
		if s.cfg.FirebaseAuthDomain == "" && s.cfg.FirebaseProjectID != "" {
			s.cfg.FirebaseAuthDomain = s.cfg.FirebaseProjectID + ".firebaseapp.com"
		}
		return nil
	}
}

// WithAuthProviderName sets the active authentication provider name (e.g. "admin", "firebase").
func WithAuthProviderName(name string) Option {
	return func(s *Server) error {
		s.cfg.AuthProvider = strings.TrimSpace(name)
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

// WithWorkerPoolConfig customizes the background generation worker pool concurrency, queue capacity,
// and per-tenant concurrency ceiling.
func WithWorkerPoolConfig(concurrency, queueSize, maxPerTenant int) Option {
	return func(s *Server) error {
		if concurrency <= 0 {
			return fmt.Errorf("server: worker concurrency must be > 0, got %d", concurrency)
		}
		if queueSize <= 0 {
			return fmt.Errorf("server: worker queueSize must be > 0, got %d", queueSize)
		}
		if maxPerTenant <= 0 {
			return fmt.Errorf("server: maxPerTenant must be > 0, got %d", maxPerTenant)
		}
		s.cfg.WorkerConcurrency = concurrency
		s.cfg.WorkerQueueSize = queueSize
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

// WithStore injects a pre-initialized domain.Store implementation (such as an in-memory, SQLite, or PostgreSQL store).
// Stores provided via WithStore are managed by the caller and are not closed automatically by Server.Run.
func WithStore(store domain.Store) Option {
	return func(s *Server) error {
		if store == nil {
			return errors.New("server: Store cannot be nil")
		}
		s.store = store
		return nil
	}
}

// WithStoreInitializer injects a custom StoreInitializer factory executed during Server.Run startup.
// Stores created via WithStoreInitializer are automatically closed when Server.Run shuts down.
func WithStoreInitializer(initFn StoreInitializer) Option {
	return func(s *Server) error {
		if initFn == nil {
			return errors.New("server: StoreInitializer cannot be nil")
		}
		s.storeInit = initFn
		return nil
	}
}

// WithAuthProvider injects a custom domain.AuthProvider implementation for request authentication.
func WithAuthProvider(provider domain.AuthProvider) Option {
	return func(s *Server) error {
		if provider == nil {
			return errors.New("server: AuthProvider cannot be nil")
		}
		s.authProvider = provider
		return nil
	}
}

// WithTokenVerifier injects a custom domain.TokenVerifier wrapped as an AuthProvider with the given name.
func WithTokenVerifier(name string, verifier domain.TokenVerifier) Option {
	return func(s *Server) error {
		if verifier == nil {
			return errors.New("server: TokenVerifier cannot be nil")
		}
		s.authProvider = auth.NewTokenVerifierProvider(name, verifier)
		return nil
	}
}

// WithAuthRegistry injects a pre-configured authentication Registry.
func WithAuthRegistry(reg *auth.Registry) Option {
	return func(s *Server) error {
		if reg == nil {
			return errors.New("server: AuthRegistry cannot be nil")
		}
		s.authRegistry = reg
		return nil
	}
}

// WithLLMRegistry injects a pre-configured llm.Registry, replacing default environment-based LLM provider registration.
func WithLLMRegistry(reg *llm.Registry) Option {
	return func(s *Server) error {
		if reg == nil {
			return errors.New("server: LLMRegistry cannot be nil")
		}
		s.llmRegistry = reg
		return nil
	}
}

// WithLLMClient registers an additional pure-transport domain.LLMClient (wrapped via llm.NewClientProvider)
// into the server's LLM registry.
func WithLLMClient(client domain.LLMClient, opts ...llm.Option) Option {
	return func(s *Server) error {
		if client == nil {
			return errors.New("server: LLMClient cannot be nil")
		}
		s.llmClients = append(s.llmClients, registeredLLMClient{
			client: client,
			opts:   opts,
		})
		return nil
	}
}

// WithLLMProvider registers a custom domain.LLMProvider implementation into the server's LLM registry.
func WithLLMProvider(provider domain.LLMProvider) Option {
	return func(s *Server) error {
		if provider == nil {
			return errors.New("server: LLMProvider cannot be nil")
		}
		s.llmProviders = append(s.llmProviders, provider)
		return nil
	}
}

// WithLLMProviderConfig registers built-in LLM transport providers from an explicit llm.ProviderConfig.
func WithLLMProviderConfig(cfg llm.ProviderConfig, opts ...llm.Option) Option {
	return func(s *Server) error {
		s.llmProviderCfgs = append(s.llmProviderCfgs, registeredLLMProviderCfg{
			cfg:  cfg,
			opts: opts,
		})
		return nil
	}
}

// WithTemplateRenderer injects a custom domain.TemplateRenderer implementation.
func WithTemplateRenderer(r domain.TemplateRenderer) Option {
	return func(s *Server) error {
		if r == nil {
			return errors.New("server: TemplateRenderer cannot be nil")
		}
		s.templateRenderer = r
		return nil
	}
}

// WithValidator injects a custom validator.ProvenanceValidator implementation.
func WithValidator(v validator.ProvenanceValidator) Option {
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

// WithResumeExtractor injects a custom llm.ResumeExtractor implementation.
func WithResumeExtractor(extractor llm.ResumeExtractor) Option {
	return func(s *Server) error {
		if extractor == nil {
			return errors.New("server: ResumeExtractor cannot be nil")
		}
		s.resumeExtractor = extractor
		return nil
	}
}

// WithPromptCompiler injects a custom domain.PromptCompiler implementation.
func WithPromptCompiler(compiler domain.PromptCompiler) Option {
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
