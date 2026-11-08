package mcp

import (
	"context"
	"log/slog"
	"time"

	"resumeee/internal/auth"
	"resumeee/internal/branch"
	"resumeee/internal/domain"
	"resumeee/internal/profile"
	"resumeee/internal/repository"
	"resumeee/internal/worker"
)

// contextKey defines a private string type used for context value keys in the mcp package.
type contextKey string

// userIDContextKey is the context key under which authenticated user IDs are stored.
const userIDContextKey contextKey = "mcp_user_id"

// ServerConfig encapsulates all dependencies, repositories, services, timeouts, and authorization
// parameters required by the MCP server subsystem across both stdio and SSE HTTP transports.
type ServerConfig struct {
	// ProfileService provides main career profile snapshot retrieval and mutation operations.
	ProfileService profile.Service
	// BranchService coordinates targeted resume branch lifecycles, version generation, and gap analysis.
	BranchService branch.Service
	// TemplateRenderer handles multi-format (HTML, Markdown, PlainText) resume rendering.
	TemplateRenderer domain.TemplateRenderer
	// ProfileRepo provides persistence access for career entities, bullet facts, and skills.
	ProfileRepo domain.ProfileRepository
	// BranchRepo provides persistence access for resume branches and branch versions.
	BranchRepo domain.BranchRepository
	// TemplateRepo provides persistence access for built-in and custom template sets.
	TemplateRepo domain.TemplateRepository
	// PresetRepo provides access to tailoring presets and strategy instructions.
	PresetRepo domain.PresetRepository
	// UserRepo provides persistence access for multi-tenant user accounts.
	UserRepo domain.UserRepository
	// ResumeExtractor provides PDF resume text parsing and structured fact extraction.
	ResumeExtractor worker.ResumeExtractor
	// JobEnqueuer coordinates asynchronous resume tailoring job processing.
	JobEnqueuer worker.JobEnqueuer
	// DefaultUserID specifies the fallback user identifier when no auth header or token is provided.
	DefaultUserID string
	// Admin indicates local single-user mode bypassing external token verification.
	Admin bool
	// AuthProvider authenticates incoming requests across transports.
	AuthProvider auth.Provider
	// TokenVerifier validates bearer tokens in multi-tenant mode.
	TokenVerifier domain.TokenVerifier
	// Logger provides structured diagnostic logging across MCP tool invocations and transports.
	Logger *slog.Logger
	// RESTTimeout specifies the maximum execution deadline for synchronous database and rendering operations.
	RESTTimeout time.Duration
	// TailoringTimeout specifies the execution deadline for asynchronous LLM resume tailoring jobs.
	TailoringTimeout time.Duration
	// MaxConnectionsPerUser defines the concurrent active SSE connection limit per individual tenant.
	MaxConnectionsPerUser int
	// MaxTotalConnections defines the global concurrent active SSE connection limit across all tenants.
	MaxTotalConnections int
	// AuthTokens maps Bearer authentication tokens to verified user identifiers.
	AuthTokens map[string]string
}

// WithUserID returns a child context populated with the given authenticated user ID.
//
// Parameters:
//   - ctx: Parent context.Context.
//   - userID: Authenticated user identifier string.
//
// Returns:
//   - Context containing the user identifier value.
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDContextKey, userID)
}

// UserIDFromContext retrieves the authenticated user ID from ctx, falling back to defaultID if unset.
//
// Parameters:
//   - ctx: Context potentially holding an authenticated user identifier.
//   - defaultID: Fallback identifier to return if no user ID is present in ctx.
//
// Returns:
//   - Resolved user identifier string.
func UserIDFromContext(ctx context.Context, defaultID string) string {
	if ctx != nil {
		if val, ok := ctx.Value(userIDContextKey).(string); ok && val != "" {
			return val
		}
	}
	if defaultID != "" {
		return defaultID
	}
	return "default"
}

// setDefaults populates zero-value fields in ServerConfig with safe domain defaults.
func (c *ServerConfig) setDefaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.DefaultUserID == "" {
		c.DefaultUserID = "default"
	}
	if c.RESTTimeout <= 0 {
		c.RESTTimeout = 30 * time.Second
	}
	if c.TailoringTimeout <= 0 {
		c.TailoringTimeout = 10 * time.Minute
	}
	if c.MaxConnectionsPerUser <= 0 {
		c.MaxConnectionsPerUser = 5
	}
	if c.MaxTotalConnections <= 0 {
		c.MaxTotalConnections = 100
	}
}

// EnsureTenant lazily provisions a tenant in the repository if unprovisioned.
// It ensures default profile, branch, and templates exist for the given user.
func (c *ServerConfig) EnsureTenant(ctx context.Context, userID string) error {
	if userID == "" || c.ProfileRepo == nil || c.BranchRepo == nil || c.TemplateRepo == nil {
		return nil
	}
	return repository.EnsureTenantProvisionedRepos(ctx, c.ProfileRepo, c.BranchRepo, c.TemplateRepo, c.UserRepo, userID)
}

