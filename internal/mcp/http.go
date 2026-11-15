package mcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	serverSDK "github.com/mark3labs/mcp-go/server"

	"github.com/resumeee-app/resumeee-api/auth"
	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
)

// connectionLimiter manages active SSE stream concurrency limits per user and across the system.
type connectionLimiter struct {
	mu           sync.Mutex
	userCounts   map[string]int
	totalCount   int
	maxPerUser   int
	maxTotalConn int
}

// newConnectionLimiter constructs a connectionLimiter with specified per-user and total concurrency bounds.
func newConnectionLimiter(maxPerUser, maxTotal int) *connectionLimiter {
	return &connectionLimiter{
		userCounts:   make(map[string]int),
		maxPerUser:   maxPerUser,
		maxTotalConn: maxTotal,
	}
}

// acquire attempts to reserve an SSE connection slot for the given userID.
// Returns true if the connection slot was acquired, or false if concurrency limits were exceeded.
func (l *connectionLimiter) acquire(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.totalCount >= l.maxTotalConn {
		return false
	}
	if l.userCounts[userID] >= l.maxPerUser {
		return false
	}

	l.userCounts[userID]++
	l.totalCount++
	return true
}

// release decrements the active connection counts for the given userID upon connection closure.
func (l *connectionLimiter) release(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.userCounts[userID] > 0 {
		l.userCounts[userID]--
		if l.userCounts[userID] == 0 {
			delete(l.userCounts, userID)
		}
	}
	if l.totalCount > 0 {
		l.totalCount--
	}
}

// sessionHolderKeyType is an unexported type used as a context key to pass session identifiers across handlers.
type sessionHolderKeyType struct{}

// sessionHolderKey is the context key used to associate a sessionIDHolder with an incoming SSE request context.
var sessionHolderKey = sessionHolderKeyType{}

// sessionIDHolder holds the dynamically generated SSE session ID across generator and handler scopes.
type sessionIDHolder struct {
	id string
}

// maxMCPRequestBodyBytes caps incoming MCP HTTP POST and SSE request bodies at 10 MiB.
const maxMCPRequestBodyBytes = 10 << 20

// withSessionHolder attaches holder to ctx so the SSE session generator can record the generated ID.
func withSessionHolder(ctx context.Context, holder *sessionIDHolder) context.Context {
	return context.WithValue(ctx, sessionHolderKey, holder)
}

// sessionHolderFromContext returns the active sessionIDHolder attached to ctx, if present.
func sessionHolderFromContext(ctx context.Context) *sessionIDHolder {
	holder, ok := ctx.Value(sessionHolderKey).(*sessionIDHolder)
	if !ok {
		return nil
	}
	return holder
}

// sessionRegistry tracks the authenticated owner of each active MCP SSE session.
type sessionRegistry struct {
	mu     sync.RWMutex
	owners map[string]string
}

// newSessionRegistry constructs an empty sessionRegistry.
func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{
		owners: make(map[string]string),
	}
}

// bind records userID as the owner of sessionID.
func (sr *sessionRegistry) bind(sessionID, userID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.owners[sessionID] = userID
}

// owner returns the owning userID for sessionID and whether the session is registered.
func (sr *sessionRegistry) owner(sessionID string) (string, bool) {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	userID, ok := sr.owners[sessionID]
	return userID, ok
}

// delete removes sessionID from the registry once its SSE connection closes.
func (sr *sessionRegistry) delete(sessionID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	delete(sr.owners, sessionID)
}

// authenticateRequest extracts and verifies the client identity from the request's Authorization header,
// falling back to development headers or the configured default user ID in local admin mode.
//
// Parameters:
//   - r: Incoming *http.Request.
//   - cfg: ServerConfig providing AuthTokens dictionary, AuthProvider, and DefaultUserID fallback.
//
// Returns:
//   - Authenticated *core.TokenInfo, or an error if authorization fails.
func authenticateRequest(r *http.Request, cfg ServerConfig) (*core.TokenInfo, error) {
	bearerToken, _ := auth.ExtractBearerToken(r)

	// 1. If explicit token dictionary configured, require valid Bearer token
	if len(cfg.AuthTokens) > 0 {
		if bearerToken == "" {
			return nil, errors.New("missing or malformed Authorization Bearer header")
		}
		userID, ok := cfg.AuthTokens[bearerToken]
		if !ok || userID == "" {
			return nil, errors.New("invalid authorization token")
		}
		return &core.TokenInfo{UID: userID}, nil
	}

	// 2. AuthProvider if configured
	if cfg.AuthProvider != nil {
		tokenInfo, err := cfg.AuthProvider.AuthenticateRequest(r)
		if err != nil || tokenInfo == nil || tokenInfo.UID == "" {
			if cfg.AuthProvider.Name() != auth.ProviderAdmin && err != nil && (errors.Is(err, auth.ErrMissingBearerHeader) || errors.Is(err, auth.ErrEmptyBearerToken)) {
				return nil, errors.New("missing or malformed Authorization Bearer header")
			}
			return nil, core.ErrUnauthorized
		}
		return tokenInfo, nil
	}

	// 3. Multi-tenant mode (not Admin): strictly require an AuthProvider or AuthTokens
	if !cfg.Admin {
		return nil, core.ErrUnauthorized
	}

	// 4. Local Admin Mode (cfg.Admin is true): X-User-ID -> DefaultUserID
	uid := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if uid == "" {
		uid = cfg.DefaultUserID
	}
	if uid == "" {
		uid = config.DefaultUserID
	}
	return &core.TokenInfo{
		UID:   uid,
		Admin: true,
		Role:  string(core.UserRoleAdmin),
	}, nil
}

// NewHTTPHandler constructs an http.Handler wrapping the MCP SSEServer with authentication,
// session hijacking defense, 10MB payload body bounding, and active connection limits.
//
// Parameters:
//   - mcpServer: Core *serverSDK.MCPServer with registered tools, resources, and prompts.
//   - cfg: ServerConfig containing connection limits, auth tokens, and timeouts.
//
// Returns:
//   - Configured http.Handler ready to mount on an HTTP router.
func NewHTTPHandler(mcpServer *serverSDK.MCPServer, cfg ServerConfig) http.Handler {
	cfg.setDefaults()

	sessions := newSessionRegistry()
	limiter := newConnectionLimiter(cfg.MaxConnectionsPerUser, cfg.MaxTotalConnections)
	mcpSSE := newSSEServer(mcpServer, cfg, sessions)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenInfo, err := authenticateRequest(r, cfg)
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		userID := tokenInfo.UID
		ctx := WithUserID(r.Context(), userID)
		ctx = core.WithIsAdmin(ctx, tokenInfo.IsAdmin())
		if isAdminBypass(cfg) {
			ctx = withSkipTenantProvision(ctx)
		} else if pErr := cfg.EnsureTenant(ctx, userID); pErr != nil {
			cfg.Logger.ErrorContext(ctx, "failed to lazily provision tenant", "user_id", userID, "err", pErr)
			http.Error(w, "internal server error provisioning tenant", http.StatusInternalServerError)
			return
		}
		r = r.WithContext(ctx)

		if r.Method == http.MethodGet && (r.URL.Path == "/mcp/sse" || strings.HasSuffix(r.URL.Path, "/sse")) {
			serveSSERequest(w, r, userID, limiter, sessions, mcpSSE)
			return
		}

		if r.Method == http.MethodPost {
			if !validatePostMessage(w, r, userID, sessions) {
				return
			}
		}

		mcpSSE.ServeHTTP(w, r)
	})
}

// newSSEServer constructs the underlying MCP SSEServer wired to session ownership tracking and tenant context.
func newSSEServer(mcpServer *serverSDK.MCPServer, cfg ServerConfig, sessions *sessionRegistry) *serverSDK.SSEServer {
	return serverSDK.NewSSEServer(
		mcpServer,
		serverSDK.WithStaticBasePath("/mcp"),
		serverSDK.WithMessageEndpoint("/messages"),
		serverSDK.WithSSEMaxRequestBodyBytes(maxMCPRequestBodyBytes),
		serverSDK.WithKeepAlive(true),
		serverSDK.WithKeepAliveInterval(cfg.SSEKeepAliveInterval),
		serverSDK.WithSessionIDGenerator(func(_ context.Context, r *http.Request) (string, error) {
			sessionID := uuid.New().String()
			userID := UserIDFromContext(r.Context(), cfg.DefaultUserID)
			sessions.bind(sessionID, userID)
			if h := sessionHolderFromContext(r.Context()); h != nil {
				h.id = sessionID
			}
			return sessionID, nil
		}),
		serverSDK.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			userID := UserIDFromContext(r.Context(), cfg.DefaultUserID)
			ctx = WithUserID(ctx, userID)
			ctx = core.WithIsAdmin(ctx, core.IsAdminFromContext(r.Context()))
			if isAdminBypass(cfg) || skipTenantProvisionFromContext(r.Context()) {
				ctx = withSkipTenantProvision(ctx)
			}
			return ctx
		}),
	)
}

// serveSSERequest enforces SSE connection limits, registers cleanup of the session owner, and streams SSE events.
func serveSSERequest(w http.ResponseWriter, r *http.Request, userID string, limiter *connectionLimiter, sessions *sessionRegistry, mcpSSE *serverSDK.SSEServer) {
	if !limiter.acquire(userID) {
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		return
	}
	defer limiter.release(userID)

	holder := &sessionIDHolder{}
	r = r.WithContext(withSessionHolder(r.Context(), holder))
	defer func() {
		if holder.id != "" {
			sessions.delete(holder.id)
		}
	}()

	// Clear write deadlines so long-lived SSE streams are not interrupted by server HTTP timeouts.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	mcpSSE.ServeHTTP(w, r)
}

// validatePostMessage enforces POST body size limits and verifies that any targeted session belongs to userID.
func validatePostMessage(w http.ResponseWriter, r *http.Request, userID string, sessions *sessionRegistry) bool {
	if r.ContentLength > maxMCPRequestBodyBytes {
		http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBodyBytes)
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		return true
	}
	if owner, ok := sessions.owner(sessionID); ok && owner != userID {
		http.Error(w, "Forbidden: session belongs to another user", http.StatusForbidden)
		return false
	}
	return true
}
