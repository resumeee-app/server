package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/resumeee-app/resumeee-core"
)

// AdminAuthProvider implements auth.Provider for single-user local administration deployments.
// It bypasses external token verification and resolves identity from the X-User-ID header
// or falls back to a configured default user identifier, always granting administrative privileges.
type AdminAuthProvider struct {
	defaultUserID string
}

// AdminOption configures an AdminAuthProvider.
type AdminOption func(*AdminAuthProvider)

// WithDefaultUserID sets the fallback default user ID on an AdminAuthProvider.
func WithDefaultUserID(userID string) AdminOption {
	return func(p *AdminAuthProvider) {
		if strings.TrimSpace(userID) != "" {
			p.defaultUserID = strings.TrimSpace(userID)
		}
	}
}

// NewAdminAuthProvider constructs an AdminAuthProvider with the designated default tenant user identifier
// and optional functional AdminOptions.
func NewAdminAuthProvider(defaultUserID string, opts ...AdminOption) *AdminAuthProvider {
	trimmed := strings.TrimSpace(defaultUserID)
	if trimmed == "" {
		trimmed = "default"
	}
	p := &AdminAuthProvider{
		defaultUserID: trimmed,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// Name returns the provider identifier "admin".
func (p *AdminAuthProvider) Name() string {
	return ProviderAdmin
}

// DefaultUserID returns the configured fallback tenant user identifier.
func (p *AdminAuthProvider) DefaultUserID() string {
	if p == nil || p.defaultUserID == "" {
		return "default"
	}
	return p.defaultUserID
}

// AuthenticateRequest extracts the tenant user ID from the X-User-ID header or falls back to defaultUserID.
func (p *AdminAuthProvider) AuthenticateRequest(r *http.Request) (*core.TokenInfo, error) {
	uid := "default"
	if p != nil && p.defaultUserID != "" {
		uid = p.defaultUserID
	}
	if r != nil {
		if headerUID := strings.TrimSpace(r.Header.Get("X-User-ID")); headerUID != "" {
			uid = headerUID
		}
	}
	return &core.TokenInfo{
		UID:   uid,
		Admin: true,
		Role:  string(core.UserRoleAdmin),
	}, nil
}

// VerifyIDToken decodes the provided token string as a user identifier or falls back to defaultUserID.
func (p *AdminAuthProvider) VerifyIDToken(ctx context.Context, token string) (*core.TokenInfo, error) {
	uid := strings.TrimSpace(token)
	if uid == "" {
		if p != nil && p.defaultUserID != "" {
			uid = p.defaultUserID
		} else {
			uid = "default"
		}
	}
	return &core.TokenInfo{
		UID:   uid,
		Admin: true,
		Role:  string(core.UserRoleAdmin),
	}, nil
}
