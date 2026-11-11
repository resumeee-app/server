package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-api/internal/pdf"
	"github.com/resumeee-app/resumeee-core/services/profile"
	"github.com/resumeee-app/resumeee-core/validator"
	"github.com/resumeee-app/resumeee-core/services/worker"
)

// JobEnqueuer defines the interface for submitting version jobs to the async generation worker pool.
type JobEnqueuer = worker.JobEnqueuer

// PDFJobEnqueuer defines the interface for submitting PDF parse tasks to the async worker pool.
type PDFJobEnqueuer = worker.PDFJobEnqueuer

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

// Push sends an item to the buffered channel or returns domain.ErrQueueFull if saturated.
func (q *ChannelQueue[T]) Push(item T) error {
	select {
	case q.ch <- item:
		return nil
	default:
		return domain.ErrQueueFull
	}
}

// Channel exposes the underlying receive channel for background consumers.
func (q *ChannelQueue[T]) Channel() <-chan T {
	return q.ch
}

// ChannelJobEnqueuer implements JobEnqueuer backed by ChannelQueue[worker.GenerationJob].
type ChannelJobEnqueuer struct {
	queue *ChannelQueue[worker.GenerationJob]
}

// NewChannelJobEnqueuer creates a new ChannelJobEnqueuer with the given buffer capacity.
func NewChannelJobEnqueuer(capacity int) *ChannelJobEnqueuer {
	return &ChannelJobEnqueuer{
		queue: NewChannelQueue[worker.GenerationJob](capacity),
	}
}

// Enqueue sends a GenerationJob to the buffered channel or returns domain.ErrQueueFull if saturated.
func (e *ChannelJobEnqueuer) Enqueue(job worker.GenerationJob) error {
	return e.queue.Push(job)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelJobEnqueuer) Channel() <-chan worker.GenerationJob {
	return e.queue.Channel()
}

// ChannelPDFJobEnqueuer implements PDFJobEnqueuer backed by ChannelQueue[worker.PDFParseJobTask].
type ChannelPDFJobEnqueuer struct {
	queue *ChannelQueue[worker.PDFParseJobTask]
}

// NewChannelPDFJobEnqueuer creates a new ChannelPDFJobEnqueuer with the given buffer capacity.
func NewChannelPDFJobEnqueuer(capacity int) *ChannelPDFJobEnqueuer {
	return &ChannelPDFJobEnqueuer{
		queue: NewChannelQueue[worker.PDFParseJobTask](capacity),
	}
}

// EnqueuePDFJob sends a PDFParseJobTask to the buffered channel or returns domain.ErrQueueFull if saturated.
func (e *ChannelPDFJobEnqueuer) EnqueuePDFJob(task worker.PDFParseJobTask) error {
	return e.queue.Push(task)
}

// Channel exposes the underlying receive channel for background workers.
func (e *ChannelPDFJobEnqueuer) Channel() <-chan worker.PDFParseJobTask {
	return e.queue.Channel()
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
	PDFJobRepo      domain.PDFParseJobRepository
	PDFJobEnqueuer  PDFJobEnqueuer
	QuotaRepo       domain.QuotaRepository
	BranchService   branch.Service
	ProfileService  profile.Service
	PDFExtractor    pdf.PDFTextExtractor
	ResumeExtractor llm.ResumeExtractor
	Renderer        domain.TemplateRenderer
	Validator       validator.ProvenanceValidator
	JobEnqueuer     JobEnqueuer
	PromptCompiler  domain.PromptCompiler
	ModelEnumerator ModelEnumerator
	DefaultUserID     string
	Admin             bool
	AuthProvider      auth.Provider
	TokenVerifier     domain.TokenVerifier
	FirebaseProjectID string
	UserRepo          domain.UserRepository
	Store             domain.Store
	Provisioner       TenantProvisioner
	Logger            *slog.Logger
	RESTTimeout       time.Duration
	LLMTimeout        time.Duration
	DailyRequestLimit int
	MonthlyTokenLimit int
}

type defaultTenantProvisioner struct {
	store        domain.Store
	profileRepo  domain.ProfileRepository
	branchRepo   domain.BranchRepository
	templateRepo domain.TemplateRepository
	userRepo     domain.UserRepository
	provisioned  sync.Map
}

func (p *defaultTenantProvisioner) EnsureTenantProvisioned(ctx context.Context, userID string, userInfo *domain.User) error {
	if _, ok := p.provisioned.Load(userID); ok {
		return nil
	}
	var err error
	if p.store != nil {
		err = memory.EnsureTenantProvisioned(ctx, p.store, userID, userInfo)
	} else if p.profileRepo != nil && p.branchRepo != nil && p.templateRepo != nil {
		err = memory.EnsureTenantProvisionedRepos(ctx, p.profileRepo, p.branchRepo, p.templateRepo, p.userRepo, userID, userInfo)
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
	if cfg.PDFJobRepo == nil {
		cfg.PDFJobRepo = NewInMemoryPDFJobRepository()
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

	if cfg.PromptCompiler == nil {
		cfg.PromptCompiler = llm.NewPromptCompiler()
	}

	if cfg.BranchService == nil && cfg.BranchRepo != nil {
		cfg.BranchService = branch.NewService(branch.Config{
			BranchRepo:        cfg.BranchRepo,
			ProfileRepo:       cfg.ProfileRepo,
			PresetRepo:        cfg.PresetRepo,
			TemplateRepo:      cfg.TemplateRepo,
			QuotaRepo:         cfg.QuotaRepo,
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
	if provisioner == nil && (cfg.Store != nil || cfg.ProfileRepo != nil) {
		provisioner = &defaultTenantProvisioner{
			store:        cfg.Store,
			profileRepo:  cfg.ProfileRepo,
			branchRepo:   cfg.BranchRepo,
			templateRepo: cfg.TemplateRepo,
			userRepo:     cfg.UserRepo,
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
