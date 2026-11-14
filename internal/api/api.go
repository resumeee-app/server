// Package api implements the REST HTTP API for Resumeee, strictly adhering to the
// OpenAPI 3.1 specification.
//
// It provides HTTP routers, middleware (CORS, content type negotiation, structured
// logging, panic recovery, and timeout handling), and request/response handlers for:
//   - Profile management and career graph querying (/api/v1/profile)
//   - Main profile snapshot imports and exports (/api/v1/profile/jsonresume/*)
//   - Asynchronous PDF resume parsing jobs (/api/v1/profile/pdf/*)
//   - Resume branch creation, updates, and synchronization (/api/v1/branches/*)
//   - Resume version generation, iteration, and rendering (/api/v1/branches/{id}/versions/*)
//   - Multi-format template previews and rendering (/api/v1/templates/*)
//   - Tailoring preset configurations (/api/v1/presets/*)
//   - LLM model enumeration (/api/v1/models)
//
// Handlers communicate with underlying domain services, persistence repositories,
// and asynchronous worker pools via decoupled interfaces.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-core/pdf"
	"github.com/resumeee-app/resumeee-core/profile"
	"github.com/resumeee-app/resumeee-core/renderer"
	"github.com/resumeee-app/resumeee-core/seed"
	"github.com/resumeee-app/resumeee-core/worker"
)

// ChannelQueue provides a generic bounded channel-backed queue for asynchronous task dispatch.
type ChannelQueue[T any] struct {
	ch chan T
}

// NewChannelQueue constructs a ChannelQueue with the specified buffer capacity.
func NewChannelQueue[T any](capacity int) *ChannelQueue[T] {
	if capacity <= 0 {
		capacity = 100
	}
	return &ChannelQueue[T]{
		ch: make(chan T, capacity),
	}
}

// Push sends an item to the buffered channel or returns core.ErrQueueFull if saturated.
func (q *ChannelQueue[T]) Push(item T) error {
	select {
	case q.ch <- item:
		return nil
	default:
		return core.ErrQueueFull
	}
}

// Channel exposes the underlying receive channel for background consumers.
func (q *ChannelQueue[T]) Channel() <-chan T {
	return q.ch
}

// ChannelJobEnqueuer implements JobEnqueuer backed by ChannelQueue[core.GenerationJob].
type ChannelJobEnqueuer struct {
	queue *ChannelQueue[core.GenerationJob]
}

// NewChannelJobEnqueuer creates a new ChannelJobEnqueuer with the given buffer capacity.
func NewChannelJobEnqueuer(capacity int) *ChannelJobEnqueuer {
	return &ChannelJobEnqueuer{
		queue: NewChannelQueue[core.GenerationJob](capacity),
	}
}

// Enqueue sends a GenerationJob to the buffered channel or returns core.ErrQueueFull if saturated.
func (e *ChannelJobEnqueuer) Enqueue(job core.GenerationJob) error {
	return e.queue.Push(job)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelJobEnqueuer) Channel() <-chan core.GenerationJob {
	return e.queue.Channel()
}

// ChannelPDFJobEnqueuer implements PDFJobEnqueuer backed by ChannelQueue[core.PDFParseJobTask].
type ChannelPDFJobEnqueuer struct {
	queue *ChannelQueue[core.PDFParseJobTask]
}

// NewChannelPDFJobEnqueuer creates a new ChannelPDFJobEnqueuer with the given buffer capacity.
func NewChannelPDFJobEnqueuer(capacity int) *ChannelPDFJobEnqueuer {
	return &ChannelPDFJobEnqueuer{
		queue: NewChannelQueue[core.PDFParseJobTask](capacity),
	}
}

// EnqueuePDFJob sends a PDFParseJobTask to the buffered channel or returns core.ErrQueueFull if saturated.
func (e *ChannelPDFJobEnqueuer) EnqueuePDFJob(task core.PDFParseJobTask) error {
	return e.queue.Push(task)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelPDFJobEnqueuer) Channel() <-chan core.PDFParseJobTask {
	return e.queue.Channel()
}

// PDFTextExtractor defines the narrow PDF text extraction contract required by upload handlers.
type PDFTextExtractor interface {
	ExtractText(ctx context.Context, r io.ReaderAt, size int64) (string, error)
}

// TemplateRenderer defines the template validation and rendering contract required by API handlers.
type TemplateRenderer interface {
	renderer.TemplateRenderer
	Validate(tmpl *core.TemplateSet) error
}

// RouterConfig contains all dependencies required by the API router.
type RouterConfig struct {
	ProfileStore      core.ProfileStore
	CareerStore       core.CareerStore
	PresetStore       core.PresetStore
	TemplateStore     core.TemplateStore
	BranchStore       core.BranchStore
	PDFJobStore       core.PDFJobStore
	PDFJobEnqueuer    core.PDFJobEnqueuer
	QuotaStore        core.QuotaStore
	BranchService     *branch.Service
	ProfileService    *profile.Service
	PDFExtractor      PDFTextExtractor
	ResumeExtractor   worker.ResumeExtractor
	Renderer          TemplateRenderer
	Validator         branch.ProvenanceValidator
	JobEnqueuer       branch.JobEnqueuer
	PromptCompiler    branch.PromptCompiler
	ModelEnumerator   core.ModelEnumerator
	DefaultUserID     string
	Admin             bool
	AuthProvider      auth.Provider
	TokenVerifier     core.TokenVerifier
	FirebaseProjectID string
	UserStore         core.UserStore
	Store             core.Store
	Provisioner       TenantProvisioner
	Logger            *slog.Logger
	RESTTimeout       time.Duration
	LLMTimeout        time.Duration
	DailyRequestLimit int
	MonthlyTokenLimit int
}

type defaultTenantProvisioner struct {
	store         core.Store
	profileStore  core.ProfileStore
	branchStore   core.BranchStore
	templateStore core.TemplateStore
	userStore     core.UserStore
	provisioned   sync.Map
}

func (p *defaultTenantProvisioner) EnsureTenantProvisioned(ctx context.Context, userID string, userInfo *core.User) error {
	if _, ok := p.provisioned.Load(userID); ok {
		return nil
	}
	var err error
	if p.store != nil {
		err = seed.EnsureTenantProvisioned(ctx, p.store, userID, userInfo)
	} else if p.profileStore != nil && p.branchStore != nil && p.templateStore != nil {
		err = seed.EnsureTenantProvisionedStores(ctx, p.profileStore, p.branchStore, p.templateStore, p.userStore, userID, userInfo)
	}
	if err == nil {
		p.provisioned.Store(userID, true)
	}
	return err
}

// getUserID extracts the tenant user ID strictly from the request context, falling back to defaultID if not in context.
func getUserID(r *http.Request, defaultID string) string {
	if uid, err := UserIDFromContext(r.Context()); err == nil && uid != "" {
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
	if cfg.RESTTimeout <= 0 {
		cfg.RESTTimeout = 20 * time.Second
	}
	if cfg.LLMTimeout <= 0 {
		cfg.LLMTimeout = 10 * time.Minute
	}
	if cfg.DailyRequestLimit <= 0 {
		cfg.DailyRequestLimit = 50
	}
	if cfg.MonthlyTokenLimit <= 0 {
		cfg.MonthlyTokenLimit = 1000000
	}
	if cfg.PDFJobStore == nil {
		cfg.PDFJobStore = memory.New()
	}
	if cfg.PDFExtractor == nil {
		cfg.PDFExtractor = pdf.NewExtractor()
	}
	if cfg.ResumeExtractor == nil {
		cfg.ResumeExtractor = llm.NewRegistryExtractor(nil)
	}
	if cfg.ModelEnumerator == nil {
		if me, ok := cfg.ResumeExtractor.(core.ModelEnumerator); ok {
			cfg.ModelEnumerator = me
		}
	}

	if cfg.PromptCompiler == nil {
		cfg.PromptCompiler = llm.NewPromptCompiler()
	}

	if cfg.BranchService == nil && cfg.BranchStore != nil {
		cfg.BranchService = branch.New(branch.Config{
			Branches:          cfg.BranchStore,
			Profiles:          cfg.ProfileStore,
			Presets:           cfg.PresetStore,
			Templates:         cfg.TemplateStore,
			Quotas:            cfg.QuotaStore,
			JobEnqueuer:       cfg.JobEnqueuer,
			Validator:         cfg.Validator,
			Renderer:          cfg.Renderer,
			PromptCompiler:    cfg.PromptCompiler,
			DailyRequestLimit: cfg.DailyRequestLimit,
			MonthlyTokenLimit: cfg.MonthlyTokenLimit,
			Logger:            cfg.Logger,
		})
	}

	mux := http.NewServeMux()

	registerHealthRoutes(mux)
	registerModelRoutes(mux, cfg)
	registerProfileRoutes(mux, cfg)
	registerPresetRoutes(mux, cfg)
	registerTemplateRoutes(mux, cfg)
	registerBranchRoutes(mux, cfg)
	registerQuotaRoutes(mux, cfg)

	provisioner := cfg.Provisioner
	if provisioner == nil && (cfg.Store != nil || cfg.ProfileStore != nil) {
		provisioner = &defaultTenantProvisioner{
			store:         cfg.Store,
			profileStore:  cfg.ProfileStore,
			branchStore:   cfg.BranchStore,
			templateStore: cfg.TemplateStore,
			userStore:     cfg.UserStore,
		}
	}

	// Wrap in middleware chain: Recovery -> Logging -> CORS -> Auth -> Timeout -> ContentType
	var handler http.Handler = mux
	handler = ContentTypeMiddleware(handler)
	handler = TimeoutMiddleware(cfg.RESTTimeout)(handler)

	if cfg.AuthProvider != nil {
		handler = AuthMiddlewareWithProvider(cfg.AuthProvider, provisioner)(handler)
	} else {
		handler = AuthMiddleware(cfg.Admin, cfg.DefaultUserID, cfg.TokenVerifier, provisioner)(handler)
	}

	handler = CORSMiddleware(handler)
	handler = LoggingMiddleware(cfg.Logger)(handler)
	handler = RecoveryMiddleware(cfg.Logger)(handler)

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
		var models []core.ModelOption
		if cfg.ModelEnumerator != nil {
			models = cfg.ModelEnumerator.AvailableModels()
		}
		if models == nil {
			models = []core.ModelOption{}
		}
		writeJSON(w, http.StatusOK, models)
	}

	mux.HandleFunc("GET /api/v1/models", modelsHandler)
	mux.HandleFunc("GET /models", modelsHandler)
}
