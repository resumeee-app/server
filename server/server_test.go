package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/core/provenance"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/resumeee-api/auth"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	tcpAddr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		_ = l.Close()
		t.Fatalf("expected *net.TCPAddr listener address, got %T", l.Addr())
	}
	port := tcpAddr.Port
	_ = l.Close()
	return port
}

func waitForHealthy(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/api/v1/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for server at %s to become healthy", baseURL)
}

type mockLLMClient struct {
	name             string
	defaultModel     string
	structuredOutput bool
}

func (m *mockLLMClient) Name() string {
	return m.name
}

func (m *mockLLMClient) DefaultModel() string {
	return m.defaultModel
}

func (m *mockLLMClient) Models() []core.ModelOption {
	return []core.ModelOption{
		{
			Provider:    m.name,
			Model:       m.defaultModel,
			DisplayName: "Custom Injected Model",
			IsDefault:   true,
		},
	}
}

func (m *mockLLMClient) SupportsStructuredOutput(_ string) bool {
	return m.structuredOutput
}

func (m *mockLLMClient) Complete(_ context.Context, _ llm.Request) (*llm.Response, error) {
	return &llm.Response{
		Text:       `{"tailored_content":{}}`,
		StatusCode: http.StatusOK,
		Usage: core.TokenUsage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}, nil
}

type mockAuthProvider struct {
	expectedToken string
	userID        string
}

func (m *mockAuthProvider) Name() string {
	return "custom-auth"
}

func (m *mockAuthProvider) VerifyIDToken(_ context.Context, idToken string) (*core.TokenInfo, error) {
	if idToken == m.expectedToken {
		return &core.TokenInfo{
			UID:   m.userID,
			Email: "custom@example.com",
		}, nil
	}
	return nil, errors.New("invalid token")
}

type mockPDFExtractor struct {
	extracted string
}

func (m *mockPDFExtractor) ExtractText(_ context.Context, _ io.ReaderAt, _ int64) (string, error) {
	return m.extracted, nil
}

func TestServer_RunWithInjectedDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}

	customUser := "custom-tenant"
	if err := memStore.Profiles().UpsertProfile(ctx, &core.Profile{
		UserID:   customUser,
		FullName: "Ada Lovelace",
		Headline: "Analytical Engine Architect",
	}); err != nil {
		t.Fatalf("failed to seed custom tenant profile: %v", err)
	}

	port := freePort(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	customClient := &mockLLMClient{
		name:         "custom-llm",
		defaultModel: "custom-model-v1",
	}

	srv, err := New(
		WithPort(port),
		WithAdminMode(true),
		WithDefaultUserID(customUser),
		WithStore(memStore),
		WithLLMClient(customClient),
		WithQuotaLimits(100, 500000),
		WithWorkerPoolConfig(4, 3),
		WithTimeouts(15*time.Second, 5*time.Minute),
		WithEnvironment("development"),
		WithLogLevel(slog.LevelDebug),
		WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealthy(t, baseURL)

	// 1. Verify profile uses the custom DefaultUserID and injected Store
	resp, err := http.Get(baseURL + "/api/v1/profile")
	if err != nil {
		t.Fatalf("GET /api/v1/profile failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("expected 200 OK for profile, got %d", resp.StatusCode)
	}
	var prof core.Profile
	if err := json.NewDecoder(resp.Body).Decode(&prof); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("failed to decode profile: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if prof.FullName != "Ada Lovelace" {
		t.Errorf("expected FullName 'Ada Lovelace', got %q", prof.FullName)
	}

	// 2. Verify injected LLMClient appears in /api/v1/models
	modelsResp, err := http.Get(baseURL + "/api/v1/models")
	if err != nil {
		t.Fatalf("GET /api/v1/models failed: %v", err)
	}
	if modelsResp.StatusCode != http.StatusOK {
		_ = modelsResp.Body.Close()
		t.Fatalf("expected 200 OK for models, got %d", modelsResp.StatusCode)
	}
	bodyBytes, err := io.ReadAll(modelsResp.Body)
	_ = modelsResp.Body.Close()
	if err != nil {
		t.Fatalf("failed reading models response: %v", err)
	}
	if !strings.Contains(string(bodyBytes), "custom-model-v1") {
		t.Errorf("expected models response to include 'custom-model-v1', got: %s", string(bodyBytes))
	}

	// 3. Verify graceful shutdown
	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on shutdown, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func TestServer_WithCustomAuthProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}

	authProv := &mockAuthProvider{
		expectedToken: "secret-custom-token",
		userID:        "default",
	}

	port := freePort(t)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx,
			WithPort(port),
			WithAdminMode(false),
			WithStore(memStore),
			WithAuthProvider(authProv),
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealthy(t, baseURL)

	// Unauthenticated request should fail with 401
	unauthResp, err := http.Get(baseURL + "/api/v1/profile")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, unauthResp.Body)
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized without token, got %d", unauthResp.StatusCode)
	}

	// Request with valid custom bearer token should succeed with 200
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/profile", nil)
	req.Header.Set("Authorization", "Bearer secret-custom-token")
	authResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, authResp.Body)
	_ = authResp.Body.Close()
	if authResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK with valid custom token, got %d", authResp.StatusCode)
	}

	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestServer_WithDomainServicesAndMCPToggle(t *testing.T) {
	tmpDir := t.TempDir()
	debugDir := filepath.Join(tmpDir, "llm-debug")
	port := freePort(t)

	memStore, err := memory.NewSeeded(context.Background())
	if err != nil {
		t.Fatalf("failed to create memory store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx,
			WithPort(port),
			WithAdminMode(true),
			WithLLMDebug(true, debugDir),
			WithStore(memStore),
			WithMCPEnabled(false),
			WithTemplateRenderer(renderer.New()),
			WithValidator(provenance.New()),
			WithPDFExtractor(&mockPDFExtractor{extracted: "mock pdf text"}),
			WithPromptCompiler(llm.NewPromptCompiler()),
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealthy(t, baseURL)

	// Verify LLM debug directory was created
	if info, err := os.Stat(debugDir); err != nil || !info.IsDir() {
		t.Errorf("expected debug dir %s to exist, err=%v", debugDir, err)
	}

	// Verify MCP route is disabled when WithMCPEnabled(false) is set
	mcpResp, err := http.Get(baseURL + "/mcp/sse")
	if err != nil {
		t.Fatalf("GET /mcp/sse failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, mcpResp.Body)
	_ = mcpResp.Body.Close()
	if mcpResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 Not Found when MCP is disabled, got %d", mcpResp.StatusCode)
	}

	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestServer_RequiresInjectedStoreAndAuth(t *testing.T) {
	t.Run("missing store returns error", func(t *testing.T) {
		err := Run(context.Background(), WithAdminMode(true))
		if err == nil || !strings.Contains(err.Error(), "WithStore") {
			t.Fatalf("expected error requiring WithStore, got: %v", err)
		}
	})

	t.Run("missing auth provider when not in admin mode returns error", func(t *testing.T) {
		memStore, err := memory.NewSeeded(context.Background())
		if err != nil {
			t.Fatalf("failed to create memory store: %v", err)
		}
		err = Run(context.Background(),
			WithStore(memStore),
			WithAdminMode(false),
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)
		if err == nil || !strings.Contains(err.Error(), "WithAuthProvider") {
			t.Fatalf("expected error requiring WithAuthProvider or WithAdminMode(true), got: %v", err)
		}
	})
}

func TestServer_OptionValidation(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
	}{
		{"nil Config", WithConfig(nil)},
		{"WithConfig invalid port", WithConfig(func() *Config { c := DefaultConfig(); c.Port = 0; return c }())},
		{"WithConfig empty DefaultUserID", WithConfig(func() *Config { c := DefaultConfig(); c.DefaultUserID = "  "; return c }())},
		{"WithConfig zero daily quota", WithConfig(func() *Config { c := DefaultConfig(); c.LLMDailyRequestLimit = 0; return c }())},
		{"WithConfig zero worker concurrency", WithConfig(func() *Config { c := DefaultConfig(); c.WorkerConcurrency = 0; return c }())},
		{"WithConfig zero RESTTimeout", WithConfig(func() *Config { c := DefaultConfig(); c.RESTTimeout = 0; return c }())},
		{"WithConfig zero HTTPReadHeaderTimeout", WithConfig(func() *Config { c := DefaultConfig(); c.HTTPReadHeaderTimeout = 0; return c }())},
		{"WithConfig zero MCPSSEKeepAliveInterval", WithConfig(func() *Config { c := DefaultConfig(); c.MCPSSEKeepAliveInterval = 0; return c }())},
		{"invalid WithHandlerTimeouts pdfParse", WithHandlerTimeouts(0, time.Second, time.Second)},
		{"invalid WithHandlerTimeouts renderPreview", WithHandlerTimeouts(time.Second, 0, time.Second)},
		{"invalid WithHandlerTimeouts keepAlive", WithHandlerTimeouts(time.Second, time.Second, 0)},
		{"invalid WithHTTPTimeouts readHeader", WithHTTPTimeouts(0, time.Second, time.Second, time.Second, time.Second, time.Second)},
		{"invalid WithHTTPTimeouts read", WithHTTPTimeouts(time.Second, 0, time.Second, time.Second, time.Second, time.Second)},
		{"invalid WithHTTPTimeouts minWrite", WithHTTPTimeouts(time.Second, time.Second, 0, time.Second, time.Second, time.Second)},
		{"invalid WithHTTPTimeouts writeBuffer", WithHTTPTimeouts(time.Second, time.Second, time.Second, 0, time.Second, time.Second)},
		{"invalid WithHTTPTimeouts idle", WithHTTPTimeouts(time.Second, time.Second, time.Second, time.Second, 0, time.Second)},
		{"invalid WithHTTPTimeouts shutdown", WithHTTPTimeouts(time.Second, time.Second, time.Second, time.Second, time.Second, 0)},
		{"invalid port 0", WithPort(0)},
		{"invalid port 70000", WithPort(70000)},
		{"empty DefaultUserID", WithDefaultUserID("")},
		{"invalid daily quota", WithQuotaLimits(0, 100)},
		{"invalid monthly quota", WithQuotaLimits(10, 0)},
		{"invalid worker concurrency", WithWorkerPoolConfig(0, 2)},
		{"invalid worker maxPerTenant", WithWorkerPoolConfig(2, 0)},
		{"invalid restTimeout", WithTimeouts(0, time.Minute)},
		{"invalid llmTimeout", WithTimeouts(time.Second, 0)},
		{"nil Store", WithStore(nil)},
		{"nil AuthProvider", WithAuthProvider(nil)},
		{"admin AuthProvider in WithAuthProvider", WithAuthProvider(auth.NewAdminProvider("default"))},
		{"nil RequestAuthProvider", WithRequestAuthProvider(nil)},
		{"nil TokenVerifier", WithTokenVerifier("custom", nil)},
		{"empty TokenVerifier name", WithTokenVerifier("   ", &mockAuthProvider{})},
		{"admin TokenVerifier name", WithTokenVerifier("admin", &mockAuthProvider{})},
		{"nil LLMRegistry", WithLLMRegistry(nil)},
		{"nil LLMClient", WithLLMClient(nil)},
		{"nil TemplateRenderer", WithTemplateRenderer(nil)},
		{"nil Validator", WithValidator(nil)},
		{"nil PDFExtractor", WithPDFExtractor(nil)},
		{"nil ResumeExtractor", WithResumeExtractor(nil)},
		{"nil PromptCompiler", WithPromptCompiler(nil)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.opt); err == nil {
				t.Errorf("expected validation error for %s, got nil", tc.name)
			}
		})
	}
}

func TestServer_WithRequestAuthProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}

	adminProv := auth.NewAdminProvider("request-tenant")
	if err := memStore.Profiles().UpsertProfile(ctx, &core.Profile{
		UserID:   "request-tenant",
		FullName: "Grace Hopper",
	}); err != nil {
		t.Fatalf("failed to seed profile: %v", err)
	}

	port := freePort(t)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx,
			WithPort(port),
			WithAdminMode(false),
			WithStore(memStore),
			WithRequestAuthProvider(adminProv),
			WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealthy(t, baseURL)

	resp, err := http.Get(baseURL + "/api/v1/profile")
	if err != nil {
		t.Fatalf("GET /api/v1/profile failed: %v", err)
	}
	var prof core.Profile
	if err := json.NewDecoder(resp.Body).Decode(&prof); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("failed decoding profile: %v", err)
	}
	_ = resp.Body.Close()
	if prof.UserID != "request-tenant" || prof.FullName != "Grace Hopper" {
		t.Errorf("unexpected profile from WithRequestAuthProvider: %+v", prof)
	}

	http.DefaultClient.CloseIdleConnections()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected shutdown error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestServer_TimeoutAndConfigOptions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DefaultUserID = "  custom-user  "
	cfg.Environment = " "
	cfg.LLMDebugDir = ""

	srv, err := New(
		WithConfig(cfg),
		WithHandlerTimeouts(12*time.Minute, 15*time.Second, 20*time.Second),
		WithHTTPTimeouts(6*time.Second, 70*time.Second, 40*time.Second, 15*time.Second, 150*time.Second, 12*time.Second),
	)
	if err != nil {
		t.Fatalf("expected valid config and timeout options, got error: %v", err)
	}
	if srv.cfg.DefaultUserID != "custom-user" {
		t.Errorf("expected trimmed DefaultUserID 'custom-user', got %q", srv.cfg.DefaultUserID)
	}
	if srv.cfg.Environment != DefaultConfig().Environment {
		t.Errorf("expected normalized Environment %q, got %q", DefaultConfig().Environment, srv.cfg.Environment)
	}
	if srv.cfg.LLMDebugDir != DefaultConfig().LLMDebugDir {
		t.Errorf("expected normalized LLMDebugDir %q, got %q", DefaultConfig().LLMDebugDir, srv.cfg.LLMDebugDir)
	}
	if srv.cfg.PDFParseTimeout != 12*time.Minute || srv.cfg.RenderPreviewTimeout != 15*time.Second || srv.cfg.MCPSSEKeepAliveInterval != 20*time.Second {
		t.Errorf("unexpected handler timeouts: %+v", srv.cfg)
	}
	if srv.cfg.HTTPReadHeaderTimeout != 6*time.Second || srv.cfg.HTTPReadTimeout != 70*time.Second || srv.cfg.HTTPMinWriteTimeout != 40*time.Second ||
		srv.cfg.HTTPWriteTimeoutBuffer != 15*time.Second || srv.cfg.HTTPIdleTimeout != 150*time.Second || srv.cfg.HTTPShutdownTimeout != 12*time.Second {
		t.Errorf("unexpected HTTP timeouts: %+v", srv.cfg)
	}
}

func TestServer_DefaultPromptCompilerUsesLLMRegistry(t *testing.T) {
	tests := []struct {
		name              string
		structuredOutput  bool
		wantSchemaPresent bool
	}{
		{
			name:              "omits fallback schema when resolved client supports structured output",
			structuredOutput:  true,
			wantSchemaPresent: false,
		},
		{
			name:              "includes fallback schema when resolved client lacks structured output",
			structuredOutput:  false,
			wantSchemaPresent: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			memStore, err := memory.NewSeeded(ctx)
			if err != nil {
				t.Fatalf("failed to create seeded memory store: %v", err)
			}

			port := freePort(t)
			client := &mockLLMClient{
				name:             "custom-llm",
				defaultModel:     "custom-model-v1",
				structuredOutput: tc.structuredOutput,
			}

			errCh := make(chan error, 1)
			go func() {
				errCh <- Run(ctx,
					WithPort(port),
					WithAdminMode(true),
					WithStore(memStore),
					WithLLMClient(client),
					WithMCPEnabled(false),
					WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
				)
			}()

			baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitForHealthy(t, baseURL)

			createResp, err := http.Post(
				baseURL+"/api/v1/branches",
				"application/json",
				strings.NewReader(`{"name":"Preview Test","target_role":"Staff Engineer","target_company":"Acme","template_set_id":"`+core.TemplateStandardCleanID+`"}`),
			)
			if err != nil {
				t.Fatalf("POST /api/v1/branches failed: %v", err)
			}
			var created core.Branch
			if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
				_ = createResp.Body.Close()
				t.Fatalf("decode created branch failed: %v", err)
			}
			_ = createResp.Body.Close()
			if createResp.StatusCode != http.StatusCreated {
				t.Fatalf("expected 201 Created, got %d", createResp.StatusCode)
			}

			previewResp, err := http.Get(baseURL + "/api/v1/branches/" + created.ID + "/prompt-preview")
			if err != nil {
				t.Fatalf("GET /api/v1/branches/{id}/prompt-preview failed: %v", err)
			}
			var preview core.PromptPreview
			if err := json.NewDecoder(previewResp.Body).Decode(&preview); err != nil {
				_ = previewResp.Body.Close()
				t.Fatalf("decode prompt preview failed: %v", err)
			}
			_ = previewResp.Body.Close()
			if previewResp.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 OK, got %d", previewResp.StatusCode)
			}

			hasSchema := strings.Contains(preview.SystemPrompt, "<response_schema>")
			if hasSchema != tc.wantSchemaPresent {
				t.Errorf("prompt preview <response_schema> present = %v, want %v", hasSchema, tc.wantSchemaPresent)
			}

			http.DefaultClient.CloseIdleConnections()
			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("unexpected shutdown error: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for server shutdown")
			}
		})
	}
}
