package api

import (
	"net/http"

	"github.com/google/uuid"

	"resumeee/internal/domain"
)

type presetHandler struct {
	cfg RouterConfig
}

func registerPresetRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &presetHandler{cfg: cfg}

	mux.HandleFunc("GET /api/v1/presets", h.listPresets)
	mux.HandleFunc("POST /api/v1/presets", h.createPreset)
	mux.HandleFunc("GET /api/v1/presets/{id}", h.getPreset)
	mux.HandleFunc("PUT /api/v1/presets/{id}", h.updatePreset)
	mux.HandleFunc("DELETE /api/v1/presets/{id}", h.deletePreset)
}

type CreatePresetRequest struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	SystemInstructions string `json:"system_instructions"`
	TemplateSetID      string `json:"template_set_id"`
	ProviderOverride   string `json:"provider_override"`
	ModelOverride      string `json:"model_override"`
	IncludeAdminFields bool   `json:"include_admin_fields"`
}

type UpdatePresetRequest struct {
	Name               *string `json:"name"`
	Description        *string `json:"description"`
	SystemInstructions *string `json:"system_instructions"`
	TemplateSetID      *string `json:"template_set_id"`
	ProviderOverride   *string `json:"provider_override"`
	ModelOverride      *string `json:"model_override"`
	IncludeAdminFields *bool   `json:"include_admin_fields"`
}

func (h *presetHandler) listPresets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	presets, err := h.cfg.PresetRepo.ListPresets(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	if presets == nil {
		presets = []domain.Preset{}
	}
	writeJSON(w, http.StatusOK, presets)
}

func (h *presetHandler) createPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	var req CreatePresetRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name == "" || req.SystemInstructions == "" || req.TemplateSetID == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name, system_instructions, and template_set_id are required", "")
		return
	}

	preset := domain.Preset{
		ID:                 "preset-" + uuid.New().String(),
		UserID:             userID,
		Name:               req.Name,
		Description:        req.Description,
		SystemInstructions: req.SystemInstructions,
		TemplateSetID:      req.TemplateSetID,
		ProviderOverride:   req.ProviderOverride,
		ModelOverride:      req.ModelOverride,
		IncludeAdminFields: req.IncludeAdminFields,
	}

	if err := h.cfg.PresetRepo.CreatePreset(ctx, &preset); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, preset)
}

func (h *presetHandler) getPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

func (h *presetHandler) updatePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	var req UpdatePresetRequest
	if err := parseJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "malformed json payload", err.Error())
		return
	}

	if req.Name != nil {
		preset.Name = *req.Name
	}
	if req.Description != nil {
		preset.Description = *req.Description
	}
	if req.SystemInstructions != nil {
		preset.SystemInstructions = *req.SystemInstructions
	}
	if req.TemplateSetID != nil {
		preset.TemplateSetID = *req.TemplateSetID
	}
	if req.ProviderOverride != nil {
		preset.ProviderOverride = *req.ProviderOverride
	}
	if req.ModelOverride != nil {
		preset.ModelOverride = *req.ModelOverride
	}
	if req.IncludeAdminFields != nil {
		preset.IncludeAdminFields = *req.IncludeAdminFields
	}

	if err := h.cfg.PresetRepo.UpdatePreset(ctx, preset); err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

func (h *presetHandler) deletePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if err := h.cfg.PresetRepo.DeletePreset(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
