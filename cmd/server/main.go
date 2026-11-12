// Package main is the entry point for the Resumeee server daemon.
//
// It loads configuration, initializes SQLite persistence, sets up LLM providers,
// spins up the background worker pool and MCP server, and starts the REST HTTP server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/api"
	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core/llm"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-api/internal/pdf"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	repository "github.com/resumeee-app/resumeee-core/domain"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
	"github.com/resumeee-app/resumeee-core/validator"
	"github.com/resumeee-app/resumeee-core/services/worker"
)

// StoreInitializer initializes a storage backend and provides a repository.Store implementation.
type StoreInitializer func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (repository.Store, error)

// defaultSQLiteInitializer establishes the default SQLite persistence backend, executing migrations and default seeding.
//
// Parameters:
//   - ctx: Server startup context.
//   - cfg: Server runtime configuration.
//   - logger: System logger for recording initialization milestones.
//
// Returns:
//   - repository.Store: Initialized SQLite store bundle.
//   - error: Non-nil if database connection, schema migration, or default seeding fails.
func defaultSQLiteInitializer(ctx context.Context, cfg *config.Config, logger *slog.Logger) (repository.Store, error) {
	store, err := storage.Open(ctx, storage.Config{
		Driver:        storage.DriverSQLite,
		DSN:           cfg.DBPath,
		AutoMigrate:   true,
		DefaultUserID: cfg.DefaultUserID,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("database seeded with default profile, built-in templates, and presets")
	return store, nil
}

// main initializes runtime context and executes the primary resumeee server process.
func main() {
	if err := run(context.Background(), os.Args[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

// run orchestrates database initialization, migrations, seeding, background worker startup, and HTTP serving using default SQLite persistence.
//
// Parameters:
//   - parent: The parent context governing server lifecycle and cancellation signals.
//   - args: Command line arguments passed to flag parsing.
//
// Returns:
//   - error: Non-nil if configuration loading, initialization, or serving fails.
func run(parent context.Context, args ...string) error {
	return runWithStore(parent, defaultSQLiteInitializer, args...)
}

// runWithStore orchestrates configuration loading, store initialization, background workers, and HTTP serving.
//
// Parameters:
//   - parent: The parent context governing server lifecycle and cancellation signals.
//   - storeInit: Storage backend initializer injecting repository implementations.
//   - args: Command line arguments passed to flag parsing.
//
// Returns:
//   - error: Non-nil if configuration loading, initialization, or serving fails.
func runWithStore(parent context.Context, storeInit StoreInitializer, args ...string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if err := cfg.ParseFlags(args); err != nil {
		return fmt.Errorf("parsing command line flags: %w", err)
	}

	logger := config.NewLogger(cfg, os.Stdout)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting resumeee server",
		"port", cfg.Port,
		"env", cfg.Environment,
		"db_path", cfg.DBPath,
		"default_user_id", cfg.DefaultUserID,
		"worker_concurrency", cfg.WorkerConcurrency,
		"worker_queue_size", cfg.WorkerQueueSize,
		"worker_max_concurrent_per_tenant", cfg.MaxConcurrentJobsPerTenant,
		"llm_debug_prompts", cfg.LLMDebugPrompts,
		"llm_debug_dir", cfg.LLMDebugDir,
	)

	// Ensure LLM debug directory exists if prompt debugging is enabled
	if cfg.LLMDebugPrompts && strings.TrimSpace(cfg.LLMDebugDir) != "" {
		if err := os.MkdirAll(cfg.LLMDebugDir, 0750); err != nil {
			return fmt.Errorf("creating llm debug directory: %w", err)
		}
	}

	store, err := storeInit(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("failed to close store", "err", err)
		}
	}()

	branchRepo := store.Branch()
	// 1. Recover in-flight versions left interrupted by process stop
	if err := branchRepo.FailInterruptedVersions(ctx, "server restarted while generation job was in flight"); err != nil {
		logger.Warn("failed to clean up interrupted versions on boot", "err", err)
	} else {
		logger.Info("cleaned up interrupted generation versions")
	}

	// 2. Initialize repositories and domain services
	profileRepo := store.Profile()
	presetRepo := store.Preset()
	templateRepo := store.Template()
	pdfJobRepo := store.PDFJob()
	quotaRepo := store.Quota()
	profileSvc := profile.NewService(profileRepo, branchRepo)
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()

	// 3. Initialize authentication registry and resolve active auth provider
	authRegistry, err := auth.NewRegistryFromConfig(auth.Config{
		DefaultUserID:     cfg.DefaultUserID,
		FirebaseProjectID: cfg.FirebaseProjectID,
		AuthProvider:      cfg.AuthProvider,
		Admin:             cfg.Admin,
	})
	if err != nil {
		logger.Error("failed to initialize auth registry", "err", err)
		return fmt.Errorf("initializing auth registry: %w", err)
	}

	authProvider, err := authRegistry.Default()
	if err != nil {
		logger.Error("multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode")
		return errors.New("multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode")
	}

	if authProvider.Name() == auth.ProviderAdmin {
		logger.Info("running in local admin mode: token verification bypassed, default user id applied", "user_id", cfg.DefaultUserID)
	} else if authProvider.Name() == auth.ProviderFirebase {
		logger.Info("initialized firebase token verifier", "project_id", cfg.FirebaseProjectID)
	} else {
		logger.Info("initialized authentication provider", "provider", authProvider.Name())
	}

	// 4. Initialize LLM registry and service adapter
	llmRegistry := llm.NewRegistry(
		llm.WithLogger(logger),
		llm.WithDebugPrompts(cfg.LLMDebugPrompts, cfg.LLMDebugDir),
	)
	llm.RegisterClients(llmRegistry, llm.ProviderConfig{
		GeminiAPIKey:    cfg.GeminiAPIKey,
		OpenAIAPIKey:    cfg.OpenAIAPIKey,
		AnthropicAPIKey: cfg.AnthropicAPIKey,
		CompatBaseURL:   cfg.OpenAICompatBaseURL,
		CompatAPIKey:    cfg.OpenAICompatAPIKey,
	}, llm.WithTimeout(cfg.LLMTimeout))
	llmAdapter := llm.NewServiceAdapter(llmRegistry)
	resumeExtractor := llm.NewDefaultResumeExtractor(llmRegistry)

	// 5. Initialize and start asynchronous generation worker pool
	workerPool := worker.NewPool(worker.PoolConfig{
		Concurrency:            cfg.WorkerConcurrency,
		QueueSize:              cfg.WorkerQueueSize,
		MaxConcurrentPerTenant: cfg.MaxConcurrentJobsPerTenant,
		BranchRepo:             branchRepo,
		ProfileRepo:     profileRepo,
		PresetRepo:      presetRepo,
		TemplateRepo:    templateRepo,
		PDFJobRepo:      pdfJobRepo,
		QuotaRepo:       quotaRepo,
		ResumeExtractor: resumeExtractor,
		LLMTimeout:      cfg.LLMTimeout,
		LLMService:      llmAdapter,
		Validator:       provValidator,
		Renderer:        templateRenderer,
		Logger:          logger,
	})
	if err := workerPool.RecoverStaleJobs(ctx, "server restarted during job processing"); err != nil {
		logger.Warn("failed to recover stale jobs", "err", err)
	}
	workerPool.Start(ctx)
	defer workerPool.Stop()

	// 6. Initialize Branch Service
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
		PromptCompiler:    llm.NewPromptCompiler(),
		DailyRequestLimit: cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: cfg.LLMMonthlyTokenLimit,
		Logger:            logger,
	})

	// 7. Build API router with /api/v1 routes and middlewares
	apiRouter := api.NewRouter(api.RouterConfig{
		ProfileRepo:       profileRepo,
		PresetRepo:        presetRepo,
		TemplateRepo:      templateRepo,
		BranchRepo:        branchRepo,
		PDFJobRepo:        pdfJobRepo,
		QuotaRepo:         quotaRepo,
		BranchService:     branchSvc,
		ProfileService:    profileSvc,
		PDFExtractor:      pdf.NewExtractor(),
		ResumeExtractor:   resumeExtractor,
		Renderer:          templateRenderer,
		Validator:         provValidator,
		JobEnqueuer:       workerPool,
		PDFJobEnqueuer:    workerPool,
		ModelEnumerator:   llmRegistry,
		DefaultUserID:     cfg.DefaultUserID,
		Admin:             cfg.Admin,
		AuthProvider:      authProvider,
		TokenVerifier:     authProvider,
		FirebaseProjectID: cfg.FirebaseProjectID,
		UserRepo:          store.User(),
		Store:             store,
		DailyRequestLimit: cfg.LLMDailyRequestLimit,
		MonthlyTokenLimit: cfg.LLMMonthlyTokenLimit,
		RESTTimeout:       cfg.RESTTimeout,
		LLMTimeout:        cfg.LLMTimeout,
		Logger:            logger,
	})

	// 8. Build MCP server and HTTP handler
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
		DefaultUserID:         cfg.DefaultUserID,
		Admin:                 cfg.Admin,
		AuthProvider:          authProvider,
		TokenVerifier:         authProvider,
		Logger:                logger,
		RESTTimeout:           cfg.RESTTimeout,
		TailoringTimeout:      10 * time.Minute,
		MaxConnectionsPerUser: 5,
		MaxTotalConnections:   100,
	}
	mcpServer := resumeeeMCP.NewServer(mcpCfg)
	mcpHandler := resumeeeMCP.NewHTTPHandler(mcpServer, mcpCfg)

	// 9. Unify API and MCP routes under single root router
	rootHandler := buildRootHandler(apiRouter, mcpHandler)

	writeTimeout := cfg.RESTTimeout + 10*time.Second
	if writeTimeout < 30*time.Second {
		writeTimeout = 30 * time.Second
	}
	server := buildServer(fmt.Sprintf(":%d", cfg.Port), rootHandler, writeTimeout)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("HTTP server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http server graceful shutdown: %w", err)
	}

	logger.Info("server shutdown complete")
	return nil
}

// buildRootHandler unifies the API router and MCP handler into a single http.Handler.
// - /api/v1/... and health endpoints are dispatched to the API router.
// - /mcp/... and /mcp are dispatched to the MCP handler.
//
// Parameters:
//   - apiRouter: HTTP handler serving REST API endpoints and health checks (/api/v1, /health, /healthz).
//   - mcpHandler: Optional HTTP handler serving Model Context Protocol (MCP) endpoints (/mcp, /mcp/).
//
// Returns:
//   - http.Handler: The multiplexed root HTTP handler.
func buildRootHandler(apiRouter http.Handler, mcpHandler ...http.Handler) http.Handler {
	rootMux := http.NewServeMux()

	// API and health routes
	rootMux.Handle("/api/v1/", apiRouter)
	rootMux.Handle("GET /healthz", apiRouter)
	rootMux.Handle("GET /health", apiRouter)

	// MCP routes
	if len(mcpHandler) > 0 && mcpHandler[0] != nil {
		rootMux.Handle("/mcp/", mcpHandler[0])
		rootMux.Handle("/mcp", mcpHandler[0])
	}

	return rootMux
}

// buildServer configures an http.Server instance with defense-in-depth timeouts and header size boundaries.
//
// Parameters:
//   - addr: Network address and port string to listen on (e.g. ":8080").
//   - handler: Multiplexed root HTTP handler.
//   - writeTimeout: Maximum duration before timing out writes of the response.
//
// Returns:
//   - *http.Server: Configured HTTP server instance.
func buildServer(addr string, handler http.Handler, writeTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB boundary protects against HTTP header exhaustion attacks
	}
}
