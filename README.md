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
- `internal/pdf/` — PDF text extraction utility.
- `api/openapi.yaml` — Canonical OpenAPI 3.1 contract.

---

## Programmatic Usage (`server` Package)

External callers can import `github.com/resumeee-app/resumeee-api/server` to customize configuration, storage backends, authentication providers, and LLM transport clients:

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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := server.Run(ctx,
		server.WithEnvConfig(),
		server.WithFlags(os.Args[1:]...),
		server.WithPort(8080),
		server.WithAdminMode(true),
		server.WithQuotaLimits(100, 2_000_000),
		server.WithWorkerPoolConfig(4, 200, 3),
		server.WithTimeouts(30*time.Second, 10*time.Minute),
		// Inject custom dependencies as needed:
		// server.WithStore(customStore),
		// server.WithAuthProvider(customAuthProvider),
		// server.WithLLMClient(customLLMClient),
	)
	if err != nil {
		log.Fatal(err)
	}
}
```
