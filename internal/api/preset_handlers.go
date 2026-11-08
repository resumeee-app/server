package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"resumeee/internal/domain"
)

type presetHandler struct {
	cfg RouterConfig
}

// registerPresetRoutes registers REST handlers for listing, creating, and modifying resume tailoring presets and strategies.
func registerPresetRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &presetHandler{cfg: cfg}

	routes := []string{"/api/v1/presets", "/api/v1/tailoring-strategies", "/api/v1/strategies"}
	for _, base := range routes {
		mux.HandleFunc("GET "+base, h.listPresets)
		mux.HandleFunc("POST "+base, h.createPreset)
		mux.HandleFunc("GET "+base+"/{id}", h.getPreset)
		mux.HandleFunc("PUT "+base+"/{id}", h.updatePreset)
		mux.HandleFunc("DELETE "+base+"/{id}", h.deletePreset)
	}
}

type CreatePresetRequest struct {
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	SystemInstructions string  `json:"system_instructions"`
	TemplateSetID      string  `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           string  `json:"language,omitempty"`
	IncludeAdminFields bool    `json:"include_admin_fields"`
}

type UpdatePresetRequest struct {
	Name               *string `json:"name"`
	Description        *string `json:"description"`
	SystemInstructions *string `json:"system_instructions"`
	TemplateSetID      *string `json:"template_set_id"`
	SuggestedThemeID   *string `json:"suggested_theme_id"`
	Language           *string `json:"language,omitempty"`
	IncludeAdminFields *bool   `json:"include_admin_fields"`
}

// validateReasoningLevel verifies that the given reasoning effort level is empty or
// matches one of the accepted enum values ("none", "low", "medium", "high").
func validateReasoningLevel(level string) error {
	switch level {
	case "", "none", "low", "medium", "high":
		return nil
	default:
		return errors.New("reasoning_level must be one of: none, low, medium, high")
	}
}

// validateModelOverride verifies that the specified model override and optional provider override
// correspond to an available LLM model option registered in the enumerator catalog.
func validateModelOverride(enumerator ModelEnumerator, providerOverride, modelOverride string) error {
	if enumerator == nil || modelOverride == "" {
		return nil
	}

	available := enumerator.AvailableModels()
	if len(available) == 0 {
		return nil
	}

	var found bool
	for _, m := range available {
		if strings.EqualFold(m.Model, modelOverride) {
			if providerOverride == "" || strings.EqualFold(m.Provider, providerOverride) {
				found = true
				break
			}
		}
	}

	if !found {
		return errors.New("model_override is not available for the specified provider")
	}

	return nil
}

// validateTemplateAccess verifies that the target template exists and is accessible by the user.
func (h *presetHandler) validateTemplateAccess(ctx context.Context, templateID string, userID string) error {
	if templateID == "" || h.cfg.TemplateRepo == nil {
		return nil
	}
	tmpl, err := h.cfg.TemplateRepo.GetTemplate(ctx, userID, templateID)
	if err != nil || tmpl == nil || (tmpl.UserID != userID && !tmpl.IsBuiltIn) {
		return domain.ErrForbidden
	}
	return nil
}

// listPresets retrieves all user and built-in tailoring presets.
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
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent template", "")
			return
		}
	}
	if req.TemplateSetID != "" && (themeID == nil || *themeID != req.TemplateSetID) {
		if err := h.validateTemplateAccess(ctx, req.TemplateSetID, userID); err != nil {
			writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign or nonexistent template", "")
			return
		}
	}

	lang, err := domain.ValidateLanguage(req.Language)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
		return
	}

	preset := domain.Preset{
		ID:                 "preset-" + uuid.New().String(),
		UserID:             userID,
		Name:               req.Name,
		Description:        req.Description,
		SystemInstructions: req.SystemInstructions,
		SuggestedThemeID:   themeID,
		TemplateSetID:      req.TemplateSetID,
		Language:           lang,
		IncludeAdminFields: req.IncludeAdminFields,
	}
	if preset.TemplateSetID == "" && themeID != nil {
		preset.TemplateSetID = *themeID
	}
	if preset.SuggestedThemeID == nil && preset.TemplateSetID != "" {
		preset.SuggestedThemeID = &preset.TemplateSetID
	}

	if err := h.cfg.PresetRepo.CreatePreset(ctx, &preset); err != nil {
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

	preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, id)
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

	preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, id)
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
				writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign template", "")
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
				writeError(w, http.StatusForbidden, CodeForbidden, "cannot access foreign template", "")
				return
			}
		}
		preset.TemplateSetID = *req.TemplateSetID
		if preset.SuggestedThemeID == nil && *req.TemplateSetID != "" {
			preset.SuggestedThemeID = req.TemplateSetID
		}
	}
	if req.Language != nil {
		lang, err := domain.ValidateLanguage(*req.Language)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error(), "")
			return
		}
		preset.Language = lang
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

// deletePreset removes a custom preset by its identifier.
func (h *presetHandler) deletePreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(r, h.cfg.DefaultUserID)
	id := r.PathValue("id")

	preset, err := h.cfg.PresetRepo.GetPreset(ctx, userID, id)
	if err != nil {
		writeDomainError(w, err)
		return
	}

	if preset.IsBuiltIn() {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in presets cannot be deleted", "")
		return
	}

	if err := h.cfg.PresetRepo.DeletePreset(ctx, userID, id); err != nil {
		writeDomainError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
