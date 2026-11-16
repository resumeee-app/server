# resumeee-api

REST API and Model Context Protocol (MCP) server for **Resumeee** (`github.com/resumeee-app/resumeee-api`).

## Overview

`resumeee-api` is a thin transport and dependency-injection layer built on top of `resumeee-core`, `resumeee-storage-sql`, and `resumeee-auth-firebase`:

- `server/` — Public Go package (`github.com/resumeee-app/resumeee-api/server`) for embedding, configuring, and launching customized Resumeee servers with functional `With<Option>` constructors.
- `auth/` — Public Go package (`github.com/resumeee-app/resumeee-api/auth`) providing request authentication providers and multi-scheme registry resolution.
- `cmd/server/` — Default unified HTTP server binary serving REST `/api/v1/*` and MCP `/mcp/sse` + `/mcp/messages` endpoints.
- `cmd/mcp/` — Standalone MCP CLI binary supporting `stdio` transport for desktop AI assistants (Claude Desktop, Cursor, Antigravity).
- `api/` — OpenAPI 3.1 contract (`api/openapi.yaml`) and repository-wide guardrail tests (`godoc_test.go`, `gofmt_test.go`, `import_boundary_test.go`, `interface_assertion_test.go`, `openapi_test.go`).
- `internal/access/` — Shared tenant ownership, system-resource, and store availability guards.
- `internal/api/` — HTTP handlers, routers, and authentication/timeout middleware.
- `internal/mcp/` — MCP tools (`get_main_profile`, `add_experience`, `add_achievement_bullet`, `list_branches`, `create_branch`, `tailor_resume`, `render_resume`, `run_gap_analysis`, `simulate_ats`, etc.) and SSE/stdio handlers.
- `internal/config/` — Environment and CLI flag configuration.

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
	"github.com/resumeee-app/resumeee-core/llm/gemini"
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
	authProvider := auth.NewFirebaseTokenVerifier("my-firebase-project")

	// 3. Configure LLM provider(s) in resumeee-core/llm
	geminiClient := gemini.New(
		os.Getenv("GEMINI_API_KEY"),
		gemini.WithDefaultModel("gemini-2.5-flash"),
	)

	// 4. Inject configured dependencies into resumeee-api/server
	err = server.Run(ctx,
		server.WithStore(store),
		server.WithAuthProvider(authProvider),
		server.WithLLMClient(geminiClient),
		server.WithPort(8080),
		server.WithQuotaLimits(100, 2_000_000),
		server.WithWorkerPoolConfig(4, 3),
		server.WithTimeouts(30*time.Second, 10*time.Minute),
	)
	if err != nil {
		log.Fatal(err)
	}
}
```

---

## Quality Gates

```bash
# Run all formatting, test (-race), guardrail, and golangci-lint checks
make verify

# Format check (gofmt)
test -z "$(gofmt -l .)"

# Unit & race detection tests (includes TestGoFmtCompliance and AST guardrails)
go test -race -v ./...

# Check formatting & static analysis
golangci-lint run ./...
```

---

## Running Binaries & Docker

Requires **Go 1.27+**. Copy `.env.example` to `.env` (or export environment variables) before starting:

```bash
cp .env.example .env

# Run unified REST + HTTP/SSE MCP server
go run ./cmd/server

# Run standalone stdio MCP server
go run ./cmd/mcp
```

See [`.env.example`](./.env.example) for configuration variables (`PORT`, `DB_PATH`, `ADMIN_MODE`, `FIREBASE_PROJECT_ID`, `GEMINI_API_KEY`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENAI_COMPAT_BASE_URL`).

`Dockerfile` expects sibling build contexts when building from the umbrella workspace:

```bash
docker build \
  --build-context resumeee-core=../resumeee-core \
  --build-context resumeee-storage-sql=../resumeee-storage-sql \
  --build-context resumeee-auth-firebase=../resumeee-auth-firebase \
  -t resumeee-api .
```

---

## License

GNU Affero General Public License v3.0 (AGPL-3.0). See [LICENSE](./LICENSE).


