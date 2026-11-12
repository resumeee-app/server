package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/services/renderer"
	"github.com/resumeee-app/resumeee-core/memory"
)

func TestBranch_PromptPreviewEndpoint(t *testing.T) {
	env := setupTestServer(t, 100)
	client := env.server.Client()
	baseURL := env.server.URL

	// 1. Unknown branch returns 404
	resp, _ := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/non-existent-id/prompt-preview", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 NotFound for unknown branch, got %d", resp.StatusCode)
	}

	// 2. Create a targeted branch
	createReq := map[string]any{
		"name":                "Staff Backend - Stripe",
		"target_role":         "Staff Distributed Systems Engineer",
		"target_company":      "Stripe",
		"target_requirements": "Strong experience with Raft consensus and high-throughput systems.",
		"branch_instructions": "Focus on reliability and fault injection.",
		"template_set_id":     renderer.TemplateStandardCleanID,
	}
	createResp, createBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches", createReq, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", createResp.StatusCode, string(createBody))
	}
	branch := decodeJSON[core.Branch](t, createBody)

	// 3. GET prompt preview using saved branch state
	getResp, getBody := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID+"/prompt-preview", nil, nil)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from GET prompt-preview, got %d: %s", getResp.StatusCode, string(getBody))
	}

	type promptPreviewResponse struct {
		SystemPrompt string `json:"system_prompt"`
		UserPrompt   string `json:"user_prompt"`
	}

	var preview promptPreviewResponse
	if err := json.Unmarshal(getBody, &preview); err != nil {
		t.Fatalf("failed to decode prompt-preview response: %v", err)
	}

	if !strings.Contains(preview.SystemPrompt, "Anti-Hallucination (\"No Lies\" Invariant)") {
		t.Errorf("expected SystemPrompt to contain anti-hallucination directive")
	}
	if !strings.Contains(preview.UserPrompt, "Target Role: Staff Distributed Systems Engineer") {
		t.Errorf("expected UserPrompt to contain saved target role")
	}
	if !strings.Contains(preview.UserPrompt, "Target Company: Stripe") {
		t.Errorf("expected UserPrompt to contain saved target company")
	}
	if !strings.Contains(preview.UserPrompt, "Focus on reliability and fault injection.") {
		t.Errorf("expected UserPrompt to contain saved branch instructions")
	}
	if strings.Contains(preview.UserPrompt, "Default User") {
		t.Errorf("expected UserPrompt to NOT leak candidate real FullName 'Default User'")
	}
	if strings.Contains(preview.UserPrompt, "[John Doe]") {
		t.Errorf("expected UserPrompt to NOT contain candidate placeholder '[John Doe]' since name is stripped")
	}
	for _, forbidden := range []string{`"user_id"`, `"created_at"`, `"updated_at"`, `"metadata"`, `"main_revision"`, `"exported_at"`} {
		if strings.Contains(preview.UserPrompt, forbidden) {
			t.Errorf("expected UserPrompt to NOT leak internal field %s", forbidden)
		}
	}

	// 4. POST prompt preview with draft overrides
	overrideReq := map[string]any{
		"target_role":         "Principal Systems Architect",
		"target_company":      "Anthropic",
		"target_requirements": "Distributed inference engines and KV cache management.",
		"branch_instructions": "Highlight low-level memory efficiency.",
	}
	postResp, postBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/prompt-preview", overrideReq, nil)
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from POST prompt-preview, got %d: %s", postResp.StatusCode, string(postBody))
	}

	var overridePreview promptPreviewResponse
	if err := json.Unmarshal(postBody, &overridePreview); err != nil {
		t.Fatalf("failed to decode override prompt-preview response: %v", err)
	}

	if !strings.Contains(overridePreview.UserPrompt, "Target Role: Principal Systems Architect") {
		t.Errorf("expected UserPrompt to reflect overridden target role")
	}
	if !strings.Contains(overridePreview.UserPrompt, "Target Company: Anthropic") {
		t.Errorf("expected UserPrompt to reflect overridden target company")
	}
	if !strings.Contains(overridePreview.UserPrompt, "Highlight low-level memory efficiency.") {
		t.Errorf("expected UserPrompt to reflect overridden branch instructions")
	}

	// 5. Verify the persisted branch was NOT mutated
	branchResp, branchBody := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/branches/"+branch.ID, nil, nil)
	if branchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK fetching branch, got %d", branchResp.StatusCode)
	}
	persisted := decodeJSON[core.Branch](t, branchBody)
	if persisted.TargetRole != "Staff Distributed Systems Engineer" {
		t.Errorf("persisted branch target role was unexpectedly modified: %s", persisted.TargetRole)
	}

	// 6. POST prompt preview with preset, pinned, and excluded entity overrides
	createPresetReq := map[string]any{
		"name":                "Executive Preset",
		"system_instructions": "Focus on P&L leadership and executive governance.",
		"template_set_id":     renderer.TemplateStandardCleanID,
	}
	presetResp, presetBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/presets", createPresetReq, nil)
	if presetResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created from POST /presets, got %d: %s", presetResp.StatusCode, string(presetBody))
	}
	createdPreset := decodeJSON[core.Preset](t, presetBody)

	// Fetch profile to retrieve entity IDs for pinning/excluding
	profResp, profBody := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile", nil, nil)
	if profResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK fetching profile, got %d", profResp.StatusCode)
	}
	var mainProf struct {
		Experiences []core.WorkExperience `json:"experiences"`
		Projects    []core.Project        `json:"projects"`
	}
	if err := json.Unmarshal(profBody, &mainProf); err != nil {
		t.Fatalf("failed to decode profile: %v", err)
	}

	var pinnedID, excludedID string
	if len(mainProf.Experiences) > 0 {
		pinnedID = mainProf.Experiences[0].ID
	}
	if len(mainProf.Projects) > 0 {
		excludedID = mainProf.Projects[0].ID
	}

	advancedOverrides := map[string]any{
		"preset_id":           createdPreset.ID,
		"pinned_entity_ids":   []string{pinnedID},
		"excluded_entity_ids": []string{excludedID},
	}
	advResp, advBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/prompt-preview", advancedOverrides, nil)
	if advResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from POST prompt-preview with overrides, got %d: %s", advResp.StatusCode, string(advBody))
	}

	var advPreview promptPreviewResponse
	if err := json.Unmarshal(advBody, &advPreview); err != nil {
		t.Fatalf("failed to decode advanced prompt-preview response: %v", err)
	}

	if !strings.Contains(advPreview.UserPrompt, "Focus on P&L leadership and executive governance.") {
		t.Errorf("expected UserPrompt to contain overridden preset instructions")
	}
	if pinnedID != "" && !strings.Contains(advPreview.UserPrompt, "Pinned Entity IDs (Must Include):") {
		t.Errorf("expected UserPrompt to contain pinned entities section")
	}
	if excludedID != "" && !strings.Contains(advPreview.UserPrompt, "Excluded Entity IDs (Must Omit):") {
		t.Errorf("expected UserPrompt to contain excluded entities section")
	}

	// 7. Negative path: POST prompt preview with malformed JSON body returns 400 Bad Request
	badResp, badBody := doRequest(t, client, http.MethodPost, baseURL+"/api/v1/branches/"+branch.ID+"/prompt-preview", "{invalid-json", nil)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 BadRequest for malformed JSON, got %d: %s", badResp.StatusCode, string(badBody))
	}
}

type mockVerifierForPromptPreview struct {
	verifyFunc func(ctx context.Context, token string) (*core.TokenInfo, error)
}

func (m *mockVerifierForPromptPreview) VerifyIDToken(ctx context.Context, token string) (*core.TokenInfo, error) {
	if m.verifyFunc != nil {
		return m.verifyFunc(ctx, token)
	}
	return nil, core.ErrUnauthorized
}

func TestBranch_PromptPreviewAdminAuthorization(t *testing.T) {
	ctx := context.Background()
	defaultUserID := "test-user-preview-auth"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed store: %v", err)
	}

	verifier := &mockVerifierForPromptPreview{
		verifyFunc: func(ctx context.Context, token string) (*core.TokenInfo, error) {
			switch token {
			case "token-admin-user":
				return &core.TokenInfo{UID: defaultUserID, Email: "admin@example.com", Admin: true}, nil
			case "token-admin-role":
				return &core.TokenInfo{UID: defaultUserID, Email: "admin2@example.com", Role: "admin"}, nil
			case "token-regular-user":
				return &core.TokenInfo{UID: defaultUserID, Email: "user@example.com", Role: "user", Admin: false}, nil
			default:
				return nil, core.ErrUnauthorized
			}
		},
	}

	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:   store,
		PresetRepo:    store,
		TemplateRepo:  store,
		BranchRepo:    store,
		QuotaRepo:     store,
		Renderer:      renderer.NewTemplateRenderer(),
		DefaultUserID: defaultUserID,
		Admin:         false,
		TokenVerifier: verifier,
	})
	server := httptest.NewServer(router)
	defer server.Close()
	client := server.Client()

	// Create branch in memory store
	branch := &core.Branch{
		ID:                 "branch-preview-auth-1",
		UserID:             defaultUserID,
		Name:               "Targeted Branch",
		TargetRole:         "Lead Architect",
		TargetCompany:      "Acme Corp",
		TargetRequirements: "Extensive distributed systems experience.",
		TemplateSetID:      renderer.TemplateStandardCleanID,
	}
	if err := store.CreateBranch(ctx, branch); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	previewURL := server.URL + "/api/v1/branches/" + branch.ID + "/prompt-preview"

	t.Run("unauthenticated call returns 401 Unauthorized", func(t *testing.T) {
		resp, body := doRequest(t, client, http.MethodGet, previewURL, nil, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("regular non-admin user is rejected with 403 Forbidden for GET and POST", func(t *testing.T) {
		headers := map[string]string{"Authorization": "Bearer token-regular-user"}

		// GET prompt-preview
		getResp, getBody := doRequest(t, client, http.MethodGet, previewURL, nil, headers)
		if getResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for non-admin GET, got %d: %s", getResp.StatusCode, string(getBody))
		}
		var getErrResp api.ErrorResponse
		if err := json.Unmarshal(getBody, &getErrResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if getErrResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code %q, got %q", api.CodeForbidden, getErrResp.Error.Code)
		}
		if !strings.Contains(strings.ToLower(getErrResp.Error.Message), "admin") {
			t.Errorf("expected error message to reference administrators, got %q", getErrResp.Error.Message)
		}

		// POST prompt-preview
		postReq := map[string]any{"target_role": "Staff Architect"}
		postResp, postBody := doRequest(t, client, http.MethodPost, previewURL, postReq, headers)
		if postResp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for non-admin POST, got %d: %s", postResp.StatusCode, string(postBody))
		}
		var postErrResp api.ErrorResponse
		if err := json.Unmarshal(postBody, &postErrResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if postErrResp.Error.Code != api.CodeForbidden {
			t.Errorf("expected error code %q, got %q", api.CodeForbidden, postErrResp.Error.Code)
		}
		if !strings.Contains(strings.ToLower(postErrResp.Error.Message), "admin") {
			t.Errorf("expected error message to reference administrators, got %q", postErrResp.Error.Message)
		}
	})

	t.Run("admin user with Admin flag is allowed (200 OK)", func(t *testing.T) {
		headers := map[string]string{"Authorization": "Bearer token-admin-user"}

		getResp, getBody := doRequest(t, client, http.MethodGet, previewURL, nil, headers)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for admin GET, got %d: %s", getResp.StatusCode, string(getBody))
		}

		postReq := map[string]any{"target_role": "Staff Architect"}
		postResp, postBody := doRequest(t, client, http.MethodPost, previewURL, postReq, headers)
		if postResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for admin POST, got %d: %s", postResp.StatusCode, string(postBody))
		}
	})

	t.Run("admin user with admin Role claim is allowed (200 OK)", func(t *testing.T) {
		headers := map[string]string{"Authorization": "Bearer token-admin-role"}

		getResp, getBody := doRequest(t, client, http.MethodGet, previewURL, nil, headers)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for admin role GET, got %d: %s", getResp.StatusCode, string(getBody))
		}

		postReq := map[string]any{"target_role": "Staff Architect"}
		postResp, postBody := doRequest(t, client, http.MethodPost, previewURL, postReq, headers)
		if postResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for admin role POST, got %d: %s", postResp.StatusCode, string(postBody))
		}
	})

	t.Run("ADMIN_MODE=true allows prompt-preview without token", func(t *testing.T) {
		adminRouter := api.NewRouter(api.RouterConfig{
			ProfileRepo:   store,
			PresetRepo:    store,
			TemplateRepo:  store,
			BranchRepo:    store,
			QuotaRepo:     store,
			Renderer:      renderer.NewTemplateRenderer(),
			DefaultUserID: defaultUserID,
			Admin:         true,
		})
		adminServer := httptest.NewServer(adminRouter)
		defer adminServer.Close()

		adminURL := adminServer.URL + "/api/v1/branches/" + branch.ID + "/prompt-preview"
		getResp, getBody := doRequest(t, adminServer.Client(), http.MethodGet, adminURL, nil, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK under ADMIN_MODE=true, got %d: %s", getResp.StatusCode, string(getBody))
		}
	})
}
