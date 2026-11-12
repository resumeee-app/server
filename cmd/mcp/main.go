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

	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core/llm"
	resumeeeMCP "github.com/resumeee-app/resumeee-api/internal/mcp"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
	"github.com/resumeee-app/resumeee-core/validator"
	"github.com/resumeee-app/resumeee-core/services/worker"
)

// StoreInitializer is a factory function initializing domain repositories and storage resources for the MCP server.
type StoreInitializer func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (core.Store, error)

// defaultSQLiteInitializer creates a SQLite persistence backend and seeds default records.
//
// Parameters:
//   - ctx: Startup context.
//   - cfg: MCP runtime configuration.
//   - logger: System logger for recording operational messages.
//
// Returns:
//   - core.Store: Initialized SQLite store.
//   - error: Non-nil if SQLite connection or seeding fails.
func defaultSQLiteInitializer(ctx context.Context, cfg *config.Config, logger *slog.Logger) (core.Store, error) {
	return storage.Open(ctx, storage.Config{
		Driver:        storage.DriverSQLite,
		DSN:           cfg.DBPath,
		AutoMigrate:   true,
		DefaultUserID: cfg.DefaultUserID,
	})
}

// main is the primary process entrypoint, listening for OS interrupt signals and launching run().
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Stdin, os.Stdout, os.Args[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

// run parses CLI flags and configuration, initializes database and services using default SQLite persistence, and serves MCP JSON-RPC over stdio.
//
// Parameters:
//   - parent: Cancellation context for process lifecycle.
//   - in: Standard input reader for incoming JSON-RPC requests.
//   - out: Standard output writer for outgoing JSON-RPC responses.
//   - args: Command-line arguments passed to flag parsing.
//
// Returns:
//   - error: Non-nil if configuration, database initialization, or stdio serving encounters an unrecoverable failure.
func run(parent context.Context, in io.Reader, out io.Writer, args ...string) error {
	return runWithStore(parent, defaultSQLiteInitializer, in, out, args...)
}

// runWithStore parses CLI flags and configuration, initializes injected storage and services, and serves MCP JSON-RPC over stdio.
//
// Parameters:
//   - parent: Cancellation context for process lifecycle.
//   - storeInit: Storage backend initializer injecting repository implementations.
//   - in: Standard input reader for incoming JSON-RPC requests.
//   - out: Standard output writer for outgoing JSON-RPC responses.
//   - args: Command-line arguments passed to flag parsing.
//
// Returns:
//   - error: Non-nil if configuration, storage initialization, or stdio serving encounters an unrecoverable failure.
func runWithStore(parent context.Context, storeInit StoreInitializer, in io.Reader, out io.Writer, args ...string) error {
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
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{
			DBPath:        "data/resumeee.db",
			DefaultUserID: "default",
			RESTTimeout:   30 * time.Second,
			LLMTimeout:    10 * time.Minute,
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

	// Logging must go to stderr so stdout remains purely reserved for JSON-RPC messages
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	store, err := storeInit(parent, cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Warn("failed to close store", "err", err)
		}
	}()

	profileRepo := store.Profile()
	presetRepo := store.Preset()
	templateRepo := store.Template()
	branchRepo := store.Branch()
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()

	profileSvc := profile.NewService(profileRepo, branchRepo)

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

	workerPool := worker.NewPool(worker.PoolConfig{
		Concurrency:     2,
		QueueSize:       50,
		BranchRepo:      branchRepo,
		ProfileRepo:     profileRepo,
		PresetRepo:      presetRepo,
		TemplateRepo:    templateRepo,
		QuotaRepo:       store.Quota(),
		ResumeExtractor: resumeExtractor,
		LLMTimeout:      cfg.LLMTimeout,
		LLMService:      llmAdapter,
		Validator:       provValidator,
		Renderer:        templateRenderer,
		Logger:          logger,
	})
	workerPool.Start(parent)
	defer workerPool.Stop()

	branchSvc := branch.NewService(branch.Config{
		BranchRepo:   branchRepo,
		ProfileRepo:  profileRepo,
		PresetRepo:   presetRepo,
		TemplateRepo: templateRepo,
		QuotaRepo:    store.Quota(),
		JobEnqueuer:    workerPool,
		Validator:      provValidator,
		Renderer:       templateRenderer,
		LLMService:     llmAdapter,
		PromptCompiler: llm.NewPromptCompiler(),
		Logger:         logger,
	})

	mcpCfg := resumeeeMCP.ServerConfig{
		ProfileService:   profileSvc,
		BranchService:    branchSvc,
		TemplateRenderer: templateRenderer,
		ProfileRepo:      profileRepo,
		BranchRepo:       branchRepo,
		TemplateRepo:     templateRepo,
		PresetRepo:       presetRepo,
		UserRepo:         store.User(),
		ResumeExtractor:  resumeExtractor,
		JobEnqueuer:      workerPool,
		DefaultUserID:    cfg.DefaultUserID,
		Admin:            cfg.Admin,
		Logger:           logger,
		RESTTimeout:      cfg.RESTTimeout,
		TailoringTimeout: cfg.LLMTimeout,
	}

	authReg, err := auth.NewRegistryFromConfig(auth.Config{
		DefaultUserID:     cfg.DefaultUserID,
		FirebaseProjectID: cfg.FirebaseProjectID,
		AuthProvider:      cfg.AuthProvider,
		Admin:             cfg.Admin,
	})
	if err != nil {
		logger.WarnContext(parent, "failed to initialize auth registry for mcp", "err", err)
	} else if prov, err := authReg.Default(); err != nil {
		logger.WarnContext(parent, "no active auth provider resolved for mcp from config", "err", err)
	} else {
		mcpCfg.AuthProvider = prov
		mcpCfg.TokenVerifier = prov
	}

	if err := mcpCfg.EnsureTenant(parent, cfg.DefaultUserID); err != nil {
		logger.WarnContext(parent, "failed to provision default tenant", "user_id", cfg.DefaultUserID, "err", err)
	}

	mcpServer := resumeeeMCP.NewServer(mcpCfg)
	stdioServer := serverSDK.NewStdioServer(mcpServer)

	return stdioServer.Listen(parent, in, out)
}
