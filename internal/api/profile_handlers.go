package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"resumeee/internal/domain"
	"resumeee/internal/llm"
	"resumeee/internal/worker"
)

type profileHandler struct {
	cfg RouterConfig
}

// registerProfileRoutes registers all profile and career graph endpoints on the HTTP mux.
func registerProfileRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &profileHandler{cfg: cfg}

	// Main Profile
	mux.HandleFunc("GET /api/v1/profile", h.getProfile)
	mux.HandleFunc("PUT /api/v1/profile", h.updateProfile)
	mux.HandleFunc("GET /api/v1/profile/full", h.getFullProfile)
	mux.HandleFunc("POST /api/v1/profile/jsonresume/import", h.importJSONResume)
	mux.HandleFunc("GET /api/v1/profile/jsonresume/export", h.exportJSONResume)
	mux.HandleFunc("POST /api/v1/profile/pdf/parse", h.parsePDFResume)
	mux.HandleFunc("GET /api/v1/profile/pdf/jobs/{id}", h.getPDFParseJob)
	mux.HandleFunc("GET /profile/pdf/jobs/{id}", h.getPDFParseJob)
	mux.HandleFunc("GET /api/v1/profile/pdf/parse/{id}", h.getPDFParseJob)
	mux.HandleFunc("POST /api/v1/profile/pdf/commit", h.commitPDFResume)

	// Skills
	mux.HandleFunc("GET /api/v1/skills", h.listSkills)
	mux.HandleFunc("POST /api/v1/skills", h.createSkill)
	mux.HandleFunc("GET /api/v1/skills/{id}", h.getSkill)
	mux.HandleFunc("PUT /api/v1/skills/{id}", h.updateSkill)
	mux.HandleFunc("DELETE /api/v1/skills/{id}", h.deleteSkill)

	// Work Experiences
	mux.HandleFunc("GET /api/v1/experiences", h.listExperiences)
	mux.HandleFunc("POST /api/v1/experiences", h.createExperience)
	mux.HandleFunc("GET /api/v1/experiences/{id}", h.getExperience)
	mux.HandleFunc("PUT /api/v1/experiences/{id}", h.updateExperience)
	mux.HandleFunc("DELETE /api/v1/experiences/{id}", h.deleteExperience)

	// Projects
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("POST /api/v1/projects", h.createProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", h.getProject)
	mux.HandleFunc("PUT /api/v1/projects/{id}", h.updateProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", h.deleteProject)

	// Education
	mux.HandleFunc("GET /api/v1/education", h.listEducation)
	mux.HandleFunc("POST /api/v1/education", h.createEducation)
	mux.HandleFunc("GET /api/v1/education/{id}", h.getEducation)
	mux.HandleFunc("PUT /api/v1/education/{id}", h.updateEducation)
	mux.HandleFunc("DELETE /api/v1/education/{id}", h.deleteEducation)

	// Certifications
	mux.HandleFunc("GET /api/v1/certifications", h.listCertifications)
	mux.HandleFunc("POST /api/v1/certifications", h.createCertification)
	mux.HandleFunc("GET /api/v1/certifications/{id}", h.getCertification)
	mux.HandleFunc("PUT /api/v1/certifications/{id}", h.updateCertification)
	mux.HandleFunc("DELETE /api/v1/certifications/{id}", h.deleteCertification)

	// Facts
	mux.HandleFunc("GET /api/v1/facts", h.listFacts)
	mux.HandleFunc("POST /api/v1/facts", h.createFact)
	mux.HandleFunc("GET /api/v1/facts/{id}", h.getFact)
	mux.HandleFunc("PUT /api/v1/facts/{id}", h.updateFact)
	mux.HandleFunc("DELETE /api/v1/facts/{id}", h.deleteFact)
}

// --- Request DTOs ---

type UpdateProfileRequest struct {
	FullName           *string             `json:"full_name"`
	Headline           *string             `json:"headline"`
	Email              *string             `json:"email"`
	Phone              *string             `json:"phone"`
	Location           *domain.Location    `json:"location"`
	Profiles           []domain.SocialLink `json:"profiles"`
	Summary            *string             `json:"summary"`
	GlobalWritingRules *string             `json:"global_writing_rules"`
	DefaultProvider    *string             `json:"default_provider"`
	DefaultModel       *string             `json:"default_model"`
	Metadata           map[string]any      `json:"metadata"`
}

type CreateSkillRequest struct {
	ShortID         string         `json:"short_id,omitempty"`
	Name            string         `json:"name"`
	Category        string         `json:"category"`
	Proficiency     string         `json:"proficiency"`
	YearsExperience float64        `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

type UpdateSkillRequest struct {
	ShortID         *string        `json:"short_id,omitempty"`
	Name            *string        `json:"name"`
	Category        *string        `json:"category"`
	Proficiency     *string        `json:"proficiency"`
	YearsExperience *float64       `json:"years_experience"`
	Keywords        []string       `json:"keywords"`
	Metadata        map[string]any `json:"metadata"`
}

type CreateExperienceRequest struct {
	ShortID           string                `json:"short_id,omitempty"`
	Company           string                `json:"company"`
	RoleTitle         string                `json:"role_title"`
	EmploymentType    domain.EmploymentType `json:"employment_type"`
	StartDate         domain.Date           `json:"start_date"`
	EndDate           *domain.Date          `json:"end_date"`
	IsCurrent         bool                  `json:"is_current"`
	Location          domain.Location       `json:"location"`
	CompanyLocation   *domain.Location      `json:"company_location"`
	CompanyContact    *domain.ContactDetail `json:"company_contact"`
	HoursPerWeek      *int                  `json:"hours_per_week"`
	SupervisorName    string                `json:"supervisor_name"`
	SupervisorContact string                `json:"supervisor_contact"`
	DutiesSummary     string                `json:"duties_summary"`
	SkillIDs          []string              `json:"skill_ids"`
	Metadata          map[string]any        `json:"metadata"`
}

type UpdateExperienceRequest struct {
	ShortID           *string                `json:"short_id,omitempty"`
	Company           *string                `json:"company"`
	RoleTitle         *string                `json:"role_title"`
	EmploymentType    *domain.EmploymentType `json:"employment_type"`
	StartDate         *domain.Date           `json:"start_date"`
	EndDate           *domain.Date           `json:"end_date"`
	IsCurrent         *bool                  `json:"is_current"`
	Location          *domain.Location       `json:"location"`
	CompanyLocation   *domain.Location       `json:"company_location"`
	CompanyContact    *domain.ContactDetail  `json:"company_contact"`
	HoursPerWeek      *int                   `json:"hours_per_week"`
	SupervisorName    *string                `json:"supervisor_name"`
	SupervisorContact *string                `json:"supervisor_contact"`
	DutiesSummary     *string                `json:"duties_summary"`
	SkillIDs          []string               `json:"skill_ids"`
	Metadata          map[string]any         `json:"metadata"`
}

type CreateProjectRequest struct {
	ShortID   string         `json:"short_id,omitempty"`
	Name      string         `json:"name"`
	Role      string         `json:"role"`
	URL       string         `json:"url"`
	StartDate *domain.Date   `json:"start_date"`
	EndDate   *domain.Date   `json:"end_date"`
	Summary   string         `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

type UpdateProjectRequest struct {
	ShortID   *string        `json:"short_id,omitempty"`
	Name      *string        `json:"name"`
	Role      *string        `json:"role"`
	URL       *string        `json:"url"`
	StartDate *domain.Date   `json:"start_date"`
	EndDate   *domain.Date   `json:"end_date"`
	Summary   *string        `json:"summary"`
	SkillIDs  []string       `json:"skill_ids"`
	Metadata  map[string]any `json:"metadata"`
}

type CreateEducationRequest struct {
	ShortID     string          `json:"short_id,omitempty"`
	Institution string          `json:"institution"`
	Degree      string          `json:"degree"`
	Field       string          `json:"field"`
	StartDate   *domain.Date    `json:"start_date"`
	EndDate     *domain.Date    `json:"end_date"`
	Score       string          `json:"score"`
	Location    domain.Location `json:"location"`
	Metadata    map[string]any  `json:"metadata"`
}

type UpdateEducationRequest struct {
	ShortID     *string          `json:"short_id,omitempty"`
	Institution *string          `json:"institution"`
	Degree      *string          `json:"degree"`
	Field       *string          `json:"field"`
	StartDate   *domain.Date     `json:"start_date"`
	EndDate     *domain.Date     `json:"end_date"`
	Score       *string          `json:"score"`
	Location    *domain.Location `json:"location"`
	Metadata    map[string]any   `json:"metadata"`
}

type CreateCertificationRequest struct {
	ShortID  string         `json:"short_id,omitempty"`
	Name     string         `json:"name"`
	Issuer   string         `json:"issuer"`
	Date     *domain.Date   `json:"date"`
	URL      string         `json:"url"`
	Summary  string         `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

type UpdateCertificationRequest struct {
	ShortID  *string        `json:"short_id,omitempty"`
	Name     *string        `json:"name"`
	Issuer   *string        `json:"issuer"`
	Date     *domain.Date   `json:"date"`
	URL      *string        `json:"url"`
	Summary  *string        `json:"summary"`
	Metadata map[string]any `json:"metadata"`
}

type CreateFactRequest struct {
	ShortID   string           `json:"short_id,omitempty"`
	OwnerID   string           `json:"owner_id"`
	OwnerType domain.OwnerType `json:"owner_type"`
	Statement string           `json:"statement"`
	Impact    string           `json:"impact"`
	SkillIDs  []string         `json:"skill_ids"`
}

type UpdateFactRequest struct {
	ShortID   *string  `json:"short_id,omitempty"`
	Statement *string  `json:"statement"`
	Impact    *string  `json:"impact"`
	SkillIDs  []string `json:"skill_ids"`
}

// parseSkillsInput converts raw skill identifier/name strings from user payloads into domain.Skill instances.
// Tokens matching UUID formats (with or without prefixes) route to domain.Skill{ID: ...}.
// Plain text names (e.g. "Kubernetes", "Go") route to domain.Skill{Name: ...} for canonical resolution.
func parseSkillsInput(skillIDs []string) []domain.Skill {
	if len(skillIDs) == 0 {
		return []domain.Skill{}
	}
	skills := make([]domain.Skill, 0, len(skillIDs))
	for _, s := range skillIDs {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		rawUUID := strings.TrimPrefix(trimmed, "skill-")
		rawUUID = strings.TrimPrefix(rawUUID, "sk-")
		if _, err := uuid.Parse(rawUUID); err == nil {
			skills = append(skills, domain.Skill{ID: trimmed})
		} else {
			skills = append(skills, domain.Skill{Name: trimmed})
		}
	}
	return skills
}

// --- Main Profile Handlers ---

// getProfile retrieves basic profile fields for the authenticated user.
func (h *profileHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, prof)
}

// updateProfile validates and upserts the user's main profile record.
func (h *profileHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req UpdateProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			prof = &domain.Profile{
				ID:           "profile-" + userID,
				UserID:       userID,
				MainRevision: 0,
			}
		} else {
			writeDomainError(w, err)
			return
		}
	}

	if req.FullName != nil {
		prof.FullName = *req.FullName
	}
	if req.Headline != nil {
		prof.Headline = *req.Headline
	}
	if req.Email != nil {
		prof.Email = *req.Email
	}
	if req.Phone != nil {
		prof.Phone = *req.Phone
	}
	if req.Location != nil {
		prof.Location = *req.Location
	}
	if req.Profiles != nil {
		prof.Profiles = req.Profiles
	}
	if req.Summary != nil {
		prof.Summary = *req.Summary
	}
	if req.GlobalWritingRules != nil {
		prof.GlobalWritingRules = *req.GlobalWritingRules
	}
	if req.DefaultProvider != nil {
		prof.DefaultProvider = *req.DefaultProvider
	}
	if req.DefaultModel != nil {
		prof.DefaultModel = *req.DefaultModel
	}
	if req.Metadata != nil {
		prof.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpsertProfile(ctx, prof); err != nil {
		writeDomainError(w, err)
		return
	}

	updated, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// getFullProfile returns the full Career Graph snapshot for the authenticated user.
func (h *profileHandler) getFullProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	snap, err := h.cfg.ProfileRepo.GetSnapshot(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// importJSONResume imports an entire profile and career graph from raw JSON Resume bytes.
func (h *profileHandler) importJSONResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty JSON Resume payload", "")
		return
	}
	defer func() { _ = r.Body.Close() }()

	payload, err := io.ReadAll(r.Body)
	if err != nil || len(payload) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty or unreadable JSON Resume payload", "")
		return
	}

	var testMap map[string]any
	if err := json.Unmarshal(payload, &testMap); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed JSON payload", err.Error())
		return
	}

	snap, err := h.cfg.ProfileService.ImportJSONResume(ctx, userID, payload)
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid JSON Resume payload", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// exportJSONResume exports the user's main career graph as a standard JSON Resume document.
func (h *profileHandler) exportJSONResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var versionIDPtr *string
	if v := strings.TrimSpace(r.URL.Query().Get("version_id")); v != "" {
		versionIDPtr = &v
	}

	resume, err := h.cfg.ProfileService.ExportJSONResume(ctx, userID, versionIDPtr)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resume)
}

// DefaultPDFParseTimeout defines the timeout budget for parsing PDF resumes and extracting structured data with an LLM.
const DefaultPDFParseTimeout = 10 * time.Minute

// resolveExtractionProviderAndModel determines the active LLM provider and model for resume parsing.
// Explicit request values take precedence; omitted fields fall back to the authenticated user's main
// profile defaults, verified against configured models when ModelEnumerator is present.
func (h *profileHandler) resolveExtractionProviderAndModel(ctx context.Context, userID, reqProvider, reqModel string) (string, string) {
	provider := strings.TrimSpace(reqProvider)
	model := strings.TrimSpace(reqModel)

	if h.cfg.ProfileRepo == nil || (provider != "" && model != "") {
		return provider, model
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil || prof == nil {
		return provider, model
	}

	var availableModels []domain.ModelOption
	if h.cfg.ModelEnumerator != nil {
		availableModels = h.cfg.ModelEnumerator.AvailableModels()
	}

	isProviderConfigured := func(candidate string) bool {
		if len(availableModels) == 0 {
			return true
		}
		for _, opt := range availableModels {
			if strings.EqualFold(opt.Provider, candidate) ||
				(strings.EqualFold(candidate, "openai-compat") && strings.EqualFold(opt.Provider, "compat")) ||
				(strings.EqualFold(candidate, "compat") && strings.EqualFold(opt.Provider, "openai-compat")) {
				return true
			}
		}
		return false
	}

	profileProviderAccepted := false
	if provider == "" && strings.TrimSpace(prof.DefaultProvider) != "" {
		candidate := strings.TrimSpace(prof.DefaultProvider)
		if isProviderConfigured(candidate) {
			provider = candidate
			profileProviderAccepted = true
		}
	} else if provider != "" {
		if strings.EqualFold(provider, strings.TrimSpace(prof.DefaultProvider)) {
			profileProviderAccepted = true
		}
	}

	if model == "" && strings.TrimSpace(prof.DefaultModel) != "" {
		candidateModel := strings.TrimSpace(prof.DefaultModel)
		if len(availableModels) == 0 {
			if profileProviderAccepted || strings.TrimSpace(prof.DefaultProvider) == "" {
				model = candidateModel
			}
		} else {
			if profileProviderAccepted || strings.TrimSpace(prof.DefaultProvider) == "" {
				for _, opt := range availableModels {
					providerMatches := provider == "" || strings.EqualFold(opt.Provider, provider) ||
						(strings.EqualFold(provider, "openai-compat") && strings.EqualFold(opt.Provider, "compat")) ||
						(strings.EqualFold(provider, "compat") && strings.EqualFold(opt.Provider, "openai-compat"))

					if providerMatches && strings.EqualFold(opt.Model, candidateModel) {
						model = candidateModel
						break
					}
				}
				if model == "" && strings.TrimSpace(provider) != "" && strings.EqualFold(provider, strings.TrimSpace(prof.DefaultProvider)) {
					model = candidateModel
				}
			}
		}
	}

	return provider, model
}

// AsyncPDFJobResponse represents the acknowledgement payload returned when an asynchronous PDF parse job is accepted.
type AsyncPDFJobResponse struct {
	JobID   string                   `json:"job_id"`
	Status  domain.PDFParseJobStatus `json:"status"`
	Message string                   `json:"message"`
}

// reserveQuota reserves quota for an LLM operation, writing error responses directly and returning (reservationID, ok).
func (h *profileHandler) reserveQuota(ctx context.Context, w http.ResponseWriter, userID string, op domain.LLMOperationType, estimatedTokens, dailyLimit, monthlyLimit int) (string, bool) {
	if h.cfg.QuotaRepo == nil {
		return "", true
	}
	rec, qErr := h.cfg.QuotaRepo.ReserveQuota(ctx, userID, op, estimatedTokens, dailyLimit, monthlyLimit)
	if qErr != nil {
		if errors.Is(qErr, domain.ErrQuotaExceeded) {
			writeQuotaError(w, 3600, "llm operation quota exceeded", qErr.Error())
			return "", false
		}
		writeDomainError(w, qErr)
		return "", false
	}
	return rec.ID, true
}

// parsePDFResume parses an uploaded candidate resume PDF. By default, it operates asynchronously,
// enqueuing the extraction task and returning 202 Accepted with a job ID for status polling.
// If the request explicitly supplies ?sync=true, it executes synchronously for backward compatibility.
func (h *profileHandler) parsePDFResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 10 MB maximum upload size
	const maxUploadSize = 10 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf upload exceeded maximum size limit", "limit_bytes", maxUploadSize, "error", err)
			}
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "file exceeds maximum allowed size (10MB)", "")
			return
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "failed to parse multipart form for pdf upload", "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "file exceeds maximum allowed size (10MB) or invalid form data", err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, fileHeader, err := r.FormFile("file")
	if err != nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "missing required 'file' form field", "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "missing required 'file' form field", err.Error())
		return
	}
	defer func() { _ = file.Close() }()

	userID := getUserID(r, h.cfg.DefaultUserID)
	provider, model := h.resolveExtractionProviderAndModel(ctx, userID, r.FormValue("provider"), r.FormValue("model"))

	fileSize := fileHeader.Size
	if fileSize <= 0 {
		if h.cfg.Logger != nil {
			h.cfg.Logger.WarnContext(ctx, "empty file uploaded", "filename", fileHeader.Filename)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty file uploaded", "")
		return
	}

	data, err := io.ReadAll(file)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf upload exceeded maximum size limit", "limit_bytes", maxUploadSize, "error", err)
			}
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "file exceeds maximum allowed size (10MB)", "")
			return
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "failed to read uploaded file", "filename", fileHeader.Filename, "error", err)
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "failed to read uploaded file", err.Error())
		return
	}

	readerAt := bytes.NewReader(data)
	text, err := h.cfg.PDFExtractor.ExtractText(ctx, readerAt, int64(len(data)))
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			if h.cfg.Logger != nil {
				h.cfg.Logger.WarnContext(ctx, "pdf text extraction validation failed", "filename", fileHeader.Filename, "error", err)
			}
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return
		}
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "unexpected pdf extraction error", "filename", fileHeader.Filename, "error", err)
		}
		writeDomainError(w, err)
		return
	}

	if h.cfg.ResumeExtractor == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "LLM resume extractor not configured", "filename", fileHeader.Filename)
		}
		writeError(w, http.StatusInternalServerError, CodeInternalError, "LLM resume extractor not configured", "")
		return
	}

	// Determine execution mode: asynchronous by default when an enqueuer is configured
	// (or when ?async=true), synchronous when ?sync=true or when no worker queue is configured.
	runAsync := strings.EqualFold(r.URL.Query().Get("async"), "true") || (h.cfg.PDFJobEnqueuer != nil && !strings.EqualFold(r.URL.Query().Get("sync"), "true"))

	dailyLimit := h.cfg.DailyRequestLimit
	if dailyLimit <= 0 {
		dailyLimit = 50
	}
	monthlyLimit := h.cfg.MonthlyTokenLimit
	if monthlyLimit <= 0 {
		monthlyLimit = 1000000
	}

	// Synchronous execution path
	if !runAsync {
		reservationID, ok := h.reserveQuota(ctx, w, userID, domain.OperationPDFParse, 1000, dailyLimit, monthlyLimit)
		if !ok {
			return
		}

		llmTimeout := h.cfg.LLMTimeout
		if llmTimeout <= 0 {
			llmTimeout = DefaultPDFParseTimeout
		}
		syncCtx, cancel := context.WithTimeout(r.Context(), llmTimeout)
		defer cancel()

		extracted, usage, err := llm.ExtractResumeWithTokenUsage(syncCtx, h.cfg.ResumeExtractor, text, provider, model, 1000)
		if err != nil {
			if h.cfg.QuotaRepo != nil && reservationID != "" {
				_ = h.cfg.QuotaRepo.ReleaseQuota(ctx, reservationID, err.Error())
			}

			logLevel := slog.LevelError
			var (
				logMsg     string
				statusCode int
				errorCode  string
				respMsg    string
			)

			switch {
			case errors.Is(err, llm.ErrNoProviders):
				logMsg = "no LLM provider configured on server"
				statusCode = http.StatusServiceUnavailable
				errorCode = CodeServiceUnavailable
			case errors.Is(err, llm.ErrUnauthorized):
				logMsg = "upstream LLM provider authentication failed"
				statusCode = http.StatusBadGateway
				errorCode = CodeUpstreamError
			case errors.Is(err, llm.ErrRateLimited):
				logMsg = "upstream LLM provider rate limit exceeded"
				statusCode = http.StatusTooManyRequests
				errorCode = CodeUpstreamError
				respMsg = "upstream LLM provider rate limit exceeded; retry shortly"
			case errors.Is(err, llm.ErrUpstream):
				logMsg = "upstream LLM provider returned server error"
				statusCode = http.StatusBadGateway
				errorCode = CodeUpstreamError
			case errors.Is(err, llm.ErrInvalidResponse):
				logMsg = "upstream LLM provider returned invalid response"
				statusCode = http.StatusBadGateway
				errorCode = CodeUpstreamError
			case errors.Is(err, llm.ErrProviderNotFound):
				logLevel = slog.LevelWarn
				logMsg = "requested LLM provider not configured"
				statusCode = http.StatusBadRequest
				errorCode = CodeInvalidRequest
			case errors.Is(err, context.DeadlineExceeded):
				logMsg = "upstream LLM request timed out"
				statusCode = http.StatusGatewayTimeout
				errorCode = CodeUpstreamError
				respMsg = "upstream LLM parsing timed out after 10 minutes"
			case errors.Is(err, domain.ErrValidation):
				logLevel = slog.LevelWarn
				logMsg = "failed to extract structured resume validation error"
				statusCode = http.StatusBadRequest
				errorCode = CodeInvalidRequest
				respMsg = "failed to extract structured resume: " + err.Error()
			default:
				logMsg = "unexpected error during resume extraction"
			}

			if respMsg == "" {
				respMsg = logMsg
			}

			if h.cfg.Logger != nil {
				h.cfg.Logger.Log(ctx, logLevel, logMsg, "error", err, "provider", provider)
			}

			if statusCode > 0 {
				writeError(w, statusCode, errorCode, respMsg, "")
			} else {
				writeDomainError(w, err)
			}
			return
		}

		if h.cfg.QuotaRepo != nil && reservationID != "" {
			_ = h.cfg.QuotaRepo.CommitUsage(ctx, reservationID, usage)
		}

		if h.cfg.Logger != nil {
			h.cfg.Logger.InfoContext(ctx, "successfully parsed pdf resume synchronously", "filename", fileHeader.Filename, "provider", provider, "model", model)
		}
		writeJSON(w, http.StatusOK, extracted)
		return
	}

	// Asynchronous mode (default): enqueue and return 202 Accepted
	if h.cfg.PDFJobRepo == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "PDF job repository not configured", "")
		return
	}

	if h.cfg.PDFJobEnqueuer == nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.ErrorContext(ctx, "PDF worker queue not configured on server", "job_id", "")
		}
		writeError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, "PDF worker queue not configured on server", "")
		return
	}

	reservationID, ok := h.reserveQuota(ctx, w, userID, domain.OperationPDFParse, 1000, dailyLimit, monthlyLimit)
	if !ok {
		return
	}

	jobID := "pdf-job-" + uuid.New().String()
	now := time.Now().UTC()
	job := &domain.PDFParseJob{
		ID:        jobID,
		UserID:    userID,
		Status:    domain.PDFParseJobStatusPending,
		Filename:  fileHeader.Filename,
		Provider:  provider,
		Model:     model,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.cfg.PDFJobRepo.CreateJob(ctx, job); err != nil {
		if h.cfg.QuotaRepo != nil && reservationID != "" {
			_ = h.cfg.QuotaRepo.ReleaseQuota(ctx, reservationID, "failed to create PDF job")
		}
		writeDomainError(w, err)
		return
	}

	task := worker.PDFParseJobTask{
		JobID:         jobID,
		UserID:        userID,
		RawText:       text,
		Provider:      provider,
		Model:         model,
		ReservationID: reservationID,
	}

	if err := h.cfg.PDFJobEnqueuer.EnqueuePDFJob(task); err != nil {
		if h.cfg.QuotaRepo != nil && reservationID != "" {
			_ = h.cfg.QuotaRepo.ReleaseQuota(ctx, reservationID, "failed to enqueue PDF job")
		}
		if errors.Is(err, domain.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	if h.cfg.Logger != nil {
		h.cfg.Logger.InfoContext(ctx, "enqueued pdf resume parse job", "job_id", jobID, "filename", fileHeader.Filename)
	}

	writeJSON(w, http.StatusAccepted, AsyncPDFJobResponse{
		JobID:   jobID,
		Status:  domain.PDFParseJobStatusPending,
		Message: "PDF parsing job queued",
	})
}

// getPDFParseJob returns the status and candidate result of an asynchronous PDF resume parsing job.
func (h *profileHandler) getPDFParseJob(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	jobID := r.PathValue("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "job id is required", "")
		return
	}

	if h.cfg.PDFJobRepo == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "PDF job repository not configured", "")
		return
	}

	job, err := h.cfg.PDFJobRepo.GetJob(ctx, userID, jobID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "pdf parse job not found", "")
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, job)
}

// commitPDFResume commits approved JSON Resume data into the user's main career graph.
func (h *profileHandler) commitPDFResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty request body", "")
		return
	}
	defer func() { _ = r.Body.Close() }()

	payload, err := io.ReadAll(r.Body)
	if err != nil || len(payload) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "empty or unreadable payload", "")
		return
	}

	var testMap map[string]any
	if err := json.Unmarshal(payload, &testMap); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed JSON payload", err.Error())
		return
	}

	snap, err := h.cfg.ProfileService.ImportJSONResume(ctx, userID, payload)
	if err != nil {
		if errors.Is(err, domain.ErrValidation) {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid JSON Resume payload", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// --- Skills Handlers ---

// listSkills returns skills registered under the authenticated user, optionally filtered by origin type.
func (h *profileHandler) listSkills(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var (
		skills []domain.Skill
		err    error
	)

	skillType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	switch skillType {
	case "", "all":
		skills, err = h.cfg.ProfileRepo.ListSkills(ctx, userID)
	case "global":
		skills, err = h.cfg.ProfileRepo.ListGlobalSkills(ctx)
	case "custom":
		var allSkills []domain.Skill
		allSkills, err = h.cfg.ProfileRepo.ListSkills(ctx, userID)
		if err == nil {
			skills = make([]domain.Skill, 0, len(allSkills))
			for _, s := range allSkills {
				if !s.IsGlobal && s.UserID == userID {
					skills = append(skills, s)
				}
			}
		}
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid skill type filter: must be one of all, global, custom", "")
		return
	}

	if err != nil {
		writeDomainError(w, err)
		return
	}
	if skills == nil {
		skills = []domain.Skill{}
	}
	writeJSON(w, http.StatusOK, skills)
}

// createSkill handles POST /api/v1/skills requests, persisting a new Skill.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateSkillRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("skill")
	}

	skill := domain.Skill{
		ID:              domain.GenerateID("skill"),
		ShortID:         shortID,
		UserID:          userID,
		Name:            req.Name,
		Category:        req.Category,
		Proficiency:     req.Proficiency,
		YearsExperience: req.YearsExperience,
		Keywords:        req.Keywords,
		Metadata:        req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateSkill(ctx, &skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, skill)
}

// getSkill retrieves a single skill by ID.
func (h *profileHandler) getSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.ProfileRepo.GetSkill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

// updateSkill handles PUT/PATCH /api/v1/skills/{id} requests, updating skill attributes
// including optional short_id updates.
func (h *profileHandler) updateSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	skill, err := h.cfg.ProfileRepo.GetSkill(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateSkillRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		skill.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Name != nil {
		skill.Name = *req.Name
	}
	if req.Category != nil {
		skill.Category = *req.Category
	}
	if req.Proficiency != nil {
		skill.Proficiency = *req.Proficiency
	}
	if req.YearsExperience != nil {
		skill.YearsExperience = *req.YearsExperience
	}
	if req.Keywords != nil {
		skill.Keywords = req.Keywords
	}
	if req.Metadata != nil {
		skill.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateSkill(ctx, skill); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, skill)
}

// deleteSkill deletes a skill by ID.
func (h *profileHandler) deleteSkill(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteSkill(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Work Experiences Handlers ---

// listExperiences retrieves all work experience entries for the authenticated user.
func (h *profileHandler) listExperiences(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	exps, err := h.cfg.ProfileRepo.ListExperiences(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if exps == nil {
		exps = []domain.WorkExperience{}
	}
	writeJSON(w, http.StatusOK, exps)
}

// createExperience handles POST /api/v1/experiences requests, persisting a new WorkExperience.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateExperienceRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Company == "" || req.RoleTitle == "" || req.StartDate.IsZero() {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "company, role_title, and start_date are required", "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = parseSkillsInput(req.SkillIDs)
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("exp")
	}

	var endDate *domain.Date
	if req.EndDate != nil && !req.EndDate.IsZero() {
		endDate = req.EndDate
	}

	exp := domain.WorkExperience{
		ID:                domain.GenerateID("exp"),
		ShortID:           shortID,
		UserID:            userID,
		Company:           req.Company,
		RoleTitle:         req.RoleTitle,
		EmploymentType:    req.EmploymentType,
		StartDate:         req.StartDate,
		EndDate:           endDate,
		IsCurrent:         req.IsCurrent,
		Location:          req.Location,
		HoursPerWeek:      req.HoursPerWeek,
		SupervisorName:    req.SupervisorName,
		SupervisorContact: req.SupervisorContact,
		DutiesSummary:     req.DutiesSummary,
		Skills:            skills,
		Metadata:          req.Metadata,
	}
	if req.CompanyLocation != nil {
		exp.CompanyLocation = *req.CompanyLocation
	}
	if req.CompanyContact != nil {
		exp.CompanyContact = req.CompanyContact
	}

	if err := h.cfg.ProfileRepo.CreateExperience(ctx, &exp); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, exp)
}

// getExperience retrieves a specific work experience entry by its identifier.
func (h *profileHandler) getExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

// updateExperience handles PUT/PATCH /api/v1/experiences/{id} requests, updating experience attributes
// including optional short_id updates.
func (h *profileHandler) updateExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	exp, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateExperienceRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		exp.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Company != nil {
		exp.Company = *req.Company
	}
	if req.RoleTitle != nil {
		exp.RoleTitle = *req.RoleTitle
	}
	if req.EmploymentType != nil {
		exp.EmploymentType = *req.EmploymentType
	}
	if req.StartDate != nil {
		exp.StartDate = *req.StartDate
	}
	if req.EndDate != nil {
		if req.EndDate.IsZero() {
			exp.EndDate = nil
		} else {
			exp.EndDate = req.EndDate
		}
	}
	if req.IsCurrent != nil {
		exp.IsCurrent = *req.IsCurrent
	}
	if req.Location != nil {
		exp.Location = *req.Location
	}
	if req.CompanyLocation != nil {
		exp.CompanyLocation = *req.CompanyLocation
	}
	if req.CompanyContact != nil {
		if req.CompanyContact.Value == "" {
			exp.CompanyContact = nil
		} else {
			exp.CompanyContact = req.CompanyContact
		}
	}
	if req.HoursPerWeek != nil {
		if *req.HoursPerWeek <= 0 {
			exp.HoursPerWeek = nil
		} else {
			exp.HoursPerWeek = req.HoursPerWeek
		}
	}
	if req.SupervisorName != nil {
		exp.SupervisorName = *req.SupervisorName
	}
	if req.SupervisorContact != nil {
		exp.SupervisorContact = *req.SupervisorContact
	}
	if req.DutiesSummary != nil {
		exp.DutiesSummary = *req.DutiesSummary
	}
	if req.Metadata != nil {
		exp.Metadata = req.Metadata
	}
	if req.SkillIDs != nil {
		exp.Skills = parseSkillsInput(req.SkillIDs)
	}

	if err := h.cfg.ProfileRepo.UpdateExperience(ctx, exp); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, exp)
}

// deleteExperience removes a work experience entry by its identifier.
func (h *profileHandler) deleteExperience(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteExperience(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Projects Handlers ---

// listProjects returns all portfolio projects for the authenticated user.
func (h *profileHandler) listProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	projs, err := h.cfg.ProfileRepo.ListProjects(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if projs == nil {
		projs = []domain.Project{}
	}
	writeJSON(w, http.StatusOK, projs)
}

// createProject handles POST /api/v1/projects requests, persisting a new Project.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = parseSkillsInput(req.SkillIDs)
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("proj")
	}

	var startDate *domain.Date
	if req.StartDate != nil && !req.StartDate.IsZero() {
		startDate = req.StartDate
	}
	var endDate *domain.Date
	if req.EndDate != nil && !req.EndDate.IsZero() {
		endDate = req.EndDate
	}

	proj := domain.Project{
		ID:        domain.GenerateID("proj"),
		ShortID:   shortID,
		UserID:    userID,
		Name:      req.Name,
		Role:      req.Role,
		URL:       req.URL,
		StartDate: startDate,
		EndDate:   endDate,
		Summary:   req.Summary,
		Skills:    skills,
		Metadata:  req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateProject(ctx, &proj); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, proj)
}

// getProject retrieves a project record by its identifier.
func (h *profileHandler) getProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.ProfileRepo.GetProject(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

// updateProject handles PUT/PATCH /api/v1/projects/{id} requests, updating project attributes
// including optional short_id updates.
func (h *profileHandler) updateProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	proj, err := h.cfg.ProfileRepo.GetProject(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		proj.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Name != nil {
		proj.Name = *req.Name
	}
	if req.Role != nil {
		proj.Role = *req.Role
	}
	if req.URL != nil {
		proj.URL = *req.URL
	}
	if req.StartDate != nil {
		if req.StartDate.IsZero() {
			proj.StartDate = nil
		} else {
			proj.StartDate = req.StartDate
		}
	}
	if req.EndDate != nil {
		if req.EndDate.IsZero() {
			proj.EndDate = nil
		} else {
			proj.EndDate = req.EndDate
		}
	}
	if req.Summary != nil {
		proj.Summary = *req.Summary
	}
	if req.Metadata != nil {
		proj.Metadata = req.Metadata
	}
	if req.SkillIDs != nil {
		proj.Skills = parseSkillsInput(req.SkillIDs)
	}

	if err := h.cfg.ProfileRepo.UpdateProject(ctx, proj); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, proj)
}

// deleteProject removes a project entry by its identifier.
func (h *profileHandler) deleteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteProject(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Education Handlers ---

// listEducation returns all education records for the authenticated user.
func (h *profileHandler) listEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	edus, err := h.cfg.ProfileRepo.ListEducation(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if edus == nil {
		edus = []domain.Education{}
	}
	writeJSON(w, http.StatusOK, edus)
}

// createEducation handles POST /api/v1/education requests, persisting a new Education record.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateEducationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Institution == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "institution is required", "")
		return
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("edu")
	}

	var startDate *domain.Date
	if req.StartDate != nil && !req.StartDate.IsZero() {
		startDate = req.StartDate
	}
	var endDate *domain.Date
	if req.EndDate != nil && !req.EndDate.IsZero() {
		endDate = req.EndDate
	}

	edu := domain.Education{
		ID:          domain.GenerateID("edu"),
		ShortID:     shortID,
		UserID:      userID,
		Institution: req.Institution,
		Degree:      req.Degree,
		Field:       req.Field,
		StartDate:   startDate,
		EndDate:     endDate,
		Score:       req.Score,
		Location:    req.Location,
		Metadata:    req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateEducation(ctx, &edu); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, edu)
}

// getEducation retrieves an education record by its identifier.
func (h *profileHandler) getEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

// updateEducation handles PUT/PATCH /api/v1/education/{id} requests, updating education attributes
// including optional short_id updates.
func (h *profileHandler) updateEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	edu, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateEducationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		edu.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Institution != nil {
		edu.Institution = *req.Institution
	}
	if req.Degree != nil {
		edu.Degree = *req.Degree
	}
	if req.Field != nil {
		edu.Field = *req.Field
	}
	if req.StartDate != nil {
		if req.StartDate.IsZero() {
			edu.StartDate = nil
		} else {
			edu.StartDate = req.StartDate
		}
	}
	if req.EndDate != nil {
		if req.EndDate.IsZero() {
			edu.EndDate = nil
		} else {
			edu.EndDate = req.EndDate
		}
	}
	if req.Score != nil {
		edu.Score = *req.Score
	}
	if req.Location != nil {
		edu.Location = *req.Location
	}
	if req.Metadata != nil {
		edu.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateEducation(ctx, edu); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, edu)
}

// deleteEducation removes an education entry by its identifier.
func (h *profileHandler) deleteEducation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteEducation(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Certifications Handlers ---

// listCertifications returns all professional certifications recorded for the user.
func (h *profileHandler) listCertifications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	certs, err := h.cfg.ProfileRepo.ListCertifications(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if certs == nil {
		certs = []domain.Certification{}
	}
	writeJSON(w, http.StatusOK, certs)
}

// createCertification handles POST /api/v1/certifications requests, persisting a new Certification.
// It generates a canonical UUID and auto-generates a compact short_id unless explicitly provided by the caller.
func (h *profileHandler) createCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateCertificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name is required", "")
		return
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("cert")
	}

	var certDate *domain.Date
	if req.Date != nil && !req.Date.IsZero() {
		certDate = req.Date
	}

	cert := domain.Certification{
		ID:       domain.GenerateID("cert"),
		ShortID:  shortID,
		UserID:   userID,
		Name:     req.Name,
		Issuer:   req.Issuer,
		Date:     certDate,
		URL:      req.URL,
		Summary:  req.Summary,
		Metadata: req.Metadata,
	}

	if err := h.cfg.ProfileRepo.CreateCertification(ctx, &cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, cert)
}

// getCertification retrieves a certification record by its identifier.
func (h *profileHandler) getCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.ProfileRepo.GetCertification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

// updateCertification handles PUT/PATCH /api/v1/certifications/{id} requests, updating certification attributes
// including optional short_id updates.
func (h *profileHandler) updateCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	cert, err := h.cfg.ProfileRepo.GetCertification(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateCertificationRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		cert.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Name != nil {
		cert.Name = *req.Name
	}
	if req.Issuer != nil {
		cert.Issuer = *req.Issuer
	}
	if req.Date != nil {
		if req.Date.IsZero() {
			cert.Date = nil
		} else {
			cert.Date = req.Date
		}
	}
	if req.URL != nil {
		cert.URL = *req.URL
	}
	if req.Summary != nil {
		cert.Summary = *req.Summary
	}
	if req.Metadata != nil {
		cert.Metadata = req.Metadata
	}

	if err := h.cfg.ProfileRepo.UpdateCertification(ctx, cert); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, cert)
}

// deleteCertification deletes a certification record by its identifier.
func (h *profileHandler) deleteCertification(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteCertification(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Facts Handlers ---

// listFacts returns career facts associated with an entity specified by owner_type and owner_id query params.
func (h *profileHandler) listFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	ownerType := r.URL.Query().Get("owner_type")
	ownerID := r.URL.Query().Get("owner_id")

	if ownerType == "" || ownerID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_type and owner_id query parameters are required", "")
		return
	}

	facts, err := h.cfg.ProfileRepo.ListFactsByOwner(ctx, userID, ownerType, ownerID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if facts == nil {
		facts = []domain.Fact{}
	}
	writeJSON(w, http.StatusOK, facts)
}

// createFact handles POST /api/v1/facts requests, persisting a new child Fact.
// It generates a canonical UUID, auto-generates a short_id if omitted, and resolves owner_id to its canonical UUID.
func (h *profileHandler) createFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateFactRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.OwnerID == "" || req.OwnerType == "" || req.Statement == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "owner_id, owner_type, and statement are required", "")
		return
	}

	var canonicalOwnerID string
	switch req.OwnerType {
	case domain.OwnerTypeWorkExperience:
		exp, err := h.cfg.ProfileRepo.GetExperience(ctx, userID, req.OwnerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for work_experience", err.Error())
			return
		}
		canonicalOwnerID = exp.ID
	case domain.OwnerTypeProject:
		proj, err := h.cfg.ProfileRepo.GetProject(ctx, userID, req.OwnerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for project", err.Error())
			return
		}
		canonicalOwnerID = proj.ID
	case domain.OwnerTypeEducation:
		edu, err := h.cfg.ProfileRepo.GetEducation(ctx, userID, req.OwnerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid owner_id for education", err.Error())
			return
		}
		canonicalOwnerID = edu.ID
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("invalid owner_type: %q", req.OwnerType), "")
		return
	}

	var skills []domain.Skill
	if len(req.SkillIDs) > 0 {
		skills = parseSkillsInput(req.SkillIDs)
	}

	shortID := strings.TrimSpace(req.ShortID)
	if shortID == "" {
		shortID = domain.GenerateShortID("fact")
	}

	fact := domain.Fact{
		ID:        domain.GenerateID("fact"),
		ShortID:   shortID,
		UserID:    userID,
		OwnerID:   canonicalOwnerID,
		OwnerType: req.OwnerType,
		Statement: req.Statement,
		Impact:    req.Impact,
		Skills:    skills,
	}

	if err := h.cfg.ProfileRepo.CreateFact(ctx, &fact); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, fact)
}

// getFact retrieves an individual fact by its identifier.
func (h *profileHandler) getFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.ProfileRepo.GetFact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

// updateFact handles PUT/PATCH /api/v1/facts/{id} requests, updating fact attributes
// including optional short_id updates.
func (h *profileHandler) updateFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	fact, err := h.cfg.ProfileRepo.GetFact(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateFactRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.ShortID != nil && strings.TrimSpace(*req.ShortID) != "" {
		fact.ShortID = strings.TrimSpace(*req.ShortID)
	}
	if req.Statement != nil {
		fact.Statement = *req.Statement
	}
	if req.Impact != nil {
		fact.Impact = *req.Impact
	}
	if req.SkillIDs != nil {
		fact.Skills = parseSkillsInput(req.SkillIDs)
	}

	if err := h.cfg.ProfileRepo.UpdateFact(ctx, fact); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, fact)
}

// deleteFact removes a fact record by its identifier.
func (h *profileHandler) deleteFact(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.ProfileRepo.DeleteFact(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
