package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"resumeee/internal/domain"
)

type mockTokenVerifier struct {
	verifyFunc func(ctx context.Context, token string) (*domain.TokenInfo, error)
}

func (m *mockTokenVerifier) VerifyIDToken(ctx context.Context, token string) (*domain.TokenInfo, error) {
	return m.verifyFunc(ctx, token)
}

func TestAuthenticateRequest(t *testing.T) {
	t.Run("admin mode honors X-User-ID header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("X-User-ID", "custom-admin-user")
		cfg := ServerConfig{
			Admin:         true,
			DefaultUserID: "default-admin",
		}

		uid, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if uid != "custom-admin-user" {
			t.Errorf("expected uid 'custom-admin-user', got %q", uid)
		}
	})

	t.Run("admin mode falls back to DefaultUserID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		cfg := ServerConfig{
			Admin:         true,
			DefaultUserID: "default-admin",
		}

		uid, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if uid != "default-admin" {
			t.Errorf("expected uid 'default-admin', got %q", uid)
		}
	})

	t.Run("multi-tenant mode fails closed when TokenVerifier is nil even if X-User-ID provided", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("X-User-ID", "attacker-user-id")
		req.Header.Set("Authorization", "Bearer some-token")
		cfg := ServerConfig{
			Admin:         false,
			TokenVerifier: nil,
		}

		_, err := authenticateRequest(req, cfg)
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized when verifier is nil and Admin is false, got %v", err)
		}
	})

	t.Run("multi-tenant mode validates Bearer token via TokenVerifier", func(t *testing.T) {
		verifier := &mockTokenVerifier{
			verifyFunc: func(ctx context.Context, token string) (*domain.TokenInfo, error) {
				if token == "valid-token" {
					return &domain.TokenInfo{UID: "verified-uid-42"}, nil
				}
				return nil, domain.ErrUnauthorized
			},
		}
		cfg := ServerConfig{
			Admin:         false,
			TokenVerifier: verifier,
		}

		// Valid token
		reqValid := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqValid.Header.Set("Authorization", "Bearer valid-token")
		uid, err := authenticateRequest(reqValid, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if uid != "verified-uid-42" {
			t.Errorf("expected uid 'verified-uid-42', got %q", uid)
		}

		// Invalid token
		reqInvalid := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqInvalid.Header.Set("Authorization", "Bearer invalid-token")
		_, err = authenticateRequest(reqInvalid, cfg)
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized for invalid token, got %v", err)
		}
	})

	t.Run("static token dictionary verification", func(t *testing.T) {
		cfg := ServerConfig{
			AuthTokens: map[string]string{
				"secret-token-1": "user-static-1",
			},
		}

		req := httptest.NewRequest("GET", "/mcp/sse", nil)
		req.Header.Set("Authorization", "Bearer secret-token-1")
		uid, err := authenticateRequest(req, cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if uid != "user-static-1" {
			t.Errorf("expected uid 'user-static-1', got %q", uid)
		}

		reqBad := httptest.NewRequest("GET", "/mcp/sse", nil)
		reqBad.Header.Set("Authorization", "Bearer wrong-token")
		_, err = authenticateRequest(reqBad, cfg)
		if err == nil {
			t.Fatal("expected error for unknown static token")
		}
	})
}
