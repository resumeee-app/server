package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/access"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
)

// Standard OpenAPI error codes.
const (
	// CodeInvalidRequest indicates malformed input, missing required fields, or validation failure.
	CodeInvalidRequest = "INVALID_REQUEST"
	// CodeUnauthorized indicates missing, malformed, or expired authentication credentials.
	CodeUnauthorized = "UNAUTHORIZED"
	// CodeForbidden indicates the authenticated tenant lacks permission or tried to mutate an immutable resource.
	CodeForbidden = "FORBIDDEN"
	// CodeNotFound indicates the requested resource does not exist for the authenticated tenant.
	CodeNotFound = "NOT_FOUND"
	// CodeConflict indicates a state conflict such as duplicate creation or concurrent modification.
	CodeConflict = "CONFLICT"
	// CodeQuotaExceeded indicates the tenant exceeded its daily LLM request or monthly token quota.
	CodeQuotaExceeded = "QUOTA_EXCEEDED"
	// CodeTemplateValidationError indicates syntax or rendering validation failure in a template set.
	CodeTemplateValidationError = "TEMPLATE_VALIDATION_ERROR"
	// CodeProvenanceError indicates generated content failed provenance anchoring checks.
	CodeProvenanceError = "PROVENANCE_ERROR"
	// CodeQueueFull indicates the asynchronous worker queue cannot currently accept additional jobs.
	CodeQueueFull = "QUEUE_FULL"
	// CodeInternalError indicates an unexpected server-side failure.
	CodeInternalError = "INTERNAL_ERROR"
	// CodePayloadTooLarge indicates an uploaded body or file exceeds allowed size limits.
	CodePayloadTooLarge = "PAYLOAD_TOO_LARGE"
	// CodeUpstreamError indicates an upstream dependency such as an LLM provider failed.
	CodeUpstreamError = "UPSTREAM_ERROR"
	// CodeServiceUnavailable indicates a required service component is temporarily unavailable.
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
)

// ErrorDetail describes an error payload in the uniform JSON error envelope.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

// ErrorResponse represents the top-level uniform error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// MaxRequestBodyBytes defines the maximum allowed size for incoming JSON request bodies (10 MB).
const MaxRequestBodyBytes = 10 << 20

// decodeJSON decodes a bounded JSON request body into dst.
// If decoding fails, it writes the appropriate HTTP error response (413 or 400) and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "request body is empty", "")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	defer func() { _ = r.Body.Close() }()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "request body exceeds maximum allowed size", maxBytesErr.Error())
			return false
		}
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request body", err.Error())
		return false
	}
	return true
}

// writeJSON serializes data as JSON with the given HTTP status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

// writeError serializes an ErrorResponse as JSON with the specified status code and error details.
func writeError(w http.ResponseWriter, status int, code, message, details string) {
	writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}

// writeAccessError translates store-unavailability or forbidden errors into 500 or 403 JSON responses.
func writeAccessError(w http.ResponseWriter, err error, forbiddenMsg string) {
	if msg, ok := access.StoreUnavailableMessage(err); ok {
		writeError(w, http.StatusInternalServerError, CodeInternalError, msg, "")
		return
	}
	writeError(w, http.StatusForbidden, CodeForbidden, forbiddenMsg, "")
}

// writeDomainError translates known domain errors into uniform JSON error envelopes,
// logging unexpected internal failures through logger (or slog.Default() when nil).
func writeDomainError(w http.ResponseWriter, logger *slog.Logger, err error) {
	if err == nil {
		return
	}

	// Domain sentinels carry client-safe diagnostics constructed by resumeee-core;
	// storage adapters must never wrap raw SQL or DSN strings inside core.Err* sentinels.
	if errors.Is(err, core.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "resource not found", err.Error())
		return
	}
	if errors.Is(err, core.ErrUnauthorized) {
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, "unauthorized", err.Error())
		return
	}
	if errors.Is(err, core.ErrQuotaExceeded) {
		writeQuotaError(w, 3600, "llm operation quota exceeded", err.Error())
		return
	}
	if errors.Is(err, core.ErrGlobalSkillImmutable) {
		writeError(w, http.StatusForbidden, CodeForbidden, "global skill cannot be modified or deleted", err.Error())
		return
	}
	if errors.Is(err, core.ErrTemplateSyntax) {
		writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
		return
	}
	if errors.Is(err, core.ErrProvenance) {
		writeError(w, http.StatusUnprocessableEntity, CodeProvenanceError, "provenance validation failed", err.Error())
		return
	}
	if errors.Is(err, core.ErrQueueFull) {
		writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
		return
	}
	if errors.Is(err, core.ErrValidation) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request data", err.Error())
		return
	}
	if errors.Is(err, core.ErrDefaultBranchImmutable) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "default branch is immutable", err.Error())
		return
	}
	if errors.Is(err, core.ErrStrategyImmutable) {
		writeError(w, http.StatusForbidden, CodeForbidden, "built-in strategy cannot be modified", err.Error())
		return
	}
	if errors.Is(err, core.ErrForbidden) {
		writeError(w, http.StatusForbidden, CodeForbidden, "access forbidden", err.Error())
		return
	}
	if errors.Is(err, core.ErrConflict) {
		writeError(w, http.StatusConflict, CodeConflict, "resource conflict", err.Error())
		return
	}

	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("unhandled internal api error", "error", err)
	writeError(w, http.StatusInternalServerError, CodeInternalError, "an internal error occurred while processing the request", "")
}

// writeFormattedTextOutput writes html, markdown/md, or text/txt rendered outputs and returns true when handled.
func writeFormattedTextOutput(w http.ResponseWriter, format string, out core.RenderOutput) bool {
	switch format {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:; base-uri 'none'; form-action 'none'")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.HTML))
		return true
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.Markdown))
		return true
	case "text", "txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.PlainText))
		return true
	default:
		return false
	}
}

// writeRenderOutput writes rendered resume output according to the format query parameter (html, markdown, text, or default json).
func writeRenderOutput(w http.ResponseWriter, r *http.Request, out core.RenderOutput) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	if writeFormattedTextOutput(w, format, out) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// writeQuotaError writes a structured 429 Too Many Requests response with guaranteed Retry-After header.
func writeQuotaError(w http.ResponseWriter, retryAfterSeconds int, message, details string) {
	if retryAfterSeconds <= 0 {
		retryAfterSeconds = 3600
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds))
	writeError(w, http.StatusTooManyRequests, CodeQuotaExceeded, message, details)
}

// extendResponseWriteDeadline extends the per-request HTTP write deadline beyond timeout by
// config.DefaultHTTPWriteTimeoutBuffer so timeout error responses can flush after context cancellation.
func extendResponseWriteDeadline(w http.ResponseWriter, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout + config.DefaultHTTPWriteTimeoutBuffer))
}
