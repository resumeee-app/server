package api

import (
	"context"

	"resumeee/internal/domain"
)

// WithUserID binds an authenticated tenant user ID into the request context.
func WithUserID(ctx context.Context, userID string) context.Context {
	return domain.WithUserID(ctx, userID)
}

// UserIDFromContext extracts the authenticated tenant user ID from the context.
// Returns domain.ErrUnauthorized if the context lacks an authenticated user.
func UserIDFromContext(ctx context.Context) (string, error) {
	return domain.UserIDFromContext(ctx)
}

// WithIsAdmin binds an administrative privilege indicator into the request context.
func WithIsAdmin(ctx context.Context, isAdmin bool) context.Context {
	return domain.WithIsAdmin(ctx, isAdmin)
}

// IsAdminFromContext extracts whether the request context has administrative privileges.
func IsAdminFromContext(ctx context.Context) bool {
	return domain.IsAdminFromContext(ctx)
}
