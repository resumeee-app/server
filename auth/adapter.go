package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/resumeee-app/resumeee-core"
)

// TokenVerifierProvider adapts any core.TokenVerifier into a full auth.Provider.
type TokenVerifierProvider struct {
	name     string
	verifier core.TokenVerifier
}

// NewTokenVerifierProvider wraps an existing core.TokenVerifier as an auth.Provider.
func NewTokenVerifierProvider(name string, verifier core.TokenVerifier) *TokenVerifierProvider {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		trimmed = "custom"
	}
	return &TokenVerifierProvider{
		name:     trimmed,
		verifier: verifier,
	}
}

// Name returns the configured provider identifier.
func (p *TokenVerifierProvider) Name() string {
	if p == nil || p.name == "" {
		return "custom"
	}
	return p.name
}

// VerifyIDToken delegates to the underlying core.TokenVerifier.
func (p *TokenVerifierProvider) VerifyIDToken(ctx context.Context, idToken string) (*core.TokenInfo, error) {
	if p == nil || p.verifier == nil {
		return nil, core.ErrUnauthorized
	}
	return p.verifier.VerifyIDToken(ctx, idToken)
}

// AuthenticateRequest extracts the Bearer token and delegates to VerifyIDToken.
func (p *TokenVerifierProvider) AuthenticateRequest(r *http.Request) (*core.TokenInfo, error) {
	if p == nil || p.verifier == nil {
		return nil, ErrProviderNotConfigured
	}
	token, err := ExtractBearerToken(r)
	if err != nil {
		return nil, err
	}
	return p.VerifyIDToken(r.Context(), token)
}
