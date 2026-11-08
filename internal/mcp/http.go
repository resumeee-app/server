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

	"resumeee/internal/domain"
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

// authenticateRequest extracts and verifies the client identity from the request's Authorization header,
// falling back to development headers or the configured default user ID.
//
// Parameters:
//   - r: Incoming *http.Request.
//   - cfg: ServerConfig providing AuthTokens dictionary and DefaultUserID fallback.
//
// Returns:
//   - Authenticated user ID string, or an error if authorization fails.
func authenticateRequest(r *http.Request, cfg ServerConfig) (string, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	var bearerToken string
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		bearerToken = strings.TrimSpace(authHeader[7:])
	}

	// 1. If explicit token dictionary configured, require valid Bearer token
	if len(cfg.AuthTokens) > 0 {
		if bearerToken == "" {
			return "", errors.New("missing or malformed Authorization Bearer header")
		}
		userID, ok := cfg.AuthTokens[bearerToken]
		if !ok || userID == "" {
			return "", errors.New("invalid authorization token")
		}
		return userID, nil
	}

	// 2. Multi-tenant mode (not Admin): strictly require token verification
	if !cfg.Admin {
		if cfg.TokenVerifier == nil {
			return "", domain.ErrUnauthorized
		}
		if bearerToken == "" {
			return "", errors.New("missing or malformed Authorization Bearer header")
		}
		tokenInfo, err := cfg.TokenVerifier.VerifyIDToken(r.Context(), bearerToken)
		if err != nil || tokenInfo == nil || tokenInfo.UID == "" {
			return "", domain.ErrUnauthorized
		}
		return tokenInfo.UID, nil
	}

	// 3. Local Admin Mode (cfg.Admin is true): X-User-ID -> DefaultUserID
	if uid := strings.TrimSpace(r.Header.Get("X-User-ID")); uid != "" {
		return uid, nil
	}

	if cfg.DefaultUserID != "" {
		return cfg.DefaultUserID, nil
	}

	return "default", nil
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

	var sessionOwners sync.Map
	limiter := newConnectionLimiter(cfg.MaxConnectionsPerUser, cfg.MaxTotalConnections)

	mcpSSE := serverSDK.NewSSEServer(
		mcpServer,
		serverSDK.WithStaticBasePath("/mcp"),
		serverSDK.WithMessageEndpoint("/messages"),
		serverSDK.WithSSEMaxRequestBodyBytes(10<<20),
		serverSDK.WithKeepAlive(true),
		serverSDK.WithKeepAliveInterval(15*time.Second),
		serverSDK.WithSessionIDGenerator(func(ctx context.Context, r *http.Request) (string, error) {
			sessionID := uuid.New().String()
			userID := UserIDFromContext(r.Context(), cfg.DefaultUserID)
			sessionOwners.Store(sessionID, userID)
			if h, ok := r.Context().Value(sessionHolderKey).(*sessionIDHolder); ok && h != nil {
				h.id = sessionID
			}
			return sessionID, nil
		}),
		serverSDK.WithSSEContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			userID := UserIDFromContext(r.Context(), cfg.DefaultUserID)
			return WithUserID(ctx, userID)
		}),
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Authenticate Request
		userID, err := authenticateRequest(r, cfg)
		if err != nil {
			http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
			return
		}
		if pErr := cfg.EnsureTenant(r.Context(), userID); pErr != nil {
			cfg.Logger.ErrorContext(r.Context(), "failed to lazily provision tenant", "user_id", userID, "err", pErr)
		}
		ctx := WithUserID(r.Context(), userID)
		r = r.WithContext(ctx)

		// 2. Handle SSE Stream: Connection Limiting & WriteDeadline Reset & Session Lifecycle Cleanup
		if r.Method == http.MethodGet && (r.URL.Path == "/mcp/sse" || strings.HasSuffix(r.URL.Path, "/sse")) {
			if !limiter.acquire(userID) {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			defer limiter.release(userID)

			holder := &sessionIDHolder{}
			r = r.WithContext(context.WithValue(r.Context(), sessionHolderKey, holder))
			defer func() {
				if holder.id != "" {
					sessionOwners.Delete(holder.id)
				}
			}()

			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Time{})

			mcpSSE.ServeHTTP(w, r)
			return
		}

		// 3. Handle Messages: 10MB Payload Limit & Session Ownership Validation
		if r.Method == http.MethodPost {
			// Enforce 10MB body boundary
			if r.ContentLength > 10<<20 {
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 10<<20)

			// Session hijacking defense
			sessionID := r.URL.Query().Get("sessionId")
			if sessionID != "" {
				ownerVal, loaded := sessionOwners.Load(sessionID)
				if loaded {
					if owner, ok := ownerVal.(string); ok && owner != userID {
						http.Error(w, "Forbidden: session belongs to another user", http.StatusForbidden)
						return
					}
				}
				// If not loaded, do NOT store spurious sessions into sessionOwners
			}
		}

		mcpSSE.ServeHTTP(w, r)
	})
}
