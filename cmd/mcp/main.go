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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	serverSDK "github.com/mark3labs/mcp-go/server"

	"resumeee/internal/branch"
	"resumeee/internal/config"
	"resumeee/internal/llm"
	resumeeeMCP "resumeee/internal/mcp"
	"resumeee/internal/profile"
	"resumeee/internal/renderer"
	"resumeee/internal/repository/sqlite"
	"resumeee/internal/validator"
	"resumeee/internal/worker"
)

// main is the primary process entrypoint, listening for OS interrupt signals and launching run().
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Stdin, os.Stdout, os.Args[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

// run parses CLI flags and configuration, initializes database and services, and serves MCP JSON-RPC over stdio.
//
// Parameters:
//   - parent: Cancellation context for process lifecycle.
//   - in: Standard input reader for incoming JSON-RPC requests.
//   - out: Standard output writer for outgoing JSON-RPC responses.
//   - args: Command-line arguments passed to flag parsing.
//
// Returns:
//   - error if configuration, database initialization, or stdio serving encounters an unrecoverable failure.
func run(parent context.Context, in io.Reader, out io.Writer, args ...string) error {
	fs := flag.NewFlagSet("resumeee-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		dbPath     string
		userID     string
		configFile string
	)

	fs.StringVar(&dbPath, "db", "", "Path to SQLite database file (or :memory:)")
	fs.StringVar(&userID, "user-id", "", "Default authenticated user ID")
	fs.StringVar(&configFile, "config", "", "Optional path to environment config file")

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

	// Ensure DB directory exists if file-backed
	if cfg.DBPath != ":memory:" && !strings.Contains(cfg.DBPath, "mode=memory") {
		dir := filepath.Dir(cfg.DBPath)
		if dir != "." && dir != "" {
			_ = os.MkdirAll(dir, 0750)
		}
	}

	db, err := sqlite.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
		defer func() { _ = sqlDB.Close() }()
	}

	// Idempotently seed default data
	if err := sqlite.SeedDefaultData(parent, db, cfg.DefaultUserID); err != nil {
		return fmt.Errorf("seeding default data: %w", err)
	}

	profileRepo := sqlite.NewProfileRepository(db)
	presetRepo := sqlite.NewPresetRepository(db)
	templateRepo := sqlite.NewTemplateRepository(db)
	branchRepo := sqlite.NewBranchRepository(db)
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()

	profileSvc := profile.NewService(profileRepo, branchRepo)

	llmRegistry := llm.NewRegistry(cfg, llm.WithLogger(logger))
	llmAdapter := llm.NewServiceAdapter(llmRegistry)
	resumeExtractor := llm.NewDefaultResumeExtractor(llmRegistry)

	workerPool := worker.NewPool(worker.PoolConfig{
		Concurrency:     2,
		QueueSize:       50,
		BranchRepo:      branchRepo,
		ProfileRepo:     profileRepo,
		PresetRepo:      presetRepo,
		TemplateRepo:    templateRepo,
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
		JobEnqueuer:  workerPool,
		Validator:    provValidator,
		Renderer:     templateRenderer,
		LLMService:   llmAdapter,
		Logger:       logger,
	})

	mcpCfg := resumeeeMCP.ServerConfig{
		ProfileService:   profileSvc,
		BranchService:    branchSvc,
		TemplateRenderer: templateRenderer,
		ProfileRepo:      profileRepo,
		BranchRepo:       branchRepo,
		TemplateRepo:     templateRepo,
		PresetRepo:       presetRepo,
		ResumeExtractor:  resumeExtractor,
		JobEnqueuer:      workerPool,
		DefaultUserID:    cfg.DefaultUserID,
		Logger:           logger,
		RESTTimeout:      cfg.RESTTimeout,
		TailoringTimeout: cfg.LLMTimeout,
	}

	mcpServer := resumeeeMCP.NewServer(mcpCfg)
	stdioServer := serverSDK.NewStdioServer(mcpServer)

	return stdioServer.Listen(parent, in, out)
}
