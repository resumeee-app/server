package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"resumeee/internal/api"
	"resumeee/internal/domain"
	"resumeee/internal/renderer"
	"resumeee/internal/repository/memory"
)

type mockModelEnum struct {
	models []domain.ModelOption
}

func (m *mockModelEnum) AvailableModels() []domain.ModelOption {
	return m.models
}

func setupPresetTestServer(t *testing.T, enumerator api.ModelEnumerator) (*httptest.Server, domain.PresetRepository) {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "default"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	presetRepo := store

	router := api.NewRouter(api.RouterConfig{
		PresetRepo:      presetRepo,
		DefaultUserID:   defaultUserID,
		ModelEnumerator: enumerator,
		Admin:           true,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return server, presetRepo
}

func TestPresetImmutability_BuiltInPresets(t *testing.T) {
	server, _ := setupPresetTestServer(t, nil)
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

func TestPresetValidation_ReasoningLevel(t *testing.T) {
	server, _ := setupPresetTestServer(t, nil)
	client := server.Client()
	baseURL := server.URL

	t.Run("valid reasoning_level values succeed on POST", func(t *testing.T) {
		validLevels := []string{"none", "low", "medium", "high"}
		for _, lvl := range validLevels {
			body, err := json.Marshal(api.CreatePresetRequest{
				Name:               "Preset-" + lvl,
				SystemInstructions: "Instructions for " + lvl,
				TemplateSetID:      renderer.TemplateStandardCleanID,
				ReasoningLevel:     lvl,
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
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("expected 201 Created for reasoning_level %q, got %d", lvl, resp.StatusCode)
			}

			var created domain.Preset
			if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
				t.Fatalf("failed to decode created preset: %v", err)
			}
			_ = resp.Body.Close()

			if created.ReasoningLevel != lvl {
				t.Errorf("expected preset.ReasoningLevel %q, got %q", lvl, created.ReasoningLevel)
			}
		}
	})

	t.Run("invalid reasoning_level returns 400 Bad Request on POST", func(t *testing.T) {
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Invalid Reasoning Preset",
			SystemInstructions: "Instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ReasoningLevel:     "infinite-turbo",
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

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for invalid reasoning_level, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Message, "reasoning_level must be one of") {
			t.Errorf("expected message containing 'reasoning_level must be one of', got %q", errResp.Error.Message)
		}
	})

	t.Run("table-driven reasoning_level validation on PUT", func(t *testing.T) {
		// First create a custom preset
		createBody, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Preset For Update",
			SystemInstructions: "Initial instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ReasoningLevel:     "low",
		})
		if err != nil {
			t.Fatalf("failed to marshal create request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/presets", bytes.NewReader(createBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("create request failed: %v", err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
		}

		var created domain.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created preset: %v", err)
		}
		_ = resp.Body.Close()

		tests := []struct {
			name           string
			reasoningLevel string
			expectStatus   int
			expectCode     string
		}{
			{name: "update to medium", reasoningLevel: "medium", expectStatus: http.StatusOK},
			{name: "update to high", reasoningLevel: "high", expectStatus: http.StatusOK},
			{name: "update to none", reasoningLevel: "none", expectStatus: http.StatusOK},
			{name: "reset/clear reasoning level with empty string", reasoningLevel: "", expectStatus: http.StatusOK},
			{name: "invalid reasoning level", reasoningLevel: "hyper-speed", expectStatus: http.StatusBadRequest, expectCode: api.CodeInvalidRequest},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				updateBody, err := json.Marshal(api.UpdatePresetRequest{
					ReasoningLevel: &tc.reasoningLevel,
				})
				if err != nil {
					t.Fatalf("failed to marshal update request: %v", err)
				}
				upReq, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/presets/"+created.ID, bytes.NewReader(updateBody))
				if err != nil {
					t.Fatalf("failed to create request: %v", err)
				}
				upReq.Header.Set("Content-Type", "application/json")

				upResp, err := client.Do(upReq)
				if err != nil {
					t.Fatalf("update request failed: %v", err)
				}
				defer func() { _ = upResp.Body.Close() }()

				if upResp.StatusCode != tc.expectStatus {
					t.Fatalf("expected status %d, got %d", tc.expectStatus, upResp.StatusCode)
				}

				if tc.expectStatus == http.StatusOK {
					var updated domain.Preset
					if err := json.NewDecoder(upResp.Body).Decode(&updated); err != nil {
						t.Fatalf("failed to decode updated preset: %v", err)
					}
					if updated.ReasoningLevel != tc.reasoningLevel {
						t.Errorf("expected ReasoningLevel %q, got %q", tc.reasoningLevel, updated.ReasoningLevel)
					}
				} else {
					var errResp api.ErrorResponse
					if err := json.NewDecoder(upResp.Body).Decode(&errResp); err != nil {
						t.Fatalf("failed to decode error response: %v", err)
					}
					if errResp.Error.Code != tc.expectCode {
						t.Errorf("expected error code %q, got %q", tc.expectCode, errResp.Error.Code)
					}
					if !strings.Contains(errResp.Error.Message, "reasoning_level must be one of") {
						t.Errorf("expected error message containing 'reasoning_level must be one of', got %q", errResp.Error.Message)
					}
				}
			})
		}
	})
}

func TestPresetValidation_ModelOverrideWithEnumerator(t *testing.T) {
	mockCatalog := []domain.ModelOption{
		{
			Provider:    "gemini",
			Model:       "gemini-3.8-flash",
			DisplayName: "Google Gemini — gemini-3.8-flash",
			IsDefault:   true,
		},
		{
			Provider:    "gemini",
			Model:       "gemini-4-argon",
			DisplayName: "Google Gemini — gemini-4-argon",
			IsDefault:   false,
		},
		{
			Provider:    "anthropic",
			Model:       "claude-3-7-sonnet-20250219",
			DisplayName: "Anthropic — claude-3-7-sonnet-20250219",
			IsDefault:   false,
		},
	}

	server, _ := setupPresetTestServer(t, &mockModelEnum{models: mockCatalog})
	client := server.Client()
	baseURL := server.URL

	t.Run("valid model and provider succeed on POST", func(t *testing.T) {
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Valid Model Preset",
			SystemInstructions: "Instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ProviderOverride:   "gemini",
			ModelOverride:      "gemini-3.8-flash",
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
	})

	t.Run("valid model without provider succeeds on POST", func(t *testing.T) {
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Valid Model Without Provider",
			SystemInstructions: "Instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ModelOverride:      "gemini-4-argon",
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
	})

	t.Run("unsupported model returns 400 Bad Request on POST", func(t *testing.T) {
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Invalid Model Preset",
			SystemInstructions: "Instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ProviderOverride:   "gemini",
			ModelOverride:      "gpt-4o-unknown",
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

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for unavailable model, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Message, "model_override is not available") {
			t.Errorf("expected error message containing 'model_override is not available', got %q", errResp.Error.Message)
		}
	})

	t.Run("mismatched provider and model returns 400 Bad Request on POST", func(t *testing.T) {
		body, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Mismatched Provider Model Preset",
			SystemInstructions: "Instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ProviderOverride:   "gemini",
			ModelOverride:      "claude-3-7-sonnet-20250219", // Belongs to anthropic, not gemini
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

		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for mismatched provider/model, got %d", resp.StatusCode)
		}

		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != api.CodeInvalidRequest {
			t.Errorf("expected error code %q, got %q", api.CodeInvalidRequest, errResp.Error.Code)
		}
		if !strings.Contains(errResp.Error.Message, "model_override is not available for the specified provider") {
			t.Errorf("expected error message containing 'model_override is not available for the specified provider', got %q", errResp.Error.Message)
		}
	})

	t.Run("table-driven model and provider override validation on PUT", func(t *testing.T) {
		// First create a custom preset with provider: gemini, model: gemini-3.8-flash
		createBody, err := json.Marshal(api.CreatePresetRequest{
			Name:               "Custom Override Preset",
			SystemInstructions: "Initial instructions",
			TemplateSetID:      renderer.TemplateStandardCleanID,
			ProviderOverride:   "gemini",
			ModelOverride:      "gemini-3.8-flash",
		})
		if err != nil {
			t.Fatalf("failed to marshal create request: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/presets", bytes.NewReader(createBody))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("create request failed: %v", err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
		}

		var created domain.Preset
		if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
			t.Fatalf("failed to decode created preset: %v", err)
		}
		_ = resp.Body.Close()

		validModel := "gemini-4-argon"
		invalidModel := "gpt-4o-unknown"
		mismatchedProvider := "anthropic"
		emptyString := ""

		tests := []struct {
			name             string
			providerOverride *string
			modelOverride    *string
			expectStatus     int
			expectCode       string
			expectedMessage  string
			verifyCleared    bool
		}{
			{
				name:          "valid model update to gemini-4-argon",
				modelOverride: &validModel,
				expectStatus:  http.StatusOK,
			},
			{
				name:            "invalid model update",
				modelOverride:   &invalidModel,
				expectStatus:    http.StatusBadRequest,
				expectCode:      api.CodeInvalidRequest,
				expectedMessage: "model_override is not available",
			},
			{
				name:             "mismatched provider update without model (effective model is gemini-4-argon but provider is anthropic)",
				providerOverride: &mismatchedProvider,
				expectStatus:     http.StatusBadRequest,
				expectCode:       api.CodeInvalidRequest,
				expectedMessage:  "model_override is not available for the specified provider",
			},
			{
				name:             "reset both provider and model overrides back to system default (empty strings)",
				providerOverride: &emptyString,
				modelOverride:    &emptyString,
				expectStatus:     http.StatusOK,
				verifyCleared:    true,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				updateBody, err := json.Marshal(api.UpdatePresetRequest{
					ProviderOverride: tc.providerOverride,
					ModelOverride:    tc.modelOverride,
				})
				if err != nil {
					t.Fatalf("failed to marshal update request: %v", err)
				}
				upReq, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/presets/"+created.ID, bytes.NewReader(updateBody))
				if err != nil {
					t.Fatalf("failed to create request: %v", err)
				}
				upReq.Header.Set("Content-Type", "application/json")

				upResp, err := client.Do(upReq)
				if err != nil {
					t.Fatalf("update request failed: %v", err)
				}
				defer func() { _ = upResp.Body.Close() }()

				if upResp.StatusCode != tc.expectStatus {
					t.Fatalf("expected status %d, got %d", tc.expectStatus, upResp.StatusCode)
				}

				if tc.expectStatus == http.StatusOK {
					var updated domain.Preset
					if err := json.NewDecoder(upResp.Body).Decode(&updated); err != nil {
						t.Fatalf("failed to decode updated preset: %v", err)
					}
					if tc.verifyCleared {
						if updated.ProviderOverride != "" {
							t.Errorf("expected ProviderOverride to be cleared, got %q", updated.ProviderOverride)
						}
						if updated.ModelOverride != "" {
							t.Errorf("expected ModelOverride to be cleared, got %q", updated.ModelOverride)
						}
					} else if tc.modelOverride != nil && updated.ModelOverride != *tc.modelOverride {
						t.Errorf("expected ModelOverride %q, got %q", *tc.modelOverride, updated.ModelOverride)
					}
				} else {
					var errResp api.ErrorResponse
					if err := json.NewDecoder(upResp.Body).Decode(&errResp); err != nil {
						t.Fatalf("failed to decode error response: %v", err)
					}
					if errResp.Error.Code != tc.expectCode {
						t.Errorf("expected error code %q, got %q", tc.expectCode, errResp.Error.Code)
					}
					if !strings.Contains(errResp.Error.Message, tc.expectedMessage) {
						t.Errorf("expected error message containing %q, got %q", tc.expectedMessage, errResp.Error.Message)
					}
				}
			})
		}
	})
}

