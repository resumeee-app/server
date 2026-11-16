// Package server provides a configurable, dependency-injected HTTP REST and Model Context Protocol (MCP)
// server for Resumeee.
//
// External dependencies (such as *store.Store, core.AuthProvider, and llm.Client / *llm.Registry)
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

	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/pdf"
	"github.com/resumeee-app/core/profile"
	"github.com/resumeee-app/core/provenance"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/core/worker"
	"github.com/resumeee-app/server/auth"
	"github.com/resumeee-app/server/internal/api"
	"github.com/resumeee-app/server/internal/config"
	resumeeeMCP "github.com/resumeee-app/server/internal/mcp"
)

// httpMaxHeaderBytes caps incoming HTTP request header bytes at 1 MiB.
const httpMaxHeaderBytes = 1 << 20

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
	MaxConcurrentJobsPerTenant int
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

// DefaultConfig returns a Config initialized with standard Resumeee server defaults.
func DefaultConfig() *Config {
	return &Config{
		Port:                       config.DefaultPort,
		DefaultUserID:              config.DefaultUserID,
		Admin:                      config.DefaultAdmin,
		LLMDailyRequestLimit:       config.DefaultLLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       config.DefaultLLMMonthlyTokenLimit,
		WorkerConcurrency:          config.DefaultWorkerConcurrency,
		MaxConcurrentJobsPerTenant: config.DefaultMaxConcurrentJobsPerTenant,
		Environment:                config.DefaultEnvironment,
		LogLevel:                   slog.LevelInfo,
		LLMDebugPrompts:            false,
		LLMDebugDir:                config.DefaultLLMDebugDir,
		RESTTimeout:                config.DefaultRESTTimeout,
		LLMTimeout:                 config.DefaultLLMTimeout,
		PDFParseTimeout:            config.DefaultPDFParseTimeout,
		RenderPreviewTimeout:       config.DefaultRenderPreviewTimeout,
		HTTPReadHeaderTimeout:      config.DefaultHTTPReadHeaderTimeout,
		HTTPReadTimeout:            config.DefaultHTTPReadTimeout,
		HTTPMinWriteTimeout:        config.DefaultHTTPMinWriteTimeout,
		HTTPWriteTimeoutBuffer:     config.DefaultHTTPWriteTimeoutBuffer,
		HTTPIdleTimeout:            config.DefaultHTTPIdleTimeout,
		HTTPShutdownTimeout:        config.DefaultHTTPShutdownTimeout,
		MCPSSEKeepAliveInterval:    config.DefaultMCPSSEKeepAliveInterval,
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
		MaxConcurrentJobsPerTenant: c.MaxConcurrentJobsPerTenant,
		Environment:                c.Environment,
		LogLevel:                   c.LogLevel,
		LLMDebugPrompts:            c.LLMDebugPrompts,
		LLMDebugDir:                c.LLMDebugDir,
		RESTTimeout:                c.RESTTimeout,
		LLMTimeout:                 c.LLMTimeout,
		PDFParseTimeout:            c.PDFParseTimeout,
		RenderPreviewTimeout:       c.RenderPreviewTimeout,
		HTTPReadHeaderTimeout:      c.HTTPReadHeaderTimeout,
		HTTPReadTimeout:            c.HTTPReadTimeout,
		HTTPMinWriteTimeout:        c.HTTPMinWriteTimeout,
		HTTPWriteTimeoutBuffer:     c.HTTPWriteTimeoutBuffer,
		HTTPIdleTimeout:            c.HTTPIdleTimeout,
		HTTPShutdownTimeout:        c.HTTPShutdownTimeout,
		MCPSSEKeepAliveInterval:    c.MCPSSEKeepAliveInterval,
	}
}

// HTTPTimeouts groups socket-level read, write, and idle timeouts for BuildHTTPServer.
type HTTPTimeouts struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Server orchestrates the Resumeee HTTP REST API, MCP server, background worker pool, and domain services.
type Server struct {
	cfg              *Config
	logger           *slog.Logger
	store            *store.Store
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
		return errors.New("server: a configured *store.Store must be injected via WithStore")
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

// initServices instantiates domain services and starts the background worker pool.
func (s *Server) initServices(ctx context.Context, logger *slog.Logger) (*serverDeps, error) {
	authProvider, err := s.resolveAuthProvider(logger)
	if err != nil {
		return nil, err
	}

	llmRegistry := s.resolveLLMRegistry(logger)
	templateRenderer, provValidator, pdfExtractor, promptCompiler := s.resolveCoreAdapters(llmRegistry)
	llmService := llm.NewService(llmRegistry)
	resumeExtractor := s.resolveResumeExtractor(llmRegistry)

	workerPool := s.startWorkerPool(ctx, logger, resumeExtractor, llmService, provValidator, templateRenderer)
	branchStore := s.store.Branches()
	profileSvc := profile.New(s.store.Profiles(), s.store.Career(), branchStore)
	branchSvc := branch.New(
		branch.WithBranches(branchStore),
		branch.WithJobs(s.store.Jobs()),
		branch.WithProfiles(s.store.Profiles()),
		branch.WithPresets(s.store.Presets()),
		branch.WithTemplates(s.store.Templates()),
		branch.WithQuotas(s.store.Quotas()),
		branch.WithJobNotifier(workerPool),
		branch.WithValidator(provValidator),
		branch.WithRenderer(templateRenderer),
		branch.WithGenerator(llmService),
		branch.WithPromptCompiler(promptCompiler),
		branch.WithDailyRequestLimit(s.cfg.LLMDailyRequestLimit),
		branch.WithMonthlyTokenLimit(s.cfg.LLMMonthlyTokenLimit),
		branch.WithLogger(logger),
	)

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

// resolveCoreAdapters returns injected adapters or constructs standard defaults backed by llmRegistry.
func (s *Server) resolveCoreAdapters(llmRegistry *llm.Registry) (TemplateRenderer, ProvenanceValidator, PDFTextExtractor, branch.PromptCompiler) {
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
		promptCompiler = llm.NewPromptCompiler(llm.WithPromptRegistry(llmRegistry))
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
	workerPool := worker.New(
		worker.WithConcurrency(s.cfg.WorkerConcurrency),
		worker.WithMaxConcurrentPerTenant(s.cfg.MaxConcurrentJobsPerTenant),
		worker.WithJobs(s.store.Jobs()),
		worker.WithBranches(s.store.Branches()),
		worker.WithProfiles(s.store.Profiles()),
		worker.WithPresets(s.store.Presets()),
		worker.WithTemplates(s.store.Templates()),
		worker.WithQuotas(s.store.Quotas()),
		worker.WithResumeExtractor(resumeExtractor),
		worker.WithLLMTimeout(s.cfg.LLMTimeout),
		worker.WithGenerator(llmService),
		worker.WithValidator(provValidator),
		worker.WithRenderer(templateRenderer),
		worker.WithLogger(logger),
	)
	if err := workerPool.RecoverStaleJobs(ctx, "server restarted while generation job was in flight"); err != nil {
		logger.Warn("failed to recover stale jobs", "err", err)
	}
	workerPool.Start(ctx)
	return workerPool
}

// buildHTTPHandler mounts the REST API router and optional MCP HTTP handler onto a single root handler.
func (s *Server) buildHTTPHandler(deps *serverDeps, logger *slog.Logger) http.Handler {
	isLocalAdmin := deps.authProvider != nil && deps.authProvider.Name() == auth.ProviderAdmin

	routerCfg := api.NewRouterConfigFromStore(s.store)
	routerCfg.JobNotifier = deps.workerPool
	routerCfg.BranchService = deps.branchService
	routerCfg.ProfileService = deps.profileService
	routerCfg.PDFExtractor = deps.pdfExtractor
	routerCfg.ResumeExtractor = deps.resumeExtractor
	routerCfg.Renderer = deps.templateRenderer
	routerCfg.Validator = deps.provValidator
	routerCfg.PromptCompiler = deps.promptCompiler
	routerCfg.ModelEnumerator = deps.llmRegistry
	routerCfg.DefaultUserID = s.cfg.DefaultUserID
	routerCfg.Admin = isLocalAdmin
	routerCfg.AuthProvider = deps.authProvider
	routerCfg.DailyRequestLimit = s.cfg.LLMDailyRequestLimit
	routerCfg.MonthlyTokenLimit = s.cfg.LLMMonthlyTokenLimit
	routerCfg.RESTTimeout = s.cfg.RESTTimeout
	routerCfg.LLMTimeout = s.cfg.LLMTimeout
	routerCfg.PDFParseTimeout = s.cfg.PDFParseTimeout
	routerCfg.RenderPreviewTimeout = s.cfg.RenderPreviewTimeout
	routerCfg.HTTPWriteTimeoutBuffer = s.cfg.HTTPWriteTimeoutBuffer
	routerCfg.Logger = logger

	apiRouter := api.NewRouter(routerCfg)

	if !s.mcpEnabled {
		return BuildRootHandler(apiRouter, nil)
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
		DefaultUserID:         s.cfg.DefaultUserID,
		Admin:                 isLocalAdmin,
		AuthProvider:          deps.authProvider,
		Logger:                logger,
		RESTTimeout:           s.cfg.RESTTimeout,
		TailoringTimeout:      s.cfg.LLMTimeout,
		SSEKeepAliveInterval:  s.cfg.MCPSSEKeepAliveInterval,
		MaxConnectionsPerUser: config.DefaultMCPMaxConnsPerUser,
		MaxTotalConnections:   config.DefaultMCPMaxTotalConns,
	}
	mcpSrv := resumeeeMCP.NewServer(mcpCfg)
	return BuildRootHandler(apiRouter, resumeeeMCP.NewHTTPHandler(mcpSrv, mcpCfg))
}

// serveHTTP runs the HTTP listener until ctx is canceled and performs graceful connection draining.
func (s *Server) serveHTTP(ctx context.Context, rootHandler http.Handler, logger *slog.Logger) error {
	writeTimeout := max(s.cfg.RESTTimeout+s.cfg.HTTPWriteTimeoutBuffer, s.cfg.HTTPMinWriteTimeout)
	httpServer := BuildHTTPServer(fmt.Sprintf(":%d", s.cfg.Port), rootHandler, HTTPTimeouts{
		ReadHeaderTimeout: s.cfg.HTTPReadHeaderTimeout,
		ReadTimeout:       s.cfg.HTTPReadTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       s.cfg.HTTPIdleTimeout,
	})

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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.cfg.HTTPShutdownTimeout)
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

	const msg = "multi-tenant mode requires an injected AuthProvider (WithRequestAuthProvider / WithAuthProvider / WithTokenVerifier) or explicit WithAdminMode(true)"
	logger.Error(msg)
	return nil, errors.New(msg)
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
func BuildRootHandler(apiRouter, mcpHandler http.Handler) http.Handler {
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

// BuildHTTPServer configures an *http.Server with defense-in-depth read/write/idle timeouts and header bounds.
func BuildHTTPServer(addr string, handler http.Handler, timeouts HTTPTimeouts) *http.Server {
	if timeouts.ReadHeaderTimeout <= 0 {
		timeouts.ReadHeaderTimeout = config.DefaultHTTPReadHeaderTimeout
	}
	if timeouts.ReadTimeout <= 0 {
		timeouts.ReadTimeout = config.DefaultHTTPReadTimeout
	}
	if timeouts.WriteTimeout <= 0 {
		timeouts.WriteTimeout = config.DefaultHTTPMinWriteTimeout
	}
	if timeouts.IdleTimeout <= 0 {
		timeouts.IdleTimeout = config.DefaultHTTPIdleTimeout
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: timeouts.ReadHeaderTimeout,
		ReadTimeout:       timeouts.ReadTimeout,
		WriteTimeout:      timeouts.WriteTimeout,
		IdleTimeout:       timeouts.IdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}
