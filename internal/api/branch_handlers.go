package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"resumeee/internal/domain"
)

type branchHandler struct {
	cfg RouterConfig
}

func registerBranchRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &branchHandler{cfg: cfg}

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

func getVersionFromPath(ctx context.Context, branchRepo domain.BranchRepository, branchID, v string) (*domain.BranchVersion, error) {
	if num, err := strconv.Atoi(v); err == nil && num > 0 {
		ver, err := branchRepo.GetVersionByNumber(ctx, branchID, num)
		if err == nil {
			return ver, nil
		}
	}
	return branchRepo.GetVersion(ctx, branchID, v)
}

func (h *branchHandler) listBranches(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	branches, err := h.cfg.BranchRepo.ListBranches(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	staleOnly := strings.EqualFold(r.URL.Query().Get("stale"), "true")
	if staleOnly {
		var filtered []domain.Branch
		for _, b := range branches {
			if b.IsStale {
				filtered = append(filtered, b)
			}
		}
		if filtered == nil {
			filtered = []domain.Branch{}
		}
		writeJSON(w, http.StatusOK, filtered)
		return
	}

	if branches == nil {
		branches = []domain.Branch{}
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

	branch := domain.Branch{
		ID:                 "branch-" + uuid.New().String(),
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

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err == nil && prof != nil {
		branch.LastSyncedMasterRevision = prof.MasterRevision
	}

	if err := h.cfg.BranchRepo.CreateBranch(ctx, &branch); err != nil {
		writeDomainError(w, err)
		return
	}

	created, err := h.cfg.BranchRepo.GetBranch(ctx, userID, branch.ID)
	if err == nil && created != nil {
		writeJSON(w, http.StatusCreated, created)
		return
	}

	writeJSON(w, http.StatusCreated, branch)
}

func (h *branchHandler) getBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	branch, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, branch)
}

func (h *branchHandler) updateBranch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	branch, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id)
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

	if err := h.cfg.BranchRepo.UpdateBranch(ctx, branch); err != nil {
		writeDomainError(w, err)
		return
	}

	updated, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id)
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

	if err := h.cfg.BranchRepo.DeleteBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *branchHandler) generateBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	branch, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	provider := branch.ProviderOverride
	if provider == "" {
		provider = prof.DefaultProvider
	}
	model := branch.ModelOverride
	if model == "" {
		model = prof.DefaultModel
	}

	ver := &domain.BranchVersion{
		ID:                 "ver-" + uuid.New().String(),
		BranchID:           branch.ID,
		Status:             domain.VersionStatusPending,
		MasterRevisionUsed: prof.MasterRevision,
		TemplateSetIDUsed:  branch.TemplateSetID,
		ProviderUsed:       provider,
		ModelUsed:          model,
		SourceType:         domain.SourceTypeLLMGeneration,
		ParentVersionID:    branch.ActiveVersionID,
	}

	if err := h.cfg.BranchRepo.CreateVersion(ctx, ver); err != nil {
		writeDomainError(w, err)
		return
	}

	if err := h.cfg.JobEnqueuer.Enqueue(ver.ID); err != nil {
		if errors.Is(err, domain.ErrQueueFull) {
			_ = h.cfg.BranchRepo.UpdateVersionStatus(ctx, ver.ID, domain.VersionStatusFailed, "generation queue full")
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusAccepted, AsyncJobEnqueued{
		VersionID: ver.ID,
		Status:    domain.VersionStatusPending,
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

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var targetBranches []domain.Branch
	if len(req.BranchIDs) > 0 {
		for _, bID := range req.BranchIDs {
			b, err := h.cfg.BranchRepo.GetBranch(ctx, userID, bID)
			if err != nil {
				writeDomainError(w, err)
				return
			}
			targetBranches = append(targetBranches, *b)
		}
	} else {
		allBranches, err := h.cfg.BranchRepo.ListBranches(ctx, userID)
		if err != nil {
			writeDomainError(w, err)
			return
		}
		for _, b := range allBranches {
			if b.IsStale {
				targetBranches = append(targetBranches, b)
			}
		}
	}

	var enqueuedIDs []string
	for _, b := range targetBranches {
		provider := b.ProviderOverride
		if provider == "" {
			provider = prof.DefaultProvider
		}
		model := b.ModelOverride
		if model == "" {
			model = prof.DefaultModel
		}

		ver := &domain.BranchVersion{
			ID:                 "ver-" + uuid.New().String(),
			BranchID:           b.ID,
			Status:             domain.VersionStatusPending,
			MasterRevisionUsed: prof.MasterRevision,
			TemplateSetIDUsed:  b.TemplateSetID,
			ProviderUsed:       provider,
			ModelUsed:          model,
			SourceType:         domain.SourceTypeLLMGeneration,
			ParentVersionID:    b.ActiveVersionID,
		}

		if err := h.cfg.BranchRepo.CreateVersion(ctx, ver); err != nil {
			writeDomainError(w, err)
			return
		}

		if err := h.cfg.JobEnqueuer.Enqueue(ver.ID); err != nil {
			if errors.Is(err, domain.ErrQueueFull) {
				_ = h.cfg.BranchRepo.UpdateVersionStatus(ctx, ver.ID, domain.VersionStatusFailed, "generation queue full")
				writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
				return
			}
			writeDomainError(w, err)
			return
		}
		enqueuedIDs = append(enqueuedIDs, ver.ID)
	}

	if enqueuedIDs == nil {
		enqueuedIDs = []string{}
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

	if _, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	versions, err := h.cfg.BranchRepo.ListVersions(ctx, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if versions == nil {
		versions = []domain.BranchVersion{}
	}
	writeJSON(w, http.StatusOK, versions)
}

func (h *branchHandler) getBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	v := r.PathValue("v")

	if _, err := h.cfg.BranchRepo.GetBranch(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	ver, err := getVersionFromPath(ctx, h.cfg.BranchRepo, id, v)
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

	if _, err := h.cfg.BranchRepo.GetBranch(ctx, userID, branchID); err != nil {
		writeDomainError(w, err)
		return
	}

	ver, err := getVersionFromPath(ctx, h.cfg.BranchRepo, branchID, v)
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

	branch, err := h.cfg.BranchRepo.GetBranch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	parentVer, err := getVersionFromPath(ctx, h.cfg.BranchRepo, branchID, v)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req EditVersionRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.TailoredContent == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "tailored_content is required", "")
		return
	}

	snapshot, err := h.cfg.ProfileRepo.GetSnapshot(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	prof, err := h.cfg.ProfileRepo.GetProfile(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, branch.TemplateSetID)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	// Provenance validation ("No Lies" invariant)
	if err := h.cfg.Validator.ValidateStrict(req.TailoredContent, snapshot, branch); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeProvenanceError, "provenance validation failed", err.Error())
		return
	}

	// Synchronously execute Go templates to render HTML/Markdown/Text
	rendered, err := h.cfg.Renderer.Render(ctx, tmpl, prof, req.TailoredContent)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template rendering failed", err.Error())
		return
	}

	newVer := &domain.BranchVersion{
		ID:                 "ver-" + uuid.New().String(),
		BranchID:           branch.ID,
		Status:             domain.VersionStatusCompleted,
		MasterRevisionUsed: snapshot.MasterRevision,
		TemplateSetIDUsed:  branch.TemplateSetID,
		ProviderUsed:       "manual",
		ModelUsed:          "manual",
		SourceType:         domain.SourceTypeManualEdit,
		ParentVersionID:    &parentVer.ID,
		TailoredContent:    req.TailoredContent,
		HTMLOutput:         rendered.HTML,
		MarkdownOutput:     rendered.Markdown,
		PlainTextOutput:    rendered.PlainText,
	}

	if err := h.cfg.BranchRepo.CreateVersion(ctx, newVer); err != nil {
		writeDomainError(w, err)
		return
	}

	if err := h.cfg.BranchRepo.CompleteVersion(ctx, newVer); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, newVer)
}
