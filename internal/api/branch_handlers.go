package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/resumeee-app/resumeee-core/services/branch"
	"github.com/resumeee-app/resumeee-core/domain"
	"github.com/resumeee-app/resumeee-core/services/renderer"
)

type branchHandler struct {
	cfg RouterConfig
	svc branch.Service
}

// registerBranchRoutes binds REST endpoints for branch/resume creation, modification, version generation, preview rendering, and exports.
func registerBranchRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &branchHandler{
		cfg: cfg,
		svc: cfg.BranchService,
	}

	bases := []string{"/api/v1/branches", "/api/v1/resumes"}
	for _, base := range bases {
		mux.HandleFunc("GET "+base, h.listBranches)
		mux.HandleFunc("POST "+base, h.createBranch)
		mux.HandleFunc("POST "+base+"/sync", h.syncBranches)
		mux.HandleFunc("GET "+base+"/{id}", h.getBranch)
		mux.HandleFunc("PUT "+base+"/{id}", h.updateBranch)
		mux.HandleFunc("DELETE "+base+"/{id}", h.deleteBranch)
		mux.HandleFunc("POST "+base+"/{id}/generate", h.generateBranchVersion)
		mux.HandleFunc("POST "+base+"/{id}/tailor", h.generateBranchVersion)
		mux.HandleFunc("POST "+base+"/{id}/gap-analysis", h.analyzeBranchGaps)
		mux.HandleFunc("POST "+base+"/{id}/ats-score", h.simulateBranchATSScore)
		mux.HandleFunc("GET "+base+"/{id}/prompt-preview", h.getPromptPreview)
		mux.HandleFunc("POST "+base+"/{id}/prompt-preview", h.previewPrompt)
		mux.HandleFunc("GET "+base+"/{id}/versions", h.listBranchVersions)
		mux.HandleFunc("GET "+base+"/{id}/versions/{v}", h.getBranchVersion)
		mux.HandleFunc("GET "+base+"/{id}/versions/{v}/preview", h.previewBranchVersion)
		mux.HandleFunc("GET "+base+"/{id}/versions/{v}/export", h.exportBranchVersion)
		mux.HandleFunc("POST "+base+"/{id}/versions/{v}/edit", h.editBranchVersion)
		mux.HandleFunc("POST "+base+"/{id}/versions/{v}/render-preview", h.renderPreview)
		mux.HandleFunc("POST "+base+"/{id}/versions/{v}/ats-score", h.simulateBranchATSScore)
	}
}

type CreateBranchRequest struct {
	Name                string   `json:"name"`
	IsDefault           *bool    `json:"is_default,omitempty"`
	TargetRole          string   `json:"target_role"`
	TargetCompany       string   `json:"target_company"`
	TargetRequirements  string   `json:"target_requirements"`
	BranchInstructions  string   `json:"branch_instructions"`
	CustomDirectives    string   `json:"custom_directives"`
	PresetID            *string  `json:"preset_id"`
	TailoringStrategyID *string  `json:"tailoring_strategy_id"`
	TemplateSetID       string   `json:"template_set_id"`
	ProviderOverride    string   `json:"provider_override"`
	ModelOverride       string   `json:"model_override"`
	ReasoningLevel      string   `json:"reasoning_level,omitempty"`
	PinnedEntityIDs     []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs   []string `json:"excluded_entity_ids"`
}

type UpdateBranchRequest struct {
	Name                *string  `json:"name"`
	TargetRole          *string  `json:"target_role"`
	TargetCompany       *string  `json:"target_company"`
	TargetRequirements  *string  `json:"target_requirements"`
	BranchInstructions  *string  `json:"branch_instructions"`
	CustomDirectives    *string  `json:"custom_directives"`
	PresetID            *string  `json:"preset_id"`
	TailoringStrategyID *string  `json:"tailoring_strategy_id"`
	TemplateSetID       *string  `json:"template_set_id"`
	ProviderOverride    *string  `json:"provider_override"`
	ModelOverride       *string  `json:"model_override"`
	ReasoningLevel      *string  `json:"reasoning_level,omitempty"`
	PinnedEntityIDs     []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs   []string `json:"excluded_entity_ids"`
	ActiveVersionID     *string  `json:"active_version_id"`
}

type RenderPreviewRequest struct {
	TemplateSetID string `json:"template_set_id"`
}

type AsyncJobEnqueued struct {
	VersionID string               `json:"version_id"`
	Status    domain.VersionStatus `json:"status"`
	Message   string               `json:"message"`
}

type SyncBranchesRequest struct {
	BranchIDs []string `json:"branch_ids"`
}

type SyncBranchesResponse struct {
	EnqueuedCount int      `json:"enqueued_count"`
	VersionIDs    []string `json:"version_ids"`
}

type EditVersionRequest struct {
	TailoredContent *domain.TailoredContent `json:"tailored_content"`
}

type PromptPreviewRequest struct {
	TargetRole         *string  `json:"target_role,omitempty"`
	TargetCompany      *string  `json:"target_company,omitempty"`
	TargetRequirements *string  `json:"target_requirements,omitempty"`
	BranchInstructions *string  `json:"branch_instructions,omitempty"`
	PresetID           *string  `json:"preset_id,omitempty"`
	PinnedEntityIDs    []string `json:"pinned_entity_ids,omitempty"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids,omitempty"`
}

// listBranches retrieves all branches for the authenticated user, optionally filtering for stale branches.
func (h *branchHandler) listBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	staleOnly := strings.EqualFold(r.URL.Query().Get("stale"), "true")

	branches, err := h.svc.ListBranches(ctx, userID, staleOnly)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, branches)
}

// createBranch validates and registers a new resume branch.
func (h *branchHandler) createBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateBranchRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" || req.TemplateSetID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name and template_set_id are required", "")
		return
	}

	if req.IsDefault != nil && *req.IsDefault {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "custom default branches cannot be created", "")
		return
	}

	if h.cfg.TemplateRepo != nil {
		tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, req.TemplateSetID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn) {
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent template", "")
			return
		}
	}

	stratID := req.TailoringStrategyID
	if stratID == nil && req.PresetID != nil {
		stratID = req.PresetID
	}
	if stratID != nil && *stratID != "" && h.cfg.PresetRepo != nil {
		preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, *stratID)
		if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn()) {
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent tailoring strategy", "")
			return
		}
	}

	isAdmin := h.cfg.Admin || IsAdminFromContext(r.Context())
	if !isAdmin && (req.ProviderOverride != "" || req.ModelOverride != "" || req.ReasoningLevel != "") {
		writeError(w, http.StatusForbidden, CodeForbidden, "model and reasoning selection are restricted to administrators", "")
		return
	}

	if err := validateReasoningLevel(req.ReasoningLevel); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}
	if err := validateModelOverride(h.cfg.ModelEnumerator, req.ProviderOverride, req.ModelOverride); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}

	directives := req.CustomDirectives
	if directives == "" && req.BranchInstructions != "" {
		directives = req.BranchInstructions
	}

	b := &domain.Branch{
		UserID:                   userID,
		Name:                     req.Name,
		TargetRole:               req.TargetRole,
		TargetCompany:            req.TargetCompany,
		TargetRequirements:       req.TargetRequirements,
		CustomDirectives:         directives,
		BranchInstructions:       directives,
		TailoringStrategyID:      stratID,
		PresetID:                 stratID,
		TemplateSetID:            req.TemplateSetID,
		ProviderOverride:         req.ProviderOverride,
		ModelOverride:            req.ModelOverride,
		ReasoningLevel:           req.ReasoningLevel,
		PinnedEntityIDs:          req.PinnedEntityIDs,
		ExcludedEntityIDs:        req.ExcludedEntityIDs,
	}

	created, err := h.svc.CreateBranch(ctx, b)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// getBranch retrieves a branch record by its identifier.
func (h *branchHandler) getBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	b, err := h.svc.GetBranch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, b)
}

// updateBranch modifies fields of an existing branch.
func (h *branchHandler) updateBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	branch, err := h.svc.GetBranch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdateBranchRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name != nil {
		branch.Name = *req.Name
	}
	if req.TargetRole != nil {
		branch.TargetRole = *req.TargetRole
	}
	if req.TargetCompany != nil {
		branch.TargetCompany = *req.TargetCompany
	}
	if req.TargetRequirements != nil {
		branch.TargetRequirements = *req.TargetRequirements
	}
	if req.CustomDirectives != nil {
		branch.CustomDirectives = *req.CustomDirectives
		branch.BranchInstructions = *req.CustomDirectives
	} else if req.BranchInstructions != nil {
		branch.BranchInstructions = *req.BranchInstructions
		branch.CustomDirectives = *req.BranchInstructions
	}
	stratIDToValidate := req.TailoringStrategyID
	if stratIDToValidate == nil {
		stratIDToValidate = req.PresetID
	}
	if stratIDToValidate != nil && *stratIDToValidate != "" && h.cfg.PresetRepo != nil {
		preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, *stratIDToValidate)
		if err != nil || preset == nil || (preset.UserID != userID && !preset.IsBuiltIn()) {
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent tailoring strategy", "")
			return
		}
	}

	if req.TemplateSetID != nil && *req.TemplateSetID != "" && h.cfg.TemplateRepo != nil {
		tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, *req.TemplateSetID)
		if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn) {
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent template", "")
			return
		}
	}

	isAdmin := h.cfg.Admin || IsAdminFromContext(r.Context())
	if !isAdmin && (req.ProviderOverride != nil || req.ModelOverride != nil || req.ReasoningLevel != nil) {
		writeError(w, http.StatusForbidden, CodeForbidden, "model and reasoning selection are restricted to administrators", "")
		return
	}

	effectiveProvider := branch.ProviderOverride
	if req.ProviderOverride != nil {
		effectiveProvider = *req.ProviderOverride
	}
	effectiveModel := branch.ModelOverride
	if req.ModelOverride != nil {
		effectiveModel = *req.ModelOverride
	}

	if req.ReasoningLevel != nil {
		if err := validateReasoningLevel(*req.ReasoningLevel); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return
		}
	}

	if req.ProviderOverride != nil || req.ModelOverride != nil {
		if err := validateModelOverride(h.cfg.ModelEnumerator, effectiveProvider, effectiveModel); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return
		}
	}

	if req.TailoringStrategyID != nil {
		branch.TailoringStrategyID = req.TailoringStrategyID
		branch.PresetID = req.TailoringStrategyID
	} else if req.PresetID != nil {
		branch.PresetID = req.PresetID
		branch.TailoringStrategyID = req.PresetID
	}
	if req.TemplateSetID != nil {
		branch.TemplateSetID = *req.TemplateSetID
	}
	if req.ProviderOverride != nil {
		branch.ProviderOverride = *req.ProviderOverride
	}
	if req.ModelOverride != nil {
		branch.ModelOverride = *req.ModelOverride
	}
	if req.ReasoningLevel != nil {
		branch.ReasoningLevel = *req.ReasoningLevel
	}
	if req.PinnedEntityIDs != nil {
		branch.PinnedEntityIDs = req.PinnedEntityIDs
	}
	if req.ExcludedEntityIDs != nil {
		branch.ExcludedEntityIDs = req.ExcludedEntityIDs
	}
	if req.ActiveVersionID != nil {
		branch.ActiveVersionID = req.ActiveVersionID
	}

	updated, err := h.svc.UpdateBranch(ctx, branch)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// deleteBranch removes a branch and associated versions.
func (h *branchHandler) deleteBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.svc.DeleteBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// generateBranchVersion enqueues or renders a new resume version for the specified branch.
func (h *branchHandler) generateBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	ver, err := h.svc.GenerateVersion(ctx, userID, id)
	if err != nil {
		if errors.Is(err, domain.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	if ver.Status == domain.VersionStatusCompleted {
		writeJSON(w, http.StatusOK, AsyncJobEnqueued{
			VersionID: ver.ID,
			Status:    ver.Status,
			Message:   "Resume rendered from main profile",
		})
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncJobEnqueued{
		VersionID: ver.ID,
		Status:    ver.Status,
		Message:   "Resume generation job queued",
	})
}

// syncBranches enqueues regeneration jobs for stale branches matching the filter.
func (h *branchHandler) syncBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req SyncBranchesRequest
	if r.Body != nil && r.ContentLength > 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	enqueuedIDs, err := h.svc.SyncBranches(ctx, userID, req.BranchIDs)
	if err != nil {
		if errors.Is(err, domain.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, SyncBranchesResponse{
		EnqueuedCount: len(enqueuedIDs),
		VersionIDs:    enqueuedIDs,
	})
}

// listBranchVersions returns all generated versions belonging to a branch.
func (h *branchHandler) listBranchVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if _, err := h.svc.GetBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	versions, err := h.svc.ListVersions(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// getBranchVersion retrieves a specific branch version by version number.
func (h *branchHandler) getBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	v := r.PathValue("v")

	if _, err := h.svc.GetBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	ver, err := h.svc.GetVersion(ctx, id, v)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, ver)
}

// getOrRenderVersionOutputs resolves or dynamically re-renders resume outputs incorporating targetTemplateID.
func (h *branchHandler) getOrRenderVersionOutputs(ctx context.Context, userID, _, _ string, b *domain.Branch, ver *domain.BranchVersion, targetTemplateID string) domain.RenderOutput {
	return renderer.ResolveVersionOutputs(ctx, h.cfg.Renderer, h.cfg.TemplateRepo, h.cfg.ProfileRepo, userID, b, ver, targetTemplateID)
}

// exportBranchVersion renders and outputs a branch version in html, markdown, text, or jsonresume format.
func (h *branchHandler) exportBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	b, err := h.svc.GetBranch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	ver, err := h.svc.GetVersion(ctx, branchID, v)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "html"
	}

	targetTemplateID := strings.TrimSpace(r.URL.Query().Get("template_set_id"))
	outputs := h.getOrRenderVersionOutputs(ctx, userID, branchID, v, b, ver, targetTemplateID)

	switch format {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(outputs.HTML))
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(outputs.Markdown))
	case "text", "txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(outputs.PlainText))
	case "jsonresume":
		if h.cfg.ProfileService == nil {
			writeError(w, http.StatusInternalServerError, CodeInternalError, "profile service unavailable", "")
			return
		}
		res, err := h.cfg.ProfileService.ExportJSONResume(ctx, userID, &ver.ID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	default:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "unsupported export format: "+format, "")
	}
}

// previewBranchVersion serves rendered branch version resume outputs across HTML, Markdown, and text formats.
func (h *branchHandler) previewBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	b, err := h.svc.GetBranch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	ver, err := h.svc.GetVersion(ctx, branchID, v)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	targetTemplateID := strings.TrimSpace(r.URL.Query().Get("template_set_id"))
	outputs := h.getOrRenderVersionOutputs(ctx, userID, branchID, v, b, ver, targetTemplateID)

	writeRenderOutput(w, r, outputs)
}

// editBranchVersion validates provenance and creates an edited resume version with updated tailored content.
func (h *branchHandler) editBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	var req EditVersionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.TailoredContent == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "tailored_content is required", "")
		return
	}

	newVer, err := h.svc.EditVersion(ctx, userID, branchID, v, *req.TailoredContent)
	if err != nil {
		if errors.Is(err, domain.ErrProvenance) {
			writeError(w, http.StatusUnprocessableEntity, CodeProvenanceError, "provenance validation failed", err.Error())
			return
		}
		if errors.Is(err, domain.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template rendering failed", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, newVer)
}

// analyzeBranchGaps executes decoupled fast gap analysis against the branch's target requirements.
func (h *branchHandler) analyzeBranchGaps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	if strings.TrimSpace(branchID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	report, err := h.svc.AnalyzeGaps(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// simulateBranchATSScore executes on-demand ATS extraction and job matching score calculation for a tailored branch version.
func (h *branchHandler) simulateBranchATSScore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	if strings.TrimSpace(branchID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	var opts domain.ATSSimulationOptions
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		if !decodeJSON(w, r, &opts) {
			return
		}
	}
	if vPath := strings.TrimSpace(r.PathValue("v")); vPath != "" && opts.VersionID == "" {
		opts.VersionID = vPath
	}

	score, err := h.svc.SimulateATS(ctx, userID, branchID, &opts)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, score)
}

// requireAdmin verifies administrative privilege, writing a 403 Forbidden response if unauthorized.
func (h *branchHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !h.cfg.Admin && !IsAdminFromContext(r.Context()) {
		writeError(w, http.StatusForbidden, CodeForbidden, "LLM prompt preview is restricted to administrators", "")
		return false
	}
	return true
}

// getPromptPreview renders the system and user prompt strings using the branch's saved state.
func (h *branchHandler) getPromptPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	preview, err := h.svc.GetPromptPreview(ctx, userID, id, nil)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

// previewPrompt renders the system and user prompt strings, optionally incorporating live draft overrides from the request body.
func (h *branchHandler) previewPrompt(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}

	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	var opts *domain.PromptPreviewOptions
	if r.Body != nil && r.Body != http.NoBody {
		var req PromptPreviewRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		opts = &domain.PromptPreviewOptions{
			TargetRole:         req.TargetRole,
			TargetCompany:      req.TargetCompany,
			TargetRequirements: req.TargetRequirements,
			BranchInstructions: req.BranchInstructions,
			PresetID:           req.PresetID,
			PinnedEntityIDs:    req.PinnedEntityIDs,
			ExcludedEntityIDs:  req.ExcludedEntityIDs,
		}
	}

	preview, err := h.svc.GetPromptPreview(ctx, userID, id, opts)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

// renderPreview provides fast, ephemeral rendering of a resume version under a requested template set.
// It executes strictly zero LLM invocations and guarantees snapshot immutability with zero database writes.
func (h *branchHandler) renderPreview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	// 1. Validate Resume / Branch Ownership
	b, err := h.svc.GetBranch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if b == nil || b.UserID != userID {
		writeError(w, http.StatusNotFound, CodeNotFound, "resume not found", "")
		return
	}

	// 2. Validate Version Association
	ver, err := h.svc.GetVersion(ctx, branchID, v)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if ver == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "version not found", "")
		return
	}
	parentID := ver.ResumeID
	if parentID == "" {
		parentID = ver.BranchID
	}
	if parentID != b.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "version does not belong to resume", "")
		return
	}

	// 3. State Preconditions
	if ver.Status == domain.VersionStatusPending || ver.Status == domain.VersionStatusProcessing {
		writeError(w, http.StatusConflict, CodeConflict, "resume version generation is currently in progress", "")
		return
	}
	if ver.TailoredContent == nil {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidRequest, "version contains no tailored content to render", "")
		return
	}

	// Parse Request Body (1MB bound enforced by decodeJSON)
	var req RenderPreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.TemplateSetID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "template_set_id is required", "")
		return
	}

	// 4. Template Access Authorization
	if h.cfg.TemplateRepo == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "template repository unavailable", "")
		return
	}
	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, req.TemplateSetID)
	if err != nil || tmpl == nil {
		writeError(w, http.StatusForbidden, CodeForbidden, "cannot access target template", "")
		return
	}
	if tmpl.UserID != userID && !tmpl.IsBuiltIn {
		writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign template", "")
		return
	}

	// 5. Execution Pipeline: Pure rendering without LLM calls or DB mutations
	if h.cfg.Renderer == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "renderer unavailable", "")
		return
	}

	var prof *domain.Profile
	if h.cfg.ProfileRepo != nil {
		prof, _ = h.cfg.ProfileRepo.GetProfile(ctx, userID)
	}

	rendered, err := h.cfg.Renderer.Render(ctx, tmpl, prof, ver.TailoredContent)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"template_set_id":   tmpl.ID,
		"html_output":       rendered.HTML,
		"markdown_output":   rendered.Markdown,
		"plain_text_output": rendered.PlainText,
		"html":              rendered.HTML,
		"markdown":          rendered.Markdown,
		"plain_text":        rendered.PlainText,
	})
}


