package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/resumeee-app/core"
)

// profileHandler serves main profile, JSON Resume import/export, PDF parsing, and career graph entity endpoints.
type profileHandler struct {
	cfg RouterConfig
}

// registerProfileRoutes registers all profile and career graph endpoints on the HTTP mux.
func registerProfileRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &profileHandler{cfg: cfg}

	profileGuard := routeGuard{ok: cfg.ProfileStore != nil, msg: "profile store unavailable"}
	serviceGuard := routeGuard{ok: cfg.ProfileService != nil, msg: "profile service unavailable"}
	careerGuard := routeGuard{ok: cfg.CareerStore != nil, msg: "career store unavailable"}
	pdfJobGuard := routeGuard{ok: cfg.JobStore != nil, msg: "pdf job store unavailable"}

	// Main profile, JSON Resume, and PDF ingestion routes.
	mux.HandleFunc("GET /api/v1/profile", withRouteGuards(h.getProfile, profileGuard))
	mux.HandleFunc("PUT /api/v1/profile", withRouteGuards(h.updateProfile, profileGuard))
	mux.HandleFunc("GET /api/v1/profile/full", withRouteGuards(h.getFullProfile, profileGuard))
	mux.HandleFunc("POST /api/v1/profile/jsonresume/import", h.importJSONResume)
	mux.HandleFunc("GET /api/v1/profile/jsonresume/export", withRouteGuards(h.exportJSONResume, serviceGuard))
	mux.HandleFunc("POST /api/v1/profile/pdf/parse", h.parsePDFResume)
	mux.HandleFunc("GET /api/v1/profile/pdf/jobs/{id}", withRouteGuards(h.getPDFParseJob, pdfJobGuard))
	mux.HandleFunc("GET /profile/pdf/jobs/{id}", withRouteGuards(h.getPDFParseJob, pdfJobGuard))
	mux.HandleFunc("GET /api/v1/profile/pdf/parse/{id}", withRouteGuards(h.getPDFParseJob, pdfJobGuard))
	mux.HandleFunc("POST /api/v1/profile/pdf/commit", h.commitPDFResume)

	handleCareer := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, withRouteGuards(fn, careerGuard))
	}

	// Skills
	handleCareer("GET /api/v1/skills", h.listSkills)
	handleCareer("POST /api/v1/skills", h.createSkill)
	handleCareer("GET /api/v1/skills/{id}", h.getSkill)
	handleCareer("PUT /api/v1/skills/{id}", h.updateSkill)
	handleCareer("DELETE /api/v1/skills/{id}", h.deleteSkill)

	// Work Experiences
	handleCareer("GET /api/v1/experiences", h.listExperiences)
	handleCareer("POST /api/v1/experiences", h.createExperience)
	handleCareer("GET /api/v1/experiences/{id}", h.getExperience)
	handleCareer("PUT /api/v1/experiences/{id}", h.updateExperience)
	handleCareer("DELETE /api/v1/experiences/{id}", h.deleteExperience)

	// Projects
	handleCareer("GET /api/v1/projects", h.listProjects)
	handleCareer("POST /api/v1/projects", h.createProject)
	handleCareer("GET /api/v1/projects/{id}", h.getProject)
	handleCareer("PUT /api/v1/projects/{id}", h.updateProject)
	handleCareer("DELETE /api/v1/projects/{id}", h.deleteProject)

	// Education
	handleCareer("GET /api/v1/education", h.listEducation)
	handleCareer("POST /api/v1/education", h.createEducation)
	handleCareer("GET /api/v1/education/{id}", h.getEducation)
	handleCareer("PUT /api/v1/education/{id}", h.updateEducation)
	handleCareer("DELETE /api/v1/education/{id}", h.deleteEducation)

	// Certifications
	handleCareer("GET /api/v1/certifications", h.listCertifications)
	handleCareer("POST /api/v1/certifications", h.createCertification)
	handleCareer("GET /api/v1/certifications/{id}", h.getCertification)
	handleCareer("PUT /api/v1/certifications/{id}", h.updateCertification)
	handleCareer("DELETE /api/v1/certifications/{id}", h.deleteCertification)

	// Facts
	handleCareer("GET /api/v1/facts", h.listFacts)
	handleCareer("POST /api/v1/facts", h.createFact)
	handleCareer("GET /api/v1/facts/{id}", h.getFact)
	handleCareer("PUT /api/v1/facts/{id}", h.updateFact)
	handleCareer("DELETE /api/v1/facts/{id}", h.deleteFact)
}

// UpdateProfileRequest defines the partial JSON payload for updating a user's main profile.
type UpdateProfileRequest struct {
	FullName           *string           `json:"full_name"`
	Headline           *string           `json:"headline"`
	Email              *string           `json:"email"`
	Phone              *string           `json:"phone"`
	Location           *core.Location    `json:"location"`
	Profiles           []core.SocialLink `json:"profiles"`
	Summary            *string           `json:"summary"`
	GlobalWritingRules *string           `json:"global_writing_rules"`
	Metadata           map[string]any    `json:"metadata"`
}

// resolveEntityShortID returns the trimmed client shortID or generates a prefixed one when blank.
func resolveEntityShortID(raw, prefix string) string {
	shortID := strings.TrimSpace(raw)
	if shortID == "" {
		return core.GenerateShortID(prefix)
	}
	return shortID
}

// applyShortIDUpdate overwrites target with *shortID when shortID is non-nil and non-blank.
func applyShortIDUpdate(target *string, shortID *string) {
	if shortID != nil && strings.TrimSpace(*shortID) != "" {
		*target = strings.TrimSpace(*shortID)
	}
}

// optionalDatePtr returns d if non-nil and non-zero, or nil otherwise.
func optionalDatePtr(d *core.Date) *core.Date {
	if d == nil || d.IsZero() {
		return nil
	}
	return d
}

// applyOptionalDate updates *target from src when src is non-nil, clearing *target when src is zero.
func applyOptionalDate(target **core.Date, src *core.Date) {
	if src == nil {
		return
	}
	if src.IsZero() {
		*target = nil
		return
	}
	*target = src
}

// ensureProfileService checks that ProfileService is configured and writes a 500 JSON error otherwise.
func (h *profileHandler) ensureProfileService(w http.ResponseWriter) bool {
	if h.cfg.ProfileService == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "profile service unavailable", "")
		return false
	}
	return true
}

// getProfile retrieves basic profile fields for the authenticated user.
func (h *profileHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	prof, err := h.cfg.ProfileStore.Profile(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, prof)
}

// updateProfile validates and upserts the user's main profile record.
func (h *profileHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req UpdateProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	prof, err := h.cfg.ProfileStore.Profile(ctx, userID)
	if err != nil {
		if !errors.Is(err, core.ErrNotFound) {
			writeDomainError(w, h.cfg.Logger, err)
			return
		}
		prof = &core.Profile{
			ID:           "profile-" + userID,
			UserID:       userID,
			MainRevision: 0,
		}
	}

	applyProfileUpdates(prof, req)

	if err := h.cfg.ProfileStore.UpsertProfile(ctx, prof); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	updated, err := h.cfg.ProfileStore.Profile(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// applyProfileUpdates copies non-nil scalar and slice fields from req onto prof.
func applyProfileUpdates(prof *core.Profile, req UpdateProfileRequest) {
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
	if req.Metadata != nil {
		prof.Metadata = req.Metadata
	}
}

// getFullProfile returns the full Career Graph snapshot for the authenticated user.
func (h *profileHandler) getFullProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	snap, err := h.cfg.ProfileStore.Snapshot(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// importJSONResume imports an entire profile and career graph from raw JSON Resume bytes.
func (h *profileHandler) importJSONResume(w http.ResponseWriter, r *http.Request) {
	h.handleJSONResumeImport(w, r, "empty JSON Resume payload", "empty or unreadable JSON Resume payload")
}

// handleJSONResumeImport validates raw JSON Resume request bytes and imports them via ProfileService.
func (h *profileHandler) handleJSONResumeImport(w http.ResponseWriter, r *http.Request, emptyMsg, unreadableMsg string) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, emptyMsg, "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	defer func() { _ = r.Body.Close() }()

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "request body exceeds maximum allowed size", maxBytesErr.Error())
			return
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, unreadableMsg, "")
		return
	}
	if len(payload) == 0 {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, unreadableMsg, "")
		return
	}

	var testMap map[string]any
	if err := json.Unmarshal(payload, &testMap); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed JSON payload", err.Error())
		return
	}

	if !h.ensureProfileService(w) {
		return
	}

	snap, err := h.cfg.ProfileService.ImportJSONResume(ctx, userID, payload)
	if err != nil {
		if errors.Is(err, core.ErrValidation) {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid JSON Resume payload", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, snap)
}

// exportJSONResume exports the user's main career graph as a standard JSON Resume document.
func (h *profileHandler) exportJSONResume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var versionIDPtr *string
	if v := strings.TrimSpace(r.URL.Query().Get("version_id")); v != "" {
		versionIDPtr = &v
	}

	resume, err := h.cfg.ProfileService.ExportJSONResume(ctx, userID, versionIDPtr)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, resume)
}
