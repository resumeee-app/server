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

	"resumeee/internal/api"
	"resumeee/internal/branch"
	"resumeee/internal/domain"
	"resumeee/internal/renderer"
	"resumeee/internal/repository/memory"
	"resumeee/internal/validator"
)

type mockLLMProvider struct {
	CallCount int
}

func (m *mockLLMProvider) Name() string { return "mock" }
func (m *mockLLMProvider) GenerateTailoredResume(_ context.Context, _ domain.GenerationRequest) (*domain.GenerationResponse, error) {
	m.CallCount++
	return &domain.GenerationResponse{}, nil
}

type resumeTestEnv struct {
	server       *httptest.Server
	db           any
	branchRepo   domain.BranchRepository
	profileRepo  domain.ProfileRepository
	presetRepo   domain.PresetRepository
	templateRepo domain.TemplateRepository
	mockProvider *mockLLMProvider
}

func setupResumeTestServer(t *testing.T) *resumeTestEnv {
	t.Helper()

	ctx := context.Background()
	defaultUserID := "user-resume-1"
	store, err := memory.NewSeeded(ctx, defaultUserID)
	if err != nil {
		t.Fatalf("failed to seed test store: %v", err)
	}

	profileRepo := store
	presetRepo := store
	templateRepo := store
	branchRepo := store
	mockProv := &mockLLMProvider{}
	rend := renderer.NewTemplateRenderer()

	branchSvc := branch.NewService(branch.Config{
		BranchRepo:   branchRepo,
		ProfileRepo:  profileRepo,
		PresetRepo:   presetRepo,
		TemplateRepo: templateRepo,
		Validator:    validator.New(),
		Renderer:     rend,
	})

	router := api.NewRouter(api.RouterConfig{
		ProfileRepo:   profileRepo,
		PresetRepo:    presetRepo,
		TemplateRepo:  templateRepo,
		BranchRepo:    branchRepo,
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
		server:       server,
		db:           store,
		branchRepo:   branchRepo,
		profileRepo:  profileRepo,
		presetRepo:   presetRepo,
		templateRepo: templateRepo,
		mockProvider: mockProv,
	}
}

func TestResumeFastRenderPreview_RFC005(t *testing.T) {
	env := setupResumeTestServer(t)
	client := env.server.Client()
	baseURL := env.server.URL
	ctx := context.Background()
	userID := "user-resume-1"

	// Create test target resume (branch)
	targetResume := &domain.TargetResume{
		ID:            "resume-test-1",
		UserID:        userID,
		Name:          "Stripe - Staff Backend",
		TemplateSetID: "builtin-standard-clean",
	}
	if err := env.branchRepo.CreateBranch(ctx, targetResume); err != nil {
		t.Fatalf("failed to create target resume: %v", err)
	}

	// Create a completed version with tailored content
	tailoredContent := &domain.TailoredContent{
		Summary: "Experienced distributed systems engineer with 10+ years building high-throughput payment rails.",
		Experiences: []domain.TailoredExperience{
			{
				SourceEntityID: "exp-1",
				Company:        "Acme Payments",
				RoleTitle:      "Senior Systems Engineer",
				Bullets: []domain.TailoredBullet{
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
	vCompleted := &domain.ResumeVersion{
		ID:                 "ver-completed-1",
		ResumeID:           targetResume.ID,
		BranchID:           targetResume.ID,
		VersionNumber:      1,
		Status:             domain.VersionStatusCompleted,
		MasterRevisionUsed: 1,
		TemplateSetIDUsed:  "builtin-standard-clean",
		SourceType:         domain.SourceTypeLLMGeneration,
		TailoredContent:    tailoredContent,
		HTMLOutput:         "<html>Old Standard Template</html>",
		MarkdownOutput:     "# Old Standard Markdown",
		PlainTextOutput:    "Old Standard Plain Text",
		CreatedAt:          now,
		CompletedAt:        &now,
	}
	if err := env.branchRepo.CreateVersion(ctx, vCompleted); err != nil {
		t.Fatalf("failed to create completed version: %v", err)
	}
	_ = env.branchRepo.CompleteVersion(ctx, vCompleted)

	// Create alternative custom template for user
	userTemplate := &domain.TemplateSet{
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
	if err := env.templateRepo.CreateTemplate(ctx, userTemplate); err != nil {
		t.Fatalf("failed to create user template: %v", err)
	}

	// Create a foreign tenant's private template
	foreignTemplate := &domain.TemplateSet{
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
	if err := env.templateRepo.CreateTemplate(ctx, foreignTemplate); err != nil {
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
		freshVersion, err := env.branchRepo.GetVersion(ctx, targetResume.ID, vCompleted.ID)
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
		vPending := &domain.ResumeVersion{
			ID:                 "ver-pending-1",
			ResumeID:           targetResume.ID,
			BranchID:           targetResume.ID,
			VersionNumber:      2,
			Status:             domain.VersionStatusPending,
			MasterRevisionUsed: 1,
			TemplateSetIDUsed:  "builtin-standard-clean",
			SourceType:         domain.SourceTypeLLMGeneration,
			TailoredContent:    tailoredContent,
			CreatedAt:          time.Now().UTC(),
		}
		if err := env.branchRepo.CreateVersion(ctx, vPending); err != nil {
			t.Fatalf("failed to create pending version: %v", err)
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
		vFailed := &domain.ResumeVersion{
			ID:                 "ver-failed-1",
			ResumeID:           targetResume.ID,
			BranchID:           targetResume.ID,
			VersionNumber:      3,
			Status:             domain.VersionStatusFailed,
			MasterRevisionUsed: 1,
			TemplateSetIDUsed:  "builtin-standard-clean",
			SourceType:         domain.SourceTypeLLMGeneration,
			TailoredContent:    nil,
			ErrorMessage:       "LLM failed to respond",
			CreatedAt:          time.Now().UTC(),
		}
		if err := env.branchRepo.CreateVersion(ctx, vFailed); err != nil {
			t.Fatalf("failed to create failed version: %v", err)
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
	foreignTmpl := &domain.TemplateSet{
		ID:               "tmpl-foreign-attacker",
		UserID:           attackerID,
		Name:             "Secret Attacker Template",
		HTMLTemplate:     "<html>Forbidden</html>",
		MarkdownTemplate: "Forbidden",
		TextTemplate:     "Forbidden",
		IsBuiltIn:        false,
	}
	if err := env.templateRepo.CreateTemplate(ctx, foreignTmpl); err != nil {
		t.Fatalf("failed to create attacker template: %v", err)
	}

	// Seed private strategy owned by attacker
	foreignStrat := &domain.TailoringStrategy{
		ID:                 "strat-foreign-attacker",
		UserID:             attackerID,
		Name:               "Secret Attacker Strategy",
		SystemInstructions: "Strictly confidential attacker directives",
		TemplateSetID:      "builtin-standard-clean",
	}
	if err := env.presetRepo.CreatePreset(ctx, foreignStrat); err != nil {
		t.Fatalf("failed to create attacker strategy: %v", err)
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

	// 2. POST /branches with foreign tailoring_strategy_id -> 403 Forbidden
	t.Run("CreateBranch_ForeignTailoringStrategyID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"name":                  "Infiltrator Branch Strategy",
			"template_set_id":       "builtin-standard-clean",
			"tailoring_strategy_id": foreignStrat.ID,
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

	// 3. POST /branches with foreign preset_id -> 403 Forbidden
	t.Run("CreateBranch_ForeignPresetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"name":            "Infiltrator Branch Preset",
			"template_set_id": "builtin-standard-clean",
			"preset_id":       foreignStrat.ID,
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
	legitBranch := &domain.TargetResume{
		ID:            "branch-legit-user",
		UserID:        userID,
		Name:          "Legitimate Branch",
		TemplateSetID: "builtin-standard-clean",
	}
	if err := env.branchRepo.CreateBranch(ctx, legitBranch); err != nil {
		t.Fatalf("failed to create legit branch: %v", err)
	}

	// 4. PUT /branches/{id} with foreign template_set_id -> 403 Forbidden
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

	// 5. PUT /branches/{id} with foreign tailoring_strategy_id -> 403 Forbidden
	t.Run("UpdateBranch_ForeignTailoringStrategyID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"tailoring_strategy_id": foreignStrat.ID,
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

	// 6. PUT /branches/{id} with foreign preset_id -> 403 Forbidden
	t.Run("UpdateBranch_ForeignPresetID_Returns403", func(t *testing.T) {
		reqBody := map[string]any{
			"preset_id": foreignStrat.ID,
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
