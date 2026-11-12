package api

import (
	"context"

	"github.com/resumeee-app/resumeee-core"
)

// WithUserID binds an authenticated tenant user ID into the request context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return core.WithUserID(ctx, userID)
}

// UserIDFromContext extracts the authenticated tenant user ID from the context.
// Returns core.ErrUnauthorized if the context lacks an authenticated user.
func UserIDFromContext(ctx context.Context) (string, error) {
	return core.UserIDFromContext(ctx)
}

// WithIsAdmin binds an administrative privilege indicator into the request context.
func WithIsAdmin(ctx context.Context, isAdmin bool) context.Context {
	return core.WithIsAdmin(ctx, isAdmin)
}

// IsAdminFromContext extracts whether the request context has administrative privileges.
func IsAdminFromContext(ctx context.Context) bool {
	return core.IsAdminFromContext(ctx)
}
