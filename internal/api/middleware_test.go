package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-core"
)

func TestTimeoutMiddleware(t *testing.T) {
	t.Run("bounds regular request context by specified duration", func(t *testing.T) {
		timeout := 100 * time.Millisecond
		var capturedDeadline time.Time
		var hasDeadline bool

		handler := TimeoutMiddleware(timeout)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedDeadline, hasDeadline = r.Context().Deadline()
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !hasDeadline {
			t.Fatalf("expected request context to have deadline")
		}
		remaining := time.Until(capturedDeadline)
		if remaining <= 0 || remaining > timeout {
			t.Errorf("expected deadline within %v, got %v", timeout, remaining)
		}
	})

	t.Run("cancels context after timeout expires", func(t *testing.T) {
		timeout := 20 * time.Millisecond
		ctxDone := make(chan struct{})

		handler := TimeoutMiddleware(timeout)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
				close(ctxDone)
			case <-time.After(200 * time.Millisecond):
				t.Error("timeout did not trigger context cancellation")
			}
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/skills", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		select {
		case <-ctxDone:
			// Success
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timed out waiting for context cancellation")
		}
	})

	t.Run("bypasses REST timeout for PDF parse paths including trailing slashes", func(t *testing.T) {
		paths := []string{
			"/api/v1/profile/pdf/parse",
			"/api/v1/profile/pdf/parse/",
			"/profile/pdf/parse",
		}

		for _, p := range paths {
			t.Run(p, func(t *testing.T) {
				var hasDeadline bool
				handler := TimeoutMiddleware(20 * time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, hasDeadline = r.Context().Deadline()
					w.WriteHeader(http.StatusOK)
				}))

				req := httptest.NewRequest(http.MethodPost, p, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if hasDeadline {
					t.Errorf("expected path %s to bypass REST timeout middleware", p)
				}
			})
		}
	})

	t.Run("defaults non-positive timeout to 20 seconds", func(t *testing.T) {
		var capturedDeadline time.Time
		var hasDeadline bool

		handler := TimeoutMiddleware(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedDeadline, hasDeadline = r.Context().Deadline()
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/branches", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if !hasDeadline {
			t.Fatalf("expected request context to have deadline")
		}
		remaining := time.Until(capturedDeadline)
		if remaining < 19*time.Second || remaining > 20*time.Second {
			t.Errorf("expected default deadline around 20s, got %v", remaining)
		}
	})
}

type mockTokenVerifier struct {
	verifyFunc func(ctx context.Context, token string) (*core.TokenInfo, error)
}

func (m *mockTokenVerifier) VerifyIDToken(ctx context.Context, token string) (*core.TokenInfo, error) {
	return m.verifyFunc(ctx, token)
}

func TestAuthMiddleware(t *testing.T) {
	t.Run("admin mode bypasses verification and sets defaultUserID", func(t *testing.T) {
		var capturedUID string
		var capturedAdmin bool
		handler := AuthMiddleware(true, "admin-user-default", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID, _ = UserIDFromContext(r.Context())
			capturedAdmin = IsAdminFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if capturedUID != "admin-user-default" {
			t.Errorf("expected captured UID 'admin-user-default', got %q", capturedUID)
		}
		if !capturedAdmin {
			t.Errorf("expected captured admin privilege to be true in admin mode")
		}
	})

	t.Run("admin mode honors X-User-ID header override", func(t *testing.T) {
		var capturedUID string
		handler := AuthMiddleware(true, "default", nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID, _ = UserIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		req.Header.Set("X-User-ID", "custom-tenant-99")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if capturedUID != "custom-tenant-99" {
			t.Errorf("expected captured UID 'custom-tenant-99', got %q", capturedUID)
		}
	})

	t.Run("multi-tenant mode with valid bearer token injects UID", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				if token == "valid-firebase-jwt" {
					return &core.TokenInfo{UID: "firebase-uid-42", Email: "alex@example.com"}, nil
				}
				return nil, core.ErrUnauthorized
			},
		}

		var capturedUID string
		handler := AuthMiddleware(false, "default", verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID, _ = UserIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/branches", nil)
		req.Header.Set("Authorization", "Bearer valid-firebase-jwt")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}
		if capturedUID != "firebase-uid-42" {
			t.Errorf("expected captured UID 'firebase-uid-42', got %q", capturedUID)
		}
	})

	t.Run("multi-tenant mode injects admin status from token info", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, token string) (*core.TokenInfo, error) {
				switch token {
				case "admin-token-flag":
					return &core.TokenInfo{UID: "admin-1", Email: "admin@example.com", Admin: true}, nil
				case "admin-token-role":
					return &core.TokenInfo{UID: "admin-2", Email: "admin2@example.com", Role: "admin"}, nil
				case "normal-user-token":
					return &core.TokenInfo{UID: "user-1", Email: "user@example.com", Role: "user", Admin: false}, nil
				default:
					return nil, core.ErrUnauthorized
				}
			},
		}

		tests := []struct {
			name          string
			token         string
			expectedAdmin bool
		}{
			{
				name:          "token with Admin flag true",
				token:         "admin-token-flag",
				expectedAdmin: true,
			},
			{
				name:          "token with admin role",
				token:         "admin-token-role",
				expectedAdmin: true,
			},
			{
				name:          "standard user token without admin privileges",
				token:         "normal-user-token",
				expectedAdmin: false,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				var capturedAdmin bool
				handler := AuthMiddleware(false, "default", verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					capturedAdmin = IsAdminFromContext(r.Context())
					w.WriteHeader(http.StatusOK)
				}))

				req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
				req.Header.Set("Authorization", "Bearer "+tc.token)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Fatalf("expected status 200, got %d", rec.Code)
				}
				if capturedAdmin != tc.expectedAdmin {
					t.Errorf("expected capturedAdmin=%v, got %v", tc.expectedAdmin, capturedAdmin)
				}
			})
		}
	})

	t.Run("multi-tenant mode rejects missing authorization header with 401", func(t *testing.T) {
		verifier := &mockTokenVerifier{}
		handler := AuthMiddleware(false, "default", verifier)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", rec.Code)
		}
	})

	t.Run("multi-tenant mode rejects invalid token with 401", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(_ context.Context, _ string) (*core.TokenInfo, error) {
				return nil, core.ErrUnauthorized
			},
		}
		handler := AuthMiddleware(false, "default", verifier)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		req.Header.Set("Authorization", "Bearer invalid-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401, got %d", rec.Code)
		}
	})

	t.Run("public routes bypass token verification in multi-tenant mode", func(t *testing.T) {
		verifier := &mockTokenVerifier{}
		handler := AuthMiddleware(false, "default", verifier)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		for _, p := range []string{"/healthz", "/api/v1/health", "/api/v1/models"} {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Errorf("expected path %s to bypass auth with 200, got %d", p, rec.Code)
			}
		}
	})

	t.Run("fails closed with 401 when admin is false and verifier is nil even with X-User-ID", func(t *testing.T) {
		handler := AuthMiddleware(false, "default", nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		req.Header.Set("X-User-ID", "attacker-user-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401 when verifier is nil and admin is false, got %d", rec.Code)
		}
	})

	t.Run("AuthMiddlewareWithProvider with AdminProvider injects admin context", func(t *testing.T) {
		provider := auth.NewAdminProvider("test-default")
		var capturedUID string
		var capturedAdmin bool

		handler := AuthMiddlewareWithProvider(provider)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID, _ = UserIDFromContext(r.Context())
			capturedAdmin = IsAdminFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		req.Header.Set("X-User-ID", "custom-admin-id")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if capturedUID != "custom-admin-id" {
			t.Errorf("expected UID 'custom-admin-id', got %q", capturedUID)
		}
		if !capturedAdmin {
			t.Errorf("expected capturedAdmin true")
		}
	})

	t.Run("AuthMiddlewareWithProvider fails closed when provider is nil", func(t *testing.T) {
		handler := AuthMiddlewareWithProvider(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/profile", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected status 401 when provider is nil, got %d", rec.Code)
		}
	})
}
