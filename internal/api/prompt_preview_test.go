package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"resumeee/internal/domain"
	"resumeee/internal/renderer"
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
	branch := decodeJSON[domain.Branch](t, createBody)

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
	for _, forbidden := range []string{`"user_id"`, `"created_at"`, `"updated_at"`, `"metadata"`, `"master_revision"`, `"exported_at"`} {
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
	persisted := decodeJSON[domain.Branch](t, branchBody)
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
	createdPreset := decodeJSON[domain.Preset](t, presetBody)

	// Fetch profile to retrieve entity IDs for pinning/excluding
	profResp, profBody := doRequest(t, client, http.MethodGet, baseURL+"/api/v1/profile", nil, nil)
	if profResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK fetching profile, got %d", profResp.StatusCode)
	}
	var masterProf struct {
		Experiences []domain.WorkExperience `json:"experiences"`
		Projects    []domain.Project        `json:"projects"`
	}
	if err := json.Unmarshal(profBody, &masterProf); err != nil {
		t.Fatalf("failed to decode profile: %v", err)
	}

	var pinnedID, excludedID string
	if len(masterProf.Experiences) > 0 {
		pinnedID = masterProf.Experiences[0].ID
	}
	if len(masterProf.Projects) > 0 {
		excludedID = masterProf.Projects[0].ID
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
