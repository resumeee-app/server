// Package server provides a configurable, dependency-injected HTTP REST and Model Context Protocol (MCP)
// server for Resumeee.
//
// External dependencies (such as domain.Store, domain.AuthProvider, and domain.LLMClient / domain.LLMProvider)
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

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-api/internal/config"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-core/auth"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/pdf"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core/services/worker"
	"github.com/resumeee-app/resumeee-core/validator"
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

// PDFTextExtractor abstracts extracting textual content from PDF binary streams.
type PDFTextExtractor = domain.PDFTextExtractor

type registeredLLMClient struct {
	client domain.LLMClient
	opts   []llm.Option
}

// Server orchestrates the Resumeee HTTP REST API, MCP server, background worker pool, and domain services.
type Server struct {
	cfg              *Config
	logger           *slog.Logger
	store            domain.Store
	authProvider     domain.AuthProvider
	llmRegistry      *llm.Registry
	llmClients       []registeredLLMClient
	llmProviders     []domain.LLMProvider
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

// Run initializes all injected and default domain services, starts the background worker pool,
// serves the HTTP API (and optional MCP routes), and performs a graceful shutdown when ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	if s.cfg == nil {
		s.cfg = DefaultConfig()
	}

	if s.store == nil {
		return errors.New("server: a configured domain.Store must be injected via WithStore")
	}

	logger := s.logger
	if logger == nil {
		logger = config.NewLogger(s.cfg.toInternalConfig(), os.Stdout)
	}

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

	if s.cfg.LLMDebugPrompts && strings.TrimSpace(s.cfg.LLMDebugDir) != "" {
		if err := os.MkdirAll(s.cfg.LLMDebugDir, 0750); err != nil {
			return fmt.Errorf("creating llm debug directory: %w", err)
		}
	}

	store := s.store
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

func (s *Server) resolveAuthProvider(logger *slog.Logger) (auth.Provider, error) {
	if s.authProvider != nil {
		logger.Info("initialized authentication provider", "provider", s.authProvider.Name())
		if ap, ok := s.authProvider.(auth.Provider); ok {
			return ap, nil
		}
		return auth.NewTokenVerifierProvider(s.authProvider.Name(), s.authProvider), nil
	}

	if s.cfg.Admin {
		logger.Info("running in local admin mode: token verification bypassed, default user id applied", "user_id", s.cfg.DefaultUserID)
		return auth.NewAdminAuthProvider(s.cfg.DefaultUserID), nil
	}

	logger.Error("multi-tenant mode requires an injected AuthProvider (WithAuthProvider / WithTokenVerifier) or explicit WithAdminMode(true)")
	return nil, errors.New("multi-tenant mode requires an injected AuthProvider (WithAuthProvider / WithTokenVerifier) or explicit WithAdminMode(true)")
}

func (s *Server) resolveLLMRegistry(logger *slog.Logger) *llm.Registry {
	reg := s.llmRegistry
	if reg == nil {
		reg = llm.NewRegistry(
			llm.WithLogger(logger),
			llm.WithDebugPrompts(s.cfg.LLMDebugPrompts, s.cfg.LLMDebugDir),
		)
	}

	for _, c := range s.llmClients {
		opts := c.opts
		if len(opts) == 0 {
			opts = []llm.Option{llm.WithTimeout(s.cfg.LLMTimeout)}
		}
		reg.RegisterClient(c.client, opts...)
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
