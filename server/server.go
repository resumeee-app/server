// Package server provides a configurable, dependency-injected HTTP REST and Model Context Protocol (MCP)
// server for Resumeee.
//
// It allows callers to customize runtime configuration, storage backends, authentication providers,
// LLM transport clients or providers, and domain services using functional With<Option> constructors,
// while providing sensible 12-Factor defaults out of the box.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-api/internal/config"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-api/internal/pdf"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core/services/worker"
	"github.com/resumeee-app/resumeee-core/validator"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
)

// Config encapsulates all runtime configuration parameters for a Resumeee Server.
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
}

// DefaultConfig returns a Config initialized with standard Resumeee defaults.
func DefaultConfig() *Config {
	return &Config{
		Port:                       config.DefaultPort,
		DBPath:                     config.DefaultDBPath,
		DefaultUserID:              config.DefaultUserIDVal,
		Admin:                      config.DefaultAdmin,
		LLMDailyRequestLimit:       config.DefaultLLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       config.DefaultLLMMonthlyTokenLimit,
		WorkerConcurrency:          config.DefaultWorkerConcurrency,
		WorkerQueueSize:            config.DefaultWorkerQueueSize,
		MaxConcurrentJobsPerTenant: config.DefaultMaxConcurrentJobsPerTenant,
		Environment:                config.DefaultEnvironment,
		LogLevel:                   slog.LevelInfo,
		LLMDebugPrompts:            false,
		LLMDebugDir:                config.DefaultLLMDebugDir,
		RESTTimeout:                config.DefaultRESTTimeout,
		LLMTimeout:                 config.DefaultLLMTimeout,
	}
}

// LoadConfig reads server configuration from process environment variables and a local .env file if present.
func LoadConfig() (*Config, error) {
	icfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return fromInternalConfig(icfg), nil
}

// LoadConfigFrom parses server configuration using a custom environment lookup function.
func LoadConfigFrom(lookup func(string) string) (*Config, error) {
	icfg, err := config.LoadFrom(lookup)
	if err != nil {
		return nil, err
	}
	return fromInternalConfig(icfg), nil
}

func fromInternalConfig(c *config.Config) *Config {
	if c == nil {
		return DefaultConfig()
	}
	return &Config{
		Port:                       c.Port,
		DBPath:                     c.DBPath,
		DefaultUserID:              c.DefaultUserID,
		Admin:                      c.Admin,
		AuthProvider:               c.AuthProvider,
		FirebaseProjectID:          c.FirebaseProjectID,
		FirebaseAPIKey:             c.FirebaseAPIKey,
		FirebaseAuthDomain:         c.FirebaseAuthDomain,
		LLMDailyRequestLimit:       c.LLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       c.LLMMonthlyTokenLimit,
		WorkerConcurrency:          c.WorkerConcurrency,
		WorkerQueueSize:            c.WorkerQueueSize,
		MaxConcurrentJobsPerTenant: c.MaxConcurrentJobsPerTenant,
		GeminiAPIKey:               c.GeminiAPIKey,
		OpenAIAPIKey:               c.OpenAIAPIKey,
		AnthropicAPIKey:            c.AnthropicAPIKey,
		OpenAICompatBaseURL:        c.OpenAICompatBaseURL,
		OpenAICompatAPIKey:         c.OpenAICompatAPIKey,
		Environment:                c.Environment,
		LogLevel:                   c.LogLevel,
		LLMDebugPrompts:            c.LLMDebugPrompts,
		LLMDebugDir:                c.LLMDebugDir,
		RESTTimeout:                c.RESTTimeout,
		LLMTimeout:                 c.LLMTimeout,
	}
}

func (c *Config) toInternalConfig() *config.Config {
	if c == nil {
		c = DefaultConfig()
	}
	return &config.Config{
		Port:                       c.Port,
		DBPath:                     c.DBPath,
		DefaultUserID:              c.DefaultUserID,
		Admin:                      c.Admin,
		AuthProvider:               c.AuthProvider,
		FirebaseProjectID:          c.FirebaseProjectID,
		FirebaseAPIKey:             c.FirebaseAPIKey,
		FirebaseAuthDomain:         c.FirebaseAuthDomain,
		LLMDailyRequestLimit:       c.LLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       c.LLMMonthlyTokenLimit,
		WorkerConcurrency:          c.WorkerConcurrency,
		WorkerQueueSize:            c.WorkerQueueSize,
		MaxConcurrentJobsPerTenant: c.MaxConcurrentJobsPerTenant,
		GeminiAPIKey:               c.GeminiAPIKey,
		OpenAIAPIKey:               c.OpenAIAPIKey,
		AnthropicAPIKey:            c.AnthropicAPIKey,
		OpenAICompatBaseURL:        c.OpenAICompatBaseURL,
		OpenAICompatAPIKey:         c.OpenAICompatAPIKey,
		Environment:                c.Environment,
		LogLevel:                   c.LogLevel,
		LLMDebugPrompts:            c.LLMDebugPrompts,
		LLMDebugDir:                c.LLMDebugDir,
		RESTTimeout:                c.RESTTimeout,
		LLMTimeout:                 c.LLMTimeout,
	}
}

// StoreInitializer initializes a storage backend and returns a domain.Store implementation.
type StoreInitializer func(ctx context.Context, cfg *Config, logger *slog.Logger) (domain.Store, error)

// PDFTextExtractor abstracts extracting textual content from PDF binary streams.
type PDFTextExtractor interface {
	ExtractText(ctx context.Context, r io.ReaderAt, size int64) (string, error)
}

type registeredLLMClient struct {
	client domain.LLMClient
	opts   []llm.Option
}

type registeredLLMProviderCfg struct {
	cfg  llm.ProviderConfig
	opts []llm.Option
}

// Server orchestrates the Resumeee HTTP REST API, MCP server, background worker pool, and domain services.
type Server struct {
	cfg              *Config
	loadEnv          bool
	flagArgs         []string
	logger           *slog.Logger
	store            domain.Store
	storeInit        StoreInitializer
	authProvider     domain.AuthProvider
	authRegistry     *auth.Registry
	llmRegistry      *llm.Registry
	llmClients       []registeredLLMClient
	llmProviders     []domain.LLMProvider
	llmProviderCfgs  []registeredLLMProviderCfg
	templateRenderer domain.TemplateRenderer
	provValidator    validator.ProvenanceValidator
	pdfExtractor     PDFTextExtractor
	resumeExtractor  llm.ResumeExtractor
	promptCompiler   domain.PromptCompiler
	mcpEnabled       bool
}

// New constructs a customizable Resumeee Server applying all provided functional Options.
func New(opts ...Option) (*Server, error) {
	s := &Server{
		cfg:        DefaultConfig(),
		mcpEnabled: true,
	}

	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(s); err != nil {
			return nil, err
		}
	}

	return s, nil
}

// Run constructs and executes a Server with the given functional Options until ctx is canceled.
func Run(ctx context.Context, opts ...Option) error {
	srv, err := New(opts...)
	if err != nil {
		return err
	}
	return srv.Run(ctx)
}

// DefaultSQLiteStoreInitializer opens the default SQLite persistence backend with auto-migration and seeding.
func DefaultSQLiteStoreInitializer(ctx context.Context, cfg *Config, logger *slog.Logger) (domain.Store, error) {
	store, err := storage.Open(ctx, storage.Config{
		Driver:        storage.DriverSQLite,
		DSN:           cfg.DBPath,
		AutoMigrate:   true,
		DefaultUserID: cfg.DefaultUserID,
	})
	if err != nil {
		return nil, err
	}
	if logger != nil {
		logger.Info("database seeded with default profile, built-in templates, and presets")
	}
	return store, nil
}

// Run initializes all configured or default dependencies, starts the background worker pool,
// serves the HTTP API (and optional MCP routes), and performs a graceful shutdown when ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg == nil {
		s.cfg = DefaultConfig()
	}

	if len(s.flagArgs) > 0 {
		icfg := s.cfg.toInternalConfig()
		if err := icfg.ParseFlags(s.flagArgs); err != nil {
			return fmt.Errorf("parsing command line flags: %w", err)
		}
		s.cfg = fromInternalConfig(icfg)
	}

	logger := s.logger
	if logger == nil {
		logger = config.NewLogger(s.cfg.toInternalConfig(), os.Stdout)
	}

	logger.Info("starting resumeee server",
		"port", s.cfg.Port,
		"env", s.cfg.Environment,
		"db_path", s.cfg.DBPath,
		"default_user_id", s.cfg.DefaultUserID,
		"worker_concurrency", s.cfg.WorkerConcurrency,
		"worker_queue_size", s.cfg.WorkerQueueSize,
		"worker_max_concurrent_per_tenant", s.cfg.MaxConcurrentJobsPerTenant,
		"llm_debug_prompts", s.cfg.LLMDebugPrompts,
		"llm_debug_dir", s.cfg.LLMDebugDir,
	)

	if s.cfg.LLMDebugPrompts && strings.TrimSpace(s.cfg.LLMDebugDir) != "" {
		if err := os.MkdirAll(s.cfg.LLMDebugDir, 0750); err != nil {
			return fmt.Errorf("creating llm debug directory: %w", err)
		}
	}

	store, closeStore, err := s.resolveStore(ctx, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	if closeStore {
		defer func() {
			if err := store.Close(); err != nil {
				logger.Warn("failed to close store", "err", err)
			}
		}()
	}

	branchRepo := store.Branch()
	if err := branchRepo.FailInterruptedVersions(ctx, "server restarted while generation job was in flight"); err != nil {
		logger.Warn("failed to clean up interrupted versions on boot", "err", err)
	} else {
		logger.Info("cleaned up interrupted generation versions")
	}

	profileRepo := store.Profile()
	presetRepo := store.Preset()
	templateRepo := store.Template()
	pdfJobRepo := store.PDFJob()
	quotaRepo := store.Quota()
	profileSvc := profile.NewService(profileRepo, branchRepo)

	templateRenderer := s.templateRenderer
	if templateRenderer == nil {
		templateRenderer = renderer.NewTemplateRenderer()
	}

	provValidator := s.provValidator
	if provValidator == nil {
		provValidator = validator.New()
	}

	pdfExtractor := s.pdfExtractor
	if pdfExtractor == nil {
		pdfExtractor = pdf.NewExtractor()
	}

	promptCompiler := s.promptCompiler
	if promptCompiler == nil {
		promptCompiler = llm.NewPromptCompiler()
	}

	authProvider, err := s.resolveAuthProvider(logger)
	if err != nil {
		return err
	}

	llmRegistry := s.resolveLLMRegistry(logger)
	llmAdapter := llm.NewServiceAdapter(llmRegistry)

	resumeExtractor := s.resumeExtractor
	if resumeExtractor == nil {
		resumeExtractor = llm.NewDefaultResumeExtractor(llmRegistry)
	}

	workerPool := worker.NewPool(worker.PoolConfig{
		Concurrency:            s.cfg.WorkerConcurrency,
		QueueSize:              s.cfg.WorkerQueueSize,
		MaxConcurrentPerTenant: s.cfg.MaxConcurrentJobsPerTenant,
		BranchRepo:             branchRepo,
		ProfileRepo:            profileRepo,
		PresetRepo:             presetRepo,
		TemplateRepo:           templateRepo,
		PDFJobRepo:             pdfJobRepo,
		QuotaRepo:              quotaRepo,
		ResumeExtractor:        resumeExtractor,
		LLMTimeout:             s.cfg.LLMTimeout,
		LLMService:             llmAdapter,
		Validator:              provValidator,
		Renderer:               templateRenderer,
		Logger:                 logger,
	})
	if err := workerPool.RecoverStaleJobs(ctx, "server restarted during job processing"); err != nil {
		logger.Warn("failed to recover stale jobs", "err", err)
	}
	workerPool.Start(ctx)
	defer workerPool.Stop()

	branchSvc := branch.NewService(branch.Config{
		BranchRepo:        branchRepo,
		ProfileRepo:       profileRepo,
		PresetRepo:        presetRepo,
		TemplateRepo:      templateRepo,
		QuotaRepo:         quotaRepo,
		JobEnqueuer:       workerPool,
		Validator:         provValidator,
		Renderer:          templateRenderer,
		LLMService:        llmAdapter,
		PromptCompiler:    promptCompiler,
		DailyRequestLimit: s.cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: s.cfg.LLMMonthlyTokenLimit,
		Logger:            logger,
	})

	apiRouter := api.NewRouter(api.RouterConfig{
		ProfileRepo:       profileRepo,
		PresetRepo:        presetRepo,
		TemplateRepo:      templateRepo,
		BranchRepo:        branchRepo,
		PDFJobRepo:        pdfJobRepo,
		QuotaRepo:         quotaRepo,
		BranchService:     branchSvc,
		ProfileService:    profileSvc,
		PDFExtractor:      pdfExtractor,
		ResumeExtractor:   resumeExtractor,
		Renderer:          templateRenderer,
		Validator:         provValidator,
		JobEnqueuer:       workerPool,
		PDFJobEnqueuer:    workerPool,
		ModelEnumerator:   llmRegistry,
		DefaultUserID:     s.cfg.DefaultUserID,
		Admin:             s.cfg.Admin,
		AuthProvider:      authProvider,
		TokenVerifier:     authProvider,
		FirebaseProjectID: s.cfg.FirebaseProjectID,
		UserRepo:          store.User(),
		Store:             store,
		DailyRequestLimit: s.cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: s.cfg.LLMMonthlyTokenLimit,
		RESTTimeout:       s.cfg.RESTTimeout,
		LLMTimeout:        s.cfg.LLMTimeout,
		Logger:            logger,
	})

	var mcpHandler http.Handler
	if s.mcpEnabled {
		mcpCfg := resumeeeMCP.ServerConfig{
			ProfileService:        profileSvc,
			BranchService:         branchSvc,
			TemplateRenderer:      templateRenderer,
			ProfileRepo:           profileRepo,
			BranchRepo:            branchRepo,
			TemplateRepo:          templateRepo,
			PresetRepo:            presetRepo,
			UserRepo:              store.User(),
			ResumeExtractor:       resumeExtractor,
			JobEnqueuer:           workerPool,
			DefaultUserID:         s.cfg.DefaultUserID,
			Admin:                 s.cfg.Admin,
			AuthProvider:          authProvider,
			TokenVerifier:         authProvider,
			Logger:                logger,
			RESTTimeout:           s.cfg.RESTTimeout,
			TailoringTimeout:      s.cfg.LLMTimeout,
			MaxConnectionsPerUser: 5,
			MaxTotalConnections:   100,
		}
		mcpSrv := resumeeeMCP.NewServer(mcpCfg)
		mcpHandler = resumeeeMCP.NewHTTPHandler(mcpSrv, mcpCfg)
	}

	rootHandler := buildRootHandler(apiRouter, mcpHandler)

	writeTimeout := s.cfg.RESTTimeout + 10*time.Second
	if writeTimeout < 30*time.Second {
		writeTimeout = 30 * time.Second
	}
	httpServer := buildHTTPServer(fmt.Sprintf(":%d", s.cfg.Port), rootHandler, writeTimeout)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down resumeee server gracefully", "signal", ctx.Err())
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server graceful shutdown: %w", err)
	}

	logger.Info("server shutdown complete")
	return nil
}

func (s *Server) resolveStore(ctx context.Context, logger *slog.Logger) (domain.Store, bool, error) {
	if s.store != nil {
		return s.store, false, nil
	}
	initFn := s.storeInit
	if initFn == nil {
		initFn = DefaultSQLiteStoreInitializer
	}
	st, err := initFn(ctx, s.cfg, logger)
	if err != nil {
		return nil, false, err
	}
	return st, true, nil
}

func (s *Server) resolveAuthProvider(logger *slog.Logger) (auth.Provider, error) {
	if s.authProvider != nil {
		logger.Info("initialized custom authentication provider", "provider", s.authProvider.Name())
		if ap, ok := s.authProvider.(auth.Provider); ok {
			return ap, nil
		}
		return auth.NewTokenVerifierProvider(s.authProvider.Name(), s.authProvider), nil
	}

	reg := s.authRegistry
	if reg == nil {
		var err error
		reg, err = auth.NewRegistryFromConfig(auth.Config{
			DefaultUserID:     s.cfg.DefaultUserID,
			FirebaseProjectID: s.cfg.FirebaseProjectID,
			AuthProvider:      s.cfg.AuthProvider,
			Admin:             s.cfg.Admin,
		})
		if err != nil {
			logger.Error("failed to initialize auth registry", "err", err)
			return nil, fmt.Errorf("initializing auth registry: %w", err)
		}
	}

	prov, err := reg.Default()
	if err != nil {
		logger.Error("multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode")
		return nil, errors.New("multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode")
	}

	switch prov.Name() {
	case auth.ProviderAdmin:
		logger.Info("running in local admin mode: token verification bypassed, default user id applied", "user_id", s.cfg.DefaultUserID)
	case auth.ProviderFirebase:
		logger.Info("initialized firebase token verifier", "project_id", s.cfg.FirebaseProjectID)
	default:
		logger.Info("initialized authentication provider", "provider", prov.Name())
	}

	return prov, nil
}

func (s *Server) resolveLLMRegistry(logger *slog.Logger) *llm.Registry {
	reg := s.llmRegistry
	if reg == nil {
		reg = llm.NewRegistry(
			llm.WithLogger(logger),
			llm.WithDebugPrompts(s.cfg.LLMDebugPrompts, s.cfg.LLMDebugDir),
		)
		llm.RegisterClients(reg, llm.ProviderConfig{
			GeminiAPIKey:    s.cfg.GeminiAPIKey,
			OpenAIAPIKey:    s.cfg.OpenAIAPIKey,
			AnthropicAPIKey: s.cfg.AnthropicAPIKey,
			CompatBaseURL:   s.cfg.OpenAICompatBaseURL,
			CompatAPIKey:    s.cfg.OpenAICompatAPIKey,
		}, llm.WithTimeout(s.cfg.LLMTimeout))
	}

	for _, pcfg := range s.llmProviderCfgs {
		opts := pcfg.opts
		if len(opts) == 0 {
			opts = []llm.Option{llm.WithTimeout(s.cfg.LLMTimeout)}
		}
		llm.RegisterClients(reg, pcfg.cfg, opts...)
	}

	for _, c := range s.llmClients {
		reg.RegisterClient(c.client, c.opts...)
	}

	for _, p := range s.llmProviders {
		reg.Register(p)
	}

	return reg
}

func buildRootHandler(apiRouter http.Handler, mcpHandler http.Handler) http.Handler {
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/", apiRouter)
	rootMux.Handle("GET /healthz", apiRouter)
	rootMux.Handle("GET /health", apiRouter)

	if mcpHandler != nil {
		rootMux.Handle("/mcp/", mcpHandler)
		rootMux.Handle("/mcp", mcpHandler)
	}

	return rootMux
}

func buildHTTPServer(addr string, handler http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}
