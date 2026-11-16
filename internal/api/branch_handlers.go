package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/server/auth"
	"github.com/resumeee-app/server/internal/access"
)

// branchHandler serves resume branch CRUD, synchronization, generation, and preview endpoints.
type branchHandler struct {
	cfg RouterConfig
	svc BranchService
}

// registerBranchRoutes binds REST endpoints for branch/resume creation, modification, version generation, preview rendering, and exports.
func registerBranchRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &branchHandler{
		cfg: cfg,
		svc: cfg.BranchService,
	}

	svcGuard := routeGuard{ok: h.svc != nil, msg: "branch service unavailable"}
	handleBranch := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, withRouteGuards(fn, svcGuard))
	}

	bases := []string{"/api/v1/branches", "/api/v1/resumes"}
	for _, base := range bases {
		handleBranch("GET "+base, h.listBranches)
		handleBranch("POST "+base, h.createBranch)
		handleBranch("POST "+base+"/sync", h.syncBranches)
		handleBranch("GET "+base+"/{id}", h.getBranch)
		handleBranch("PUT "+base+"/{id}", h.updateBranch)
		handleBranch("DELETE "+base+"/{id}", h.deleteBranch)
		handleBranch("POST "+base+"/{id}/generate", h.generateBranchVersion)
		handleBranch("POST "+base+"/{id}/tailor", h.generateBranchVersion)
		handleBranch("POST "+base+"/{id}/gap-analysis", h.analyzeBranchGaps)
		handleBranch("POST "+base+"/{id}/ats-score", h.simulateBranchATSScore)
		handleBranch("GET "+base+"/{id}/prompt-preview", h.getPromptPreview)
		handleBranch("POST "+base+"/{id}/prompt-preview", h.previewPrompt)
		handleBranch("GET "+base+"/{id}/versions", h.listBranchVersions)
		handleBranch("GET "+base+"/{id}/versions/{v}", h.getBranchVersion)
		handleBranch("GET "+base+"/{id}/versions/{v}/preview", h.previewBranchVersion)
		handleBranch("GET "+base+"/{id}/versions/{v}/export", h.exportBranchVersion)
		handleBranch("POST "+base+"/{id}/versions/{v}/edit", h.editBranchVersion)
		handleBranch("POST "+base+"/{id}/versions/{v}/render-preview", h.renderPreview)
		handleBranch("POST "+base+"/{id}/versions/{v}/ats-score", h.simulateBranchATSScore)
	}
}

// CreateBranchRequest defines the JSON payload for creating a targeted resume branch.
type CreateBranchRequest struct {
	Name               string   `json:"name"`
	IsDefault          *bool    `json:"is_default,omitempty"`
	TargetRole         string   `json:"target_role"`
	TargetCompany      string   `json:"target_company"`
	TargetRequirements string   `json:"target_requirements"`
	BranchInstructions string   `json:"branch_instructions"`
	PresetID           *string  `json:"preset_id"`
	TemplateSetID      string   `json:"template_set_id"`
	ProviderOverride   string   `json:"provider_override"`
	ModelOverride      string   `json:"model_override"`
	ReasoningLevel     string   `json:"reasoning_level,omitempty"`
	PinnedEntityIDs    []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids"`
}

// UpdateBranchRequest defines the partial JSON payload for updating an existing resume branch.
type UpdateBranchRequest struct {
	Name               *string  `json:"name"`
	TargetRole         *string  `json:"target_role"`
	TargetCompany      *string  `json:"target_company"`
	TargetRequirements *string  `json:"target_requirements"`
	BranchInstructions *string  `json:"branch_instructions"`
	PresetID           *string  `json:"preset_id"`
	TemplateSetID      *string  `json:"template_set_id"`
	ProviderOverride   *string  `json:"provider_override"`
	ModelOverride      *string  `json:"model_override"`
	ReasoningLevel     *string  `json:"reasoning_level,omitempty"`
	PinnedEntityIDs    []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids"`
	ActiveVersionID    *string  `json:"active_version_id"`
}

// SyncBranchesRequest defines an optional subset of branch IDs to synchronize.
type SyncBranchesRequest struct {
	BranchIDs []string `json:"branch_ids"`
}

// SyncBranchesResponse reports how many stale branch regeneration jobs were enqueued.
type SyncBranchesResponse struct {
	EnqueuedCount int      `json:"enqueued_count"`
	VersionIDs    []string `json:"version_ids"`
}

// isLocalAdminMode reports whether cfg enables unauthenticated local admin mode without an external auth provider.
func isLocalAdminMode(cfg RouterConfig) bool {
	return cfg.Admin && (cfg.AuthProvider == nil || cfg.AuthProvider.Name() == auth.ProviderAdmin)
}

// requireAdmin verifies administrative privilege, writing a 403 Forbidden response with msg if unauthorized.
func (h *branchHandler) requireAdmin(w http.ResponseWriter, r *http.Request, msg string) bool {
	if !core.IsAdminFromContext(r.Context()) && !isLocalAdminMode(h.cfg) {
		writeError(w, http.StatusForbidden, CodeForbidden, msg, "")
		return false
	}
	return true
}

// listBranches retrieves all branches for the authenticated user, optionally filtering for stale branches.
func (h *branchHandler) listBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	staleOnly := strings.EqualFold(r.URL.Query().Get("stale"), "true")

	branches, err := h.svc.ListBranches(ctx, userID, staleOnly)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, branches)
}

// createBranch validates and registers a new resume branch.
func (h *branchHandler) createBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

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

	if !h.validateBranchReferences(ctx, w, userID, req.TemplateSetID, req.PresetID) {
		return
	}

	if (req.ProviderOverride != "" || req.ModelOverride != "" || req.ReasoningLevel != "") &&
		!h.requireAdmin(w, r, "model and reasoning selection are restricted to administrators") {
		return
	}

	if !h.validateModelAndReasoning(w, req.ProviderOverride, req.ModelOverride, req.ReasoningLevel, true, true) {
		return
	}

	b := &core.Branch{
		UserID:             userID,
		Name:               req.Name,
		TargetRole:         req.TargetRole,
		TargetCompany:      req.TargetCompany,
		TargetRequirements: req.TargetRequirements,
		BranchInstructions: req.BranchInstructions,
		PresetID:           req.PresetID,
		TemplateSetID:      req.TemplateSetID,
		ProviderOverride:   req.ProviderOverride,
		ModelOverride:      req.ModelOverride,
		ReasoningLevel:     req.ReasoningLevel,
		PinnedEntityIDs:    req.PinnedEntityIDs,
		ExcludedEntityIDs:  req.ExcludedEntityIDs,
	}

	created, err := h.svc.CreateBranch(ctx, b)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// getBranch retrieves a branch record by its identifier.
func (h *branchHandler) getBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	b, err := h.svc.Branch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, b)
}

// updateBranch modifies fields of an existing branch.
func (h *branchHandler) updateBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	b, err := h.svc.Branch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	var req UpdateBranchRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var templateToValidate string
	if req.TemplateSetID != nil {
		templateToValidate = *req.TemplateSetID
	}
	if !h.validateBranchReferences(ctx, w, userID, templateToValidate, req.PresetID) {
		return
	}

	if (req.ProviderOverride != nil || req.ModelOverride != nil || req.ReasoningLevel != nil) &&
		!h.requireAdmin(w, r, "model and reasoning selection are restricted to administrators") {
		return
	}

	effectiveProvider := b.ProviderOverride
	if req.ProviderOverride != nil {
		effectiveProvider = *req.ProviderOverride
	}
	effectiveModel := b.ModelOverride
	if req.ModelOverride != nil {
		effectiveModel = *req.ModelOverride
	}
	var reasoningLevel string
	if req.ReasoningLevel != nil {
		reasoningLevel = *req.ReasoningLevel
	}
	if !h.validateModelAndReasoning(w, effectiveProvider, effectiveModel, reasoningLevel, req.ProviderOverride != nil || req.ModelOverride != nil, req.ReasoningLevel != nil) {
		return
	}

	applyBranchTextUpdates(b, req)
	applyBranchConfigUpdates(b, req)

	updated, err := h.svc.UpdateBranch(ctx, b)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, updated)
}

// validateBranchReferences verifies multi-tenant authorization for optional template set and tailoring preset IDs.
func (h *branchHandler) validateBranchReferences(ctx context.Context, w http.ResponseWriter, userID, templateSetID string, presetID *string) bool {
	if templateSetID != "" {
		if _, err := access.Template(ctx, h.cfg.TemplateStore, userID, templateSetID); err != nil {
			writeAccessError(w, err, "cannot access foreign or nonexistent template")
			return false
		}
	}
	if presetID != nil && *presetID != "" {
		if _, err := access.Preset(ctx, h.cfg.PresetStore, userID, h.cfg.DefaultUserID, *presetID); err != nil {
			writeAccessError(w, err, "cannot access foreign or nonexistent preset")
			return false
		}
	}
	return true
}

// validateModelAndReasoning validates optional reasoning level and provider/model override combinations.
func (h *branchHandler) validateModelAndReasoning(w http.ResponseWriter, providerOverride, modelOverride, reasoningLevel string, checkModel, checkReasoning bool) bool {
	if checkReasoning {
		if err := core.ValidateReasoningLevel(reasoningLevel); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return false
		}
	}
	if checkModel {
		if err := core.ValidateModelOverride(h.cfg.ModelEnumerator, providerOverride, modelOverride); err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return false
		}
	}
	return true
}

// applyBranchTextUpdates copies non-nil textual and directive fields from req onto b.
func applyBranchTextUpdates(b *core.Branch, req UpdateBranchRequest) {
	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.TargetRole != nil {
		b.TargetRole = *req.TargetRole
	}
	if req.TargetCompany != nil {
		b.TargetCompany = *req.TargetCompany
	}
	if req.TargetRequirements != nil {
		b.TargetRequirements = *req.TargetRequirements
	}
	if req.BranchInstructions != nil {
		b.BranchInstructions = *req.BranchInstructions
	}
}

// applyBranchConfigUpdates copies non-nil preset, template, model, entity pin/exclusion, and active version fields onto b.
func applyBranchConfigUpdates(b *core.Branch, req UpdateBranchRequest) {
	if req.PresetID != nil {
		b.PresetID = req.PresetID
	}
	if req.TemplateSetID != nil {
		b.TemplateSetID = *req.TemplateSetID
	}
	if req.ProviderOverride != nil {
		b.ProviderOverride = *req.ProviderOverride
	}
	if req.ModelOverride != nil {
		b.ModelOverride = *req.ModelOverride
	}
	if req.ReasoningLevel != nil {
		b.ReasoningLevel = *req.ReasoningLevel
	}
	if req.PinnedEntityIDs != nil {
		b.PinnedEntityIDs = req.PinnedEntityIDs
	}
	if req.ExcludedEntityIDs != nil {
		b.ExcludedEntityIDs = req.ExcludedEntityIDs
	}
	if req.ActiveVersionID != nil {
		b.ActiveVersionID = req.ActiveVersionID
	}
}

// deleteBranch removes a branch and associated versions.
func (h *branchHandler) deleteBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.svc.DeleteBranch(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// syncBranches enqueues regeneration jobs for stale branches matching the filter.
func (h *branchHandler) syncBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req SyncBranchesRequest
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}

	enqueuedIDs, err := h.svc.SyncBranches(ctx, userID, req.BranchIDs)
	if err != nil {
		if errors.Is(err, core.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusAccepted, SyncBranchesResponse{
		EnqueuedCount: len(enqueuedIDs),
		VersionIDs:    enqueuedIDs,
	})
}
