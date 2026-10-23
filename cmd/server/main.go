package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"resumeee/internal/api"
	"resumeee/internal/branch"
	"resumeee/internal/config"
	"resumeee/internal/domain"
	"resumeee/internal/llm"
	"resumeee/internal/profile"
	"resumeee/internal/renderer"
	"resumeee/internal/repository/sqlite"
	"resumeee/internal/validator"
	"resumeee/internal/worker"
)

type llmServiceAdapter struct {
	registry *llm.Registry
}

func (a *llmServiceAdapter) GenerateTailoredResume(ctx context.Context, req domain.GenerationRequest) (*domain.GenerationResponse, error) {
	var profile *domain.Profile
	if req.MasterProfile != nil {
		profile = &req.MasterProfile.Profile
	}
	provider, model, err := a.registry.Resolve(req, nil, profile)
	if err != nil {
		return nil, err
	}
	req.Model = model
	return provider.GenerateTailoredResume(ctx, req)
}

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

	// 3. Initialize repositories and domain services
	profileRepo := sqlite.NewProfileRepository(db)
	presetRepo := sqlite.NewPresetRepository(db)
	templateRepo := sqlite.NewTemplateRepository(db)
	profileSvc := profile.NewService(profileRepo, branchRepo)
	templateRenderer := renderer.NewTemplateRenderer()
	provValidator := validator.New()

	// 4. Initialize LLM registry and service adapter
	llmRegistry := llm.NewRegistry(cfg)
	llmAdapter := &llmServiceAdapter{registry: llmRegistry}

	// 5. Initialize and start asynchronous generation worker pool
	workerPool := worker.NewPool(worker.PoolConfig{
		Concurrency:  cfg.WorkerConcurrency,
		QueueSize:    cfg.WorkerQueueSize,
		BranchRepo:   branchRepo,
		ProfileRepo:  profileRepo,
		PresetRepo:   presetRepo,
		TemplateRepo: templateRepo,
		LLMService:   llmAdapter,
		Validator:    provValidator,
		Renderer:     templateRenderer,
		Logger:       logger,
	})
	workerPool.Start(ctx)
	defer workerPool.Stop()

	// 6. Initialize Branch Service
	branchSvc := branch.NewService(branch.Config{
		BranchRepo:   branchRepo,
		ProfileRepo:  profileRepo,
		PresetRepo:   presetRepo,
		TemplateRepo: templateRepo,
		JobEnqueuer:  workerPool,
		Validator:    provValidator,
		Renderer:     templateRenderer,
		Logger:       logger,
	})

	// 7. Build API router with /api/v1 routes and middlewares
	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:    profileRepo,
		PresetRepo:     presetRepo,
		TemplateRepo:   templateRepo,
		BranchRepo:     branchRepo,
		BranchService:  branchSvc,
		ProfileService: profileSvc,
		Renderer:       templateRenderer,
		Validator:      provValidator,
		JobEnqueuer:    workerPool,
		DefaultUserID:  cfg.DefaultUserID,
		Logger:         logger,
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

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

	workerPool.Stop()

	logger.Info("server shutdown complete")
	return nil
}
