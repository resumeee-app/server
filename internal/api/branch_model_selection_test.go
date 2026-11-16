package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/branch"
	"github.com/resumeee-app/resumeee-core/memory"
)

type mockBranchModelEnumerator struct {
	models []core.ModelOption
}

func (m *mockBranchModelEnumerator) AvailableModels() []core.ModelOption {
	return m.models
}

type mockVerifierForBranchModel struct {
	verifyFunc func(ctx context.Context, token string) (*core.TokenInfo, error)
}

func (m *mockVerifierForBranchModel) VerifyIDToken(ctx context.Context, token string) (*core.TokenInfo, error) {
	if m.verifyFunc != nil {
		return m.verifyFunc(ctx, token)
	}
	return nil, core.ErrUnauthorized
}

func setupBranchModelTestServer(t *testing.T, enumerator core.ModelEnumerator) (*httptest.Server, *core.Branch) {
	return setupBranchModelTestServerWithAdmin(t, enumerator, true)
}

func setupBranchModelTestServerWithAdmin(t *testing.T, enumerator core.ModelEnumerator, admin bool) (*httptest.Server, *core.Branch) {
	t.Helper()

	ctx := context.Background()
	defaultUser := "test-user-models"
	store, err := memory.NewSeeded(ctx, defaultUser)
	if err != nil {
		t.Fatalf("failed to seed memory store: %v", err)
	}

	var authProvider auth.Provider
	if !admin {
		authProvider = auth.NewTokenVerifierProvider("test", &mockVerifierForBranchModel{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "token-regular-user" {
					return &core.TokenInfo{UID: defaultUser, Email: "user@example.com", Role: "user", Admin: false}, nil
				}
				return nil, core.ErrUnauthorized
			},
		})
	}

	cfg := api.RouterConfig{
		DefaultUserID:   defaultUser,
		BranchStore:     store.Branches(),
		ProfileStore:    store.Profiles(),
		PresetStore:     store.Presets(),
		TemplateStore:   store.Templates(),
		QuotaStore:      store.Quotas(),
		ModelEnumerator: enumerator,
		Admin:           admin,
		AuthProvider:    authProvider,
	}

	handler := api.NewRouter(cfg)
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
	})

	// Create a non-default branch for testing
	branchService := branch.New(branch.Config{
		Branches:  store.Branches(),
		Jobs:      store.Jobs(),
		Profiles:  store.Profiles(),
		Presets:   store.Presets(),
		Templates: store.Templates(),
		Quotas:    store.Quotas(),
	})

	testBranch := &core.Branch{
		UserID:             defaultUser,
		Name:               "Targeted Staff Backend Role",
		TemplateSetID:      core.TemplateStandardCleanID,
		TargetRequirements: "Distributed systems, Go, Kubernetes",
	}
	created, err := branchService.CreateBranch(ctx, testBranch)
	if err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	return server, created
}

func TestBranchModelSelection_UpdateReasoningAndModel(t *testing.T) {
	mockModels := []core.ModelOption{
		{Provider: "gemini", Model: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"},
		{Provider: "gemini", Model: "gemini-4-argon", DisplayName: "Gemini 4 Argon"},
		{Provider: "anthropic", Model: "claude-3-7-sonnet", DisplayName: "Claude 3.7 Sonnet"},
	}
	enumerator := &mockBranchModelEnumerator{models: mockModels}
	server, testBranch := setupBranchModelTestServer(t, enumerator)
	client := server.Client()

	t.Run("table-driven reasoning_level validation on PUT", func(t *testing.T) {
		tests := []struct {
			name           string
			reasoningLevel string
			expectStatus   int
		}{
			{name: "update to medium", reasoningLevel: "medium", expectStatus: http.StatusOK},
			{name: "update to high", reasoningLevel: "high", expectStatus: http.StatusOK},
			{name: "update to low", reasoningLevel: "low", expectStatus: http.StatusOK},
			{name: "update to none", reasoningLevel: "none", expectStatus: http.StatusOK},
			{name: "clear reasoning level with empty string", reasoningLevel: "", expectStatus: http.StatusOK},
			{name: "invalid reasoning level returns 400", reasoningLevel: "turbo-overdrive", expectStatus: http.StatusBadRequest},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				req := api.UpdateBranchRequest{
					ReasoningLevel: &tc.reasoningLevel,
				}
				resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, nil)
				if resp.StatusCode != tc.expectStatus {
					t.Fatalf("expected status %d, got %d: %s", tc.expectStatus, resp.StatusCode, string(body))
				}

				if tc.expectStatus == http.StatusOK {
					updated := decodeJSON[core.Branch](t, body)
					if updated.ReasoningLevel != tc.reasoningLevel {
						t.Errorf("expected ReasoningLevel %q, got %q", tc.reasoningLevel, updated.ReasoningLevel)
					}
				} else {
					errResp := decodeJSON[api.ErrorResponse](t, body)
					if !strings.Contains(errResp.Error.Message, "reasoning_level must be one of") {
						t.Errorf("expected error message to mention valid reasoning levels, got %q", errResp.Error.Message)
					}
				}
			})
		}
	})

	t.Run("update model_override and provider_override together", func(t *testing.T) {
		prov := "anthropic"
		mod := "claude-3-7-sonnet"
		reasoning := "high"
		req := api.UpdateBranchRequest{
			ProviderOverride: &prov,
			ModelOverride:    &mod,
			ReasoningLevel:   &reasoning,
		}

		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, string(body))
		}

		updated := decodeJSON[core.Branch](t, body)
		if updated.ProviderOverride != prov {
			t.Errorf("expected ProviderOverride %q, got %q", prov, updated.ProviderOverride)
		}
		if updated.ModelOverride != mod {
			t.Errorf("expected ModelOverride %q, got %q", mod, updated.ModelOverride)
		}
		if updated.ReasoningLevel != reasoning {
			t.Errorf("expected ReasoningLevel %q, got %q", reasoning, updated.ReasoningLevel)
		}
	})

	t.Run("reject invalid model_override not in enumerator", func(t *testing.T) {
		mod := "nonexistent-gpt-9"
		req := api.UpdateBranchRequest{
			ModelOverride: &mod,
		}

		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("reject model_override mismatched with provider", func(t *testing.T) {
		prov := "gemini"
		mod := "claude-3-7-sonnet" // Belongs to anthropic
		req := api.UpdateBranchRequest{
			ProviderOverride: &prov,
			ModelOverride:    &mod,
		}

		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("partial update with incompatible provider returns 400 Bad Request", func(t *testing.T) {
		initProv := "gemini"
		initMod := "gemini-4-argon"
		setupReq := api.UpdateBranchRequest{
			ProviderOverride: &initProv,
			ModelOverride:    &initMod,
		}
		setupResp, _ := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, setupReq, nil)
		if setupResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK setting initial model, got %d", setupResp.StatusCode)
		}

		newProv := "anthropic"
		partialReq := api.UpdateBranchRequest{
			ProviderOverride: &newProv,
		}
		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, partialReq, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request on incompatible partial update, got %d: %s", resp.StatusCode, string(body))
		}
		if !strings.Contains(string(body), "model_override is not available for the specified provider") {
			t.Errorf("expected error message to mention provider mismatch, got %s", string(body))
		}

		getResp, getBody := doRequest(t, client, http.MethodGet, server.URL+"/api/v1/branches/"+testBranch.ID, nil, nil)
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK retrieving branch, got %d", getResp.StatusCode)
		}
		current := decodeJSON[core.Branch](t, getBody)
		if current.ProviderOverride != "gemini" || current.ModelOverride != "gemini-4-argon" {
			t.Errorf("branch state was unexpectedly mutated: provider=%q model=%q", current.ProviderOverride, current.ModelOverride)
		}
	})
}

func TestBranchModelSelection_NonAdminForbidden(t *testing.T) {
	mockModels := []core.ModelOption{
		{Provider: "gemini", Model: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"},
	}
	enumerator := &mockBranchModelEnumerator{models: mockModels}
	server, testBranch := setupBranchModelTestServerWithAdmin(t, enumerator, false)
	client := server.Client()
	authHeaders := map[string]string{"Authorization": "Bearer token-regular-user"}

	t.Run("non-admin POST with provider_override returns 403 Forbidden", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:             "Non Admin Branch",
			TemplateSetID:    core.TemplateStandardCleanID,
			ProviderOverride: "gemini",
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin POST with model_override returns 403 Forbidden", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:          "Non Admin Branch",
			TemplateSetID: core.TemplateStandardCleanID,
			ModelOverride: "gemini-3.8-flash",
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin POST with reasoning_level returns 403 Forbidden", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:           "Non Admin Branch",
			TemplateSetID:  core.TemplateStandardCleanID,
			ReasoningLevel: "low",
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin POST without model fields succeeds (201 Created)", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:          "Standard Non-Admin Branch",
			TemplateSetID: core.TemplateStandardCleanID,
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, authHeaders)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created for standard creation, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin PUT with provider_override returns 403 Forbidden", func(t *testing.T) {
		prov := "gemini"
		req := api.UpdateBranchRequest{
			ProviderOverride: &prov,
		}
		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin PUT with model_override returns 403 Forbidden", func(t *testing.T) {
		mod := "gemini-3.8-flash"
		req := api.UpdateBranchRequest{
			ModelOverride: &mod,
		}
		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin PUT with reasoning_level returns 403 Forbidden", func(t *testing.T) {
		rLvl := "high"
		req := api.UpdateBranchRequest{
			ReasoningLevel: &rLvl,
		}
		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, authHeaders)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("non-admin PUT with standard fields succeeds (200 OK)", func(t *testing.T) {
		name := "Renamed by Non-Admin"
		req := api.UpdateBranchRequest{
			Name: &name,
		}
		resp, body := doRequest(t, client, http.MethodPut, server.URL+"/api/v1/branches/"+testBranch.ID, req, authHeaders)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for non-admin standard update, got %d: %s", resp.StatusCode, string(body))
		}
	})
}

func TestBranchModelSelection_CreateBranchWithReasoning(t *testing.T) {
	mockModels := []core.ModelOption{
		{Provider: "gemini", Model: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash"},
	}
	enumerator := &mockBranchModelEnumerator{models: mockModels}
	server, _ := setupBranchModelTestServer(t, enumerator)
	client := server.Client()

	t.Run("creates branch with valid reasoning_level", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:             "New Targeted Role",
			TemplateSetID:    core.TemplateStandardCleanID,
			ProviderOverride: "gemini",
			ModelOverride:    "gemini-3.8-flash",
			ReasoningLevel:   "high",
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201 Created, got %d: %s", resp.StatusCode, string(body))
		}
		created := decodeJSON[core.Branch](t, body)
		if created.ReasoningLevel != "high" {
			t.Errorf("expected ReasoningLevel %q, got %q", "high", created.ReasoningLevel)
		}
	})

	t.Run("rejects branch creation with invalid reasoning_level", func(t *testing.T) {
		req := api.CreateBranchRequest{
			Name:           "Invalid Reasoning Role",
			TemplateSetID:  core.TemplateStandardCleanID,
			ReasoningLevel: "invalid-effort",
		}
		resp, body := doRequest(t, client, http.MethodPost, server.URL+"/api/v1/branches", req, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("Admin true paired with external AuthProvider still rejects non-admin overrides", func(t *testing.T) {
		ctx := context.Background()
		defaultUser := "user-mixed-cfg"
		store, err := memory.NewSeeded(ctx, defaultUser)
		if err != nil {
			t.Fatalf("failed to seed memory store: %v", err)
		}
		authProvider := auth.NewTokenVerifierProvider("test", &mockVerifierForBranchModel{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "token-regular" {
					return &core.TokenInfo{UID: defaultUser, Email: "user@example.com", Role: "user", Admin: false}, nil
				}
				return nil, core.ErrUnauthorized
			},
		})
		cfg := api.NewRouterConfigFromStore(store)
		cfg.DefaultUserID = defaultUser
		cfg.Admin = true
		cfg.AuthProvider = authProvider
		cfg.ModelEnumerator = enumerator

		mixedSrv := httptest.NewServer(api.NewRouter(cfg))
		defer mixedSrv.Close()

		req := api.CreateBranchRequest{
			Name:             "Non-Admin Override Attempt",
			TemplateSetID:    core.TemplateStandardCleanID,
			ProviderOverride: "gemini",
			ModelOverride:    "gemini-3.8-flash",
		}
		resp, body := doRequest(t, mixedSrv.Client(), http.MethodPost, mixedSrv.URL+"/api/v1/branches", req, map[string]string{
			"Authorization": "Bearer token-regular",
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden when Admin=true is paired with external AuthProvider, got %d: %s", resp.StatusCode, string(body))
		}
	})
}
