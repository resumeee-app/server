package api

import (
	"context"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"resumeee/internal/domain"
)

type responseWriterRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

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
// If timeout <= 0, it falls back to a 20-second default boundary.
// For /profile/pdf/parse (normalized), handler-level timeout budgeting is used to accommodate LLM extraction.
func TimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	if timeout <= 0 {
		timeout = 20 * time.Second
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

// AuthMiddleware inspects bearer tokens or admin mode bypass and injects the authenticated tenant ID into context.
func AuthMiddleware(admin bool, defaultUserID string, verifier domain.TokenVerifier) func(http.Handler) http.Handler {
	if defaultUserID == "" {
		defaultUserID = "default"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodOptions || isPublicRoute(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// Admin Mode bypass (only when admin is explicitly enabled)
			if admin {
				uid := strings.TrimSpace(r.Header.Get("X-User-ID"))
				if uid == "" {
					uid = defaultUserID
				}
				ctx := WithUserID(r.Context(), uid)
				ctx = WithIsAdmin(ctx, true)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// When not admin, verifier is strictly required; fail closed if nil
			if verifier == nil {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "authentication verifier not configured", "")
				return
			}

			// Multi-tenant mode with TokenVerifier
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid authorization header", "")
				return
			}

			token := strings.TrimSpace(authHeader[7:])
			if token == "" {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "bearer token is empty", "")
				return
			}

			tokenInfo, err := verifier.VerifyIDToken(r.Context(), token)
			if err != nil || tokenInfo == nil || tokenInfo.UID == "" {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired token", "")
				return
			}

			ctx := WithUserID(r.Context(), tokenInfo.UID)
			ctx = WithIsAdmin(ctx, tokenInfo.IsAdmin())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

