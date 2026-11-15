// Package server provides a configurable, dependency-injected HTTP REST and Model Context Protocol (MCP)
// server for Resumeee.
//
// External dependencies (such as core.Store, core.AuthProvider, and llm.Client / *llm.Registry)
// are configured in their respective packages and injected into Server using functional With<Option> constructors.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-api/internal/config"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/pdf"
	"github.com/resumeee-app/resumeee-core/profile"
	"github.com/resumeee-app/resumeee-core/provenance"
	"github.com/resumeee-app/resumeee-core/renderer"
	"github.com/resumeee-app/resumeee-core/worker"
)

// HTTP server timeout, header limit, and MCP connection bounding constants.
const (
	// httpReadHeaderTimeout bounds time spent reading request headers to mitigate slowloris attacks.
	httpReadHeaderTimeout = 5 * time.Second

	// httpReadTimeout bounds the full HTTP request read duration.
	httpReadTimeout = 60 * time.Second

	// httpMinWriteTimeout sets the floor for HTTP response write timeouts.
	httpMinWriteTimeout = 30 * time.Second

	// httpWriteTimeoutBuffer adds headroom above synchronous RESTTimeout for socket flushing.
	httpWriteTimeoutBuffer = 10 * time.Second

	// httpIdleTimeout caps keep-alive idle connection lifetimes.
	httpIdleTimeout = 120 * time.Second

	// httpMaxHeaderBytes caps incoming HTTP request header bytes at 1 MiB.
	httpMaxHeaderBytes = 1 << 20

	// httpShutdownTimeout bounds graceful HTTP server connection draining during shutdown.
	httpShutdownTimeout = 10 * time.Second

	// defaultMCPMaxConnsPerUser caps concurrent active MCP SSE streams per tenant.
	defaultMCPMaxConnsPerUser = 5

	// defaultMCPMaxTotalConns caps total concurrent active MCP SSE streams across all tenants.
	defaultMCPMaxTotalConns = 100
)

// Compile-time checks verify default infrastructure implementations satisfy server contracts.
var (
	_ TemplateRenderer    = (*renderer.Renderer)(nil)
	_ ProvenanceValidator = (*provenance.Validator)(nil)
	_ PDFTextExtractor    = (*pdf.Extractor)(nil)
)

// Config encapsulates HTTP server, worker pool, quota, and logging settings owned by the API server.
// Adapter-specific configurations (database drivers/paths, auth provider credentials, LLM vendor keys)
// belong to their respective packages and are injected as constructed dependencies.
type Config struct {
	Port                       int
	DefaultUserID              string
	Admin                      bool
	LLMDailyRequestLimit       int
	LLMMonthlyTokenLimit       int
	WorkerConcurrency          int
	WorkerQueueSize            int
	MaxConcurrentJobsPerTenant int
	Environment                string
	LogLevel                   slog.Level
	LLMDebugPrompts            bool
	LLMDebugDir                string
	RESTTimeout                time.Duration
	LLMTimeout                 time.Duration
}

// DefaultConfig returns a Config initialized with standard Resumeee server defaults.
func DefaultConfig() *Config {
	return &Config{
		Port:                       config.DefaultPort,
		DefaultUserID:              config.DefaultUserID,
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

// toInternalConfig maps public server.Config settings into internal/config.Config for logger initialization.
func (c *Config) toInternalConfig() *config.Config {
	if c == nil {
		c = DefaultConfig()
	}
	return &config.Config{
		Port:                       c.Port,
		DefaultUserID:              c.DefaultUserID,
		Admin:                      c.Admin,
		LLMDailyRequestLimit:       c.LLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       c.LLMMonthlyTokenLimit,
		WorkerConcurrency:          c.WorkerConcurrency,
		WorkerQueueSize:            c.WorkerQueueSize,
		MaxConcurrentJobsPerTenant: c.MaxConcurrentJobsPerTenant,
		Environment:                c.Environment,
		LogLevel:                   c.LogLevel,
		LLMDebugPrompts:            c.LLMDebugPrompts,
		LLMDebugDir:                c.LLMDebugDir,
		RESTTimeout:                c.RESTTimeout,
		LLMTimeout:                 c.LLMTimeout,
	}
}

// Server orchestrates the Resumeee HTTP REST API, MCP server, background worker pool, and domain services.
type Server struct {
	cfg              *Config
	logger           *slog.Logger
	store            core.Store
	authProvider     auth.Provider
	llmRegistry      *llm.Registry
	llmClients       []llm.Client
	templateRenderer TemplateRenderer
	provValidator    ProvenanceValidator
	pdfExtractor     PDFTextExtractor
	resumeExtractor  worker.ResumeExtractor
	promptCompiler   branch.PromptCompiler
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

// serverDeps collects resolved services and adapters wired by Server.Run.
type serverDeps struct {
	authProvider     auth.Provider
	llmRegistry      *llm.Registry
	llmService       *llm.Service
	templateRenderer TemplateRenderer
	provValidator    ProvenanceValidator
	pdfExtractor     PDFTextExtractor
	resumeExtractor  worker.ResumeExtractor
	promptCompiler   branch.PromptCompiler
	workerPool       *worker.Pool
	profileService   *profile.Service
	branchService    *branch.Service
}

// Run initializes all injected and default domain services, starts the background worker pool,
// serves the HTTP API (and optional MCP routes), and performs a graceful shutdown when ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg == nil {
		s.cfg = DefaultConfig()
	}
	if s.store == nil {
		return errors.New("server: a configured core.Store must be injected via WithStore")
	}

	logger := s.resolveLogger()
	s.logStartup(logger)

	if err := s.ensureLLMDebugDir(); err != nil {
		return err
	}

	deps, err := s.initServices(ctx, logger)
	if err != nil {
		return err
	}
	defer deps.workerPool.Stop()

	rootHandler := s.buildHTTPHandler(deps, logger)
	return s.serveHTTP(ctx, rootHandler, logger)
}

// resolveLogger returns the configured slog.Logger or initializes a default stdout logger.
func (s *Server) resolveLogger() *slog.Logger {
	if s.logger != nil {
		return s.logger
	}
	return config.NewLogger(s.cfg.toInternalConfig(), os.Stdout)
}

// logStartup writes baseline configuration telemetry at server boot.
func (s *Server) logStartup(logger *slog.Logger) {
	logger.Info("starting resumeee server",
		"port", s.cfg.Port,
		"env", s.cfg.Environment,
		"default_user_id", s.cfg.DefaultUserID,
		"worker_concurrency", s.cfg.WorkerConcurrency,
		"worker_queue_size", s.cfg.WorkerQueueSize,
		"worker_max_concurrent_per_tenant", s.cfg.MaxConcurrentJobsPerTenant,
		"llm_debug_prompts", s.cfg.LLMDebugPrompts,
		"llm_debug_dir", s.cfg.LLMDebugDir,
	)
}

// ensureLLMDebugDir creates the configured LLM prompt debug directory when debug logging is active.
func (s *Server) ensureLLMDebugDir() error {
	if !s.cfg.LLMDebugPrompts || strings.TrimSpace(s.cfg.LLMDebugDir) == "" {
		return nil
	}
	if err := os.MkdirAll(s.cfg.LLMDebugDir, 0750); err != nil {
		return fmt.Errorf("creating llm debug directory: %w", err)
	}
	return nil
}

// initServices recovers stale state, instantiates domain services, and starts the background worker pool.
func (s *Server) initServices(ctx context.Context, logger *slog.Logger) (*serverDeps, error) {
	branchStore := s.store.Branches()
	if err := branchStore.FailInterruptedVersions(ctx, "server restarted while generation job was in flight"); err != nil {
		logger.Warn("failed to clean up interrupted versions on boot", "err", err)
	} else {
		logger.Info("cleaned up interrupted generation versions")
	}

	authProvider, err := s.resolveAuthProvider(logger)
	if err != nil {
		return nil, err
	}

	templateRenderer, provValidator, pdfExtractor, promptCompiler := s.resolveCoreAdapters()
	llmRegistry := s.resolveLLMRegistry(logger)
	llmService := llm.NewService(llmRegistry)
	resumeExtractor := s.resolveResumeExtractor(llmRegistry)

	workerPool := s.startWorkerPool(ctx, logger, resumeExtractor, llmService, provValidator, templateRenderer)
	profileSvc := profile.New(s.store.Profiles(), s.store.Career(), branchStore)
	branchSvc := branch.New(branch.Config{
		Branches:          branchStore,
		Profiles:          s.store.Profiles(),
		Presets:           s.store.Presets(),
		Templates:         s.store.Templates(),
		Quotas:            s.store.Quotas(),
		JobEnqueuer:       workerPool,
		Validator:         provValidator,
		Renderer:          templateRenderer,
		Generator:         llmService,
		PromptCompiler:    promptCompiler,
		DailyRequestLimit: s.cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: s.cfg.LLMMonthlyTokenLimit,
		Logger:            logger,
	})

	return &serverDeps{
		authProvider:     authProvider,
		llmRegistry:      llmRegistry,
		llmService:       llmService,
		templateRenderer: templateRenderer,
		provValidator:    provValidator,
		pdfExtractor:     pdfExtractor,
		resumeExtractor:  resumeExtractor,
		promptCompiler:   promptCompiler,
		workerPool:       workerPool,
		profileService:   profileSvc,
		branchService:    branchSvc,
	}, nil
}

// resolveCoreAdapters returns injected adapters or constructs standard defaults.
func (s *Server) resolveCoreAdapters() (TemplateRenderer, ProvenanceValidator, PDFTextExtractor, branch.PromptCompiler) {
	templateRenderer := s.templateRenderer
	if templateRenderer == nil {
		templateRenderer = renderer.New()
	}
	provValidator := s.provValidator
	if provValidator == nil {
		provValidator = provenance.New()
	}
	pdfExtractor := s.pdfExtractor
	if pdfExtractor == nil {
		pdfExtractor = pdf.NewExtractor()
	}
	promptCompiler := s.promptCompiler
	if promptCompiler == nil {
		promptCompiler = llm.NewPromptCompiler()
	}
	return templateRenderer, provValidator, pdfExtractor, promptCompiler
}

// resolveResumeExtractor returns the injected ResumeExtractor or constructs a registry-backed extractor.
func (s *Server) resolveResumeExtractor(llmRegistry *llm.Registry) worker.ResumeExtractor {
	if s.resumeExtractor != nil {
		return s.resumeExtractor
	}
	return llm.NewRegistryExtractor(llmRegistry)
}

// startWorkerPool configures, recovers stale jobs on, and starts the asynchronous background worker pool.
func (s *Server) startWorkerPool(
	ctx context.Context,
	logger *slog.Logger,
	resumeExtractor worker.ResumeExtractor,
	llmService *llm.Service,
	provValidator ProvenanceValidator,
	templateRenderer TemplateRenderer,
) *worker.Pool {
	workerPool := worker.New(worker.Config{
		Concurrency:            s.cfg.WorkerConcurrency,
		QueueSize:              s.cfg.WorkerQueueSize,
		MaxConcurrentPerTenant: s.cfg.MaxConcurrentJobsPerTenant,
		Branches:               s.store.Branches(),
		Profiles:               s.store.Profiles(),
		Presets:                s.store.Presets(),
		Templates:              s.store.Templates(),
		PDFJobs:                s.store.PDFJobs(),
		Quotas:                 s.store.Quotas(),
		ResumeExtractor:        resumeExtractor,
		LLMTimeout:             s.cfg.LLMTimeout,
		Generator:              llmService,
		Validator:              provValidator,
		Renderer:               templateRenderer,
		Logger:                 logger,
	})
	if err := workerPool.RecoverStaleJobs(ctx, "server restarted during job processing"); err != nil {
		logger.Warn("failed to recover stale jobs", "err", err)
	}
	workerPool.Start(ctx)
	return workerPool
}

// buildHTTPHandler mounts the REST API router and optional MCP HTTP handler onto a single root handler.
func (s *Server) buildHTTPHandler(deps *serverDeps, logger *slog.Logger) http.Handler {
	apiRouter := api.NewRouter(api.RouterConfig{
		ProfileStore:      s.store.Profiles(),
		CareerStore:       s.store.Career(),
		PresetStore:       s.store.Presets(),
		TemplateStore:     s.store.Templates(),
		BranchStore:       s.store.Branches(),
		PDFJobStore:       s.store.PDFJobs(),
		QuotaStore:        s.store.Quotas(),
		BranchService:     deps.branchService,
		ProfileService:    deps.profileService,
		PDFExtractor:      deps.pdfExtractor,
		ResumeExtractor:   deps.resumeExtractor,
		Renderer:          deps.templateRenderer,
		Validator:         deps.provValidator,
		JobEnqueuer:       deps.workerPool,
		PDFJobEnqueuer:    deps.workerPool,
		ModelEnumerator:   deps.llmRegistry,
		DefaultUserID:     s.cfg.DefaultUserID,
		Admin:             s.cfg.Admin,
		AuthProvider:      deps.authProvider,
		TokenVerifier:     deps.authProvider,
		UserStore:         s.store.Users(),
		Store:             s.store,
		DailyRequestLimit: s.cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: s.cfg.LLMMonthlyTokenLimit,
		RESTTimeout:       s.cfg.RESTTimeout,
		LLMTimeout:        s.cfg.LLMTimeout,
		Logger:            logger,
	})

	if !s.mcpEnabled {
		return BuildRootHandler(apiRouter)
	}

	mcpCfg := resumeeeMCP.ServerConfig{
		ProfileService:        deps.profileService,
		BranchService:         deps.branchService,
		TemplateRenderer:      deps.templateRenderer,
		ProfileStore:          s.store.Profiles(),
		CareerStore:           s.store.Career(),
		BranchStore:           s.store.Branches(),
		TemplateStore:         s.store.Templates(),
		PresetStore:           s.store.Presets(),
		UserStore:             s.store.Users(),
		ResumeExtractor:       deps.resumeExtractor,
		JobEnqueuer:           deps.workerPool,
		DefaultUserID:         s.cfg.DefaultUserID,
		Admin:                 s.cfg.Admin,
		AuthProvider:          deps.authProvider,
		TokenVerifier:         deps.authProvider,
		Logger:                logger,
		RESTTimeout:           s.cfg.RESTTimeout,
		TailoringTimeout:      s.cfg.LLMTimeout,
		MaxConnectionsPerUser: defaultMCPMaxConnsPerUser,
		MaxTotalConnections:   defaultMCPMaxTotalConns,
	}
	mcpSrv := resumeeeMCP.NewServer(mcpCfg)
	return BuildRootHandler(apiRouter, resumeeeMCP.NewHTTPHandler(mcpSrv, mcpCfg))
}

// serveHTTP runs the HTTP listener until ctx is canceled and performs graceful connection draining.
func (s *Server) serveHTTP(ctx context.Context, rootHandler http.Handler, logger *slog.Logger) error {
	writeTimeout := max(s.cfg.RESTTimeout+httpWriteTimeoutBuffer, httpMinWriteTimeout)
	httpServer := BuildHTTPServer(fmt.Sprintf(":%d", s.cfg.Port), rootHandler, writeTimeout)

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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server graceful shutdown: %w", err)
	}

	logger.Info("server shutdown complete")
	return nil
}

// resolveAuthProvider returns an injected auth.Provider, falls back to local admin mode when enabled,
// or fails closed in multi-tenant mode when neither is configured.
func (s *Server) resolveAuthProvider(logger *slog.Logger) (auth.Provider, error) {
	if s.authProvider != nil {
		logger.Info("initialized authentication provider", "provider", s.authProvider.Name())
		return s.authProvider, nil
	}

	if s.cfg.Admin {
		logger.Info("running in local admin mode: token verification bypassed, default user id applied", "user_id", s.cfg.DefaultUserID)
		return auth.NewAdminProvider(s.cfg.DefaultUserID), nil
	}

	logger.Error("multi-tenant mode requires an injected AuthProvider (WithAuthProvider / WithTokenVerifier) or explicit WithAdminMode(true)")
	return nil, errors.New("multi-tenant mode requires an injected AuthProvider (WithAuthProvider / WithTokenVerifier) or explicit WithAdminMode(true)")
}

// resolveLLMRegistry returns the configured llm.Registry (or constructs a new one)
// and registers all explicitly injected llm.Client backends.
func (s *Server) resolveLLMRegistry(logger *slog.Logger) *llm.Registry {
	reg := s.llmRegistry
	if reg == nil {
		reg = llm.NewRegistry(
			llm.WithLogger(logger),
		)
	}

	for _, c := range s.llmClients {
		reg.Register(c)
	}

	return reg
}

// BuildRootHandler unifies the REST API router and optional MCP handler into a single root http.Handler.
func BuildRootHandler(apiRouter http.Handler, mcpHandler ...http.Handler) http.Handler {
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/v1/", apiRouter)
	rootMux.Handle("GET /healthz", apiRouter)
	rootMux.Handle("GET /health", apiRouter)

	if len(mcpHandler) > 0 && mcpHandler[0] != nil {
		rootMux.Handle("/mcp/", mcpHandler[0])
		rootMux.Handle("/mcp", mcpHandler[0])
	}

	return rootMux
}

// BuildHTTPServer configures an *http.Server with defense-in-depth read/write/idle timeouts and header bounds.
func BuildHTTPServer(addr string, handler http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}
