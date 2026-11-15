package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
)

// responseWriterRecorder wraps http.ResponseWriter to record the HTTP status code written by downstream handlers.
type responseWriterRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

// Compile-time assertion ensuring responseWriterRecorder satisfies http.ResponseWriter.
var _ http.ResponseWriter = (*responseWriterRecorder)(nil)

// WriteHeader captures the HTTP response status code before delegating to the wrapped ResponseWriter.
func (rw *responseWriterRecorder) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

// Write records a default 200 OK status code if not already sent, then writes data to the client.
func (rw *responseWriterRecorder) Write(b []byte) (int, error) {
	if !rw.written {
		rw.statusCode = http.StatusOK
		rw.written = true
	}
	return rw.ResponseWriter.Write(b)
}

// LoggingMiddleware logs incoming HTTP requests with method, path, status, and duration.
func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseWriterRecorder{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(rec, r)

			if logger != nil {
				if rec.statusCode >= 500 {
					logger.Error("http request",
						"method", r.Method,
						"path", r.URL.Path,
						"status", rec.statusCode,
						"duration_ms", time.Since(start).Milliseconds(),
						"remote_addr", r.RemoteAddr,
					)
				} else {
					logger.Info("http request",
						"method", r.Method,
						"path", r.URL.Path,
						"status", rec.statusCode,
						"duration_ms", time.Since(start).Milliseconds(),
						"remote_addr", r.RemoteAddr,
					)
				}
			}
		})
	}
}

// RecoveryMiddleware recovers from panics and responds with a uniform 500 error envelope.
func RecoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if logger != nil {
						logger.Error("panic recovered in HTTP handler",
							"panic", rec,
							"path", r.URL.Path,
							"method", r.Method,
						)
					}
					writeError(w, http.StatusInternalServerError, CodeInternalError, "An internal error occurred while processing the request", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// CORSMiddleware sets permissive CORS headers for local/cross-origin frontends.
func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-User-ID, Accept, Origin, X-Requested-With")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ContentTypeMiddleware validates that mutation requests with bodies provide application/json.
func ContentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			hasBody := r.ContentLength > 0 || r.Header.Get("Transfer-Encoding") != ""
			if hasBody {
				ct := r.Header.Get("Content-Type")
				if strings.Contains(strings.ToLower(ct), "multipart/form-data") && strings.HasSuffix(r.URL.Path, "/pdf/parse") {
					next.ServeHTTP(w, r)
					return
				}
				if ct == "" || !strings.Contains(strings.ToLower(ct), "application/json") {
					writeError(w, http.StatusBadRequest, CodeInvalidRequest, "Content-Type must be application/json", "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// TimeoutMiddleware sets a request context cancellation deadline bounded by the given duration.
// If timeout <= 0, it falls back to config.DefaultRESTTimeout.
// For /profile/pdf/parse (normalized), handler-level timeout budgeting is used to accommodate LLM extraction.
func TimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	if timeout <= 0 {
		timeout = config.DefaultRESTTimeout
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cleanPath := path.Clean(r.URL.Path)
			if cleanPath == "/api/v1/profile/pdf/parse" || strings.HasSuffix(cleanPath, "/profile/pdf/parse") {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isPublicRoute checks if a request path is exempt from authentication.
func isPublicRoute(p string) bool {
	clean := path.Clean(p)
	return clean == "/healthz" || clean == "/api/v1/health" || clean == "/health" || clean == "/api/v1/models" || clean == "/models"
}

// TenantProvisioner defines an interface for lazy onboarding of newly authenticated tenants.
type TenantProvisioner interface {
	EnsureTenantProvisioned(ctx context.Context, userID string, userInfo *core.User) error
}

// AuthMiddlewareWithProvider inspects incoming requests using the provided auth.Provider
// and injects the authenticated tenant ID and admin privileges into context.
func AuthMiddlewareWithProvider(provider auth.Provider, provisioner TenantProvisioner, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || isPublicRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			if provider == nil {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication provider not configured", "")
				return
			}

			tokenInfo, err := provider.AuthenticateRequest(r)
			if err != nil || tokenInfo == nil || tokenInfo.UID == "" {
				errMsg := "invalid or expired token"
				if err != nil {
					if errors.Is(err, auth.ErrMissingBearerHeader) {
						errMsg = "missing or invalid authorization header"
					} else if errors.Is(err, auth.ErrEmptyBearerToken) {
						errMsg = "bearer token is empty"
					}
				}
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, errMsg, "")
				return
			}

			ctx := core.WithUserID(r.Context(), tokenInfo.UID)
			ctx = core.WithIsAdmin(ctx, tokenInfo.IsAdmin())
			if provisioner != nil && provider.Name() != auth.ProviderAdmin {
				role := core.UserRoleUser
				if tokenInfo.IsAdmin() {
					role = core.UserRoleAdmin
				}
				if err := provisioner.EnsureTenantProvisioned(r.Context(), tokenInfo.UID, &core.User{
					ID:       tokenInfo.UID,
					Email:    tokenInfo.Email,
					Role:     role,
					IsActive: true,
				}); err != nil {
					logger.ErrorContext(r.Context(), "failed to provision tenant", "user_id", tokenInfo.UID, "error", err)
					writeError(w, http.StatusInternalServerError, CodeInternalError, "internal server error provisioning tenant", "")
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
