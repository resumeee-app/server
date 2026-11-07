package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"resumeee/internal/domain"
	"resumeee/internal/repository/sqlite"
)

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return port
}

func TestRunGracefulShutdown(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("PORT", freePort(t))

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
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

	// Verify database was initialized and seeded
	db, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to open database after run: %v", err)
	}

	profileRepo := sqlite.NewProfileRepository(db)
	prof, err := profileRepo.GetProfile(context.Background(), "default")
	if err != nil {
		t.Fatalf("expected default profile to be seeded, got: %v", err)
	}
	if prof.UserID != "default" {
		t.Errorf("expected UserID 'default', got %s", prof.UserID)
	}
}

func TestRun_RecoversInterruptedVersions(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "recover.db")
	t.Setenv("DB_PATH", dbPath)
	t.Setenv("PORT", freePort(t))

	// Pre-create database with an in-flight branch version
	db, err := sqlite.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create db: %v", err)
	}

	branchRepo := sqlite.NewBranchRepository(db)
	branch := &domain.Branch{
		ID:            "br-recover",
		UserID:        "default",
		Name:          "Pending Branch",
		TemplateSetID: "tmpl-1",
	}
	if err := branchRepo.CreateBranch(context.Background(), branch); err != nil {
		t.Fatalf("failed to create branch: %v", err)
	}

	ver := &domain.BranchVersion{
		ID:       "ver-interrupted",
		BranchID: branch.ID,
		Status:   domain.VersionStatusProcessing,
	}
	if err := branchRepo.CreateVersion(context.Background(), ver); err != nil {
		t.Fatalf("failed to create version: %v", err)
	}
	// Force status to processing
	_ = branchRepo.UpdateVersionStatus(context.Background(), ver.ID, domain.VersionStatusProcessing, "")

	// Now run the server boot sequence
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx)
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

	// Verify interrupted version was marked failed
	recoveredVer, err := branchRepo.GetVersion(context.Background(), branch.ID, ver.ID)
	if err != nil {
		t.Fatalf("failed to get version: %v", err)
	}
	if recoveredVer.Status != domain.VersionStatusFailed {
		t.Errorf("expected version to be transitioned to failed on boot, got: %s", recoveredVer.Status)
	}
}

func TestBuildRootHandler(t *testing.T) {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	apiMux.HandleFunc("GET /api/v1/profile", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"full_name":"Test User"}`))
	})

	handler := buildRootHandler(apiMux)

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
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
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
		var prof domain.Profile
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
	server := buildServer(":8080", mux, 11*time.Minute)

	if server.Addr != ":8080" {
		t.Errorf("expected Addr :8080, got %s", server.Addr)
	}
	if server.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("expected ReadHeaderTimeout 5s, got %v", server.ReadHeaderTimeout)
	}
	if server.ReadTimeout != 60*time.Second {
		t.Errorf("expected ReadTimeout 60s, got %v", server.ReadTimeout)
	}
	if server.WriteTimeout != 11*time.Minute {
		t.Errorf("expected WriteTimeout 11m, got %v", server.WriteTimeout)
	}
	if server.IdleTimeout != 120*time.Second {
		t.Errorf("expected IdleTimeout 120s, got %v", server.IdleTimeout)
	}
	if server.MaxHeaderBytes != 1<<20 {
		t.Errorf("expected MaxHeaderBytes 1MB, got %d", server.MaxHeaderBytes)
	}
}

func TestBuildRootHandler_MCPRouting(t *testing.T) {
	apiMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("api"))
	})
	mcpMux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mcp"))
	})

	root := buildRootHandler(apiMux, mcpMux)

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
	t.Setenv("PORT", freePort(t))

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- run(ctx, "-debug-llm-prompts", "-llm-debug-dir", debugDir)
	}()

	time.Sleep(100 * time.Millisecond)
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


