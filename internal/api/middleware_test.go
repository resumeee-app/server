package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
