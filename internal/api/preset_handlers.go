package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-core"
)

// presetHandler serves tailoring strategy preset CRUD endpoints.
type presetHandler struct {
	cfg RouterConfig
}

// registerPresetRoutes registers REST handlers for listing, creating, and modifying resume tailoring presets and strategies.
func registerPresetRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &presetHandler{cfg: cfg}

	storeGuard := routeGuard{ok: cfg.PresetStore != nil, msg: "preset store unavailable"}
	handlePreset := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, withRouteGuards(fn, storeGuard))
	}

	routes := []string{"/api/v1/presets", "/api/v1/tailoring-strategies", "/api/v1/strategies"}
	for _, base := range routes {
		handlePreset("GET "+base, h.listPresets)
		handlePreset("POST "+base, h.createPreset)
		handlePreset("GET "+base+"/{id}", h.getPreset)
		handlePreset("PUT "+base+"/{id}", h.updatePreset)
		handlePreset("DELETE "+base+"/{id}", h.deletePreset)
	}
}

// CreatePresetRequest defines the JSON payload for creating a custom tailoring strategy preset.
type CreatePresetRequest struct {
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	SystemInstructions string  `json:"system_instructions"`
	TemplateSetID      string  `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           string  `json:"language,omitempty"`
}

// UpdatePresetRequest defines the partial JSON payload for updating a custom tailoring strategy preset.
type UpdatePresetRequest struct {
	Name               *string `json:"name"`
	Description        *string `json:"description"`
	SystemInstructions *string `json:"system_instructions"`
	TemplateSetID      *string `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           *string `json:"language,omitempty"`
}

// validateTemplateAccess verifies that the target template exists and is accessible by the user.
func (h *presetHandler) validateTemplateAccess(ctx context.Context, templateID string, userID string) error {
	if templateID == "" {
		return nil
	}
	_, err := access.Template(ctx, h.cfg.TemplateStore, userID, templateID)
	return err
}

// listPresets retrieves all user and built-in tailoring presets.
func (h *presetHandler) listPresets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	presets, err := h.cfg.PresetStore.ListPresets(ctx, userID)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}
	if presets == nil {
		presets = []core.Preset{}
	}
	writeJSON(w, http.StatusOK, presets)
}

// createPreset validates and saves a new custom preset.
func (h *presetHandler) createPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	var req CreatePresetRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if req.Name == "" || req.SystemInstructions == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "name and system_instructions are required", "")
		return
	}

	themeID := req.SuggestedThemeID
	if themeID == nil && req.TemplateSetID != "" {
		themeID = &req.TemplateSetID
	}

	if themeID != nil && !h.validateOptionalTemplateAccess(ctx, w, userID, *themeID, "cannot access foreign or nonexistent template") {
		return
	}
	if themeID == nil || *themeID != req.TemplateSetID {
		if !h.validateOptionalTemplateAccess(ctx, w, userID, req.TemplateSetID, "cannot access foreign or nonexistent template") {
			return
		}
	}

	lang, err := core.ValidateLanguage(req.Language)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}

	preset := core.Preset{
		ID:                 "preset-" + uuid.New().String(),
		UserID:             userID,
		Name:               req.Name,
		Description:        req.Description,
		SystemInstructions: req.SystemInstructions,
		SuggestedThemeID:   themeID,
		TemplateSetID:      req.TemplateSetID,
		Language:           lang,
	}
	if preset.TemplateSetID == "" && themeID != nil {
		preset.TemplateSetID = *themeID
	}
	if preset.SuggestedThemeID == nil && preset.TemplateSetID != "" {
		preset.SuggestedThemeID = &preset.TemplateSetID
	}

	if err := h.cfg.PresetStore.CreatePreset(ctx, &preset); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusCreated, preset)
}

// getPreset retrieves a tailoring preset by its identifier.
func (h *presetHandler) getPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetStore.Preset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

// loadMutablePreset fetches preset id for userID and rejects system/built-in or foreign presets.
func (h *presetHandler) loadMutablePreset(ctx context.Context, w http.ResponseWriter, userID, id, immutableMsg string) (*core.Preset, bool) {
	preset, err := h.cfg.PresetStore.Preset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return nil, false
	}
	if preset == nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "resource not found", "")
		return nil, false
	}
	if access.IsSystemPreset(preset) {
		writeError(w, http.StatusForbidden, CodeForbidden, immutableMsg, "")
		return nil, false
	}
	if preset.UserID != userID {
		writeError(w, http.StatusForbidden, CodeForbidden, "access denied to preset", "")
		return nil, false
	}
	return preset, true
}

// validateOptionalTemplateAccess verifies userID may reference templateID when non-empty.
func (h *presetHandler) validateOptionalTemplateAccess(ctx context.Context, w http.ResponseWriter, userID, templateID, forbiddenMsg string) bool {
	if templateID == "" {
		return true
	}
	if err := h.validateTemplateAccess(ctx, templateID, userID); err != nil {
		writeAccessError(w, err, forbiddenMsg)
		return false
	}
	return true
}

// updatePreset updates mutable fields of an existing user-created preset.
func (h *presetHandler) updatePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, ok := h.loadMutablePreset(ctx, w, userID, id, "built-in presets cannot be modified")
	if !ok {
		return
	}

	var req UpdatePresetRequest
	if !decodeJSON(w, r, &req) {
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
	if req.SuggestedThemeID != nil {
		if !h.validateOptionalTemplateAccess(ctx, w, userID, *req.SuggestedThemeID, "cannot access foreign template") {
			return
		}
		preset.SuggestedThemeID = req.SuggestedThemeID
		if *req.SuggestedThemeID != "" {
			preset.TemplateSetID = *req.SuggestedThemeID
		}
	}
	if req.TemplateSetID != nil {
		if !h.validateOptionalTemplateAccess(ctx, w, userID, *req.TemplateSetID, "cannot access foreign template") {
			return
		}
		preset.TemplateSetID = *req.TemplateSetID
		if preset.SuggestedThemeID == nil && *req.TemplateSetID != "" {
			preset.SuggestedThemeID = req.TemplateSetID
		}
	}
	if req.Language != nil {
		lang, err := core.ValidateLanguage(*req.Language)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return
		}
		preset.Language = lang
	}

	if err := h.cfg.PresetStore.UpdatePreset(ctx, preset); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

// deletePreset removes a custom preset by its identifier.
func (h *presetHandler) deletePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	if _, ok := h.loadMutablePreset(ctx, w, userID, id, "built-in presets cannot be deleted"); !ok {
		return
	}

	if err := h.cfg.PresetStore.DeletePreset(ctx, userID, id); err != nil {
		writeDomainError(w, h.cfg.Logger, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
