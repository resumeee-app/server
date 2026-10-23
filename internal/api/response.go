package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"resumeee/internal/domain"
)

// Standard OpenAPI error codes.
const (
	CodeInvalidRequest          = "INVALID_REQUEST"
	CodeNotFound                = "NOT_FOUND"
	CodeTemplateValidationError = "TEMPLATE_VALIDATION_ERROR"
	CodeProvenanceError         = "PROVENANCE_ERROR"
	CodeQueueFull               = "QUEUE_FULL"
	CodeInternalError           = "INTERNAL_ERROR"
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

	writeError(w, http.StatusInternalServerError, CodeInternalError, "an internal error occurred while processing the request", "")
}
