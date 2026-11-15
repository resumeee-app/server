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
// Handlers communicate with underlying domain services, persistence stores,
// and asynchronous worker pools via decoupled interfaces.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-core/pdf"
	"github.com/resumeee-app/resumeee-core/profile"
	"github.com/resumeee-app/resumeee-core/renderer"
	"github.com/resumeee-app/resumeee-core/worker"
)

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
	ProfileStore         core.ProfileStore
	CareerStore          core.CareerStore
	PresetStore          core.PresetStore
	TemplateStore        core.TemplateStore
	BranchStore          core.BranchStore
	PDFJobStore          core.PDFJobStore
	PDFJobEnqueuer       core.PDFJobEnqueuer
	QuotaStore           core.QuotaStore
	BranchService        BranchService
	ProfileService       ProfileService
	PDFExtractor         PDFTextExtractor
	ResumeExtractor      worker.ResumeExtractor
	Renderer             TemplateRenderer
	Validator            branch.ProvenanceValidator
	JobEnqueuer          branch.JobEnqueuer
	PromptCompiler       branch.PromptCompiler
	ModelEnumerator      core.ModelEnumerator
	DefaultUserID        string
	Admin                bool
	AuthProvider         auth.Provider
	UserStore            core.UserStore
	Provisioner          TenantProvisioner
	Logger               *slog.Logger
	RESTTimeout          time.Duration
	LLMTimeout           time.Duration
	PDFParseTimeout      time.Duration
	RenderPreviewTimeout time.Duration
	DailyRequestLimit    int
	MonthlyTokenLimit    int
}

// NewRouterConfigFromStore populates a RouterConfig's sub-store fields from a composite core.Store.
func NewRouterConfigFromStore(store core.Store) RouterConfig {
	if store == nil {
		return RouterConfig{}
	}
	return RouterConfig{
		ProfileStore:  store.Profiles(),
		CareerStore:   store.Career(),
		PresetStore:   store.Presets(),
		TemplateStore: store.Templates(),
		BranchStore:   store.Branches(),
		PDFJobStore:   store.PDFJobs(),
		QuotaStore:    store.Quotas(),
		UserStore:     store.Users(),
	}
}

// requestUserID extracts the tenant user ID strictly from the request context, falling back to defaultID if not in context.
func requestUserID(r *http.Request, defaultID string) string {
	if uid, err := core.UserIDFromContext(r.Context()); err == nil && uid != "" {
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
		cfg.RESTTimeout = config.DefaultRESTTimeout
	}
	if cfg.LLMTimeout <= 0 {
		cfg.LLMTimeout = config.DefaultLLMTimeout
	}
	if cfg.PDFParseTimeout <= 0 {
		cfg.PDFParseTimeout = config.DefaultPDFParseTimeout
	}
	if cfg.RenderPreviewTimeout <= 0 {
		cfg.RenderPreviewTimeout = config.DefaultRenderPreviewTimeout
	}
	if cfg.DailyRequestLimit <= 0 {
		cfg.DailyRequestLimit = config.DefaultLLMDailyRequestLimit
	}
	if cfg.MonthlyTokenLimit <= 0 {
		cfg.MonthlyTokenLimit = config.DefaultLLMMonthlyTokenLimit
	}
	if cfg.PDFJobStore == nil {
		cfg.PDFJobStore = memory.New()
	}
	if cfg.PDFExtractor == nil {
		cfg.PDFExtractor = pdf.NewExtractor()
	}
	if cfg.ResumeExtractor == nil {
		defaultExtractor := llm.NewRegistryExtractor(nil)
		cfg.ResumeExtractor = defaultExtractor
		if cfg.ModelEnumerator == nil {
			cfg.ModelEnumerator = defaultExtractor
		}
	}

	if cfg.PromptCompiler == nil {
		cfg.PromptCompiler = llm.NewPromptCompiler()
	}

	if cfg.ProfileService == nil && cfg.ProfileStore != nil && cfg.CareerStore != nil && cfg.BranchStore != nil {
		cfg.ProfileService = profile.New(cfg.ProfileStore, cfg.CareerStore, cfg.BranchStore)
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
	if provisioner == nil && cfg.ProfileStore != nil && cfg.BranchStore != nil && cfg.TemplateStore != nil {
		provisioner = &defaultTenantProvisioner{
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

	authProvider := cfg.AuthProvider
	if authProvider == nil && cfg.Admin {
		authProvider = auth.NewAdminProvider(cfg.DefaultUserID)
	}
	handler = AuthMiddlewareWithProvider(authProvider, provisioner)(handler)

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

// routeGuard pairs a dependency availability check with the 500 error message returned when unavailable.
type routeGuard struct {
	ok  bool
	msg string
}

// withRouteGuards wraps fn with precondition checks that fail closed with a 500 JSON error when dependencies are missing.
func withRouteGuards(fn http.HandlerFunc, guards ...routeGuard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for _, g := range guards {
			if !g.ok {
				writeError(w, http.StatusInternalServerError, CodeInternalError, g.msg, "")
				return
			}
		}
		fn(w, r)
	}
}
