# resumeee-api

REST API and Model Context Protocol (MCP) server for **Resumeee** (`github.com/resumeee-app/resumeee-api`).

## Overview

`resumeee-api` is a thin transport and dependency-injection layer built on top of `resumeee-core`, `resumeee-llm`, `resumeee-storage-sql`, and `resumeee-auth-firebase`:

- `cmd/server/` — Unified HTTP server daemon serving REST `/api/v1/*` and MCP `/mcp/sse` + `/mcp/message` endpoints.
- `cmd/mcp/` — Standalone MCP CLI server supporting `stdio` transport for desktop AI assistants (Claude Desktop, Cursor, Antigravity).
- `internal/api/` — HTTP handlers, routers, and authentication/timeout middleware.
- `internal/mcp/` — MCP tools (`get_main_profile`, `add_career_fact`, `list_branches`, `create_tailored_branch`, `trigger_tailoring`, `get_tailored_resume`, `extract_resume`, etc.) and SSE/stdio handlers.
- `internal/config/` — Environment and CLI flag configuration.
- `internal/pdf/` — PDF text extraction utility.
- `api/openapi.yaml` — Canonical OpenAPI 3.1 contract.
