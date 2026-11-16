package mcp

import (
	"context"
	"log/slog"
	"time"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
	"github.com/resumeee-app/resumeee-core/renderer"
	"github.com/resumeee-app/resumeee-core/seed"
	"github.com/resumeee-app/resumeee-core/worker"
)

// contextKey defines a private string type used for context value keys in the mcp package.
type contextKey string

// Context value keys used within the mcp package.
const (
	// userIDContextKey is the context key under which authenticated user IDs are stored.
	userIDContextKey contextKey = "mcp_user_id"

	// skipTenantProvisionContextKey is the context key indicating lazy tenant provisioning is suppressed.
	skipTenantProvisionContextKey contextKey = "mcp_skip_tenant_provision"
)

// ServerConfig encapsulates all dependencies, stores, services, timeouts, and authorization
// parameters required by the MCP server subsystem across both stdio and SSE HTTP transports.
type ServerConfig struct {
	// ProfileService provides main career profile snapshot retrieval and mutation operations.
	ProfileService ProfileService
	// BranchService coordinates targeted resume branch lifecycles, version generation, and gap analysis.
	BranchService BranchService
	// TemplateRenderer handles multi-format (HTML, Markdown, PlainText) resume rendering.
	TemplateRenderer renderer.TemplateRenderer
	// ProfileStore provides persistence access for base profiles and career snapshots.
	ProfileStore ProfileStore
	// CareerStore provides persistence access for career entities, bullet facts, and skills.
	CareerStore CareerStore
	// BranchStore provides persistence access for resume branches and branch versions.
	BranchStore BranchStore
	// TemplateStore provides persistence access for built-in and custom template sets.
	TemplateStore TemplateStore
	// PresetStore provides access to tailoring presets and strategy instructions.
	PresetStore PresetStore
	// UserStore provides persistence access for multi-tenant user accounts.
	UserStore UserStore
	// ResumeExtractor provides PDF resume text parsing and structured fact extraction.
	ResumeExtractor worker.ResumeExtractor
	// DefaultUserID specifies the fallback user identifier when no auth header or token is provided.
	DefaultUserID string
	// Admin indicates local single-user mode bypassing external token verification.
	Admin bool
	// AuthProvider authenticates incoming requests across transports.
	AuthProvider auth.Provider
	// Logger provides structured diagnostic logging across MCP tool invocations and transports.
	Logger *slog.Logger
	// RESTTimeout specifies the maximum execution deadline for synchronous database and rendering operations.
	RESTTimeout time.Duration
	// TailoringTimeout specifies the execution deadline for asynchronous LLM resume tailoring jobs.
	TailoringTimeout time.Duration
	// SSEKeepAliveInterval controls how often MCP SSE streams emit keep-alive heartbeats.
	SSEKeepAliveInterval time.Duration
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
	return config.DefaultUserID
}

// withSkipTenantProvision returns a child context instructing EnsureTenant to skip lazy tenant provisioning.
func withSkipTenantProvision(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipTenantProvisionContextKey, true)
}

// skipTenantProvisionFromContext reports whether ctx suppresses lazy tenant provisioning.
func skipTenantProvisionFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	skip, ok := ctx.Value(skipTenantProvisionContextKey).(bool)
	return ok && skip
}

// isAdminBypass reports whether requests bypass external token verification in local admin mode.
func isAdminBypass(cfg ServerConfig) bool {
	return len(cfg.AuthTokens) == 0 && ((cfg.AuthProvider != nil && cfg.AuthProvider.Name() == auth.ProviderAdmin) || (cfg.AuthProvider == nil && cfg.Admin))
}

// isEffectiveAdmin reports whether ctx carries verified admin claims or cfg runs in local admin-bypass mode.
func isEffectiveAdmin(ctx context.Context, cfg ServerConfig) bool {
	return core.IsAdminFromContext(ctx) || isAdminBypass(cfg)
}

// setDefaults populates zero-value fields in ServerConfig with safe domain defaults.
func (c *ServerConfig) setDefaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.DefaultUserID == "" {
		c.DefaultUserID = config.DefaultUserID
	}
	if c.RESTTimeout <= 0 {
		c.RESTTimeout = config.DefaultRESTTimeout
	}
	if c.TailoringTimeout <= 0 {
		c.TailoringTimeout = config.DefaultLLMTimeout
	}
	if c.SSEKeepAliveInterval <= 0 {
		c.SSEKeepAliveInterval = config.DefaultMCPSSEKeepAliveInterval
	}
	if c.MaxConnectionsPerUser <= 0 {
		c.MaxConnectionsPerUser = config.DefaultMCPMaxConnsPerUser
	}
	if c.MaxTotalConnections <= 0 {
		c.MaxTotalConnections = config.DefaultMCPMaxTotalConns
	}
}

// EnsureTenant lazily provisions a tenant in the configured stores if unprovisioned,
// unless ctx suppresses lazy provisioning (such as HTTP admin-bypass X-User-ID requests).
// It ensures default profile, branch, and templates exist for the given user.
func (c *ServerConfig) EnsureTenant(ctx context.Context, userID string) error {
	if skipTenantProvisionFromContext(ctx) || userID == "" || c.ProfileStore == nil || c.BranchStore == nil || c.TemplateStore == nil {
		return nil
	}
	return seed.EnsureTenantProvisionedStores(ctx, c.ProfileStore, c.BranchStore, c.TemplateStore, c.UserStore, userID)
}
