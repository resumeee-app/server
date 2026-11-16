// Package auth provides HTTP authentication contracts, bearer token extraction,
// local admin mode providers, token verifier adapters, and a provider registry.
package auth

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/resumeee-app/core"
)

// Provider identifier constants.
const (
	// ProviderAdmin identifies the local single-user admin authentication provider.
	ProviderAdmin = "admin"
)

// Authentication failure sentinel errors.
var (
	// ErrMissingBearerHeader indicates the Authorization header was missing or not prefixed with Bearer.
	ErrMissingBearerHeader = fmt.Errorf("%w: missing or invalid authorization header", core.ErrUnauthorized)

	// ErrEmptyBearerToken indicates the Bearer token payload was blank or composed only of whitespace.
	ErrEmptyBearerToken = fmt.Errorf("%w: bearer token is empty", core.ErrUnauthorized)

	// ErrProviderNotConfigured indicates the authentication provider was nil or not configured.
	ErrProviderNotConfigured = fmt.Errorf("%w: authentication provider not configured", core.ErrUnauthorized)
)

// RequestAuthenticator inspects and authenticates incoming HTTP requests.
type RequestAuthenticator interface {
	AuthenticateRequest(r *http.Request) (*core.TokenInfo, error)
}

// Provider represents a full authentication provider supporting both direct HTTP request inspection
// and bearer token verification.
type Provider interface {
	core.AuthProvider
	RequestAuthenticator
}

// ExtractBearerToken parses the Bearer token from an HTTP Authorization header.
func ExtractBearerToken(r *http.Request) (string, error) {
	if r == nil {
		return "", ErrMissingBearerHeader
	}
	rawHeader := r.Header.Get("Authorization")
	trimmed := strings.TrimSpace(rawHeader)
	if trimmed == "" {
		return "", ErrMissingBearerHeader
	}
	lower := strings.ToLower(trimmed)
	if lower == "bearer" {
		return "", ErrEmptyBearerToken
	}
	if !strings.HasPrefix(lower, "bearer ") {
		return "", ErrMissingBearerHeader
	}
	token := strings.TrimSpace(trimmed[7:])
	if token == "" {
		return "", ErrEmptyBearerToken
	}
	return token, nil
}
