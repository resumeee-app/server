package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/resumeee-app/resumeee-core"
)

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

type CreatePresetRequest struct {
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	SystemInstructions string  `json:"system_instructions"`
	TemplateSetID      string  `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           string  `json:"language,omitempty"`
}

type UpdatePresetRequest struct {
	Name               *string `json:"name"`
	Description        *string `json:"description"`
	SystemInstructions *string `json:"system_instructions"`
	TemplateSetID      *string `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           *string `json:"language,omitempty"`
}

// validateReasoningLevel verifies that the given reasoning effort level is empty or
// matches one of the accepted enum values ("none", "low", "medium", "high").
func validateReasoningLevel(level string) error {
	return core.ValidateReasoningLevel(level)
}

// validateModelOverride verifies that the specified model override and optional provider override
// correspond to an available LLM model option registered in the enumerator catalog.
func validateModelOverride(enumerator core.ModelEnumerator, providerOverride, modelOverride string) error {
	return core.ValidateModelOverride(enumerator, providerOverride, modelOverride)
}

// validateTemplateAccess verifies that the target template exists and is accessible by the user.
func (h *presetHandler) validateTemplateAccess(ctx context.Context, templateID string, userID string) error {
	if templateID == "" {
		return nil
	}
	_, err := fetchAuthorizedTemplate(ctx, h.cfg.TemplateStore, userID, templateID)
	return err
}

// listPresets retrieves all user and built-in tailoring presets.
func (h *presetHandler) listPresets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)

	presets, err := h.cfg.PresetStore.ListPresets(ctx, userID)
	if err != nil {
		writeDomainError(w, err)
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
	userID := getUserID(r, h.cfg.DefaultUserID)

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

	if themeID != nil && *themeID != "" {
		if err := h.validateTemplateAccess(ctx, *themeID, userID); err != nil {
			writeAccessError(w, err, "cannot access foreign or nonexistent template")
			return
		}
	}
	if req.TemplateSetID != "" && (themeID == nil || *themeID != req.TemplateSetID) {
		if err := h.validateTemplateAccess(ctx, req.TemplateSetID, userID); err != nil {
			writeAccessError(w, err, "cannot access foreign or nonexistent template")
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
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, preset)
}

// getPreset retrieves a tailoring preset by its identifier.
func (h *presetHandler) getPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetStore.Preset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

// updatePreset updates mutable fields of an existing user-created preset.
func (h *presetHandler) updatePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetStore.Preset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	if preset.IsBuiltIn() {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in presets cannot be modified", "")
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
		if *req.SuggestedThemeID != "" {
			if err := h.validateTemplateAccess(ctx, *req.SuggestedThemeID, userID); err != nil {
				writeAccessError(w, err, "cannot access foreign template")
				return
			}
		}
		preset.SuggestedThemeID = req.SuggestedThemeID
		if *req.SuggestedThemeID != "" {
			preset.TemplateSetID = *req.SuggestedThemeID
		}
	}
	if req.TemplateSetID != nil {
		if *req.TemplateSetID != "" {
			if err := h.validateTemplateAccess(ctx, *req.TemplateSetID, userID); err != nil {
				writeAccessError(w, err, "cannot access foreign template")
				return
			}
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
		writeDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, preset)
}

// deletePreset removes a custom preset by its identifier.
func (h *presetHandler) deletePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetStore.Preset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	if preset.IsBuiltIn() {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in presets cannot be deleted", "")
		return
	}

	if err := h.cfg.PresetStore.DeletePreset(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
