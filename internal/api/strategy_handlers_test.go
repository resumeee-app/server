package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/memory"
)

func setupStrategyTestServer(t *testing.T) (*httptest.Server, *strategyTestRepos) {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "user-1"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	presetRepo := store
	templateRepo := store

	router := api.NewRouter(api.RouterConfig{
		PresetStore:   presetRepo,
		TemplateStore: templateRepo,
		DefaultUserID: defaultUserID,
		Admin:         true,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return server, &strategyTestRepos{
		db:           store,
		presetRepo:   presetRepo,
		templateRepo: templateRepo,
	}
}

type strategyTestRepos struct {
	db           any
	presetRepo   core.PresetStore
	templateRepo core.TemplateStore
}

func TestStrategyCRUD_RFC005(t *testing.T) {
	server, repos := setupStrategyTestServer(t)
	client := server.Client()
	baseURL := server.URL

	ctx := context.Background()

	// Seed another tenant's private template
	foreignTemplate := &core.TemplateSet{
		ID:               "tmpl-private-foreign",
		UserID:           "other-tenant-999",
		Name:             "Foreign Private Template",
		IsBuiltIn:        false,
		HTMLTemplate:     "<html>Foreign</html>",
		MarkdownTemplate: "# Foreign",
		TextTemplate:     "Foreign",
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := repos.templateRepo.CreateTemplate(ctx, foreignTemplate); err != nil {
		t.Fatalf("failed to create foreign template: %v", err)
	}

	// 1. POST /api/v1/tailoring-strategies creates clean strategy row
	t.Run("CreateStrategy_Success", func(t *testing.T) {
		body := map[string]any{
			"name":                "1-Page SWE",
			"description":         "High-density technical bullets with quantified metrics.",
			"system_instructions": "Focus strictly on quantifiable metrics, latency reductions, and core distributed systems.",
			"suggested_theme_id":  "builtin-standard-clean",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

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
			t.Fatalf("failed to decode response: %v", err)
		}

		if created.ID == "" || created.Name != "1-Page SWE" {
			t.Errorf("unexpected created strategy: %+v", created)
		}
		if created.SuggestedThemeID == nil || *created.SuggestedThemeID != "builtin-standard-clean" {
			t.Errorf("expected suggested_theme_id to be builtin-standard-clean, got %+v", created.SuggestedThemeID)
		}
	})

	// 2. Supplying a foreign tenant's private template ID in suggested_theme_id returns 403 Forbidden
	t.Run("CreateStrategy_ForeignTemplate_Returns403", func(t *testing.T) {
		body := map[string]any{
			"name":                "Malicious Strategy",
			"system_instructions": "Attempt to link foreign tenant's template",
			"suggested_theme_id":  "tmpl-private-foreign",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for foreign template, got %d", resp.StatusCode)
		}

		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	t.Run("CreateStrategy_ForeignTemplateSetID_Returns403", func(t *testing.T) {
		body := map[string]any{
			"name":                "Malicious TemplateSet Strategy",
			"system_instructions": "Attempt to link foreign tenant's template via template_set_id",
			"template_set_id":     "tmpl-private-foreign",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for foreign template_set_id, got %d", resp.StatusCode)
		}

		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	// 3. PUT or DELETE on built-in strategy returns 403 Forbidden (ErrStrategyImmutable)
	t.Run("UpdateBuiltInStrategy_Returns403", func(t *testing.T) {
		body := map[string]any{
			"name": "Modified Built-in",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/tailoring-strategies/builtin-1-page-tech-resume", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden when updating built-in strategy, got %d", resp.StatusCode)
		}

		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	t.Run("DeleteBuiltInStrategy_Returns403", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/tailoring-strategies/builtin-1-page-tech-resume", nil)
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden when deleting built-in strategy, got %d", resp.StatusCode)
		}

		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	// 4. Update custom strategy with foreign template returns 403 Forbidden
	t.Run("UpdateStrategy_ForeignTemplate_Returns403", func(t *testing.T) {
		customPreset := &core.Preset{
			ID:                 "preset-custom-update-test",
			UserID:             "user-1",
			Name:               "Custom Strategy For Update Test",
			SystemInstructions: "Original instructions",
			TemplateSetID:      "builtin-standard-clean",
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		}
		if err := repos.presetRepo.CreatePreset(ctx, customPreset); err != nil {
			t.Fatalf("failed to seed custom preset: %v", err)
		}

		body := map[string]any{
			"suggested_theme_id": "tmpl-private-foreign",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/tailoring-strategies/preset-custom-update-test", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden when updating strategy with foreign suggested_theme_id, got %d", resp.StatusCode)
		}

		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Errorf("expected code FORBIDDEN, got %q", errResp.Error.Code)
		}
	})

	// 5. Direct repository-level immutability defense-in-depth
	t.Run("RepositoryLevel_BuiltInImmutability_Guards", func(t *testing.T) {
		builtInPreset := &core.Preset{
			ID:                 "builtin-immutability-test",
			UserID:             "system",
			Name:               "Builtin Repo Immutability Test",
			SystemInstructions: "Immutable",
			TemplateSetID:      "builtin-standard-clean",
		}

		errUpdate := repos.presetRepo.UpdatePreset(ctx, builtInPreset)
		if errUpdate != core.ErrStrategyImmutable {
			t.Errorf("expected ErrStrategyImmutable from UpdatePreset, got %v", errUpdate)
		}

		errDelete := repos.presetRepo.DeletePreset(ctx, "system", "builtin-immutability-test")
		if errDelete != core.ErrStrategyImmutable {
			t.Errorf("expected ErrStrategyImmutable from DeletePreset, got %v", errDelete)
		}
	})

	// 6. Language selection and validation in strategy creation/update
	t.Run("CreateStrategy_WithSupportedLanguage_Success", func(t *testing.T) {
		body := map[string]any{
			"name":                "Spanish Strategy",
			"system_instructions": "Focus on high-level impact in Spanish.",
			"suggested_theme_id":  "builtin-standard-clean",
			"language":            "Spanish",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

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
			t.Fatalf("failed to decode response: %v", err)
		}

		if created.Language != "Spanish" {
			t.Errorf("expected language Spanish, got %q", created.Language)
		}
	})

	t.Run("CreateStrategy_RTL_Rejected", func(t *testing.T) {
		body := map[string]any{
			"name":                "Arabic Strategy",
			"system_instructions": "Focus on high-level impact in Arabic.",
			"suggested_theme_id":  "builtin-standard-clean",
			"language":            "Arabic",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for RTL language, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(strings.ToLower(errResp.Error.Message), "right-to-left") {
			t.Errorf("expected error message to mention right-to-left, got: %q", errResp.Error.Message)
		}
	})

	t.Run("CreateStrategy_UnsupportedLanguage_Rejected", func(t *testing.T) {
		body := map[string]any{
			"name":                "Klingon Strategy",
			"system_instructions": "Focus on high-level impact in Klingon.",
			"suggested_theme_id":  "builtin-standard-clean",
			"language":            "Klingon",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for unsupported language, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(strings.ToLower(errResp.Error.Message), "unsupported") {
			t.Errorf("expected error message to mention unsupported, got: %q", errResp.Error.Message)
		}
	})

	t.Run("UpdateStrategy_Language_Success", func(t *testing.T) {
		// First create a strategy
		body := map[string]any{
			"name":                "Update Language Strategy",
			"system_instructions": "Original instructions.",
			"suggested_theme_id":  "builtin-standard-clean",
			"language":            "French",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var created core.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created strategy: %v", err)
		}

		// Update language to German
		updateBody := map[string]any{
			"language": "German",
		}
		rawUpdate, _ := json.Marshal(updateBody)
		putReq, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/tailoring-strategies/"+created.ID, bytes.NewReader(rawUpdate))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", "user-1")

		putResp, err := client.Do(putReq)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = putResp.Body.Close() }()

		if putResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", putResp.StatusCode)
		}

		var updated core.Preset
		if err := json.NewDecoder(putResp.Body).Decode(&updated); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if updated.Language != "German" {
			t.Errorf("expected language German, got %q", updated.Language)
		}
	})

	t.Run("UpdateStrategy_InvalidLanguage_Rejected", func(t *testing.T) {
		// Create initial strategy
		body := map[string]any{
			"name":                "Strategy For Invalid Update",
			"system_instructions": "Original instructions.",
			"suggested_theme_id":  "builtin-standard-clean",
			"language":            "English",
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/tailoring-strategies", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "user-1")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		var created core.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created strategy: %v", err)
		}

		// Update with RTL language
		updateBody := map[string]any{
			"language": "Arabic",
		}
		rawUpdate, _ := json.Marshal(updateBody)
		putReq, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/tailoring-strategies/"+created.ID, bytes.NewReader(rawUpdate))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", "user-1")

		putResp, err := client.Do(putReq)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = putResp.Body.Close() }()

		if putResp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for RTL language update, got %d", putResp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(putResp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(strings.ToLower(errResp.Error.Message), "right-to-left") {
			t.Errorf("expected error message to mention right-to-left, got: %q", errResp.Error.Message)
		}
	})
}
