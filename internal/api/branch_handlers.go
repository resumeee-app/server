package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"resumeee/internal/branch"
	"resumeee/internal/domain"
)

type branchHandler struct {
	cfg RouterConfig
	svc branch.Service
}

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
	mux.HandleFunc("GET /api/v1/branches/{id}/versions", h.listBranchVersions)
	mux.HandleFunc("GET /api/v1/branches/{id}/versions/{v}", h.getBranchVersion)
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

func (h *branchHandler) createBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreateBranchRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
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
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
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

func (h *branchHandler) syncBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req SyncBranchesRequest
	if r.Body != nil && r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body", err.Error())
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

func (h *branchHandler) editBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	var req EditVersionRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
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
