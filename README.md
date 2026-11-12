# resumeee-api

REST API and Model Context Protocol (MCP) server for **Resumeee** (`github.com/resumeee-app/resumeee-api`).

## Overview

`resumeee-api` is a thin transport and dependency-injection layer built on top of `resumeee-core`, `resumeee-storage-sql`, and `resumeee-auth-firebase`:

- `server/` — Public Go package (`github.com/resumeee-app/resumeee-api/server`) for embedding, configuring, and launching customized Resumeee servers with functional `With<Option>` constructors.
- `cmd/server/` — Default unified HTTP server binary serving REST `/api/v1/*` and MCP `/mcp/sse` + `/mcp/message` endpoints.
- `cmd/mcp/` — Standalone MCP CLI binary supporting `stdio` transport for desktop AI assistants (Claude Desktop, Cursor, Antigravity).
- `internal/api/` — HTTP handlers, routers, and authentication/timeout middleware.
- `internal/mcp/` — MCP tools (`get_main_profile`, `add_career_fact`, `list_branches`, `create_tailored_branch`, `trigger_tailoring`, `get_tailored_resume`, `extract_resume`, etc.) and SSE/stdio handlers.
- `internal/config/` — Environment and CLI flag configuration.
- `api/openapi.yaml` — Canonical OpenAPI 3.1 contract.

---

## Programmatic Usage (`server` Package)

External callers configure storage, authentication, and LLM providers in their respective packages and inject them into `github.com/resumeee-app/resumeee-api/server`:

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/resumeee-app/resumeee-api/server"
	auth "github.com/resumeee-app/resumeee-auth-firebase"
	"github.com/resumeee-app/resumeee-core/llm"
	"github.com/resumeee-app/resumeee-storage-sql/sql"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 1. Configure storage in resumeee-storage-sql
	store, err := sql.OpenWithOptions(ctx,
		sql.WithDriver("sqlite"),
		sql.WithDBPath("resumeee.db"),
		sql.WithAutoMigrate(true),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	// 2. Configure authentication in resumeee-auth-firebase
	authProvider := auth.NewFirebaseAuthProvider("my-firebase-project")

	// 3. Configure LLM provider(s) in resumeee-core/llm
	gemini := llm.NewGeminiProvider(
		os.Getenv("GEMINI_API_KEY"),
		llm.WithDefaultModel("gemini-2.5-flash"),
	)

	// 4. Inject configured dependencies into resumeee-api/server
	err = server.Run(ctx,
		server.WithStore(store),
		server.WithAuthProvider(authProvider),
		server.WithLLMClient(gemini),
		server.WithPort(8080),
		server.WithQuotaLimits(100, 2_000_000),
		server.WithWorkerPoolConfig(4, 200, 3),
		server.WithTimeouts(30*time.Second, 10*time.Minute),
	)
	if err != nil {
		log.Fatal(err)
	}
}
```
