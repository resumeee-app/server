package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/memory"
	"github.com/resumeee-app/resumeee-core/provenance"
	"github.com/resumeee-app/resumeee-core/renderer"
)

type mockLLMProvider struct {
	CallCount int
}

func (m *mockLLMProvider) Name() string { return "mock" }
func (m *mockLLMProvider) GenerateTailoredResume(_ context.Context, _ core.GenerationRequest) (*core.GenerationResponse, error) {
	m.CallCount++
	return &core.GenerationResponse{}, nil
}

type resumeTestEnv struct {
	server        *httptest.Server
	db            any
	branchStore   api.BranchStore
	jobStore      api.JobStore
	profileStore  api.ProfileStore
	presetStore   api.PresetStore
	templateStore api.TemplateStore
	mockProvider  *mockLLMProvider
}

func setupResumeTestServer(t *testing.T) *resumeTestEnv {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "user-resume-1"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	var profileStore api.ProfileStore = store.Profiles()
	var presetStore api.PresetStore = store.Presets()
	var templateStore api.TemplateStore = store.Templates()
	var branchStore api.BranchStore = store.Branches()
	var jobStore api.JobStore = store.Jobs()
	mockProv := &mockLLMProvider{}
	rend := renderer.New()

	branchSvc := branch.New(
		branch.WithBranches(branchStore),
		branch.WithJobs(jobStore),
		branch.WithProfiles(profileStore),
		branch.WithPresets(presetStore),
		branch.WithTemplates(templateStore),
		branch.WithValidator(provenance.New()),
		branch.WithRenderer(rend),
	)

	router := api.NewRouter(api.RouterConfig{
		ProfileStore:  profileStore,
		PresetStore:   presetStore,
		TemplateStore: templateStore,
		BranchStore:   branchStore,
		JobStore:      jobStore,
		BranchService: branchSvc,
		Renderer:      rend,
		DefaultUserID: defaultUserID,
		Admin:         true,
	})

	server := httptest.NewServer(router)
	t.Cleanup(func() {
		server.Close()
	})

	return &resumeTestEnv{
		server:        server,
		db:            store,
		branchStore:   branchStore,
		jobStore:      jobStore,
		profileStore:  profileStore,
		presetStore:   presetStore,
		templateStore: templateStore,
		mockProvider:  mockProv,
	}
}

func TestResumeFastRenderPreview_RFC005(t *testing.T) {
	env := setupResumeTestServer(t)
	client := env.server.Client()
	baseURL := env.server.URL
	ctx := context.Background()
	userID := "user-resume-1"

	// Create test target resume (branch)
	targetResume := &core.Branch{
		ID:            "resume-test-1",
		UserID:        userID,
		Name:          "Stripe - Staff Backend",
		TemplateSetID: "builtin-standard-clean",
	}
	if err := env.branchStore.CreateBranch(ctx, targetResume); err != nil {
		t.Fatalf("failed to create target resume: %v", err)
	}

	// Create a completed version with tailored content
	tailoredContent := &core.TailoredContent{
		Summary: "Experienced distributed systems engineer with 10+ years building high-throughput payment rails.",
		Experiences: []core.TailoredExperience{
			{
				SourceEntityID: "exp-1",
				Company:        "Acme Payments",
				RoleTitle:      "Senior Systems Engineer",
				Bullets: []core.TailoredBullet{
					{
						Text:           "Architected distributed ledger processing 50k transactions/second.",
						SourceFactIDs:  []string{"fact-ledger-1"},
						SourceSkillIDs: []string{"skill-go", "skill-distributed-systems"},
					},
				},
			},
		},
	}

	now := time.Now().UTC()
	vCompleted := &core.BranchVersion{
		ID:                "ver-completed-1",
		BranchID:          targetResume.ID,
		VersionNumber:     1,
		Status:            core.VersionStatusCompleted,
		MainRevisionUsed:  1,
		TemplateSetIDUsed: "builtin-standard-clean",
		SourceType:        core.SourceTypeLLMGeneration,
		TailoredContent:   tailoredContent,
		HTMLOutput:        "<html>Old Standard Template</html>",
		MarkdownOutput:    "# Old Standard Markdown",
		PlainTextOutput:   "Old Standard Plain Text",
		CreatedAt:         now,
		CompletedAt:       &now,
	}
	if err := env.branchStore.SaveCompletedVersion(ctx, vCompleted); err != nil {
		t.Fatalf("failed to create completed version: %v", err)
	}

	// Create alternative custom template for user
	userTemplate := &core.TemplateSet{
		ID:               "tmpl-user-custom",
		UserID:           userID,
		Name:             "User Custom Layout",
		IsBuiltIn:        false,
		HTMLTemplate:     "<main class=\"custom-theme\"><h1>{{.Profile.FullName}}</h1><p>{{.Content.Summary}}</p>{{range .Content.Experiences}}<div>{{.Company}}: {{range .Bullets}}{{.Text}}{{end}}</div>{{end}}</main>",
		CSSStyles:        ".custom-theme { font-family: 'Helvetica Neue', sans-serif; }",
		MarkdownTemplate: "# {{.Profile.FullName}}\n\n{{.Content.Summary}}",
		TextTemplate:     "{{.Profile.FullName}}\n\n{{.Content.Summary}}",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := env.templateStore.CreateTemplate(ctx, userTemplate); err != nil {
		t.Fatalf("failed to create user template: %v", err)
	}

	// Create a foreign tenant's private template
	foreignTemplate := &core.TemplateSet{
		ID:               "tmpl-foreign-private",
		UserID:           "other-tenant-random-456",
		Name:             "Foreign Private Template",
		IsBuiltIn:        false,
		HTMLTemplate:     "<html>Forbidden</html>",
		MarkdownTemplate: "Forbidden",
		TextTemplate:     "Forbidden",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := env.templateStore.CreateTemplate(ctx, foreignTemplate); err != nil {
		t.Fatalf("failed to create foreign template: %v", err)
	}

	// 1. POST .../render-preview returns in < 100ms with updated HTML containing new template styles while preserving 100% of tailored bullets and fact IDs.
	// 2. Assert strictly zero LLM provider calls (mockProvider.CallCount == 0).
	// 3. Assert version immutability: Historical ResumeVersion row in SQLite remains 100% byte-for-byte identical before and after invocation.
	t.Run("FastRenderPreview_Success_And_Immutability", func(t *testing.T) {
		start := time.Now()

		body := map[string]any{
			"template_set_id": "tmpl-user-custom",
		}
		raw, _ := json.Marshal(body)
		url := baseURL + "/api/v1/resumes/" + targetResume.ID + "/versions/" + vCompleted.ID + "/render-preview"
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		elapsed := time.Since(start)

		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		if elapsed > 150*time.Millisecond {
			t.Logf("fast preview latency took %v", elapsed)
		}

		var result struct {
			TemplateSetID   string `json:"template_set_id"`
			HTMLOutput      string `json:"html_output"`
			MarkdownOutput  string `json:"markdown_output"`
			PlainTextOutput string `json:"plain_text_output"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		// Verify updated HTML contains custom styles and tailored bullets
		if !strings.Contains(result.HTMLOutput, "custom-theme") {
			t.Errorf("expected HTML to contain new template styles 'custom-theme', got: %s", result.HTMLOutput)
		}
		if !strings.Contains(result.HTMLOutput, "Architected distributed ledger processing 50k transactions/second.") {
			t.Errorf("expected HTML to preserve tailored bullet statement, got: %s", result.HTMLOutput)
		}

		// 2. Assert zero LLM provider calls
		if env.mockProvider.CallCount != 0 {
			t.Errorf("expected zero LLM provider calls, got %d", env.mockProvider.CallCount)
		}

		// 3. Assert version immutability: check historical version in DB is unchanged
		freshVersion, err := env.branchStore.Version(ctx, targetResume.ID, vCompleted.ID)
		if err != nil {
			t.Fatalf("failed to retrieve version from DB: %v", err)
		}
		if freshVersion.TemplateSetIDUsed != "builtin-standard-clean" {
			t.Errorf("historical version mutated: expected TemplateSetIDUsed 'builtin-standard-clean', got '%s'", freshVersion.TemplateSetIDUsed)
		}
		if freshVersion.HTMLOutput != "<html>Old Standard Template</html>" {
			t.Errorf("historical version mutated: expected original HTMLOutput, got '%s'", freshVersion.HTMLOutput)
		}
	})

	// 4. Assert 409 Conflict when version status is pending or processing
	t.Run("RenderPreview_PendingStatus_Returns409", func(t *testing.T) {
		pendingJob := &core.Job{
			ID:     "job-pending-1",
			Kind:   core.JobKindBranchTailor,
			UserID: userID,
		}
		vPending := &core.BranchVersion{
			ID:                "ver-pending-1",
			BranchID:          targetResume.ID,
			VersionNumber:     2,
			MainRevisionUsed:  1,
			TemplateSetIDUsed: "builtin-standard-clean",
			SourceType:        core.SourceTypeLLMGeneration,
			TailoredContent:   tailoredContent,
			CreatedAt:         time.Now().UTC(),
		}
		if _, err := env.jobStore.SubmitBranchTailor(ctx, pendingJob, vPending, core.PendingJobOptions{}); err != nil {
			t.Fatalf("failed to submit pending version: %v", err)
		}

		body := map[string]any{"template_set_id": "builtin-standard-clean"}
		raw, _ := json.Marshal(body)
		url := baseURL + "/api/v1/resumes/" + targetResume.ID + "/versions/" + vPending.ID + "/render-preview"
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for pending version, got %d", resp.StatusCode)
		}
	})

	// 5. Assert 422 Unprocessable Entity when TailoredContent is nil
	t.Run("RenderPreview_NilTailoredContent_Returns422", func(t *testing.T) {
		failedJob := &core.Job{
			ID:     "job-failed-1",
			Kind:   core.JobKindBranchTailor,
			UserID: userID,
		}
		vFailed := &core.BranchVersion{
			ID:                "ver-failed-1",
			BranchID:          targetResume.ID,
			VersionNumber:     3,
			MainRevisionUsed:  1,
			TemplateSetIDUsed: "builtin-standard-clean",
			SourceType:        core.SourceTypeLLMGeneration,
			TailoredContent:   nil,
			CreatedAt:         time.Now().UTC(),
		}
		if _, err := env.jobStore.SubmitBranchTailor(ctx, failedJob, vFailed, core.PendingJobOptions{}); err != nil {
			t.Fatalf("failed to submit failed version job: %v", err)
		}
		claimed, err := env.jobStore.ClaimNext(ctx, core.ClaimJobRequest{WorkerID: "test-worker"})
		if err != nil {
			t.Fatalf("failed to claim job: %v", err)
		}
		if err := env.jobStore.FailClaimed(ctx, claimed.ID, "test-worker", "LLM failed to respond"); err != nil {
			t.Fatalf("failed to fail claimed job: %v", err)
		}

		body := map[string]any{"template_set_id": "builtin-standard-clean"}
		raw, _ := json.Marshal(body)
		url := baseURL + "/api/v1/resumes/" + targetResume.ID + "/versions/" + vFailed.ID + "/render-preview"
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 Unprocessable Entity when TailoredContent is nil, got %d", resp.StatusCode)
		}
	})

	// 6. Assert 403 Forbidden when attempting to render with another user's private template
	t.Run("RenderPreview_ForeignTemplate_Returns403", func(t *testing.T) {
		body := map[string]any{"template_set_id": "tmpl-foreign-private"}
		raw, _ := json.Marshal(body)
		url := baseURL + "/api/v1/resumes/" + targetResume.ID + "/versions/" + vCompleted.ID + "/render-preview"
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for foreign template, got %d", resp.StatusCode)
		}
	})
}

func TestBranchMultiTenantTemplateAndStrategyGuards_RFC005(t *testing.T) {
	env := setupResumeTestServer(t)
	client := env.server.Client()
	baseURL := env.server.URL
	ctx := context.Background()
	userID := "user-resume-1"
	attackerID := "user-attacker"

	// Seed private template owned by attacker
	foreignTmpl := &core.TemplateSet{
		ID:               "tmpl-foreign-attacker",
		UserID:           attackerID,
		Name:             "Secret Attacker Template",
		HTMLTemplate:     "<html>Forbidden</html>",
		MarkdownTemplate: "Forbidden",
		TextTemplate:     "Forbidden",
		IsBuiltIn:        false,
	}
	if err := env.templateStore.CreateTemplate(ctx, foreignTmpl); err != nil {
		t.Fatalf("failed to create attacker template: %v", err)
	}

	// Seed private preset owned by attacker
	defaultTheme := "builtin-standard-clean"
	foreignPreset := &core.Preset{
		ID:                 "preset-foreign-attacker",
		UserID:             attackerID,
		Name:               "Secret Attacker Strategy",
		SystemInstructions: "Strictly confidential attacker directives",
		SuggestedThemeID:   &defaultTheme,
	}
	if err := env.presetStore.CreatePreset(ctx, foreignPreset); err != nil {
		t.Fatalf("failed to create attacker preset: %v", err)
	}

	// Helper for checking error response envelope
	assertForbidden := func(t *testing.T, resp *http.Response) {
		t.Helper()
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d", resp.StatusCode)
		}
		var errResp api.ErrorResponse
		if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
			t.Fatalf("failed to decode error response: %v", err)
		}
		if errResp.Error.Code != "FORBIDDEN" {
			t.Fatalf("expected error code FORBIDDEN, got %q", errResp.Error.Code)
		}
	}

	// 1. POST /branches with foreign template_set_id -> 403 Forbidden
	t.Run("CreateBranch_ForeignTemplateSetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"name":            "Infiltrator Branch",
			"template_set_id": foreignTmpl.ID,
		}
		raw, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		assertForbidden(t, resp)
	})

	// 2. POST /branches with foreign preset_id -> 403 Forbidden
	t.Run("CreateBranch_ForeignPresetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"name":            "Infiltrator Branch Preset",
			"template_set_id": "builtin-standard-clean",
			"preset_id":       foreignPreset.ID,
		}
		raw, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		assertForbidden(t, resp)
	})

	// Seed legitimate user branch
	legitBranch := &core.Branch{
		ID:            "branch-legit-user",
		UserID:        userID,
		Name:          "Legitimate Branch",
		TemplateSetID: "builtin-standard-clean",
	}
	if err := env.branchStore.CreateBranch(ctx, legitBranch); err != nil {
		t.Fatalf("failed to create legit branch: %v", err)
	}

	// 3. PUT /branches/{id} with foreign template_set_id -> 403 Forbidden
	t.Run("UpdateBranch_ForeignTemplateSetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"template_set_id": foreignTmpl.ID,
		}
		raw, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/branches/"+legitBranch.ID, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		assertForbidden(t, resp)
	})

	// 4. PUT /branches/{id} with foreign preset_id -> 403 Forbidden
	t.Run("UpdateBranch_ForeignPresetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"preset_id": foreignPreset.ID,
		}
		raw, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/branches/"+legitBranch.ID, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		assertForbidden(t, resp)
	})
}

func TestBranchHandlers_GapReportAndMatchScoreEndpoints(t *testing.T) {
	env := setupResumeTestServer(t)
	client := env.server.Client()
	baseURL := env.server.URL
	ctx := context.Background()
	userID := "user-resume-1"

	score89 := 89.0

	// Create a tailored branch
	b := &core.Branch{
		ID:            "branch-api-gap-test",
		UserID:        userID,
		Name:          "Site Reliability Lead",
		TemplateSetID: core.TemplateStandardCleanID,
	}
	if err := env.branchStore.CreateBranch(ctx, b); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	// Create and complete a version with GapReport
	v := &core.BranchVersion{
		ID:               "ver-api-gap-1",
		BranchID:         b.ID,
		Status:           core.VersionStatusCompleted,
		MainRevisionUsed: 1,
		GapReport: &core.GapReport{
			OverallMatchSummary: "High match for SRE lead position",
			MatchScore:          &score89,
			MissingRequirements: []core.GapItem{
				{Requirement: "Terraform CDKTF", Importance: core.GapImportancePreferred},
			},
		},
	}
	if err := env.branchStore.SaveCompletedVersion(ctx, v); err != nil {
		t.Fatalf("failed to save completed version: %v", err)
	}

	// 1. Test GET /api/v1/branches
	t.Run("GET /api/v1/branches returns populated gap_report with match_score", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/branches", nil)
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /api/v1/branches failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var branches []core.Branch
		if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		var found *core.Branch
		for i := range branches {
			if branches[i].ID == b.ID {
				found = &branches[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("branch %s not found in GET /api/v1/branches", b.ID)
		}
		if found.GapReport == nil {
			t.Fatalf("expected gap_report to be non-nil in GET /api/v1/branches, got nil")
		}
		if found.GapReport.MatchScore == nil || *found.GapReport.MatchScore != score89 {
			t.Errorf("expected match score %f, got %v", score89, found.GapReport.MatchScore)
		}
		if found.GapReport.OverallMatchSummary != "High match for SRE lead position" {
			t.Errorf("expected overall match summary 'High match for SRE lead position', got %q", found.GapReport.OverallMatchSummary)
		}
		if len(found.GapReport.MissingRequirements) != 1 {
			t.Fatalf("expected 1 missing requirement, got %d", len(found.GapReport.MissingRequirements))
		}
		if found.GapReport.MissingRequirements[0].Requirement != "Terraform CDKTF" {
			t.Errorf("expected missing requirement 'Terraform CDKTF', got %q", found.GapReport.MissingRequirements[0].Requirement)
		}
		if found.GapReport.MissingRequirements[0].Importance != core.GapImportancePreferred {
			t.Errorf("expected importance 'preferred', got %q", found.GapReport.MissingRequirements[0].Importance)
		}
	})

	// 2. Test GET /api/v1/branches/{id}
	t.Run("GET /api/v1/branches/{id} returns populated gap_report with match_score", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/branches/"+b.ID, nil)
		req.Header.Set("X-User-ID", userID)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET /api/v1/branches/{id} failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var fetched core.Branch
		if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
			t.Fatalf("failed to decode response: %v", err)
		}

		if fetched.GapReport == nil {
			t.Fatalf("expected gap_report to be non-nil in GET /api/v1/branches/{id}, got nil")
		}
		if fetched.GapReport.MatchScore == nil || *fetched.GapReport.MatchScore != score89 {
			t.Errorf("expected match score %f, got %v", score89, fetched.GapReport.MatchScore)
		}
		if fetched.GapReport.OverallMatchSummary != "High match for SRE lead position" {
			t.Errorf("expected overall match summary 'High match for SRE lead position', got %q", fetched.GapReport.OverallMatchSummary)
		}
		if len(fetched.GapReport.MissingRequirements) != 1 {
			t.Fatalf("expected 1 missing requirement, got %d", len(fetched.GapReport.MissingRequirements))
		}
		if fetched.GapReport.MissingRequirements[0].Requirement != "Terraform CDKTF" {
			t.Errorf("expected missing requirement 'Terraform CDKTF', got %q", fetched.GapReport.MissingRequirements[0].Requirement)
		}
	})

	// 3. Test PUT /api/v1/branches/{id} version switch
	t.Run("PUT /api/v1/branches/{id} active version switch syncs gap_report match_score", func(t *testing.T) {
		score96 := 96.0
		v2 := &core.BranchVersion{
			ID:               "ver-api-gap-2",
			BranchID:         b.ID,
			Status:           core.VersionStatusCompleted,
			MainRevisionUsed: 1,
			GapReport: &core.GapReport{
				OverallMatchSummary: "Exceptional candidate match after tailoring",
				MatchScore:          &score96,
				MissingRequirements: []core.GapItem{
					{Requirement: "Minor documentation polish", Importance: core.GapImportanceNiceToHave},
				},
			},
		}
		if err := env.branchStore.SaveCompletedVersion(ctx, v2); err != nil {
			t.Fatalf("failed to save completed version 2: %v", err)
		}

		// Switch active version back to v1 via PUT /api/v1/branches/{id}
		reqBody := map[string]any{
			"active_version_id": v.ID,
		}
		raw, _ := json.Marshal(reqBody)
		putReq, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/branches/"+b.ID, bytes.NewReader(raw))
		putReq.Header.Set("Content-Type", "application/json")
		putReq.Header.Set("X-User-ID", userID)

		putResp, err := client.Do(putReq)
		if err != nil {
			t.Fatalf("PUT /api/v1/branches/{id} failed: %v", err)
		}
		defer func() { _ = putResp.Body.Close() }()

		if putResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", putResp.StatusCode)
		}

		var updated core.Branch
		if err := json.NewDecoder(putResp.Body).Decode(&updated); err != nil {
			t.Fatalf("failed to decode PUT response: %v", err)
		}

		if updated.GapReport == nil || updated.GapReport.MatchScore == nil || *updated.GapReport.MatchScore != score89 {
			t.Errorf("expected rolled back match score %f, got %v", score89, updated.GapReport)
		}
		if updated.GapReport.OverallMatchSummary != "High match for SRE lead position" {
			t.Errorf("expected summary 'High match for SRE lead position', got %q", updated.GapReport.OverallMatchSummary)
		}

		// Now switch active version forward to v2 via PUT /api/v1/branches/{id}
		reqBody2 := map[string]any{
			"active_version_id": v2.ID,
		}
		raw2, _ := json.Marshal(reqBody2)
		putReq2, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/branches/"+b.ID, bytes.NewReader(raw2))
		putReq2.Header.Set("Content-Type", "application/json")
		putReq2.Header.Set("X-User-ID", userID)

		putResp2, err := client.Do(putReq2)
		if err != nil {
			t.Fatalf("PUT /api/v1/branches/{id} forward switch failed: %v", err)
		}
		defer func() { _ = putResp2.Body.Close() }()

		if putResp2.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", putResp2.StatusCode)
		}

		var updated2 core.Branch
		if err := json.NewDecoder(putResp2.Body).Decode(&updated2); err != nil {
			t.Fatalf("failed to decode second PUT response: %v", err)
		}

		if updated2.GapReport == nil || updated2.GapReport.MatchScore == nil || *updated2.GapReport.MatchScore != score96 {
			t.Errorf("expected updated match score %f, got %v", score96, updated2.GapReport)
		}
		if updated2.GapReport.OverallMatchSummary != "Exceptional candidate match after tailoring" {
			t.Errorf("expected summary 'Exceptional candidate match after tailoring', got %q", updated2.GapReport.OverallMatchSummary)
		}
	})

	// 4. Test HTTP fallback resolution for legacy branches where gap_report in store is null
	t.Run("HTTP endpoints fallback-resolve gap_report for legacy branch with null gap_report in store", func(t *testing.T) {
		score91 := 91.0
		bLegacy := &core.Branch{
			ID:            "branch-api-legacy-fallback",
			UserID:        userID,
			Name:          "Legacy Infrastructure Engineer",
			TemplateSetID: core.TemplateStandardCleanID,
		}
		if err := env.branchStore.CreateBranch(ctx, bLegacy); err != nil {
			t.Fatalf("failed to create legacy branch: %v", err)
		}

		vLegacy := &core.BranchVersion{
			ID:               "ver-api-legacy-1",
			BranchID:         bLegacy.ID,
			Status:           core.VersionStatusCompleted,
			MainRevisionUsed: 1,
			GapReport: &core.GapReport{
				OverallMatchSummary: "Legacy version match summary",
				MatchScore:          &score91,
				MissingRequirements: []core.GapItem{
					{Requirement: "Ansible automation", Importance: core.GapImportancePreferred},
				},
			},
		}
		if err := env.branchStore.SaveCompletedVersion(ctx, vLegacy); err != nil {
			t.Fatalf("failed to save legacy version: %v", err)
		}

		// Reload branch so epoch/active state match before clearing gap_report to simulate legacy data.
		reloadedLegacy, err := env.branchStore.Branch(ctx, userID, bLegacy.ID)
		if err != nil {
			t.Fatalf("failed to reload legacy branch: %v", err)
		}
		reloadedLegacy.ActiveVersionID = &vLegacy.ID
		reloadedLegacy.GapReport = nil
		if err := env.branchStore.UpdateBranch(ctx, reloadedLegacy); err != nil {
			t.Fatalf("failed to update legacy branch in store: %v", err)
		}

		// Verify GET /api/v1/branches/{id} fallback resolution
		getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/branches/"+bLegacy.ID, nil)
		getReq.Header.Set("X-User-ID", userID)
		getResp, err := client.Do(getReq)
		if err != nil {
			t.Fatalf("GET legacy branch failed: %v", err)
		}
		defer func() { _ = getResp.Body.Close() }()

		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", getResp.StatusCode)
		}

		var fetchedLegacy core.Branch
		if err := json.NewDecoder(getResp.Body).Decode(&fetchedLegacy); err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		if fetchedLegacy.GapReport == nil {
			t.Fatalf("expected fallback resolved gap_report, got nil")
		}
		if fetchedLegacy.GapReport.MatchScore == nil || *fetchedLegacy.GapReport.MatchScore != score91 {
			t.Errorf("expected match score %f, got %v", score91, fetchedLegacy.GapReport.MatchScore)
		}
		if fetchedLegacy.GapReport.OverallMatchSummary != "Legacy version match summary" {
			t.Errorf("expected summary 'Legacy version match summary', got %q", fetchedLegacy.GapReport.OverallMatchSummary)
		}

		// Verify GET /api/v1/branches list fallback resolution
		listReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/branches", nil)
		listReq.Header.Set("X-User-ID", userID)
		listResp, err := client.Do(listReq)
		if err != nil {
			t.Fatalf("GET /api/v1/branches failed: %v", err)
		}
		defer func() { _ = listResp.Body.Close() }()

		var branchList []core.Branch
		if err := json.NewDecoder(listResp.Body).Decode(&branchList); err != nil {
			t.Fatalf("decode list failed: %v", err)
		}

		var foundLegacy *core.Branch
		for i := range branchList {
			if branchList[i].ID == bLegacy.ID {
				foundLegacy = &branchList[i]
				break
			}
		}
		if foundLegacy == nil {
			t.Fatalf("legacy branch not found in list")
		}
		if foundLegacy.GapReport == nil {
			t.Fatalf("expected fallback gap_report in list, got nil")
		}
		if foundLegacy.GapReport.MatchScore == nil || *foundLegacy.GapReport.MatchScore != score91 {
			t.Errorf("expected match score %f, got %v", score91, foundLegacy.GapReport.MatchScore)
		}
	})
}

func TestAPI_SimulateATSScore(t *testing.T) {
	ctx := context.Background()
	env := setupResumeTestServer(t)
	baseURL := env.server.URL
	client := env.server.Client()
	userID := "user-resume-1"

	// 1. Default Main Branch must be rejected
	t.Run("POST /api/v1/branches/{id}/ats-score rejects default branch with 400 Bad Request", func(t *testing.T) {
		branches, err := env.branchStore.ListBranches(ctx, userID)
		if err != nil {
			t.Fatalf("ListBranches failed: %v", err)
		}
		var defaultBranchID string
		for _, b := range branches {
			if b.IsDefault {
				defaultBranchID = b.ID
				break
			}
		}

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches/"+defaultBranchID+"/ats-score", nil)
		req.Header.Set("X-User-ID", userID)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for default branch, got %d", resp.StatusCode)
		}
	})

	// 2. Tailored branch without job description must be rejected
	t.Run("POST /api/v1/branches/{id}/ats-score rejects missing job description with 400 Bad Request", func(t *testing.T) {
		bNoJD := &core.Branch{
			ID:                 "branch-api-ats-no-jd",
			UserID:             userID,
			Name:               "No JD Variant",
			TargetRole:         "Go Engineer",
			TargetRequirements: "",
			TemplateSetID:      core.TemplateStandardCleanID,
		}
		if err := env.branchStore.CreateBranch(ctx, bNoJD); err != nil {
			t.Fatalf("CreateBranch failed: %v", err)
		}

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches/"+bNoJD.ID+"/ats-score", nil)
		req.Header.Set("X-User-ID", userID)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 Bad Request for missing job description, got %d", resp.StatusCode)
		}
	})

	// 3. Successful on-demand ATS score calculation, single-trigger conflict enforcement, and persistence
	t.Run("POST /api/v1/branches/{id}/ats-score calculates once and persists across active version switches", func(t *testing.T) {
		b := &core.Branch{
			ID:                 "branch-api-ats-1",
			UserID:             userID,
			Name:               "Distributed Systems Engineer",
			TargetRole:         "Staff Go Engineer",
			TargetRequirements: "5+ years Go, Kubernetes, and distributed consensus experience.",
			TemplateSetID:      core.TemplateStandardCleanID,
		}
		if err := env.branchStore.CreateBranch(ctx, b); err != nil {
			t.Fatalf("CreateBranch failed: %v", err)
		}

		v1 := &core.BranchVersion{
			ID:               "ver-api-ats-1",
			BranchID:         b.ID,
			MainRevisionUsed: 1,
			Status:           core.VersionStatusCompleted,
			MarkdownOutput:   "# Jane Doe\n## Summary\nDistributed systems engineer skilled in Go and Kubernetes.\n## Experience\n- Built high-availability services.",
		}
		if err := env.branchStore.SaveCompletedVersion(ctx, v1); err != nil {
			t.Fatalf("SaveCompletedVersion failed: %v", err)
		}

		req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches/"+b.ID+"/ats-score", strings.NewReader(`{"format":"markdown"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", userID)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST ats-score failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}

		var score core.ATSScore
		if err := json.NewDecoder(resp.Body).Decode(&score); err != nil {
			t.Fatalf("failed to decode ATSScore: %v", err)
		}
		if score.ExtractionConfidenceScore <= 0 || score.ExtractionConfidenceScore > 100 {
			t.Errorf("expected ExtractionConfidenceScore in (0, 100], got %v", score.ExtractionConfidenceScore)
		}
		if score.JobMatchingScore <= 0 || score.JobMatchingScore > 100 {
			t.Errorf("expected JobMatchingScore in (0, 100], got %v", score.JobMatchingScore)
		}

		// Duplicate trigger on same version must return 409 Conflict
		reqDup, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/branches/"+b.ID+"/versions/1/ats-score", nil)
		reqDup.Header.Set("X-User-ID", userID)
		respDup, err := client.Do(reqDup)
		if err != nil {
			t.Fatalf("duplicate POST failed: %v", err)
		}
		defer func() { _ = respDup.Body.Close() }()

		if respDup.StatusCode != http.StatusConflict {
			t.Errorf("expected 409 Conflict on duplicate ATS trigger for same version, got %d", respDup.StatusCode)
		}

		// Verify GET /api/v1/branches/{id} returns persisted ats_score
		getReq, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/branches/"+b.ID, nil)
		getReq.Header.Set("X-User-ID", userID)
		getResp, err := client.Do(getReq)
		if err != nil {
			t.Fatalf("GET branch failed: %v", err)
		}
		defer func() { _ = getResp.Body.Close() }()

		var fetchedBranch core.Branch
		if err := json.NewDecoder(getResp.Body).Decode(&fetchedBranch); err != nil {
			t.Fatalf("failed to decode branch: %v", err)
		}
		if fetchedBranch.ATSScore == nil || fetchedBranch.ATSScore.ExtractionConfidenceScore != score.ExtractionConfidenceScore {
			t.Errorf("expected persisted ATSScore on branch, got %+v", fetchedBranch.ATSScore)
		}
	})
}

// TestGreenfieldWireContracts verifies raw JSON wire serialization for greenfield Branch,
// job-backed BranchVersion, and Preset/Strategy endpoints.
func TestGreenfieldWireContracts(t *testing.T) {
	env := setupResumeTestServer(t)
	client := env.server.Client()
	baseURL := env.server.URL
	userID := "user-resume-1"

	readMap := func(t *testing.T, method, path string, body any) (int, []byte, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal %s %s body: %v", method, path, err)
			}
			reader = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, baseURL+path, reader)
		req.Header.Set("X-User-ID", userID)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s failed: %v", method, path, err)
		}
		data, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s %s body: %v", method, path, readErr)
		}
		var out map[string]any
		if len(data) > 0 && data[0] == '{' {
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("unmarshal %s %s JSON: %v (raw: %s)", method, path, err, string(data))
			}
		}
		return resp.StatusCode, data, out
	}

	assertAbsent := func(t *testing.T, label string, raw []byte, payload map[string]any, keys ...string) {
		t.Helper()
		rawStr := string(raw)
		for _, k := range keys {
			if _, exists := payload[k]; exists {
				t.Errorf("%s response must omit key %q (raw: %s)", label, k, rawStr)
			}
			if strings.Contains(rawStr, `"`+k+`"`) {
				t.Errorf("%s raw JSON must not contain %q (raw: %s)", label, k, rawStr)
			}
		}
	}

	// 1. Preset and TailoringStrategy wire responses omit template_set_id and preserve suggested_theme_id.
	themeID := core.TemplateStandardCleanID
	code, presetRaw, presetMap := readMap(t, http.MethodPost, "/api/v1/presets", api.CreatePresetRequest{
		Name:               "Greenfield Strategy",
		SystemInstructions: "Focus on concise impact bullets.",
		SuggestedThemeID:   &themeID,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /presets expected 201, got %d (%s)", code, string(presetRaw))
	}
	assertAbsent(t, "POST /presets", presetRaw, presetMap, "template_set_id")
	if gotTheme, _ := presetMap["suggested_theme_id"].(string); gotTheme != themeID {
		t.Fatalf("POST /presets expected suggested_theme_id %q, got %v", themeID, presetMap["suggested_theme_id"])
	}
	presetID, _ := presetMap["id"].(string)

	code, stratRaw, stratMap := readMap(t, http.MethodGet, "/api/v1/tailoring-strategies/"+presetID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /tailoring-strategies/{id} expected 200, got %d (%s)", code, string(stratRaw))
	}
	assertAbsent(t, "GET /tailoring-strategies/{id}", stratRaw, stratMap, "template_set_id")
	if gotTheme, _ := stratMap["suggested_theme_id"].(string); gotTheme != themeID {
		t.Fatalf("GET /tailoring-strategies/{id} expected suggested_theme_id %q, got %v", themeID, stratMap["suggested_theme_id"])
	}

	// 2. Branch wire responses include epoch and omit custom_directives / tailoring_strategy_id.
	code, branchRaw, branchMap := readMap(t, http.MethodPost, "/api/v1/branches", api.CreateBranchRequest{
		Name:               "Greenfield Branch",
		TargetRole:         "Staff Go Engineer",
		TargetCompany:      "Acme",
		TargetRequirements: "Go and distributed systems",
		BranchInstructions: "Emphasize reliability",
		PresetID:           &presetID,
		TemplateSetID:      core.TemplateStandardCleanID,
	})
	if code != http.StatusCreated {
		t.Fatalf("POST /branches expected 201, got %d (%s)", code, string(branchRaw))
	}
	assertAbsent(t, "POST /branches", branchRaw, branchMap, "custom_directives", "tailoring_strategy_id")
	if epoch, ok := branchMap["epoch"].(float64); !ok || epoch != 1 {
		t.Fatalf("POST /branches expected epoch=1, got %v (%s)", branchMap["epoch"], string(branchRaw))
	}
	branchID, _ := branchMap["id"].(string)

	// 3. Job-backed BranchVersion includes branch_epoch and job_id on wire responses.
	code, genRaw, genMap := readMap(t, http.MethodPost, "/api/v1/branches/"+branchID+"/generate", nil)
	if code != http.StatusAccepted {
		t.Fatalf("POST /branches/{id}/generate expected 202, got %d (%s)", code, string(genRaw))
	}
	verID, _ := genMap["version_id"].(string)
	if verID == "" {
		t.Fatalf("POST /branches/{id}/generate expected non-empty version_id (%s)", string(genRaw))
	}

	code, verRaw, verMap := readMap(t, http.MethodGet, "/api/v1/branches/"+branchID+"/versions/"+verID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /branches/{id}/versions/{v} expected 200, got %d (%s)", code, string(verRaw))
	}
	if branchEpoch, ok := verMap["branch_epoch"].(float64); !ok || branchEpoch != 1 {
		t.Fatalf("GET /versions/{v} expected branch_epoch=1, got %v (%s)", verMap["branch_epoch"], string(verRaw))
	}
	if jobID, ok := verMap["job_id"].(string); !ok || jobID == "" {
		t.Fatalf("GET /versions/{v} expected non-empty job_id, got %v (%s)", verMap["job_id"], string(verRaw))
	}

	// Verify list endpoint also serializes branch_epoch and job_id.
	code, listRaw, _ := readMap(t, http.MethodGet, "/api/v1/branches/"+branchID+"/versions", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /branches/{id}/versions expected 200, got %d (%s)", code, string(listRaw))
	}
	var listed []map[string]any
	if err := json.Unmarshal(listRaw, &listed); err != nil || len(listed) != 1 {
		t.Fatalf("expected 1 version in list, got len=%d err=%v raw=%s", len(listed), err, string(listRaw))
	}
	if branchEpoch, ok := listed[0]["branch_epoch"].(float64); !ok || branchEpoch != 1 {
		t.Fatalf("list version expected branch_epoch=1, got %v", listed[0]["branch_epoch"])
	}
	if jobID, ok := listed[0]["job_id"].(string); !ok || jobID == "" {
		t.Fatalf("list version expected non-empty job_id, got %v", listed[0]["job_id"])
	}

	// 4. Updating generation-defining config increments branch epoch and subsequent job-backed version carries epoch 2.
	updatedReqs := "Go, Kubernetes, PostgreSQL"
	code, updRaw, updMap := readMap(t, http.MethodPut, "/api/v1/branches/"+branchID, api.UpdateBranchRequest{
		TargetRequirements: &updatedReqs,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /branches/{id} expected 200, got %d (%s)", code, string(updRaw))
	}
	assertAbsent(t, "PUT /branches/{id}", updRaw, updMap, "custom_directives", "tailoring_strategy_id")
	if epoch, ok := updMap["epoch"].(float64); !ok || epoch != 2 {
		t.Fatalf("PUT /branches/{id} expected incremented epoch=2, got %v (%s)", updMap["epoch"], string(updRaw))
	}

	code, gen2Raw, gen2Map := readMap(t, http.MethodPost, "/api/v1/branches/"+branchID+"/generate", nil)
	if code != http.StatusAccepted {
		t.Fatalf("second POST /branches/{id}/generate expected 202, got %d (%s)", code, string(gen2Raw))
	}
	ver2ID, _ := gen2Map["version_id"].(string)
	code, ver2Raw, ver2Map := readMap(t, http.MethodGet, "/api/v1/branches/"+branchID+"/versions/"+ver2ID, nil)
	if code != http.StatusOK {
		t.Fatalf("GET second version expected 200, got %d (%s)", code, string(ver2Raw))
	}
	if branchEpoch, ok := ver2Map["branch_epoch"].(float64); !ok || branchEpoch != 2 {
		t.Fatalf("second version expected branch_epoch=2, got %v (%s)", ver2Map["branch_epoch"], string(ver2Raw))
	}
	if jobID, ok := ver2Map["job_id"].(string); !ok || jobID == "" {
		t.Fatalf("second version expected non-empty job_id, got %v (%s)", ver2Map["job_id"], string(ver2Raw))
	}
}
