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

	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/profile"
	"github.com/resumeee-app/core/provenance"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/core/worker"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
	"github.com/resumeee-app/server/auth"
	"github.com/resumeee-app/server/internal/config"
	resumeeeMCP "github.com/resumeee-app/server/internal/mcp"
)

// StoreInitializer is a factory function initializing domain stores and storage resources for the MCP server.
type StoreInitializer func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error)

// defaultSQLiteInitializer creates a SQLite persistence backend and seeds default records.
func defaultSQLiteInitializer(ctx context.Context, cfg *config.Config, _ *slog.Logger) (*store.Store, error) {
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

	st, err := storeInit(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	defer closeMCPStore(st, logger)

	mcpCfg, workerPool, err := buildMCPServerConfig(cfg, st, logger)
	if err != nil {
		return err
	}
	if err := workerPool.RecoverStaleJobs(ctx, "mcp server restarted during job processing"); err != nil {
		logger.WarnContext(ctx, "failed to recover stale jobs", "err", err)
	}
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

	var (
		cfg *config.Config
		err error
	)
	if configFile != "" {
		if _, statErr := os.Stat(configFile); statErr != nil {
			return nil, fmt.Errorf("reading config file %q: %w", configFile, statErr)
		}
		cfg, err = config.LoadFromFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("loading config file %q: %w", configFile, err)
		}
	} else {
		cfg, err = config.Load()
		if err != nil {
			return nil, fmt.Errorf("loading configuration: %w", err)
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
func buildMCPServerConfig(cfg *config.Config, st *store.Store, logger *slog.Logger) (resumeeeMCP.ServerConfig, *worker.Pool, error) {
	authProvider, err := resolveMCPAuthProvider(cfg, logger)
	if err != nil {
		return resumeeeMCP.ServerConfig{}, nil, err
	}

	profileStore := st.Profiles()
	careerStore := st.Career()
	presetStore := st.Presets()
	templateStore := st.Templates()
	branchStore := st.Branches()
	userStore := st.Users()
	quotaStore := st.Quotas()
	jobStore := st.Jobs()

	templateRenderer := renderer.New()
	provValidator := provenance.New()
	profileSvc := profile.New(profileStore, careerStore, branchStore)

	llmRegistry := cfg.NewLLMRegistry(logger)
	llmService := llm.NewService(llmRegistry)
	resumeExtractor := llm.NewRegistryExtractor(llmRegistry)

	concurrency := cfg.WorkerConcurrency
	if concurrency <= 0 {
		concurrency = config.DefaultWorkerConcurrency
	}
	maxPerTenant := cfg.MaxConcurrentJobsPerTenant
	if maxPerTenant <= 0 {
		maxPerTenant = config.DefaultMaxConcurrentJobsPerTenant
	}

	workerPool := worker.New(
		worker.WithConcurrency(concurrency),
		worker.WithMaxConcurrentPerTenant(maxPerTenant),
		worker.WithJobs(jobStore),
		worker.WithBranches(branchStore),
		worker.WithProfiles(profileStore),
		worker.WithPresets(presetStore),
		worker.WithTemplates(templateStore),
		worker.WithQuotas(quotaStore),
		worker.WithResumeExtractor(resumeExtractor),
		worker.WithLLMTimeout(cfg.LLMTimeout),
		worker.WithGenerator(llmService),
		worker.WithValidator(provValidator),
		worker.WithRenderer(templateRenderer),
		worker.WithLogger(logger),
	)

	branchSvc := branch.New(
		branch.WithBranches(branchStore),
		branch.WithJobs(jobStore),
		branch.WithProfiles(profileStore),
		branch.WithPresets(presetStore),
		branch.WithTemplates(templateStore),
		branch.WithQuotas(quotaStore),
		branch.WithJobNotifier(workerPool),
		branch.WithValidator(provValidator),
		branch.WithRenderer(templateRenderer),
		branch.WithGenerator(llmService),
		branch.WithPromptCompiler(llm.NewPromptCompiler(llm.WithPromptRegistry(llmRegistry))),
		branch.WithDailyRequestLimit(cfg.LLMDailyRequestLimit),
		branch.WithMonthlyTokenLimit(cfg.LLMMonthlyTokenLimit),
		branch.WithLogger(logger),
	)

	mcpCfg := resumeeeMCP.ServerConfig{
		ProfileService:       profileSvc,
		BranchService:        branchSvc,
		TemplateRenderer:     templateRenderer,
		ProfileStore:         profileStore,
		CareerStore:          careerStore,
		BranchStore:          branchStore,
		TemplateStore:        templateStore,
		PresetStore:          presetStore,
		UserStore:            userStore,
		ResumeExtractor:      resumeExtractor,
		DefaultUserID:        cfg.DefaultUserID,
		Admin:                cfg.Admin && (authProvider == nil || authProvider.Name() == auth.ProviderAdmin),
		AuthProvider:         authProvider,
		Logger:               logger,
		RESTTimeout:          cfg.RESTTimeout,
		TailoringTimeout:     cfg.LLMTimeout,
		SSEKeepAliveInterval: cfg.MCPSSEKeepAliveInterval,
	}

	return mcpCfg, workerPool, nil
}

// resolveMCPAuthProvider initializes auth from config and fails fast on explicit misconfiguration while allowing local stdio operation when unconfigured.
func resolveMCPAuthProvider(cfg *config.Config, logger *slog.Logger) (auth.Provider, error) {
	authReg, err := auth.NewRegistryFromConfig(auth.Config{
		DefaultUserID:     cfg.DefaultUserID,
		FirebaseProjectID: cfg.FirebaseProjectID,
		AuthProvider:      cfg.AuthProvider,
		Admin:             cfg.Admin,
	})
	if err != nil {
		if logger != nil {
			logger.Error("failed to initialize auth registry for mcp", "err", err)
		}
		return nil, fmt.Errorf("initializing auth registry: %w", err)
	}
	prov, err := authReg.Default()
	if err != nil {
		// No remote auth provider or explicit admin mode configured; allow local stdio execution.
		return nil, nil
	}
	return prov, nil
}

// closeMCPStore closes st on exit and logs any failure to stderr.
func closeMCPStore(st *store.Store, logger *slog.Logger) {
	if st == nil {
		return
	}
	if err := st.Close(); err != nil {
		logger.Warn("failed to close store", "err", err)
	}
}
