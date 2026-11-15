// Package main provides the standalone Model Context Protocol (MCP) CLI entrypoint for Resumeee.
// Desktop AI clients (such as Claude Desktop, Cursor, and Antigravity) connect to this binary over stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/profile"
	"github.com/resumeee-app/resumeee-core/provenance"
	"github.com/resumeee-app/resumeee-core/renderer"
	"github.com/resumeee-app/resumeee-core/worker"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
)

// Operational defaults for the standalone stdio MCP server.
const (
	// mcpWorkerConcurrency sets the number of concurrent async tailoring workers in stdio CLI mode.
	mcpWorkerConcurrency = 2

	// mcpWorkerQueueSize caps buffered async tailoring tasks in stdio CLI mode.
	mcpWorkerQueueSize = 50

	// mcpFallbackDBPath is used if loading config from environment fails unexpectedly.
	mcpFallbackDBPath = "data/resumeee.db"

	// mcpFallbackRESTTimeout bounds synchronous tool/resource calls when fallback config is active.
	mcpFallbackRESTTimeout = 30 * time.Second
)

// StoreInitializer is a factory function initializing domain stores and storage resources for the MCP server.
type StoreInitializer func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (core.Store, error)

// defaultSQLiteInitializer creates a SQLite persistence backend and seeds default records.
func defaultSQLiteInitializer(ctx context.Context, cfg *config.Config, _ *slog.Logger) (core.Store, error) {
	return storage.Open(ctx, storage.Config{
		Driver:        storage.DriverSQLite,
		DSN:           cfg.DBPath,
		AutoMigrate:   true,
		DefaultUserID: cfg.DefaultUserID,
	})
}

// main is the primary process entrypoint launching the stdio MCP server.
func main() {
	if err := run(context.Background(), os.Stdin, os.Stdout, os.Args[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

// run parses CLI flags and configuration, initializes database and services using default SQLite persistence, and serves MCP JSON-RPC over stdio.
func run(parent context.Context, in io.Reader, out io.Writer, args ...string) error {
	return runWithStore(parent, defaultSQLiteInitializer, in, out, args...)
}

// runWithStore parses CLI flags and configuration, initializes injected storage and services, and serves MCP JSON-RPC over stdio.
func runWithStore(parent context.Context, storeInit StoreInitializer, in io.Reader, out io.Writer, args ...string) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadMCPConfig(args)
	if err != nil {
		return err
	}

	// Logging must go to stderr so stdout remains purely reserved for JSON-RPC messages.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	store, err := storeInit(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	defer closeMCPStore(store, logger)

	mcpCfg, workerPool := buildMCPServerConfig(ctx, cfg, store, logger)
	workerPool.Start(ctx)
	defer workerPool.Stop()

	if err := mcpCfg.EnsureTenant(ctx, cfg.DefaultUserID); err != nil {
		logger.WarnContext(ctx, "failed to provision default tenant", "user_id", cfg.DefaultUserID, "err", err)
	}

	mcpServer := resumeeeMCP.NewServer(mcpCfg)
	return serverSDK.NewStdioServer(mcpServer).Listen(ctx, in, out)
}

// loadMCPConfig loads environment config and applies CLI flag overrides for the MCP binary.
func loadMCPConfig(args []string) (*config.Config, error) {
	fs := flag.NewFlagSet("resumeee-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dbPath     string
		userID     string
		configFile string
		adminMode  bool
	)
	fs.StringVar(&dbPath, "db", "", "Path to SQLite database file (or :memory:)")
	fs.StringVar(&userID, "user-id", "", "Default authenticated user ID")
	fs.StringVar(&configFile, "config", "", "Optional path to environment config file")
	fs.BoolVar(&adminMode, "admin-mode", false, "Run in local single-user admin mode")
	fs.BoolVar(&adminMode, "admin", false, "Alias for -admin-mode")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	_ = configFile

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{
			DBPath:        mcpFallbackDBPath,
			DefaultUserID: config.DefaultUserID,
			RESTTimeout:   mcpFallbackRESTTimeout,
			LLMTimeout:    config.DefaultLLMTimeout,
		}
	}

	var adminExplicit bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "admin-mode" || f.Name == "admin" {
			adminExplicit = true
		}
	})
	if adminExplicit {
		cfg.Admin = adminMode
	}
	if dbPath != "" {
		cfg.DBPath = dbPath
	}
	if userID != "" {
		cfg.DefaultUserID = userID
	}

	return cfg, nil
}

// buildMCPServerConfig wires domain services, worker pool, and auth provider for MCP execution.
func buildMCPServerConfig(ctx context.Context, cfg *config.Config, store core.Store, logger *slog.Logger) (resumeeeMCP.ServerConfig, *worker.Pool) {
	profileStore := store.Profiles()
	careerStore := store.Career()
	presetStore := store.Presets()
	templateStore := store.Templates()
	branchStore := store.Branches()
	userStore := store.Users()
	quotaStore := store.Quotas()

	templateRenderer := renderer.New()
	provValidator := provenance.New()
	profileSvc := profile.New(profileStore, careerStore, branchStore)

	llmRegistry := cfg.NewLLMRegistry(logger)
	llmService := llm.NewService(llmRegistry)
	resumeExtractor := llm.NewRegistryExtractor(llmRegistry)

	workerPool := worker.New(worker.Config{
		Concurrency:     mcpWorkerConcurrency,
		QueueSize:       mcpWorkerQueueSize,
		Branches:        branchStore,
		Profiles:        profileStore,
		Presets:         presetStore,
		Templates:       templateStore,
		Quotas:          quotaStore,
		ResumeExtractor: resumeExtractor,
		LLMTimeout:      cfg.LLMTimeout,
		Generator:       llmService,
		Validator:       provValidator,
		Renderer:        templateRenderer,
		Logger:          logger,
	})

	branchSvc := branch.New(branch.Config{
		Branches:       branchStore,
		Profiles:       profileStore,
		Presets:        presetStore,
		Templates:      templateStore,
		Quotas:         quotaStore,
		JobEnqueuer:    workerPool,
		Validator:      provValidator,
		Renderer:       templateRenderer,
		Generator:      llmService,
		PromptCompiler: llm.NewPromptCompiler(),
		Logger:         logger,
	})

	mcpCfg := resumeeeMCP.ServerConfig{
		ProfileService:   profileSvc,
		BranchService:    branchSvc,
		TemplateRenderer: templateRenderer,
		ProfileStore:     profileStore,
		CareerStore:      careerStore,
		BranchStore:      branchStore,
		TemplateStore:    templateStore,
		PresetStore:      presetStore,
		UserStore:        userStore,
		ResumeExtractor:  resumeExtractor,
		JobEnqueuer:      workerPool,
		DefaultUserID:    cfg.DefaultUserID,
		Admin:            cfg.Admin,
		Logger:           logger,
		RESTTimeout:      cfg.RESTTimeout,
		TailoringTimeout: cfg.LLMTimeout,
	}

	resolveMCPAuthProvider(ctx, cfg, logger, &mcpCfg)
	return mcpCfg, workerPool
}

// resolveMCPAuthProvider initializes auth from config and attaches any active provider onto mcpCfg.
func resolveMCPAuthProvider(ctx context.Context, cfg *config.Config, logger *slog.Logger, mcpCfg *resumeeeMCP.ServerConfig) {
	authReg, err := auth.NewRegistryFromConfig(auth.Config{
		DefaultUserID:     cfg.DefaultUserID,
		FirebaseProjectID: cfg.FirebaseProjectID,
		AuthProvider:      cfg.AuthProvider,
		Admin:             cfg.Admin,
	})
	if err != nil {
		logger.WarnContext(ctx, "failed to initialize auth registry for mcp", "err", err)
		return
	}
	prov, err := authReg.Default()
	if err != nil {
		logger.WarnContext(ctx, "no active auth provider resolved for mcp from config", "err", err)
		return
	}
	mcpCfg.AuthProvider = prov
	mcpCfg.TokenVerifier = prov
}

// closeMCPStore closes store on exit and logs any failure to stderr.
func closeMCPStore(store core.Store, logger *slog.Logger) {
	if err := store.Close(); err != nil {
		logger.Warn("failed to close store", "err", err)
	}
}
