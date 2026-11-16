package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpSDK "github.com/mark3labs/mcp-go/mcp"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/branch"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/core/profile"
	"github.com/resumeee-app/server/auth"
)

type mockTokenVerifier struct {
	verifyFunc func(ctx context.Context, token string) (*core.TokenInfo, error)
}

func (m *mockTokenVerifier) VerifyIDToken(ctx context.Context, token string) (*core.TokenInfo, error) {
	return m.verifyFunc(ctx, token)
}

func TestAuthenticateRequest(t *testing.T) {
	t.Run("admin mode honors X-User-ID header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("X-User-ID", "custom-admin-user")
		cfg := ServerConfig{
			Admin:         true,
			DefaultUserID: "default-admin",
		}

		tokenInfo, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "custom-admin-user" {
			t.Errorf("expected uid 'custom-admin-user', got %q", tokenInfo.UID)
		}
		if !tokenInfo.IsAdmin() {
			t.Errorf("expected IsAdmin() true in admin mode")
		}
	})

	t.Run("admin mode falls back to DefaultUserID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		cfg := ServerConfig{
			Admin:         true,
			DefaultUserID: "default-admin",
		}

		tokenInfo, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "default-admin" {
			t.Errorf("expected uid 'default-admin', got %q", tokenInfo.UID)
		}
		if !tokenInfo.IsAdmin() {
			t.Errorf("expected IsAdmin() true in admin mode")
		}
	})

	t.Run("multi-tenant mode fails closed when AuthProvider is nil even if X-User-ID provided", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("X-User-ID", "attacker-user-id")
		req.Header.Set("Authorization", "Bearer some-token")
		cfg := ServerConfig{
			Admin:        false,
			AuthProvider: nil,
		}

		_, err := authenticateRequest(req, cfg)
		if !errors.Is(err, core.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized when AuthProvider is nil and Admin is false, got %v", err)
		}
	})

	t.Run("multi-tenant mode validates Bearer token via AuthProvider", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "valid-token" {
					return &core.TokenInfo{UID: "verified-uid-42"}, nil
				}
				return nil, core.ErrUnauthorized
			},
		}
		cfg := ServerConfig{
			Admin:        false,
			AuthProvider: auth.NewTokenVerifierProvider("test", verifier),
		}

		// Valid token
		reqValid := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqValid.Header.Set("Authorization", "Bearer valid-token")
		tokenInfo, err := authenticateRequest(reqValid, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "verified-uid-42" {
			t.Errorf("expected uid 'verified-uid-42', got %q", tokenInfo.UID)
		}
		if tokenInfo.IsAdmin() {
			t.Errorf("expected non-admin tokenInfo for standard user")
		}

		// Invalid token
		reqInvalid := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqInvalid.Header.Set("Authorization", "Bearer invalid-token")
		_, err = authenticateRequest(reqInvalid, cfg)
		if !errors.Is(err, core.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for invalid token, got %v", err)
		}
	})

	t.Run("static token dictionary verification", func(t *testing.T) {
		cfg := ServerConfig{
			AuthTokens: map[string]string{
				"secret-token-1": "user-static-1",
			},
		}

		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer secret-token-1")
		tokenInfo, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "user-static-1" {
			t.Errorf("expected uid 'user-static-1', got %q", tokenInfo.UID)
		}

		reqBad := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqBad.Header.Set("Authorization", "Bearer wrong-token")
		_, err = authenticateRequest(reqBad, cfg)
		if err == nil {
			t.Fatal("expected error for unknown static token")
		}
	})

	t.Run("auth provider integration with AdminProvider", func(t *testing.T) {
		adminProvider := auth.NewAdminProvider("default-user")
		cfg := ServerConfig{
			AuthProvider: adminProvider,
		}

		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("X-User-ID", "admin-mcp-tenant")
		tokenInfo, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "admin-mcp-tenant" {
			t.Errorf("expected uid 'admin-mcp-tenant', got %q", tokenInfo.UID)
		}
		if !tokenInfo.IsAdmin() {
			t.Errorf("expected AdminProvider tokenInfo.IsAdmin() to be true")
		}
	})

	t.Run("auth provider integration with custom TokenVerifierProvider", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "mcp-token" {
					return &core.TokenInfo{UID: "custom-mcp-user", Admin: true}, nil
				}
				return nil, core.ErrUnauthorized
			},
		}
		provider := auth.NewTokenVerifierProvider("custom", verifier)
		cfg := ServerConfig{
			AuthProvider: provider,
		}

		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer mcp-token")
		tokenInfo, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tokenInfo.UID != "custom-mcp-user" {
			t.Errorf("expected uid 'custom-mcp-user', got %q", tokenInfo.UID)
		}
		if !tokenInfo.IsAdmin() {
			t.Errorf("expected custom admin claim to propagate")
		}
	})
}

type failingProfileStore struct {
	ProfileStore
	err error
}

func (f *failingProfileStore) Profile(_ context.Context, _ string) (*core.Profile, error) {
	return nil, f.err
}

func (f *failingProfileStore) UpsertProfile(_ context.Context, _ *core.Profile) error {
	return f.err
}

func callMCPToolOverSSE(t *testing.T, handler http.Handler, headers map[string]string, toolName string, args map[string]any) string {
	t.Helper()

	ts := httptest.NewServer(handler)
	defer ts.Close()

	sseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/mcp/sse", nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	for k, v := range headers {
		sseReq.Header.Set(k, v)
	}

	sseResp, err := ts.Client().Do(sseReq)
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer func() { _ = sseResp.Body.Close() }()
	if sseResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /mcp/sse, got %d", sseResp.StatusCode)
	}

	msgPathCh := make(chan string, 1)
	rpcRespCh := make(chan string, 1)
	scanErrCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(sseResp.Body)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		sentEndpoint := false
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if !sentEndpoint {
				sentEndpoint = true
				msgPathCh <- payload
				continue
			}
			if strings.Contains(payload, `"id":1`) {
				rpcRespCh <- payload
				return
			}
		}
		if err := scanner.Err(); err != nil {
			scanErrCh <- err
		}
	}()

	var msgPath string
	select {
	case msgPath = <-msgPathCh:
	case scanErr := <-scanErrCh:
		t.Fatalf("SSE scan error before endpoint event: %v", scanErr)
	case <-sseCtx.Done():
		t.Fatalf("timed out waiting for SSE endpoint event")
	}

	msgURL := msgPath
	if !strings.HasPrefix(msgURL, "http") {
		msgURL = ts.URL + msgPath
	}

	reqBody := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, toolName, mustMarshalJSON(t, args))
	postReq, err := http.NewRequestWithContext(sseCtx, http.MethodPost, msgURL, strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("new POST request: %v", err)
	}
	for k, v := range headers {
		postReq.Header.Set(k, v)
	}
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := ts.Client().Do(postReq)
	if err != nil {
		t.Fatalf("POST message failed: %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusAccepted && postResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected POST status %d", postResp.StatusCode)
	}

	select {
	case respLine := <-rpcRespCh:
		return respLine
	case scanErr := <-scanErrCh:
		t.Fatalf("SSE scan error waiting for tool response: %v", scanErr)
	case <-sseCtx.Done():
		t.Fatalf("timed out waiting for SSE tool response for %q", toolName)
	}
	return ""
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
	return strings.TrimSpace(buf.String())
}

func TestNewHTTPHandler_JWTAdminAndProvisioning(t *testing.T) {
	ctx := context.Background()

	t.Run("JWT admin claims propagate via HTTP and SSE into tool context and provision non-admin provider tenants", func(t *testing.T) {
		store, err := memory.NewSeeded(ctx, "seed-owner")
		if err != nil {
			t.Fatalf("failed to seed store: %v", err)
		}

		type captured struct {
			uid   string
			admin bool
		}
		gotCh := make(chan captured, 2)

		mcpSrv := serverSDK.NewMCPServer("test", "1.0.0")
		mcpSrv.AddTool(mcpSDK.NewTool("whoami_admin"), func(toolCtx context.Context, _ mcpSDK.CallToolRequest) (*mcpSDK.CallToolResult, error) {
			gotCh <- captured{
				uid:   UserIDFromContext(toolCtx, ""),
				admin: core.IsAdminFromContext(toolCtx),
			}
			return mcpSDK.NewToolResultText("ok"), nil
		})

		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				switch token {
				case "admin-tok":
					return &core.TokenInfo{UID: "jwt-admin-1", Admin: true}, nil
				case "user-tok":
					return &core.TokenInfo{UID: "jwt-user-1", Admin: false}, nil
				default:
					return nil, core.ErrUnauthorized
				}
			},
		}

		handler := NewHTTPHandler(mcpSrv, ServerConfig{
			AuthProvider:         auth.NewTokenVerifierProvider("jwt", verifier),
			ProfileStore:         store.Profiles(),
			BranchStore:          store.Branches(),
			TemplateStore:        store.Templates(),
			UserStore:            store.Users(),
			RESTTimeout:          2 * time.Second,
			SSEKeepAliveInterval: 5 * time.Second,
		})
		ts := httptest.NewServer(handler)
		defer ts.Close()

		invokeViaSSE := func(token string) captured {
			t.Helper()
			sseCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()

			sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, ts.URL+"/mcp/sse", nil)
			if err != nil {
				t.Fatalf("new SSE request: %v", err)
			}
			sseReq.Header.Set("Authorization", "Bearer "+token)

			sseResp, err := ts.Client().Do(sseReq)
			if err != nil {
				t.Fatalf("SSE connect failed: %v", err)
			}
			defer func() { _ = sseResp.Body.Close() }()

			var msgPath string
			scanner := bufio.NewScanner(sseResp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					msgPath = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
					break
				}
			}
			if msgPath == "" {
				t.Fatalf("expected SSE endpoint event, scan err=%v", scanner.Err())
			}

			msgURL := msgPath
			if !strings.HasPrefix(msgURL, "http") {
				msgURL = ts.URL + msgPath
			}
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami_admin","arguments":{}}}`)
			postReq, err := http.NewRequestWithContext(sseCtx, http.MethodPost, msgURL, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("new POST request: %v", err)
			}
			postReq.Header.Set("Authorization", "Bearer "+token)
			postReq.Header.Set("Content-Type", "application/json")

			postResp, err := ts.Client().Do(postReq)
			if err != nil {
				t.Fatalf("POST message failed: %v", err)
			}
			_ = postResp.Body.Close()
			if postResp.StatusCode != http.StatusAccepted && postResp.StatusCode != http.StatusOK {
				t.Fatalf("unexpected POST status %d", postResp.StatusCode)
			}

			select {
			case got := <-gotCh:
				return got
			case <-sseCtx.Done():
				t.Fatalf("timed out waiting for tool invocation")
				return captured{}
			}
		}

		gotAdmin := invokeViaSSE("admin-tok")
		if gotAdmin.uid != "jwt-admin-1" || !gotAdmin.admin {
			t.Errorf("expected admin token context (uid=jwt-admin-1, admin=true), got %+v", gotAdmin)
		}
		if prof, err := store.Profiles().Profile(ctx, "jwt-admin-1"); err != nil || prof == nil {
			t.Errorf("expected non-admin auth provider request to provision tenant jwt-admin-1, err=%v", err)
		}

		gotUser := invokeViaSSE("user-tok")
		if gotUser.uid != "jwt-user-1" || gotUser.admin {
			t.Errorf("expected standard token context (uid=jwt-user-1, admin=false), got %+v", gotUser)
		}
	})

	t.Run("AdminProvider and Admin mode skip EnsureTenant across live SSE tool execution", func(t *testing.T) {
		store, err := memory.NewSeeded(ctx, "seed-owner")
		if err != nil {
			t.Fatalf("failed to seed store: %v", err)
		}

		profileSvc := profile.New(store.Profiles(), store.Career(), store.Branches())
		branchSvc := branch.New(
			branch.WithBranches(store.Branches()),
			branch.WithJobs(store.Jobs()),
			branch.WithProfiles(store.Profiles()),
			branch.WithPresets(store.Presets()),
			branch.WithTemplates(store.Templates()),
		)

		cases := []struct {
			name   string
			cfg    ServerConfig
			userID string
		}{
			{
				name: "AdminProvider",
				cfg: ServerConfig{
					AuthProvider:         auth.NewAdminProvider("default-user"),
					ProfileService:       profileSvc,
					BranchService:        branchSvc,
					ProfileStore:         store.Profiles(),
					CareerStore:          store.Career(),
					BranchStore:          store.Branches(),
					TemplateStore:        store.Templates(),
					PresetStore:          store.Presets(),
					UserStore:            store.Users(),
					RESTTimeout:          2 * time.Second,
					SSEKeepAliveInterval: 5 * time.Second,
				},
				userID: "skip-prov-provider",
			},
			{
				name: "Local Admin flag",
				cfg: ServerConfig{
					Admin:                true,
					DefaultUserID:        "default-user",
					ProfileService:       profileSvc,
					BranchService:        branchSvc,
					ProfileStore:         store.Profiles(),
					CareerStore:          store.Career(),
					BranchStore:          store.Branches(),
					TemplateStore:        store.Templates(),
					PresetStore:          store.Presets(),
					UserStore:            store.Users(),
					RESTTimeout:          2 * time.Second,
					SSEKeepAliveInterval: 5 * time.Second,
				},
				userID: "skip-prov-flag",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				mcpSrv := NewServer(tc.cfg)
				handler := NewHTTPHandler(mcpSrv, tc.cfg)

				headers := map[string]string{"X-User-ID": tc.userID}
				rpcLine := callMCPToolOverSSE(t, handler, headers, "list_branches", nil)
				if !strings.Contains(rpcLine, `"result"`) {
					t.Fatalf("expected valid SSE tool result for list_branches, got: %s", rpcLine)
				}

				if prof, _ := store.Profiles().Profile(ctx, tc.userID); prof != nil {
					t.Errorf("expected %s to skip lazy tenant provisioning for %q even during live tool execution, but profile was created", tc.name, tc.userID)
				}
			})
		}
	})

	t.Run("multi-tenant EnsureTenant error fails closed with 500", func(t *testing.T) {
		store, err := memory.NewSeeded(ctx, "seed-owner")
		if err != nil {
			t.Fatalf("failed to seed store: %v", err)
		}

		mcpSrv := serverSDK.NewMCPServer("test", "1.0.0")
		handler := NewHTTPHandler(mcpSrv, ServerConfig{
			AuthTokens: map[string]string{
				"tenant-tok": "tenant-failing-prov",
			},
			ProfileStore:  &failingProfileStore{ProfileStore: store.Profiles(), err: errors.New("database unavailable")},
			BranchStore:   store.Branches(),
			TemplateStore: store.Templates(),
			UserStore:     store.Users(),
		})

		req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer tenant-tok")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 Internal Server Error when EnsureTenant fails, got %d (%s)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "database unavailable") {
			t.Errorf("expected sanitized 500 error body without raw backend error, got %q", rec.Body.String())
		}
	})

	t.Run("Admin true with AuthTokens or non-admin AuthProvider does not elevate non-admin user", func(t *testing.T) {
		store, err := memory.NewSeeded(ctx, "user-token-1")
		if err != nil {
			t.Fatalf("failed to seed store: %v", err)
		}

		branchSvc := branch.New(
			branch.WithBranches(store.Branches()),
			branch.WithJobs(store.Jobs()),
			branch.WithProfiles(store.Profiles()),
			branch.WithPresets(store.Presets()),
			branch.WithTemplates(store.Templates()),
		)

		nonAdminVerifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "jwt-regular" {
					return &core.TokenInfo{UID: "user-token-1", Admin: false, Role: "user"}, nil
				}
				return nil, core.ErrUnauthorized
			},
		}

		cases := []struct {
			name   string
			cfg    ServerConfig
			bearer string
		}{
			{
				name: "Admin true with AuthTokens",
				cfg: ServerConfig{
					Admin:                true,
					AuthTokens:           map[string]string{"static-tok": "user-token-1"},
					BranchService:        branchSvc,
					ProfileStore:         store.Profiles(),
					BranchStore:          store.Branches(),
					TemplateStore:        store.Templates(),
					UserStore:            store.Users(),
					RESTTimeout:          2 * time.Second,
					SSEKeepAliveInterval: 5 * time.Second,
				},
				bearer: "static-tok",
			},
			{
				name: "Admin true with JWT AuthProvider",
				cfg: ServerConfig{
					Admin:                true,
					AuthProvider:         auth.NewTokenVerifierProvider("jwt", nonAdminVerifier),
					BranchService:        branchSvc,
					ProfileStore:         store.Profiles(),
					BranchStore:          store.Branches(),
					TemplateStore:        store.Templates(),
					UserStore:            store.Users(),
					RESTTimeout:          2 * time.Second,
					SSEKeepAliveInterval: 5 * time.Second,
				},
				bearer: "jwt-regular",
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				mcpSrv := NewServer(tc.cfg)
				handler := NewHTTPHandler(mcpSrv, tc.cfg)

				rpcLine := callMCPToolOverSSE(t, handler, map[string]string{
					"Authorization": "Bearer " + tc.bearer,
				}, "create_branch", map[string]any{
					"name":              "Model Override Attempt",
					"target_company":    "Acme",
					"target_role":       "Engineer",
					"provider_override": "openai",
				})

				if !strings.Contains(rpcLine, `"isError":true`) || !strings.Contains(rpcLine, "restricted to administrators") {
					t.Fatalf("expected non-admin token with %s to be denied model selection override, got: %s", tc.name, rpcLine)
				}
			})
		}
	})
}
