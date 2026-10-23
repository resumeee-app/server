package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"resumeee/internal/config"
	"resumeee/internal/repository/sqlite"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

func run(parent context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
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
	)

	// Ensure DB directory exists if file-backed
	if cfg.DBPath != ":memory:" && !strings.Contains(cfg.DBPath, "mode=memory") {
		dir := filepath.Dir(cfg.DBPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0750); err != nil {
				return fmt.Errorf("creating db directory: %w", err)
			}
		}
	}

	db, err := sqlite.New(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}

	// 1. Recover in-flight versions left interrupted by process stop
	branchRepo := sqlite.NewBranchRepository(db)
	if err := branchRepo.FailInterruptedVersions(ctx, "server restarted while generation job was in flight"); err != nil {
		logger.Warn("failed to clean up interrupted versions on boot", "err", err)
	} else {
		logger.Info("cleaned up interrupted generation versions")
	}

	// 2. Idempotently seed default user profile, built-in templates, and presets
	if err := sqlite.SeedDefaultData(ctx, db, cfg.DefaultUserID); err != nil {
		return fmt.Errorf("seeding default data: %w", err)
	}
	logger.Info("database seeded with default profile, built-in templates, and presets")

	// HTTP listeners and background workers will attach to this lifecycle in subsequent tasks.
	<-ctx.Done()

	logger.Info("shutting down resumeee server gracefully", "signal", ctx.Err())
	return nil
}
