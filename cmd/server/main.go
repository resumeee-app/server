package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"resumeee/internal/config"
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

	// In Phase 1 Task 1, we establish signal trapping and graceful lifecycle.
	// HTTP listeners and background workers will attach to this lifecycle in subsequent tasks.
	<-ctx.Done()

	logger.Info("shutting down resumeee server gracefully", "signal", ctx.Err())
	return nil
}
