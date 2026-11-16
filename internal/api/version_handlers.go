package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/resumeee-api/internal/access"
)

// RenderPreviewRequest specifies the target template set for an ephemeral branch version preview render.
type RenderPreviewRequest struct {
	TemplateSetID string `json:"template_set_id"`
}

// RenderPreviewResponse returns ephemerally rendered outputs across canonical and legacy response keys.
type RenderPreviewResponse struct {
	TemplateSetID   string `json:"template_set_id"`
	HTMLOutput      string `json:"html_output"`
	MarkdownOutput  string `json:"markdown_output"`
	PlainTextOutput string `json:"plain_text_output"`
	HTML            string `json:"html"`
	Markdown        string `json:"markdown"`
	PlainText       string `json:"plain_text"`
}

// AsyncJobEnqueued describes an asynchronous branch generation job or synchronously completed render result.
type AsyncJobEnqueued struct {
	VersionID string             `json:"version_id"`
	Status    core.VersionStatus `json:"status"`
	Message   string             `json:"message"`
}

// EditVersionRequest submits edited tailored resume sections for provenance validation and versioning.
type EditVersionRequest struct {
	TailoredContent *core.TailoredContent `json:"tailored_content"`
}

// PromptPreviewRequest supplies optional draft overrides when previewing compiled LLM tailoring prompts.
type PromptPreviewRequest struct {
	TargetRole         *string  `json:"target_role,omitempty"`
	TargetCompany      *string  `json:"target_company,omitempty"`
	TargetRequirements *string  `json:"target_requirements,omitempty"`
	BranchInstructions *string  `json:"branch_instructions,omitempty"`
	PresetID           *string  `json:"preset_id,omitempty"`
	PinnedEntityIDs    []string `json:"pinned_entity_ids,omitempty"`
	ExcludedEntityIDs  []string `json:"excluded_entity_ids,omitempty"`
}

// generateBranchVersion enqueues or renders a new resume version for the specified branch.
func (h *branchHandler) generateBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	ver, err := h.svc.GenerateVersion(ctx, userID, id)
	if err != nil {
		if errors.Is(err, core.ErrQueueFull) {
			writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	if ver.Status == core.VersionStatusCompleted {
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

// listBranchVersions returns all generated versions belonging to a branch.
func (h *branchHandler) listBranchVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if _, err := h.svc.Branch(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	versions, err := h.svc.ListVersions(ctx, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// getBranchVersion retrieves a specific branch version by version number.
func (h *branchHandler) getBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	v := r.PathValue("v")

	if _, err := h.svc.Branch(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	ver, err := h.svc.Version(ctx, id, v)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, ver)
}

// exportBranchVersion renders and outputs a branch version in html, markdown, text, or jsonresume format.
func (h *branchHandler) exportBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	b, err := h.svc.Branch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	ver, err := h.svc.Version(ctx, branchID, v)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "html"
	}

	targetTemplateID := strings.TrimSpace(r.URL.Query().Get("template_set_id"))
	outputs := renderer.ResolveVersionOutputs(ctx, h.cfg.Renderer, h.cfg.TemplateStore, h.cfg.ProfileStore, userID, b, ver, targetTemplateID)

	if writeFormattedTextOutput(w, format, outputs) {
		return
	}
	switch format {
	case "jsonresume":
		if h.cfg.ProfileService == nil {
			writeError(w, http.StatusInternalServerError, CodeInternalError, "profile service unavailable", "")
			return
		}
		res, err := h.cfg.ProfileService.ExportJSONResume(ctx, userID, &ver.ID)
		if err != nil {
			writeDomainError(w, h.cfg.Logger, err)
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
	userID := requestUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	v := r.PathValue("v")

	b, err := h.svc.Branch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	ver, err := h.svc.Version(ctx, branchID, v)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	targetTemplateID := strings.TrimSpace(r.URL.Query().Get("template_set_id"))
	outputs := renderer.ResolveVersionOutputs(ctx, h.cfg.Renderer, h.cfg.TemplateStore, h.cfg.ProfileStore, userID, b, ver, targetTemplateID)

	writeRenderOutput(w, r, outputs)
}

// editBranchVersion validates provenance and creates an edited resume version with updated tailored content.
func (h *branchHandler) editBranchVersion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
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
		if errors.Is(err, core.ErrProvenance) {
			writeError(w, http.StatusUnprocessableEntity, CodeProvenanceError, "provenance validation failed", err.Error())
			return
		}
		if errors.Is(err, core.ErrTemplateSyntax) {
			writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template rendering failed", err.Error())
			return
		}
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, newVer)
}

// analyzeBranchGaps executes decoupled fast gap analysis against the branch's target requirements.
func (h *branchHandler) analyzeBranchGaps(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	if strings.TrimSpace(branchID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	report, err := h.svc.AnalyzeGaps(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, report)
}

// simulateBranchATSScore executes on-demand ATS extraction and job matching score calculation for a tailored branch version.
func (h *branchHandler) simulateBranchATSScore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	branchID := r.PathValue("id")
	if strings.TrimSpace(branchID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	var opts core.ATSSimulationOptions
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
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, score)
}

// getPromptPreview renders the system and user prompt strings using the branch's saved state.
func (h *branchHandler) getPromptPreview(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r, "LLM prompt preview is restricted to administrators") {
		return
	}

	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	preview, err := h.svc.PromptPreview(ctx, userID, id, nil)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

// previewPrompt renders the system and user prompt strings, optionally incorporating live draft overrides from the request body.
func (h *branchHandler) previewPrompt(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r, "LLM prompt preview is restricted to administrators") {
		return
	}

	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "branch ID is required", "")
		return
	}

	var opts *core.PromptPreviewOptions
	if r.Body != nil && r.Body != http.NoBody {
		var req PromptPreviewRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		opts = &core.PromptPreviewOptions{
			TargetRole:         req.TargetRole,
			TargetCompany:      req.TargetCompany,
			TargetRequirements: req.TargetRequirements,
			BranchInstructions: req.BranchInstructions,
			PresetID:           req.PresetID,
			PinnedEntityIDs:    req.PinnedEntityIDs,
			ExcludedEntityIDs:  req.ExcludedEntityIDs,
		}
	}

	preview, err := h.svc.PromptPreview(ctx, userID, id, opts)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, preview)
}

// renderPreview provides fast, ephemeral rendering of a resume version under a requested template set.
// It executes strictly zero LLM invocations and guarantees snapshot immutability with zero database writes.
func (h *branchHandler) renderPreview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withRenderPreviewTimeout(w, r, h.cfg)
	defer cancel()

	userID := requestUserID(r, h.cfg.DefaultUserID)
	ver, ok := h.loadRenderableVersion(ctx, w, userID, r.PathValue("id"), r.PathValue("v"))
	if !ok {
		return
	}

	var req RenderPreviewRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.TemplateSetID) == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "template_set_id is required", "")
		return
	}

	tmpl, err := access.Template(ctx, h.cfg.TemplateStore, userID, req.TemplateSetID)
	if err != nil {
		writeAccessError(w, err, "cannot access target template")
		return
	}

	if h.cfg.Renderer == nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "renderer unavailable", "")
		return
	}

	var prof *core.Profile
	if h.cfg.ProfileStore != nil {
		prof, _ = h.cfg.ProfileStore.Profile(ctx, userID)
	}

	rendered, err := h.cfg.Renderer.Render(ctx, tmpl, prof, ver.TailoredContent)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, RenderPreviewResponse{
		TemplateSetID:   tmpl.ID,
		HTMLOutput:      rendered.HTML,
		MarkdownOutput:  rendered.Markdown,
		PlainTextOutput: rendered.PlainText,
		HTML:            rendered.HTML,
		Markdown:        rendered.Markdown,
		PlainText:       rendered.PlainText,
	})
}

// loadRenderableVersion validates branch ownership, version membership, and completed tailored content state.
func (h *branchHandler) loadRenderableVersion(ctx context.Context, w http.ResponseWriter, userID, branchID, versionParam string) (*core.BranchVersion, bool) {
	b, err := h.svc.Branch(ctx, userID, branchID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return nil, false
	}
	if b == nil || b.UserID != userID {
		writeError(w, http.StatusNotFound, CodeNotFound, "resume not found", "")
		return nil, false
	}

	ver, err := h.svc.Version(ctx, branchID, versionParam)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return nil, false
	}
	if ver == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "version not found", "")
		return nil, false
	}

	if ver.BranchID != b.ID {
		writeError(w, http.StatusNotFound, CodeNotFound, "version does not belong to resume", "")
		return nil, false
	}

	if ver.Status == core.VersionStatusPending || ver.Status == core.VersionStatusProcessing {
		writeError(w, http.StatusConflict, CodeConflict, "resume version generation is currently in progress", "")
		return nil, false
	}
	if ver.TailoredContent == nil {
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidRequest, "version contains no tailored content to render", "")
		return nil, false
	}

	return ver, true
}
