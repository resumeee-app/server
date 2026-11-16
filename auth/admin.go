package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/resumeee-app/core"
)

// fallbackUserID is the default local tenant identifier when none is explicitly configured.
const fallbackUserID = "default"

// AdminProvider implements Provider for single-user local administration deployments.
// It bypasses external token verification and resolves identity from the X-User-ID header
// or falls back to a configured default user identifier, always granting administrative privileges.
type AdminProvider struct {
	defaultUserID string
}

// Compile-time check verifies AdminProvider implements Provider.
var _ Provider = (*AdminProvider)(nil)

// AdminOption configures an AdminProvider.
type AdminOption func(*AdminProvider)

// WithDefaultUserID sets the fallback default user ID on an AdminProvider.
func WithDefaultUserID(userID string) AdminOption {
	return func(p *AdminProvider) {
		if strings.TrimSpace(userID) != "" {
			p.defaultUserID = strings.TrimSpace(userID)
		}
	}
}

// NewAdminProvider constructs an AdminProvider with the designated default tenant user identifier
// and optional functional AdminOptions.
func NewAdminProvider(defaultUserID string, opts ...AdminOption) *AdminProvider {
	trimmed := strings.TrimSpace(defaultUserID)
	if trimmed == "" {
		trimmed = fallbackUserID
	}
	p := &AdminProvider{
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
func (p *AdminProvider) Name() string {
	return ProviderAdmin
}

// DefaultUserID returns the configured fallback tenant user identifier.
func (p *AdminProvider) DefaultUserID() string {
	if p == nil || p.defaultUserID == "" {
		return fallbackUserID
	}
	return p.defaultUserID
}

// AuthenticateRequest extracts the tenant user ID from the X-User-ID header or falls back to defaultUserID.
func (p *AdminProvider) AuthenticateRequest(r *http.Request) (*core.TokenInfo, error) {
	uid := fallbackUserID
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
func (p *AdminProvider) VerifyIDToken(_ context.Context, token string) (*core.TokenInfo, error) {
	uid := strings.TrimSpace(token)
	if uid == "" {
		if p != nil && p.defaultUserID != "" {
			uid = p.defaultUserID
		} else {
			uid = fallbackUserID
		}
	}
	return &core.TokenInfo{
		UID:   uid,
		Admin: true,
		Role:  string(core.UserRoleAdmin),
	}, nil
}
