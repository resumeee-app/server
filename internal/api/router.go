package api

import (
	"log/slog"
	"net/http"
	"strings"

	"resumeee/internal/domain"
	"resumeee/internal/profile"
	"resumeee/internal/validator"
)

// JobEnqueuer defines the interface for submitting version IDs to the async generation worker pool.
type JobEnqueuer interface {
	Enqueue(versionID string) error
}

// ChannelJobEnqueuer implements JobEnqueuer backed by a buffered Go channel.
type ChannelJobEnqueuer struct {
	ch chan string
}

// NewChannelJobEnqueuer creates a new ChannelJobEnqueuer with the given buffer capacity.
func NewChannelJobEnqueuer(capacity int) *ChannelJobEnqueuer {
	if capacity <= 0 {
		capacity = 100
	}
	return &ChannelJobEnqueuer{
		ch: make(chan string, capacity),
	}
}

// Enqueue sends versionID to the buffered channel or returns domain.ErrQueueFull if saturated.
func (e *ChannelJobEnqueuer) Enqueue(versionID string) error {
	select {
	case e.ch <- versionID:
		return nil
	default:
		return domain.ErrQueueFull
	}
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelJobEnqueuer) Channel() <-chan string {
	return e.ch
}

// RouterConfig contains all dependencies required by the API router.
type RouterConfig struct {
	ProfileRepo    domain.ProfileRepository
	PresetRepo     domain.PresetRepository
	TemplateRepo   domain.TemplateRepository
	BranchRepo     domain.BranchRepository
	ProfileService profile.Service
	Renderer       domain.TemplateRenderer
	Validator      validator.ProvenanceValidator
	JobEnqueuer    JobEnqueuer
	DefaultUserID  string
	Logger         *slog.Logger
}

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

	mux := http.NewServeMux()

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
