// Package main is the entry point for the Resumeee server daemon.
//
// It loads configuration, initializes SQLite persistence, resolves authentication and LLM backends,
// and runs the unified REST and MCP HTTP server via the server package.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-api/server"
	"github.com/resumeee-app/resumeee-core/store"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
)

// StoreInitializer initializes a storage backend and returns a configured *store.Store.
type StoreInitializer func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error)

// defaultSQLiteInitializer establishes the default SQLite persistence backend, executing migrations and default seeding.
func defaultSQLiteInitializer(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*store.Store, error) {
	st, err := storage.Open(ctx, storage.Config{
		Driver:        storage.DriverSQLite,
		DSN:           cfg.DBPath,
		AutoMigrate:   true,
		DefaultUserID: cfg.DefaultUserID,
	})
	if err != nil {
		return nil, err
	}
	logger.Info("database seeded with default profile, built-in templates, and presets")
	return st, nil
}

// main initializes runtime context and executes the primary resumeee server process.
func main() {
	if err := run(context.Background(), os.Args[1:]...); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

// run orchestrates database initialization, migrations, seeding, background worker startup, and HTTP serving using default SQLite persistence.
func run(parent context.Context, args ...string) error {
	return runWithStore(parent, defaultSQLiteInitializer, args...)
}

// runWithStore orchestrates configuration loading, store initialization, auth setup, and delegation to server.Run.
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

	store, err := storeInit(ctx, cfg, logger)
	if err != nil {
		return fmt.Errorf("initializing store: %w", err)
	}
	defer closeStore(store, logger)

	authProvider, err := resolveCLIAuthProvider(cfg, logger)
	if err != nil {
		return err
	}

	return server.Run(ctx,
		server.WithConfig(toServerConfig(cfg)),
		server.WithStore(store),
		server.WithRequestAuthProvider(authProvider),
		server.WithLLMRegistry(cfg.NewLLMRegistry(logger)),
		server.WithLogger(logger),
	)
}

// closeStore releases persistence resources on process exit and logs any cleanup error.
func closeStore(st *store.Store, logger *slog.Logger) {
	if st == nil {
		return
	}
	if err := st.Close(); err != nil {
		logger.Warn("failed to close store", "err", err)
	}
}

// resolveCLIAuthProvider builds the auth registry from CLI/env settings and validates active selection.
func resolveCLIAuthProvider(cfg *config.Config, logger *slog.Logger) (auth.Provider, error) {
	authRegistry, err := auth.NewRegistryFromConfig(auth.Config{
		DefaultUserID:     cfg.DefaultUserID,
		FirebaseProjectID: cfg.FirebaseProjectID,
		AuthProvider:      cfg.AuthProvider,
		Admin:             cfg.Admin,
	})
	if err != nil {
		logger.Error("failed to initialize auth registry", "err", err)
		return nil, fmt.Errorf("initializing auth registry: %w", err)
	}

	authProvider, err := authRegistry.Default()
	if err != nil {
		const msg = "multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode"
		logger.Error(msg)
		return nil, errors.New(msg)
	}

	switch authProvider.Name() {
	case auth.ProviderAdmin:
		logger.Info("running in local admin mode: token verification bypassed, default user id applied", "user_id", cfg.DefaultUserID)
	case auth.ProviderFirebase:
		logger.Info("initialized firebase token verifier", "project_id", cfg.FirebaseProjectID)
	default:
		logger.Info("initialized authentication provider", "provider", authProvider.Name())
	}

	return authProvider, nil
}

// toServerConfig maps CLI/env config values into the public server.Config struct.
func toServerConfig(cfg *config.Config) *server.Config {
	return &server.Config{
		Port:                       cfg.Port,
		DefaultUserID:              cfg.DefaultUserID,
		Admin:                      cfg.Admin,
		LLMDailyRequestLimit:       cfg.LLMDailyRequestLimit,
		LLMMonthlyTokenLimit:       cfg.LLMMonthlyTokenLimit,
		WorkerConcurrency:          cfg.WorkerConcurrency,
		MaxConcurrentJobsPerTenant: cfg.MaxConcurrentJobsPerTenant,
		Environment:                cfg.Environment,
		LogLevel:                   cfg.LogLevel,
		LLMDebugPrompts:            cfg.LLMDebugPrompts,
		LLMDebugDir:                cfg.LLMDebugDir,
		RESTTimeout:                cfg.RESTTimeout,
		LLMTimeout:                 cfg.LLMTimeout,
		PDFParseTimeout:            cfg.PDFParseTimeout,
		RenderPreviewTimeout:       cfg.RenderPreviewTimeout,
		HTTPReadHeaderTimeout:      cfg.HTTPReadHeaderTimeout,
		HTTPReadTimeout:            cfg.HTTPReadTimeout,
		HTTPMinWriteTimeout:        cfg.HTTPMinWriteTimeout,
		HTTPWriteTimeoutBuffer:     cfg.HTTPWriteTimeoutBuffer,
		HTTPIdleTimeout:            cfg.HTTPIdleTimeout,
		HTTPShutdownTimeout:        cfg.HTTPShutdownTimeout,
		MCPSSEKeepAliveInterval:    cfg.MCPSSEKeepAliveInterval,
	}
}
