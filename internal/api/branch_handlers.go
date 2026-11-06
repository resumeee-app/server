package api

import (
	"errors"
	"net/http"
	"strings"

	"resumeee/internal/branch"
	"resumeee/internal/domain"
	"resumeee/internal/renderer"
)

type branchHandler struct {
	cfg RouterConfig
	svc branch.Service
}

// registerBranchRoutes binds REST endpoints for branch creation, modification, version generation, and exports.
func registerBranchRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &branchHandler{
		cfg: cfg,
		svc: cfg.BranchService,
	}

	mux.HandleFunc("GET /api/v1/branches", h.listBranches)
	mux.HandleFunc("POST /api/v1/branches", h.createBranch)
	mux.HandleFunc("POST /api/v1/branches/sync", h.syncBranches)
	mux.HandleFunc("GET /api/v1/branches/{id}", h.getBranch)
	mux.HandleFunc("PUT /api/v1/branches/{id}", h.updateBranch)
	mux.HandleFunc("DELETE /api/v1/branches/{id}", h.deleteBranch)
	mux.HandleFunc("POST /api/v1/branches/{id}/generate", h.generateBranchVersion)
	mux.HandleFunc("POST /api/v1/branches/{id}/gap-analysis", h.analyzeBranchGaps)
	mux.HandleFunc("GET /api/v1/branches/{id}/prompt-preview", h.getPromptPreview)
	mux.HandleFunc("POST /api/v1/branches/{id}/prompt-preview", h.previewPrompt)
	mux.HandleFunc("GET /api/v1/branches/{id}/versions", h.listBranchVersions)
	mux.HandleFunc("GET /api/v1/branches/{id}/versions/{v}", h.getBranchVersion)
	mux.HandleFunc("GET /api/v1/branches/{id}/versions/{v}/preview", h.previewBranchVersion)
	mux.HandleFunc("GET /api/v1/branches/{id}/versions/{v}/export", h.exportBranchVersion)
	mux.HandleFunc("POST /api/v1/branches/{id}/versions/{v}/edit", h.editBranchVersion)
}

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
	PinnedEntityIDs    []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids"`
}

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
	PinnedEntityIDs    []string `json:"pinned_entity_ids"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids"`
	ActiveVersionID    *string  `json:"active_version_id"`
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

	b := &domain.Branch{
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
		PinnedEntityIDs:    req.PinnedEntityIDs,
		ExcludedEntityIDs:  req.ExcludedEntityIDs,
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
	if req.BranchInstructions != nil {
		branch.BranchInstructions = *req.BranchInstructions
	}
	if req.PresetID != nil {
		branch.PresetID = req.PresetID
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
			Message:   "Resume rendered from master profile",
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

// exportBranchVersion renders and outputs a branch version in html, markdown, text, or jsonresume format.
func (h *branchHandler) exportBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	if _, err := h.svc.GetBranch(ctx, userID, branchID); err != nil {
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

	switch format {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ver.HTMLOutput))
	case "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ver.MarkdownOutput))
	case "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(ver.PlainTextOutput))
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

	htmlOut := ver.HTMLOutput
	mdOut := ver.MarkdownOutput
	txtOut := ver.PlainTextOutput

	if (htmlOut == "" || mdOut == "" || txtOut == "") && h.cfg.Renderer != nil && h.cfg.TemplateRepo != nil {
		tmpl, tErr := h.cfg.TemplateRepo.GetTemplate(ctx, userID, b.TemplateSetID)
		if tErr == nil && tmpl != nil {
			var prof *domain.Profile
			if h.cfg.ProfileRepo != nil {
				prof, _ = h.cfg.ProfileRepo.GetProfile(ctx, userID)
			}
			renderCtx := renderer.NewRenderContext(tmpl, prof, ver.TailoredContent)
			if htmlOut == "" && tmpl.HTMLTemplate != "" {
				if hOut, rErr := h.cfg.Renderer.RenderFormat(domain.OutputFormatHTML, tmpl.HTMLTemplate, renderCtx); rErr == nil {
					htmlOut = hOut
				}
			}
			if mdOut == "" && tmpl.MarkdownTemplate != "" {
				if mOut, rErr := h.cfg.Renderer.RenderFormat(domain.OutputFormatMarkdown, tmpl.MarkdownTemplate, renderCtx); rErr == nil {
					mdOut = mOut
				}
			}
			if txtOut == "" && tmpl.TextTemplate != "" {
				if tOut, rErr := h.cfg.Renderer.RenderFormat(domain.OutputFormatPlainText, tmpl.TextTemplate, renderCtx); rErr == nil {
					txtOut = tOut
				}
			}
		}
	}

	writeRenderOutput(w, r, domain.RenderOutput{
		HTML:      htmlOut,
		Markdown:  mdOut,
		PlainText: txtOut,
	})
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

// getPromptPreview renders the system and user prompt strings using the branch's saved state.
func (h *branchHandler) getPromptPreview(w http.ResponseWriter, r *http.Request) {
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

