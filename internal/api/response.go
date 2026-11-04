package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"resumeee/internal/domain"
)

// Standard OpenAPI error codes.
const (
	CodeInvalidRequest          = "INVALID_REQUEST"
	CodeForbidden               = "FORBIDDEN"
	CodeNotFound                = "NOT_FOUND"
	CodeTemplateValidationError = "TEMPLATE_VALIDATION_ERROR"
	CodeProvenanceError         = "PROVENANCE_ERROR"
	CodeQueueFull               = "QUEUE_FULL"
	CodeInternalError           = "INTERNAL_ERROR"
	CodePayloadTooLarge         = "PAYLOAD_TOO_LARGE"
	CodeUpstreamError           = "UPSTREAM_ERROR"
	CodeServiceUnavailable      = "SERVICE_UNAVAILABLE"
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

// writeDomainError translates known domain errors into uniform JSON error envelopes.
func writeDomainError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}

	if errors.Is(err, domain.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, "resource not found", err.Error())
		return
	}
	if errors.Is(err, domain.ErrTemplateSyntax) {
		writeError(w, http.StatusUnprocessableEntity, CodeTemplateValidationError, "template syntax error", err.Error())
		return
	}
	if errors.Is(err, domain.ErrProvenance) {
		writeError(w, http.StatusUnprocessableEntity, CodeProvenanceError, "provenance validation failed", err.Error())
		return
	}
	if errors.Is(err, domain.ErrQueueFull) {
		writeError(w, http.StatusServiceUnavailable, CodeQueueFull, "worker queue full", err.Error())
		return
	}
	if errors.Is(err, domain.ErrValidation) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid request data", err.Error())
		return
	}
	if errors.Is(err, domain.ErrDefaultBranchImmutable) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "default branch is immutable", err.Error())
		return
	}

	slog.Default().Error("unhandled internal api error", "error", err)
	writeError(w, http.StatusInternalServerError, CodeInternalError, "an internal error occurred while processing the request", "")
}

// writeRenderOutput writes rendered resume output according to the format query parameter (html, markdown, text, or default json).
func writeRenderOutput(w http.ResponseWriter, r *http.Request, out domain.RenderOutput) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	switch format {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.HTML))
	case "markdown", "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.Markdown))
	case "text", "txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(out.PlainText))
	default:
		writeJSON(w, http.StatusOK, out)
	}
}
