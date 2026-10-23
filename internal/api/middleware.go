package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type responseWriterRecorder struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (rw *responseWriterRecorder) WriteHeader(code int) {
	if !rw.written {
		rw.statusCode = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

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
				logger.Info("http request",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.statusCode,
					"duration_ms", time.Since(start).Milliseconds(),
					"remote_addr", r.RemoteAddr,
				)
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-User-ID")

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
				if ct == "" || !strings.Contains(strings.ToLower(ct), "application/json") {
					writeError(w, http.StatusBadRequest, CodeInvalidRequest, "Content-Type must be application/json", "")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
