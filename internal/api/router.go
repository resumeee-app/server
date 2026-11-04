// Package api implements the REST HTTP API for Resumeee, strictly adhering to the OpenAPI 3.1 specification.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"resumeee/internal/branch"
	"resumeee/internal/domain"
	"resumeee/internal/llm"
	"resumeee/internal/pdf"
	"resumeee/internal/profile"
	"resumeee/internal/validator"
	"resumeee/internal/worker"
)

// JobEnqueuer defines the interface for submitting version jobs to the async generation worker pool.
type JobEnqueuer = worker.JobEnqueuer

// ChannelJobEnqueuer implements JobEnqueuer backed by a buffered Go channel.
type ChannelJobEnqueuer struct {
	ch chan worker.GenerationJob
}

// NewChannelJobEnqueuer creates a new ChannelJobEnqueuer with the given buffer capacity.
func NewChannelJobEnqueuer(capacity int) *ChannelJobEnqueuer {
	if capacity <= 0 {
		capacity = 100
	}
	return &ChannelJobEnqueuer{
		ch: make(chan worker.GenerationJob, capacity),
	}
}

// Enqueue sends a GenerationJob to the buffered channel or returns domain.ErrQueueFull if saturated.
func (e *ChannelJobEnqueuer) Enqueue(job worker.GenerationJob) error {
	select {
	case e.ch <- job:
		return nil
	default:
		return domain.ErrQueueFull
	}
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelJobEnqueuer) Channel() <-chan worker.GenerationJob {
	return e.ch
}

// ModelEnumerator defines the interface for enumerating available configured LLM models.
type ModelEnumerator interface {
	AvailableModels() []domain.ModelOption
}

// RouterConfig contains all dependencies required by the API router.
type RouterConfig struct {
	ProfileRepo     domain.ProfileRepository
	PresetRepo      domain.PresetRepository
	TemplateRepo    domain.TemplateRepository
	BranchRepo      domain.BranchRepository
	BranchService   branch.Service
	ProfileService  profile.Service
	PDFExtractor    pdf.PDFTextExtractor
	ResumeExtractor llm.ResumeExtractor
	Renderer        domain.TemplateRenderer
	Validator       validator.ProvenanceValidator
	JobEnqueuer     JobEnqueuer
	ModelEnumerator ModelEnumerator
	DefaultUserID   string
	Logger          *slog.Logger
}

// getUserID extracts the user ID from the X-User-ID request header or falls back to defaultID.
func getUserID(r *http.Request, defaultID string) string {
	if uid := strings.TrimSpace(r.Header.Get("X-User-ID")); uid != "" {
		return uid
	}
	if defaultID != "" {
		return defaultID
	}
	return "default"
}

// NewRouter constructs a net/http router and registers all OpenAPI routes under /api/v1.
func NewRouter(cfg RouterConfig) http.Handler {
	if cfg.DefaultUserID == "" {
		cfg.DefaultUserID = "default"
	}
	if cfg.PDFExtractor == nil {
		cfg.PDFExtractor = pdf.NewExtractor()
	}
	if cfg.ResumeExtractor == nil {
		cfg.ResumeExtractor = llm.NewDefaultResumeExtractor(nil)
	}
	if cfg.ModelEnumerator == nil {
		if me, ok := cfg.ResumeExtractor.(ModelEnumerator); ok {
			cfg.ModelEnumerator = me
		}
	}

	if cfg.BranchService == nil && cfg.BranchRepo != nil {
		cfg.BranchService = branch.NewService(branch.Config{
			BranchRepo:   cfg.BranchRepo,
			ProfileRepo:  cfg.ProfileRepo,
			PresetRepo:   cfg.PresetRepo,
			TemplateRepo: cfg.TemplateRepo,
			JobEnqueuer:  cfg.JobEnqueuer,
			Validator:    cfg.Validator,
			Renderer:     cfg.Renderer,
			Logger:       cfg.Logger,
		})
	}

	mux := http.NewServeMux()

	registerHealthRoutes(mux)
	registerModelRoutes(mux, cfg)
	registerProfileRoutes(mux, cfg)
	registerPresetRoutes(mux, cfg)
	registerTemplateRoutes(mux, cfg)
	registerBranchRoutes(mux, cfg)

	// Wrap in middleware chain: ContentType -> Logging -> Recovery -> CORS
	var handler http.Handler = mux
	handler = ContentTypeMiddleware(handler)
	handler = LoggingMiddleware(cfg.Logger)(handler)
	handler = RecoveryMiddleware(cfg.Logger)(handler)
	handler = CORSMiddleware(handler)

	return handler
}

// registerHealthRoutes registers HTTP health check probes on /healthz and /api/v1/health.
func registerHealthRoutes(mux *http.ServeMux) {
	healthHandler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	}

	mux.HandleFunc("GET /api/v1/health", healthHandler)
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /health", healthHandler)
}

// registerModelRoutes registers endpoints exposing configured LLM provider and model options.
func registerModelRoutes(mux *http.ServeMux, cfg RouterConfig) {
	modelsHandler := func(w http.ResponseWriter, _ *http.Request) {
		var models []domain.ModelOption
		if cfg.ModelEnumerator != nil {
			models = cfg.ModelEnumerator.AvailableModels()
		}
		if models == nil {
			models = []domain.ModelOption{}
		}
		writeJSON(w, http.StatusOK, models)
	}

	mux.HandleFunc("GET /api/v1/models", modelsHandler)
	mux.HandleFunc("GET /models", modelsHandler)
}

