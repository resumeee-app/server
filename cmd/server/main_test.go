package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/resumeee-app/core"
	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/memory"
	"github.com/resumeee-app/core/provenance"
	"github.com/resumeee-app/core/renderer"
	"github.com/resumeee-app/core/store"
	"github.com/resumeee-app/core/worker"
	"github.com/resumeee-app/resumeee-api/internal/api"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-api/server"
	storage "github.com/resumeee-app/resumeee-storage-sql/sql"
)

func freePort(t *testing.T) string {
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
	port := strconv.Itoa(tcpAddr.Port)
	_ = l.Close()
	return port
}

func TestRunGracefulShutdown(t *testing.T) {
	memStore, err := memory.NewSeeded(context.Background())
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}
	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}
	t.Setenv("PORT", freePort(t))
	t.Setenv("ADMIN_MODE", "true")

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithStore(ctx, storeInit)
	}()

	// Allow run to initialize, auto-migrate, seed, and wait on ctx.Done()
	time.Sleep(100 * time.Millisecond)

	// Trigger cancellation
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown without error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}

	// Verify profile is accessible
	prof, err := memStore.Profiles().Profile(context.Background(), "default")
	if err != nil {
		t.Fatalf("expected default profile to be seeded, got: %v", err)
	}
	if prof.UserID != "default" {
		t.Errorf("expected UserID 'default', got %s", prof.UserID)
	}
}

func TestRun_RecoversInterruptedVersions(t *testing.T) {
	ctx := context.Background()
	memStore, err := memory.NewSeeded(ctx)
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}
	t.Setenv("PORT", freePort(t))
	t.Setenv("ADMIN_MODE", "true")

	br := &core.Branch{
		ID:            "br-recover",
		UserID:        "default",
		Name:          "Pending Branch",
		TemplateSetID: core.TemplateStandardCleanID,
	}
	if err := memStore.Branches().CreateBranch(ctx, br); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	job := &core.Job{
		ID:     "job-interrupted",
		UserID: "default",
		Kind:   core.JobKindBranchTailor,
	}
	ver := &core.BranchVersion{
		ID:       "ver-interrupted",
		UserID:   "default",
		BranchID: br.ID,
	}
	if _, err := memStore.Jobs().SubmitBranchTailor(ctx, job, ver, core.PendingJobOptions{}); err != nil {
		t.Fatalf("failed to submit branch tailor job: %v", err)
	}
	claimed, err := memStore.Jobs().ClaimNext(ctx, core.ClaimJobRequest{
		WorkerID: "crashed-worker",
	})
	if err != nil || claimed == nil {
		t.Fatalf("failed to claim branch tailor job: claimed=%v err=%v", claimed, err)
	}

	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}

	// Now run the server boot sequence
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runWithStore(runCtx, storeInit)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server run failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}

	// Verify interrupted version and job were marked failed on boot
	recoveredVer, err := memStore.Branches().Version(context.Background(), br.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get version: %v", err)
	}
	if recoveredVer.Status != core.VersionStatusFailed {
		t.Errorf("expected version to be transitioned to failed on boot, got: %s", recoveredVer.Status)
	}
	recoveredJob, err := memStore.Jobs().Job(context.Background(), "default", job.ID)
	if err != nil {
		t.Fatalf("failed to get job: %v", err)
	}
	if recoveredJob.Status != core.JobStatusFailed {
		t.Errorf("expected job to be transitioned to failed on boot, got: %s", recoveredJob.Status)
	}
}

func TestBuildRootHandler(t *testing.T) {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /api/v1/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"Test User"}`))
	})

	handler := server.BuildRootHandler(apiMux, nil)

	tests := []struct {
		name         string
		method       string
		path         string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "Root path returns 404 (legacy redirect removed)",
			method:       http.MethodGet,
			path:         "/",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "GET /web returns 404 (legacy UI removed)",
			method:       http.MethodGet,
			path:         "/web",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "GET /web/ returns 404 (legacy UI removed)",
			method:       http.MethodGet,
			path:         "/web/",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "GET /web/static/css/styles.css returns 404",
			method:       http.MethodGet,
			path:         "/web/static/css/styles.css",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "GET /static/css/styles.css returns 404",
			method:       http.MethodGet,
			path:         "/static/css/styles.css",
			expectedCode: http.StatusNotFound,
		},
		{
			name:         "GET /api/v1/health serves api health",
			method:       http.MethodGet,
			path:         "/api/v1/health",
			expectedCode: http.StatusOK,
			expectedBody: `{"status":"ok"}`,
		},
		{
			name:         "GET /healthz serves healthz",
			method:       http.MethodGet,
			path:         "/healthz",
			expectedCode: http.StatusOK,
			expectedBody: `{"status":"ok"}`,
		},
		{
			name:         "GET /health serves health",
			method:       http.MethodGet,
			path:         "/health",
			expectedCode: http.StatusOK,
			expectedBody: `{"status":"ok"}`,
		},
		{
			name:         "GET /api/v1/profile serves api profile",
			method:       http.MethodGet,
			path:         "/api/v1/profile",
			expectedCode: http.StatusOK,
			expectedBody: `{"full_name":"Test User"}`,
		},
		{
			name:         "GET /nonexistent returns 404",
			method:       http.MethodGet,
			path:         "/nonexistent",
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.expectedCode {
				t.Fatalf("expected status %d for %s %s, got %d", tc.expectedCode, tc.method, tc.path, rec.Code)
			}
			if tc.expectedBody != "" {
				body := rec.Body.String()
				if !strings.Contains(body, tc.expectedBody) {
					t.Errorf("expected body to contain %q, got %q", tc.expectedBody, body)
				}
			}
		})
	}
}

func TestRun_UnifiedServer_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "unified.db")
	t.Setenv("DB_PATH", dbPath)
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("ADMIN_MODE", "true")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	// Wait for server to come up
	clientNoRedirect := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		resp, err := clientNoRedirect.Get(baseURL + "/api/v1/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			ready = true
			break
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("timed out waiting for unified server to boot")
	}

	t.Run("Deprecated routes return 404", func(t *testing.T) {
		deprecatedPaths := []string{
			"/",
			"/web",
			"/web/profile",
			"/web/static/css/styles.css",
		}
		for _, path := range deprecatedPaths {
			t.Run("GET "+path, func(t *testing.T) {
				resp, err := clientNoRedirect.Get(baseURL + path)
				if err != nil {
					t.Fatalf("failed request for %s: %v", path, err)
				}
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode != http.StatusNotFound {
					t.Fatalf("expected 404 Not Found for %s, got %d", path, resp.StatusCode)
				}
			})
		}
	})

	t.Run("GET /api/v1/profile returns seeded profile JSON", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/api/v1/profile")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed reading response body: %v", err)
		}
		var prof core.Profile
		if err := json.Unmarshal(body, &prof); err != nil {
			t.Fatalf("failed to decode seeded profile json: %v; raw body: %s", err, string(body))
		}
		if prof.UserID != "default" {
			t.Errorf("expected seeded profile user_id 'default', got %q", prof.UserID)
		}
		if prof.FullName == "" {
			t.Errorf("expected non-empty full_name in seeded profile")
		}
	})

	t.Run("GET /mcp/sse connects to MCP SSE stream", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, baseURL+"/mcp/sse", nil)
		resp, err := clientNoRedirect.Do(req)
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
			t.Errorf("expected text/event-stream, got %s", ct)
		}
	})

	// Clean shutdown
	clientNoRedirect.CloseIdleConnections()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean server shutdown, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shutdown")
	}
}

func TestBuildServer_Configuration(t *testing.T) {
	mux := http.NewServeMux()
	srv := server.BuildHTTPServer(":8080", mux, server.HTTPTimeouts{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      11 * time.Minute,
		IdleTimeout:       120 * time.Second,
	})

	if srv.Addr != ":8080" {
		t.Errorf("expected Addr :8080, got %s", srv.Addr)
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("expected ReadHeaderTimeout 5s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 60*time.Second {
		t.Errorf("expected ReadTimeout 60s, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 11*time.Minute {
		t.Errorf("expected WriteTimeout 11m, got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 120*time.Second {
		t.Errorf("expected IdleTimeout 120s, got %v", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 1<<20 {
		t.Errorf("expected MaxHeaderBytes 1MB, got %d", srv.MaxHeaderBytes)
	}
}

func TestBuildRootHandler_MCPRouting(t *testing.T) {
	apiMux := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api"))
	})
	mcpMux := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mcp"))
	})

	root := server.BuildRootHandler(apiMux, mcpMux)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/mcp/sse", nil)
	root.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "mcp" {
		t.Errorf("expected mcp handler to be dispatched at /mcp/sse, got code %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRun_WithLLMDebugFlags(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "debug_test.db")
	debugDir := filepath.Join(tmpDir, "llm_dumps")
	t.Setenv("DB_PATH", dbPath)
	port := freePort(t)
	t.Setenv("PORT", port)
	t.Setenv("ADMIN_MODE", "true")

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, "-debug-llm-prompts", "-llm-debug-dir", debugDir)
	}()

	// Wait for server to come up before cancelling
	deadline := time.Now().Add(5 * time.Second)
	healthURL := fmt.Sprintf("http://127.0.0.1:%s/api/v1/health", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	for {
		resp, err := client.Get(healthURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for server to become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected clean shutdown with debug flags, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for graceful shutdown")
	}

	info, err := os.Stat(debugDir)
	if err != nil {
		t.Fatalf("expected llm debug directory %s to be created on server startup: %v", debugDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", debugDir)
	}
}

func TestRun_MultiTenantRequiresAuthOrAdmin(t *testing.T) {
	memStore, err := memory.NewSeeded(context.Background())
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}
	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}

	t.Setenv("PORT", freePort(t))
	t.Setenv("ADMIN_MODE", "false")
	t.Setenv("FIREBASE_PROJECT_ID", "")

	err = runWithStore(context.Background(), storeInit)
	if err == nil {
		t.Fatal("expected error when booting multi-tenant server without FIREBASE_PROJECT_ID or ADMIN_MODE=true")
	}
	expectedMsg := "multi-tenant mode requires FIREBASE_PROJECT_ID or explicit ADMIN_MODE=true / -admin-mode for local admin mode"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Fatalf("expected error containing %q, got: %v", expectedMsg, err)
	}
}

func TestRun_AuthProviderConfigurations(t *testing.T) {
	memStore, err := memory.NewSeeded(context.Background())
	if err != nil {
		t.Fatalf("failed to create seeded memory store: %v", err)
	}
	storeInit := func(_ context.Context, _ *config.Config, _ *slog.Logger) (*store.Store, error) {
		return memStore, nil
	}

	t.Run("boot with AUTH_PROVIDER=admin environment variable", func(t *testing.T) {
		t.Setenv("PORT", freePort(t))
		t.Setenv("ADMIN_MODE", "false")
		t.Setenv("AUTH_PROVIDER", "admin")
		t.Setenv("FIREBASE_PROJECT_ID", "")

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- runWithStore(ctx, storeInit)
		}()

		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean shutdown with AUTH_PROVIDER=admin, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for shutdown")
		}
	})

	t.Run("boot with -auth-provider=admin flag", func(t *testing.T) {
		t.Setenv("PORT", freePort(t))
		t.Setenv("ADMIN_MODE", "false")
		t.Setenv("AUTH_PROVIDER", "")
		t.Setenv("FIREBASE_PROJECT_ID", "")

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- runWithStore(ctx, storeInit, "-auth-provider=admin")
		}()

		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean shutdown with -auth-provider=admin, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for shutdown")
		}
	})

	t.Run("boot with FIREBASE_PROJECT_ID initializes firebase provider", func(t *testing.T) {
		t.Setenv("PORT", freePort(t))
		t.Setenv("ADMIN_MODE", "false")
		t.Setenv("AUTH_PROVIDER", "")
		t.Setenv("FIREBASE_PROJECT_ID", "test-firebase-proj")

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- runWithStore(ctx, storeInit)
		}()

		time.Sleep(100 * time.Millisecond)
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean shutdown with firebase provider, got: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for shutdown")
		}
	})
}

type e2eMockLLMClient struct{}

func (e2eMockLLMClient) Name() string         { return "e2e-mock" }
func (e2eMockLLMClient) DefaultModel() string { return "e2e-model-v1" }
func (e2eMockLLMClient) Models() []core.ModelOption {
	return []core.ModelOption{
		{
			Provider:    "e2e-mock",
			Model:       "e2e-model-v1",
			DisplayName: "E2E Mock Model",
			IsDefault:   true,
		},
	}
}
func (e2eMockLLMClient) SupportsStructuredOutput(_ string) bool { return true }
func (e2eMockLLMClient) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	payload := `{"tailored_content":{"summary":"E2E tailored summary for Staff Go role"},"gap_report":{"overall_match":88}}`
	if req.SchemaName == "resume_extract_response" {
		payload = `{"basics":{"name":"E2E Candidate","label":"Staff Backend Engineer","email":"e2e@example.com"}}`
	}
	return &llm.Response{
		Text:       payload,
		StatusCode: http.StatusOK,
		Usage: core.TokenUsage{
			PromptTokens:     120,
			CompletionTokens: 80,
			TotalTokens:      200,
		},
	}, nil
}

func buildMinimalTestPDF(text string) []byte {
	stream := fmt.Sprintf("BT\n/F1 12 Tf\n100 700 Td\n(%s) Tj\nET", text)
	body := fmt.Sprintf("%%PDF-1.4\n"+
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"+
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n"+
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n"+
		"4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n"+
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
		len(stream), stream)

	off1 := 9
	off2 := off1 + len("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	off3 := off2 + len("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	off4 := off3 + len("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n")
	off5 := off4 + len(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(stream), stream))
	startXref := len(body)

	xrefAndTrailer := fmt.Sprintf("xref\n0 6\n0000000000 65535 f \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"%010d 00000 n \n"+
		"trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF",
		off1, off2, off3, off4, off5, startXref)

	return []byte(body + xrefAndTrailer)
}

func assertOmitsInternalJobJSONFields(t *testing.T, label string, body []byte) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("%s invalid JSON (%v): %s", label, err, string(body))
	}
	raw := string(body)
	for _, forbidden := range []string{"raw_text", "reservation_id", "claimed_by"} {
		if _, exists := payload[forbidden]; exists {
			t.Errorf("%s response must omit JSON key %q (body: %s)", label, forbidden, raw)
		}
		if strings.Contains(raw, `"`+forbidden+`"`) {
			t.Errorf("%s raw JSON must not contain %q (body: %s)", label, forbidden, raw)
		}
	}
}

func TestSQLite_EndToEnd_RecoveryPDFAndBranchTailor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	st, err := storage.OpenStore(ctx, dbPath)
	if err != nil {
		t.Fatalf("storage.OpenStore: %v", err)
	}
	defer func() { _ = st.Close() }()

	const tenantID = "e2e-tenant"
	const branchID = "br-e2e-tailor"

	// 1. Seed tenant + profile + branch + quota.
	if err := st.Users().UpsertUser(ctx, &core.User{
		ID:    tenantID,
		Email: "e2e@example.com",
	}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := st.Profiles().UpsertProfile(ctx, &core.Profile{
		UserID:   tenantID,
		FullName: "E2E Candidate",
		Headline: "Staff Backend Engineer",
		Email:    "e2e@example.com",
	}); err != nil {
		t.Fatalf("UpsertProfile: %v", err)
	}
	if err := st.Branches().CreateBranch(ctx, &core.Branch{
		ID:                 branchID,
		UserID:             tenantID,
		Name:               "Staff Go Branch",
		TargetRole:         "Staff Go Engineer",
		TargetCompany:      "Acme",
		TargetRequirements: "Go, SQLite, Distributed Systems",
		TemplateSetID:      core.TemplateStandardCleanID,
	}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	res, err := st.Quotas().ReserveQuota(ctx, tenantID, core.OperationResumeTailoring, 1200, 50, 1000000)
	if err != nil {
		t.Fatalf("ReserveQuota: %v", err)
	}

	// 2. Submit and claim a processing job, then recover via a fresh worker.Pool.
	staleJob := &core.Job{
		ID:            "job-e2e-stale",
		UserID:        tenantID,
		Kind:          core.JobKindBranchTailor,
		ReservationID: res.ID,
	}
	staleVer := &core.BranchVersion{
		ID:       "ver-e2e-stale",
		UserID:   tenantID,
		BranchID: branchID,
	}
	if _, err := st.Jobs().SubmitBranchTailor(ctx, staleJob, staleVer, core.PendingJobOptions{}); err != nil {
		t.Fatalf("SubmitBranchTailor (stale): %v", err)
	}
	claimed, err := st.Jobs().ClaimNext(ctx, core.ClaimJobRequest{
		WorkerID: "crashed-e2e-worker",
	})
	if err != nil || claimed == nil || claimed.Status != core.JobStatusProcessing {
		t.Fatalf("ClaimNext failed: claimed=%+v err=%v", claimed, err)
	}

	recoveryPool := worker.New(
		worker.WithJobs(st.Jobs()),
		worker.WithQuotas(st.Quotas()),
	)
	if err := recoveryPool.RecoverStaleJobs(ctx, "e2e server crash recovery"); err != nil {
		t.Fatalf("RecoverStaleJobs: %v", err)
	}

	recoveredJob, err := st.Jobs().Job(ctx, tenantID, staleJob.ID)
	if err != nil {
		t.Fatalf("Job after recovery: %v", err)
	}
	if recoveredJob.Status != core.JobStatusFailed {
		t.Fatalf("expected stale job status %s, got %s", core.JobStatusFailed, recoveredJob.Status)
	}
	quotaAfterRecovery, err := st.Quotas().Quota(ctx, tenantID)
	if err != nil {
		t.Fatalf("Quota after recovery: %v", err)
	}
	if quotaAfterRecovery.DailyRequestsUsed != 0 || quotaAfterRecovery.MonthlyTokensUsed != 0 {
		t.Fatalf("expected reserved quota released (daily=0, monthly=0), got daily=%d monthly=%d",
			quotaAfterRecovery.DailyRequestsUsed, quotaAfterRecovery.MonthlyTokensUsed)
	}
	usageRecords, err := st.Quotas().ListUsageRecords(ctx, tenantID, 10)
	if err != nil {
		t.Fatalf("ListUsageRecords: %v", err)
	}
	if len(usageRecords) != 1 || usageRecords[0].Status != core.ReservationStatusReleased {
		t.Fatalf("expected reservation %s status %s, got %+v", res.ID, core.ReservationStatusReleased, usageRecords)
	}

	// 3. Start *worker.Pool + router via NewRouterConfigFromStore and verify async PDF parse E2E.
	reg := llm.NewRegistry()
	reg.Register(e2eMockLLMClient{})
	llmService := llm.NewService(reg)
	resumeExtractor := llm.NewRegistryExtractor(reg)

	pool := worker.New(
		worker.WithConcurrency(2),
		worker.WithMaxConcurrentPerTenant(2),
		worker.WithPollInterval(20*time.Millisecond),
		worker.WithJobs(st.Jobs()),
		worker.WithBranches(st.Branches()),
		worker.WithProfiles(st.Profiles()),
		worker.WithPresets(st.Presets()),
		worker.WithTemplates(st.Templates()),
		worker.WithQuotas(st.Quotas()),
		worker.WithResumeExtractor(resumeExtractor),
		worker.WithGenerator(llmService),
		worker.WithValidator(provenance.New()),
		worker.WithRenderer(renderer.New()),
	)
	pool.Start(ctx)
	defer pool.Stop()

	routerCfg := api.NewRouterConfigFromStore(st)
	routerCfg.JobNotifier = pool
	routerCfg.DefaultUserID = tenantID
	routerCfg.Admin = true
	routerCfg.ResumeExtractor = resumeExtractor
	routerCfg.Renderer = renderer.New()
	routerCfg.Validator = provenance.New()
	routerCfg.PromptCompiler = llm.NewPromptCompiler(llm.WithPromptRegistry(reg))
	routerCfg.ModelEnumerator = reg

	httpSrv := httptest.NewServer(api.NewRouter(routerCfg))
	defer httpSrv.Close()
	client := httpSrv.Client()

	var pdfForm bytes.Buffer
	mw := multipart.NewWriter(&pdfForm)
	part, err := mw.CreateFormFile("file", "resume.pdf")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(buildMinimalTestPDF("E2E Candidate Confidential Raw Text")); err != nil {
		t.Fatalf("Write PDF bytes: %v", err)
	}
	_ = mw.WriteField("provider", "e2e-mock")
	_ = mw.WriteField("model", "e2e-model-v1")
	_ = mw.Close()

	pdfReq, err := http.NewRequestWithContext(ctx, http.MethodPost, httpSrv.URL+"/api/v1/profile/pdf/parse?async=true", &pdfForm)
	if err != nil {
		t.Fatalf("NewRequest PDF: %v", err)
	}
	pdfReq.Header.Set("Content-Type", mw.FormDataContentType())
	pdfPostResp, err := client.Do(pdfReq)
	if err != nil {
		t.Fatalf("POST /api/v1/profile/pdf/parse?async=true: %v", err)
	}
	pdfPostBody, err := io.ReadAll(pdfPostResp.Body)
	_ = pdfPostResp.Body.Close()
	if err != nil {
		t.Fatalf("read PDF POST response: %v", err)
	}
	if pdfPostResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for async PDF parse, got %d (%s)", pdfPostResp.StatusCode, string(pdfPostBody))
	}
	assertOmitsInternalJobJSONFields(t, "POST /api/v1/profile/pdf/parse?async=true", pdfPostBody)

	var pdfQueued api.PDFParseJobResponse
	if err := json.Unmarshal(pdfPostBody, &pdfQueued); err != nil {
		t.Fatalf("unmarshal PDF queued response: %v (%s)", err, string(pdfPostBody))
	}
	if pdfQueued.ID == "" {
		t.Fatalf("expected non-empty PDF job ID (%s)", string(pdfPostBody))
	}

	var pdfCompleted api.PDFParseJobResponse
	var pdfCompletedBody []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/profile/pdf/jobs/"+pdfQueued.ID, nil)
		if err != nil {
			t.Fatalf("NewRequest GET /profile/pdf/jobs/{id}: %v", err)
		}
		getResp, err := client.Do(getReq)
		if err != nil {
			t.Fatalf("GET /profile/pdf/jobs/{id}: %v", err)
		}
		body, readErr := io.ReadAll(getResp.Body)
		_ = getResp.Body.Close()
		if readErr != nil {
			t.Fatalf("read PDF poll response: %v", readErr)
		}
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK polling PDF job, got %d (%s)", getResp.StatusCode, string(body))
		}
		if err := json.Unmarshal(body, &pdfCompleted); err != nil {
			t.Fatalf("unmarshal PDF poll response: %v", err)
		}
		if pdfCompleted.Status == core.JobStatusCompleted {
			pdfCompletedBody = body
			break
		}
		if pdfCompleted.Status == core.JobStatusFailed {
			t.Fatalf("PDF parse job failed unexpectedly: %s", string(body))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if pdfCompleted.Status != core.JobStatusCompleted {
		t.Fatalf("timed out waiting for PDF parse job %s to complete; last status=%s", pdfQueued.ID, pdfCompleted.Status)
	}
	assertOmitsInternalJobJSONFields(t, "GET /profile/pdf/jobs/{id} (completed)", pdfCompletedBody)
	if pdfCompleted.Filename != "resume.pdf" || pdfCompleted.Provider != "e2e-mock" || pdfCompleted.Model != "e2e-model-v1" {
		t.Errorf("unexpected PDF job metadata: filename=%q provider=%q model=%q",
			pdfCompleted.Filename, pdfCompleted.Provider, pdfCompleted.Model)
	}
	if pdfCompleted.Result == nil || pdfCompleted.Result.Basics.Name != "E2E Candidate" {
		t.Errorf("unexpected PDF extracted result: %+v", pdfCompleted.Result)
	}

	// 4. Trigger an async POST /branches/{id}/generate job and poll until completed.
	genReq, err := http.NewRequestWithContext(ctx, http.MethodPost, httpSrv.URL+"/api/v1/branches/"+branchID+"/generate", nil)
	if err != nil {
		t.Fatalf("NewRequest generate: %v", err)
	}
	genResp, err := client.Do(genReq)
	if err != nil {
		t.Fatalf("POST /api/v1/branches/{id}/generate: %v", err)
	}
	genBody, err := io.ReadAll(genResp.Body)
	_ = genResp.Body.Close()
	if err != nil {
		t.Fatalf("read generate response: %v", err)
	}
	if genResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for branch generate, got %d (%s)", genResp.StatusCode, string(genBody))
	}
	var enqueued api.AsyncJobEnqueued
	if err := json.Unmarshal(genBody, &enqueued); err != nil {
		t.Fatalf("unmarshal AsyncJobEnqueued: %v (%s)", err, string(genBody))
	}
	if enqueued.VersionID == "" {
		t.Fatalf("expected non-empty version_id in AsyncJobEnqueued (%s)", string(genBody))
	}

	var completedVer core.BranchVersion
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		verReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/api/v1/branches/"+branchID+"/versions/"+enqueued.VersionID, nil)
		if err != nil {
			t.Fatalf("NewRequest GET /api/v1/branches/{id}/versions/{v}: %v", err)
		}
		verResp, err := client.Do(verReq)
		if err != nil {
			t.Fatalf("GET /api/v1/branches/{id}/versions/{v}: %v", err)
		}
		body, readErr := io.ReadAll(verResp.Body)
		_ = verResp.Body.Close()
		if readErr != nil {
			t.Fatalf("read version poll response: %v", readErr)
		}
		if verResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK polling version, got %d (%s)", verResp.StatusCode, string(body))
		}
		if err := json.Unmarshal(body, &completedVer); err != nil {
			t.Fatalf("unmarshal BranchVersion: %v", err)
		}
		if completedVer.Status == core.VersionStatusCompleted {
			break
		}
		if completedVer.Status == core.VersionStatusFailed {
			t.Fatalf("branch generation failed unexpectedly: %s", string(body))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if completedVer.Status != core.VersionStatusCompleted {
		t.Fatalf("timed out waiting for branch version %s to complete; last status=%s", enqueued.VersionID, completedVer.Status)
	}
	if completedVer.BranchEpoch != 1 {
		t.Errorf("expected branch_epoch 1, got %d", completedVer.BranchEpoch)
	}
	if completedVer.JobID == "" {
		t.Errorf("expected non-empty job_id on completed BranchVersion")
	}
	if completedVer.TailoredContent == nil || completedVer.TailoredContent.Summary == "" {
		t.Errorf("expected non-empty tailored_content.summary on completed BranchVersion, got %+v", completedVer.TailoredContent)
	}
	if completedVer.HTMLOutput == "" || completedVer.MarkdownOutput == "" || completedVer.PlainTextOutput == "" {
		t.Errorf("expected non-empty rendered outputs (html=%d md=%d txt=%d)",
			len(completedVer.HTMLOutput), len(completedVer.MarkdownOutput), len(completedVer.PlainTextOutput))
	}
}
