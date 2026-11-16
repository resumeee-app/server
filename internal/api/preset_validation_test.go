package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/server/internal/api"
)

func setupPresetTestServer(t *testing.T) (*httptest.Server, api.PresetStore) {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "default"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	var presetStore api.PresetStore = store.Presets()
	var templateStore api.TemplateStore = store.Templates()

	router := api.NewRouter(api.RouterConfig{
		PresetStore:   presetStore,
		TemplateStore: templateStore,
		DefaultUserID: defaultUserID,
		Admin:         true,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return server, presetStore
}

func TestPresetImmutability_BuiltInPresets(t *testing.T) {
	server, _ := setupPresetTestServer(t)
	client := server.Client()
	baseURL := server.URL

	t.Run("PUT built-in preset returns 403 Forbidden", func(t *testing.T) {
		name := "Modified Builtin Preset"
		body, err := json.Marshal(api.UpdatePresetRequest{
			Name: &name,
		})
		if err != nil {
			t.Fatalf("failed to marshal request body: %v", err)
		}

		req, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/presets/builtin-1-page-tech-resume", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for updating built-in preset, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code %q, got %q", api.CodeForbidden, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Message, "built-in presets cannot be modified") {
			t.Errorf("expected message containing 'built-in presets cannot be modified', got %q", errResp.Error.Message)
		}
	})

	t.Run("DELETE built-in preset returns 403 Forbidden", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/presets/builtin-1-page-tech-resume", nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for deleting built-in preset, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code %q, got %q", api.CodeForbidden, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Message, "built-in presets cannot be deleted") {
			t.Errorf("expected message containing 'built-in presets cannot be deleted', got %q", errResp.Error.Message)
		}
	})
}

func TestPresetCRUD_CustomPreset(t *testing.T) {
	server, _ := setupPresetTestServer(t)
	client := server.Client()
	baseURL := server.URL

	t.Run("create custom preset without model or reasoning overrides", func(t *testing.T) {
		themeID := core.TemplateStandardCleanID
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Custom Engineering Preset",
			Description:        "High density technical bullets",
			SystemInstructions: "Focus on metrics and system design.",
			SuggestedThemeID:   &themeID,
			Language:           "English",
		})
		if err != nil {
			t.Fatalf("failed to marshal request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/presets", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
		}

		var created core.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created preset: %v", err)
		}

		if created.Name != "Custom Engineering Preset" {
			t.Errorf("expected name %q, got %q", "Custom Engineering Preset", created.Name)
		}
		if created.SuggestedThemeID == nil || *created.SuggestedThemeID != core.TemplateStandardCleanID {
			t.Errorf("expected suggested_theme_id %q, got %+v", core.TemplateStandardCleanID, created.SuggestedThemeID)
		}

		// Update preset
		newName := "Updated Engineering Preset"
		newDesc := "Updated description"
		upBody, err := json.Marshal(api.UpdatePresetRequest{
			Name:        &newName,
			Description: &newDesc,
		})
		if err != nil {
			t.Fatalf("failed to marshal update request: %v", err)
		}

		upReq, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/presets/"+created.ID, bytes.NewReader(upBody))
		if err != nil {
			t.Fatalf("failed to create update request: %v", err)
		}
		upReq.Header.Set("Content-Type", "application/json")

		upResp, err := client.Do(upReq)
		if err != nil {
			t.Fatalf("update request failed: %v", err)
		}
		defer func() { _ = upResp.Body.Close() }()

		if upResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", upResp.StatusCode)
		}

		var updated core.Preset
		if err := json.NewDecoder(upResp.Body).Decode(&updated); err != nil {
			t.Fatalf("failed to decode updated preset: %v", err)
		}
		if updated.Name != newName {
			t.Errorf("expected name %q, got %q", newName, updated.Name)
		}
		if updated.Description != newDesc {
			t.Errorf("expected description %q, got %q", newDesc, updated.Description)
		}

		// Clear suggested_theme_id by passing empty string, matching MCP semantics.
		emptyTheme := ""
		clearBody, err := json.Marshal(api.UpdatePresetRequest{
			SuggestedThemeID: &emptyTheme,
		})
		if err != nil {
			t.Fatalf("failed to marshal clear request: %v", err)
		}
		clearReq, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/presets/"+created.ID, bytes.NewReader(clearBody))
		if err != nil {
			t.Fatalf("failed to create clear request: %v", err)
		}
		clearReq.Header.Set("Content-Type", "application/json")
		clearResp, err := client.Do(clearReq)
		if err != nil {
			t.Fatalf("clear request failed: %v", err)
		}
		defer func() { _ = clearResp.Body.Close() }()
		if clearResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK clearing suggested_theme_id, got %d", clearResp.StatusCode)
		}
		var cleared core.Preset
		if err := json.NewDecoder(clearResp.Body).Decode(&cleared); err != nil {
			t.Fatalf("failed to decode cleared preset: %v", err)
		}
		if cleared.SuggestedThemeID != nil {
			t.Errorf("expected suggested_theme_id nil after empty update, got %q", *cleared.SuggestedThemeID)
		}
	})

	t.Run("create custom preset normalizes empty suggested_theme_id to nil", func(t *testing.T) {
		emptyTheme := "   "
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Minimal Custom Preset",
			SystemInstructions: "Keep output concise.",
			SuggestedThemeID:   &emptyTheme,
		})
		if err != nil {
			t.Fatalf("failed to marshal request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/presets", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
		}
		var created core.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created preset: %v", err)
		}
		if created.SuggestedThemeID != nil {
			t.Errorf("expected suggested_theme_id nil on empty create input, got %q", *created.SuggestedThemeID)
		}
	})
}
