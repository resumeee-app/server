package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

	uiMux := http.NewServeMux()
	uiMux.HandleFunc("GET /web", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`Workspace Dashboard`))
	})
	uiMux.HandleFunc("GET /web/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`Workspace Dashboard`))
	})
	uiMux.HandleFunc("GET /web/static/css/styles.css", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`/* styles */`))
	})
	uiMux.HandleFunc("GET /static/css/styles.css", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`/* styles */`))
	})

	handler := buildRootHandler(apiMux, uiMux)

	tests := []struct {
		name         string
		method       string
		path         string
		expectedCode int
		expectedLoc  string
		expectedBody string
	}{
		{
			name:         "Root redirects to /web",
			method:       http.MethodGet,
			path:         "/",
			expectedCode: http.StatusFound,
			expectedLoc:  "/web",
		},
		{
			name:         "GET /web serves dashboard",
			method:       http.MethodGet,
			path:         "/web",
			expectedCode: http.StatusOK,
			expectedBody: "Workspace Dashboard",
		},
		{
			name:         "GET /web/ serves dashboard",
			method:       http.MethodGet,
			path:         "/web/",
			expectedCode: http.StatusOK,
			expectedBody: "Workspace Dashboard",
		},
		{
			name:         "GET /web/static/css/styles.css serves static asset",
			method:       http.MethodGet,
			path:         "/web/static/css/styles.css",
			expectedCode: http.StatusOK,
			expectedBody: "/* styles */",
		},
		{
			name:         "GET /static/css/styles.css serves fallback static asset",
			method:       http.MethodGet,
			path:         "/static/css/styles.css",
			expectedCode: http.StatusOK,
			expectedBody: "/* styles */",
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
			if tc.expectedLoc != "" {
				loc := rec.Header().Get("Location")
				if loc != tc.expectedLoc {
					t.Errorf("expected Location %q, got %q", tc.expectedLoc, loc)
				}
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

	t.Run("GET / redirects to /web", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusFound {
			t.Errorf("expected 302 Found, got %d", resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/web" {
			t.Errorf("expected Location /web, got %q", loc)
		}
	})

	t.Run("GET /web serves dashboard with online status", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "Workspace Dashboard") {
			t.Errorf("expected body to contain 'Workspace Dashboard'")
		}
		if !strings.Contains(bodyStr, "badge-success") {
			t.Errorf("expected in-process API connection to be healthy (badge-success)")
		}
	})

	t.Run("GET /web/static/css/styles.css serves static css", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web/static/css/styles.css")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
	})

	t.Run("GET /web/profile renders master profile view via in-process API", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web/profile")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Master Profile") {
			t.Errorf("expected body to contain 'Master Profile'")
		}
	})

	t.Run("GET /web/branches renders branches view via in-process API", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web/branches")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Tailored Branches") {
			t.Errorf("expected body to contain 'Tailored Branches'")
		}
	})

	t.Run("GET /web/templates renders templates view via in-process API", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web/templates")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Templates") {
			t.Errorf("expected body to contain 'Templates'")
		}
	})

	t.Run("GET /web/presets renders presets view via in-process API", func(t *testing.T) {
		resp, err := clientNoRedirect.Get(baseURL + "/web/presets")
		if err != nil {
			t.Fatalf("failed request: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), "Presets") {
			t.Errorf("expected body to contain 'Presets'")
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
