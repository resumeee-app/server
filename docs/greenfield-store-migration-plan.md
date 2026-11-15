# Greenfield Store Architecture Migration Plan (`resumeee-api`)

## 1. Purpose & Context

This document specifies the migration required in `resumeee-api` to adopt the zero-backwards-compatibility greenfield persistence redesign across the workspace:

- **`resumeee-core` PR #26** *(merged into `main`)*: https://github.com/resumeee-app/resumeee-core/pull/26 (commit `21d6fa985d331313cf0b8fd65c0d27dfa968fce9`)
- **`resumeee-storage-sql` PR #4** *(merged into `main`)*: https://github.com/resumeee-app/resumeee-storage-sql/pull/4 (commit `421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba`; related Issue #5)

Under the new architecture, storage adapters (`resumeee-storage-*`, `memory`) implement only durable storage, atomicity, and transaction primitives via `store.Backend`, `store.ReadTx`, and `store.Tx`, while all persistence domain logic, normalization, quota coordination, job leasing, and retry/cancellation semantics live in `resumeee-core/store`, `resumeee-core/branch`, and `resumeee-core/worker`.

### Execution Order & Module Pinning

`resumeee-core` PR #26 and `resumeee-storage-sql` PR #4 are already merged into `main`. Because `resumeee-storage-sql`'s `go.mod` still references an earlier `resumeee-core` pseudo-version, pin **both** merged `origin/main` commits explicitly in `resumeee-api` using `GOWORK=off` and `GOPRIVATE=github.com/resumeee-app/*`:

```bash
GOWORK=off GOPRIVATE=github.com/resumeee-app/* go get \
  github.com/resumeee-app/resumeee-core@21d6fa985d331313cf0b8fd65c0d27dfa968fce9 \
  github.com/resumeee-app/resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba
GOWORK=off go mod tidy
```

> **Note on `go.work`**: If using a parent `go.work` checkout, all sibling repos (`resumeee-core`, `resumeee-storage-sql`, `resumeee-api`) must be on compatible commits together; otherwise run Go commands with `GOWORK=off`.
> **Note on `auth/*`**: `resumeee-auth-firebase` and `resumeee-api/auth/*` rely only on `core.TokenVerifier`, `core.TokenInfo`, and `core.AuthProvider`, which remain intact in root `core`.

---

## 2. Architectural Shift

1. **Composite `*store.Store` replaces `core.Store` / `core.StoreBundle` / `core.*Store`**:
   - Root package `core` no longer defines storage interfaces (`Store`, `StoreBundle`, `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `PDFJobStore`, `QuotaStore`, `UserStore`).
   - `sql.Open`, `sql.OpenWithOptions`, `sql.OpenStore`, and `memory.New`, `memory.NewWithClock`, `memory.NewSeeded` all return concrete `*store.Store` values from `github.com/resumeee-app/resumeee-core/store`.
   - Sub-stores are exposed via concrete accessors on `*store.Store`: `Users()`, `Presets()`, `Templates()`, `Quotas()`, `Profiles()`, `Career()`, `Branches()`, `Jobs()`, plus `Close() error`.
2. **Consumer-Side Interfaces (AST Guardrail Compliant)**:
   - `api/import_boundary_test.go` prohibits importing `resumeee-storage-sql` outside `cmd/*`, and `api/interface_assertion_test.go` prohibits runtime interface assertions (`v.(T)`).
   - `internal/access`, `internal/api`, and `internal/mcp` must define small, consumer-side interfaces satisfied directly by `*store.Store` sub-stores (or shared service interfaces such as `worker.JobStore`, `branch.JobStore`, `seed.ProfileSeeder`, `seed.BranchSeeder`, `seed.TemplateSeeder`, `seed.UserSeeder`, `core.JobNotifier`).
3. **Unified Durable Job Queue replaces Channel Queues & Inline Leases**:
   - `core.Job` (`Kind core.JobKind`, `Status core.JobStatus`: `pending`, `processing`, `completed`, `failed`, `cancelled`) stored via `*store.Jobs` replaces `core.PDFParseJob`, `core.PDFParseJobTask`, `core.PDFParseJobStatus`, `core.PDFJobEnqueuer`, `branch.JobEnqueuer`, and inline lease columns on `core.BranchVersion`.
   - `internal/api/queue.go` (`ChannelQueue`, `ChannelJobEnqueuer`, `ChannelPDFJobEnqueuer`) is deleted completely.
   - `*worker.Pool` implements `core.JobNotifier` (`Notify()`) and polls/claims jobs directly from `store.Jobs()`.
   - Crash recovery is unified through `(*worker.Pool).RecoverStaleJobs(ctx, reason)` (which calls `p.jobs.RecoverInterrupted` and refunds reservations), replacing `branchStore.FailInterruptedVersions(...)`.
4. **Greenfield Domain Model Cleanup**:
   - `core.Branch`: removed legacy fields `CustomDirectives` and `TailoringStrategyID` (canonical fields are `BranchInstructions` and `PresetID`); added `Epoch int64` (`json:"epoch"`). Note `TemplateSetID` remains on `core.Branch`.
   - `core.BranchVersion`: removed `ResumeID` and inline lease fields; added `BranchEpoch` and `JobID`.
   - `core.Preset`: removed legacy field `TemplateSetID` (`SuggestedThemeID *string` is canonical).
   - `branch.Store`: removed `CreateVersion`, `CompleteVersion`, `UpdateVersionStatus`, and `FailInterruptedVersions` (retains `SaveCompletedVersion`, `SetVersionGapReport`, `SetVersionATSScore`, CRUD, and version reads).
   - `core.ErrPendingLimitExceeded` wraps `core.ErrQueueFull`; `core.ErrJobExpired` and `core.ErrJobCancelled` wrap `core.ErrPermanentFailure`; `core.ErrLeaseExpired` wraps `core.ErrConflict`. Existing HTTP `errors.Is(err, core.ErrQueueFull)` -> `503` mapping continues to match tenant admission limits cleanly.

---

## 3. Upstream Symbol Mapping

| Removed / Changed Symbol | Replacement in Greenfield Architecture |
|---|---|
| `core.Store`, `core.StoreBundle` | `*store.Store` (`store.New`, `memory.New`, `sql.Open*`) |
| `core.Normalizable` | Removed (normalization handled internally by `store`) |
| `core.ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `QuotaStore`, `UserStore` | Consumer-defined interfaces satisfied by `*store.Profiles`, `*store.Career`, `*store.Presets`, `*store.Templates`, `*store.Branches`, `*store.Quotas`, `*store.Users` |
| `core.PDFJobStore`, `core.PDFParseJob`, `core.PDFParseJobTask`, `core.PDFParseJobStatus`, `core.PDFJobEnqueuer` | `*store.Jobs`, `*core.Job`, `core.JobKind`, `core.JobStatus`, `worker.SubmitPDFJob`, `worker.CancelPDFJob`, `core.JobNotifier` |
| `branch.Config.JobEnqueuer`, `branch.JobEnqueuer` | `branch.Config.Jobs` (`branch.JobStore`), `branch.Config.JobNotifier` (`core.JobNotifier`), `MaxPendingPerTenant`, `JobTTL`, `CleanupTimeout`, `Clock` |
| `(*branch.Service)` cancel support | `(*branch.Service).CancelVersion(ctx, userID, branchID, versionID)` |
| `branch.Store.{CreateVersion,CompleteVersion,UpdateVersionStatus,FailInterruptedVersions}` | Removed; async versions are created/completed atomically via `store.Jobs` + `worker.Pool`, recovered via `(*worker.Pool).RecoverStaleJobs`; synchronous tests/renders use `SaveCompletedVersion` |
| `worker.Config.{QueueSize,PDFJobs}` | Removed; pass `Jobs` (`worker.JobStore`), `MaxConcurrentPerTenant`, `PollInterval`, `SweepInterval`, `LeaseDuration`, `LeaseRenewInterval`, `MaxAttempts`, `RetryBaseDelay`, `MaxRetryDelay`, `ShutdownTimeout`, `CleanupTimeout`, `WorkerID`, `Clock` |
| `core.Branch.{CustomDirectives,TailoringStrategyID}` | `core.Branch.{BranchInstructions,PresetID}` (+ new `Epoch int64`) |
| `core.BranchVersion.ResumeID` | `core.BranchVersion.BranchID` (+ new `BranchEpoch`, `JobID`) |
| `core.Preset.TemplateSetID` | `core.Preset.SuggestedThemeID` |
| `seed.EnsureTenantProvisioned` | `seed.EnsureTenantProvisioned(ctx, st *store.Store, userID, userInfo...)` / `seed.EnsureTenantProvisionedStores` |

---

## 4. File-by-File Migration Breakdown

### Phase 1: Dependencies & Configuration

- **`go.mod` / `go.sum`**
  - Pin `resumeee-core@21d6fa985d331313cf0b8fd65c0d27dfa968fce9` and `resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba` and run `GOWORK=off go mod tidy`.
- **`internal/config/config.go` (`28`, `75-76`, `168`, `242`)**
  - Remove `WorkerQueueSize`, `DefaultWorkerQueueSize`, and the `WORKER_QUEUE_SIZE` env binding, since durable admission control (`MaxPendingPerTenant` on `branch.Config` / `worker.PDFSubmitConfig`) replaces bounded Go channels.
  - **Decision**: Rely on `branch`, `worker`, and `core` built-in durable defaults (`core.DefaultMaxPendingPerTenant = 25`, `core.DefaultJobTTL = 30m`, etc.) rather than adding new worker/queue env vars to `config.Config`. Add `MaxPendingPerTenant int` on `api.RouterConfig` (`>0` forwarded to `branch.Config.MaxPendingPerTenant` and `worker.PDFSubmitConfig.MaxPendingPerTenant`) for programmatic and test admission-limit control.
- **`.env.example` (`81`), `Dockerfile` (`92`), `README.md` (`47`, `73`)**
  - Remove `WORKER_QUEUE_SIZE` documentation and defaults.

### Phase 2: Server Composition & Binary Entrypoints

- **`server/options.go` (`94`, `190`, `297-306`)**
  - Change `WithStore(store core.Store)` to `WithStore(store *store.Store)`.
  - Update `Config` and `WithWorkerPoolConfig(concurrency, maxPerTenant int)` to drop `WorkerQueueSize` / `queueSize`.
- **`server/server.go` (`4`, `52`, `80`, `112`, `144`, `206`, `241`, `262`, `280-294`, `349-433`)**
  - Change `Server.store` from `core.Store` to `*store.Store` and update log/error/doc references.
  - In startup recovery (`line 262`), remove `branchStore.FailInterruptedVersions(...)`; retain `workerPool.RecoverStaleJobs(ctx, "server restarted while generation job was in flight")` so both branch tailoring and PDF parse jobs are interrupted and quota reservations refunded in one atomic path.
  - Construct `worker.Pool` with `Jobs: s.store.Jobs()` (plus `Branches`, `Profiles`, `Presets`, `Templates`, `Quotas`) instead of `QueueSize` / `PDFJobs`.
  - Construct `branch.Service` with `Jobs: s.store.Jobs()` and `JobNotifier: workerPool` instead of `JobEnqueuer: workerPool`.
  - Update `api.NewRouterConfigFromStore(s.store)` + `JobNotifier: deps.workerPool`, and pass `*store.Store` sub-stores + `BranchService` (`*branch.Service`, which holds `Jobs: s.store.Jobs()` and `JobNotifier: deps.workerPool`) to `mcp.ServerConfig`.
- **`cmd/server/main.go` (`23-40`, `91`, `138`)**
  - Change `StoreInitializer`, `defaultSQLiteInitializer`, and `closeStore` from `core.Store` to `*store.Store`.
  - Remove `WorkerQueueSize: cfg.WorkerQueueSize` from `server.Config` literal.
- **`cmd/mcp/main.go` (`31-34`, `152`, `178-189`, `267`)**
  - Change `StoreInitializer`, `defaultSQLiteInitializer`, `buildMCPServerConfig`, and `closeMCPStore` from `core.Store` to `*store.Store`.
  - Remove `queueSize` / `QueueSize` in `worker.Config`; pass `Jobs: st.Jobs()` to `worker.New` and `Jobs: st.Jobs(), JobNotifier: pool` to `branch.New`.
  - Call `pool.RecoverStaleJobs(ctx, "mcp server restarted during job processing")` before `pool.Start(ctx)` on standalone MCP server startup so interrupted jobs and reserved quotas are recovered cleanly, matching `server/server.go`.

### Phase 3: Consumer-Side Store Interfaces & Queue Removal

> **File naming convention (`AGENTS.md`)**: Group consumer interfaces into domain-named `store.go` files (`internal/api/store.go`, `internal/mcp/store.go`) and `internal/access/access.go` rather than creating catch-all `interfaces.go` files (enforced by `api/godoc_test.go`).

- **Delete `internal/api/queue.go` and `internal/api/queue_test.go`**
  - Remove `ChannelQueue`, `ChannelJobEnqueuer`, `ChannelPDFJobEnqueuer`, and their channel queue tests completely.
- **`internal/access/access.go` (`50`, `62`)**
  - Define consumer reader interfaces `TemplateReader` (`Template`) and `PresetReader` (`Preset`) and replace `core.TemplateStore` and `core.PresetStore` parameters on `Template(...)` and `Preset(...)`.
- **`internal/api/provisioner.go` (`13-37`) & `internal/api/middleware.go` (`195-261`)**
  - Replace `core.ProfileStore`, `core.BranchStore`, `core.TemplateStore`, `core.UserStore` fields in `provisioner.go` with narrow consumer interfaces matching `seed.ProfileSeeder`, `seed.BranchSeeder`, `seed.TemplateSeeder`, `seed.UserSeeder` so `seed.EnsureTenantProvisionedStores` compiles cleanly without root `core` store interfaces.
  - Keep `internal/api/middleware.go` constructing `*core.User` for `Provisioner.EnsureProvisioned`, updating doc comments/checks as needed for `*store.Store` sub-stores.
- **`internal/api/store.go` *(new)* & `internal/api/api.go` (`51-58`, `65`, `71`, `86-87`, `171`)**
  - Define domain consumer interfaces in `internal/api/store.go`: `ProfileStore` (compatible with `profile.Store` / `*store.Profiles`), `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore` (embedding `branch.Store`), `JobStore` (embedding `branch.JobStore` and `worker.JobStore`), `QuotaStore`, and `UserStore`.
  - Replace `core.*Store`, `PDFJobStore`, `PDFJobEnqueuer`, and `JobEnqueuer` fields on `RouterConfig` with the consumer interfaces from `store.go`, `JobStore`, `JobNotifier core.JobNotifier`, and `MaxPendingPerTenant int`.
  - Update `NewRouterConfigFromStore(st *store.Store)` to populate every sub-store field from `st.Profiles()`, `st.Career()`, `st.Presets()`, `st.Templates()`, `st.Branches()`, `st.Jobs()`, `st.Quotas()`, `st.Users()`.
  - Update `branch.Config` wiring (`line 171`) to pass `Jobs: cfg.JobStore`, `JobNotifier: cfg.JobNotifier`, and `MaxPendingPerTenant: cfg.MaxPendingPerTenant`.
- **`internal/mcp/store.go` *(new)* & `internal/mcp/config.go` (`39-53`)**
  - Define narrow MCP consumer interfaces in `internal/mcp/store.go`: `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, and `UserStore`.
  - Replace `core.*Store` fields on `ServerConfig` with those consumer interfaces and remove `JobEnqueuer branch.JobEnqueuer` (`MCP` triggers branch tailoring through `BranchService`, which already owns `Jobs` and `JobNotifier`).

### Phase 4: REST Handlers, MCP Tools & OpenAPI Contracts

- **`internal/api/profile_handlers.go` (`25`, `34-36`) & `internal/api/pdf_handlers.go` (`36`, `377-423`)**
  - Update route guards (`cfg.PDFJobStore != nil` -> `cfg.JobStore != nil`).
  - Keep sync-by-default behavior when `JobNotifier == nil` via `runAsync := strings.EqualFold(r.URL.Query().Get("async"), "true") || (h.cfg.JobNotifier != nil && !strings.EqualFold(r.URL.Query().Get("sync"), "true"))`.
  - In `enqueueAsyncPDFJob`: guard `h.cfg.JobStore == nil` -> HTTP `500` (`"pdf job store unavailable"`), then `h.cfg.JobNotifier == nil` -> HTTP `503` (`"PDF worker queue not configured on server"`), then call `worker.SubmitPDFJob(r.Context(), worker.PDFSubmitConfig{Jobs: h.cfg.JobStore, Quotas: h.cfg.QuotaStore, Notifier: h.cfg.JobNotifier, MaxPendingPerTenant: h.cfg.MaxPendingPerTenant}, worker.PDFJobSubmission{...})`.
  - Define `PDFParseJobResponse` and `newPDFParseJobResponse(job *core.Job) (*PDFParseJobResponse, bool)` in `internal/api/pdf_handlers.go` to preserve the flat HTTP response schema (`id`, `user_id`, `status`, `filename`, `provider`, `model`, `result`, `error_message`, `created_at`, `updated_at`, `completed_at`), verifying `job != nil && job.Kind == core.JobKindPDFParse && job.PDF != nil` (returning `404` in `getPDFParseJob` for missing or non-PDF jobs) and never exposing `PDF.RawText`, `ReservationID`, or `ClaimedBy`.
  - **Deferred scope**: `/cancel` REST endpoints (`POST .../cancel`) and MCP cancel tools are deferred to a follow-up PR so this migration preserves the 36 existing endpoints verified by `api/openapi_test.go`; embedding `branch.JobStore` and `worker.JobStore` in `api.JobStore` keeps the persistence contract cancel-ready.
- **`internal/api/branch_handlers.go` (`64-85`, `144-183`, `232-235`, `330-349`), `internal/api/response.go` (`140-210`), & `internal/mcp/branch_tools.go` (`308-310`, `409-443`, `503-504`)**
  - Remove all assignments and reads against deleted `core.Branch.CustomDirectives` and `core.Branch.TailoringStrategyID` and delete dead helper `firstNonNilStringPtr` in `branch_handlers.go`.
  - **Greenfield HTTP/MCP wire decision**: drop legacy `custom_directives` and `tailoring_strategy_id` request/response fields and MCP tool arguments (`create_branch`, `update_branch`, `preview_tailor_prompt`, and `PromptPreviewRequest`), standardizing on `branch_instructions` and `preset_id`. Expose `epoch` on `Branch` responses and `branch_epoch` / `job_id` on `BranchVersion` responses. Keep `TemplateSetID` (`template_set_id`) on `core.Branch`, `render_resume`, `RenderPreviewRequest`, and `/templates`.
- **`internal/api/version_handlers.go` (`418`)**
  - Replace deleted `ver.ResumeID` with `ver.BranchID` in `loadRenderableVersion` ownership verification (`ver.BranchID != b.ID`).
  - Verify `errors.Is(err, core.ErrQueueFull)` (`line 61`) continues mapping `core.ErrPendingLimitExceeded` -> HTTP `503`.
- **`internal/api/preset_handlers.go` (`42`, `52`, `98-131`, `226-235`) & `internal/mcp/preset_tools.go` (`156`, `224`)**
  - Remove all reads/writes against deleted `core.Preset.TemplateSetID`.
  - Drop `template_set_id` from `Preset` / Strategy HTTP request/response structs and MCP `create_preset` / `update_preset` tool schemas & normalization in favor of canonical `suggested_theme_id` (`SuggestedThemeID *string`).
- **`api/openapi.yaml` & `api/openapi_test.go`**
  - Update `VersionStatus` and `PDFParseJobStatus` enums to include `cancelled`.
  - Update `Branch` / `CreateBranchRequest` / `UpdateBranchRequest` / `PromptPreviewRequest` schemas (`-custom_directives`, `-tailoring_strategy_id`, `+epoch` on `Branch`).
  - Update `BranchVersion` schema (`+branch_epoch`, `+job_id`).
  - Update `Preset` / `CreatePresetRequest` / `UpdatePresetRequest` / `TailoringStrategy` / `CreateStrategyRequest` / `UpdateStrategyRequest` schemas (`-template_set_id`; keep `suggested_theme_id` canonical).
  - Keep `PDFParseJob` flat HTTP fields (`filename`, `provider`, `model`, `result`) backed by `PDFParseJobResponse`, and confirm all 36 endpoints pass `api/openapi_test.go`.

### Phase 5: Test Suite & Mock Updates

Every test file below fails compilation or assertions until updated:

- **`cmd/server/main_test.go` (`46`, `75-137`, `488`, `511`)**: `core.Store` -> `*store.Store`; replace direct `memStore.<Method>` calls with `memStore.<SubStore>().<Method>`; replace `CreateVersion` + `UpdateVersionStatus` in `TestRecoverStaleJobs` with `memStore.Jobs().SubmitBranchTailor` + `ClaimNext` before `RecoverStaleJobs`. (Note: `line 96` sets `TemplateSetID` on `core.Branch`, which remains valid.)
- **`cmd/mcp/main_test.go` (`102`, `215`, `245`, `272`, `283`, `314`)**: `core.Store` -> `*store.Store`.
- **`server/server_test.go` (`138`, `160`, `403-405`, `443`)**: align `WithStore` / `server.Config` tests with `*store.Store` (`memStore.Profiles().UpsertProfile`), update `WithWorkerPoolConfig(4, 200, 3)` -> `WithWorkerPoolConfig(4, 3)`, and remove `TestServer_OptionValidation/invalid_worker_queueSize`.
- **`internal/config/config_test.go` (`39-40`, `89`, `125-126`, `202-214`)**: remove `WorkerQueueSize` / `WORKER_QUEUE_SIZE` assertions and validation test cases.
- **`internal/access/access_test.go` (`14`, `24`)**: embed `access.TemplateReader` / `access.PresetReader` instead of `core.TemplateStore` / `core.PresetStore`.
- **`internal/api/queue_test.go`**: delete alongside `internal/api/queue.go`.
- **`internal/api/middleware_test.go` (`431`)**: `noopProfileStore` embeds `api.ProfileStore` instead of `core.ProfileStore`.
- **`internal/api/branch_model_selection_test.go` (`62-88`)**: update direct `memStore.*` calls to `memStore.<SubStore>().*`.
- **`internal/api/prompt_preview_test.go` (`200-245`, `331`)**: update `PromptPreviewRequest` (`CustomDirectives`/`TailoringStrategyID` -> `BranchInstructions`/`PresetID`), `core.Branch` fixtures, and `memStore.<SubStore>().*` calls.
- **`internal/api/preset_validation_test.go` (`17`, `27-28`, `126`, `156-157`)**: update store types to `api` consumer interfaces and `CreatePresetRequest.TemplateSetID` / `core.Preset.TemplateSetID` -> `SuggestedThemeID` (`suggested_theme_id`).
- **`internal/api/resume_handlers_test.go` (`34-54`, `148-151`, `267`, `304`, `375`, `416`, `482`, `550-553`, `660-663`, `756`, `910-914`)**: consumer interfaces and `memStore.<SubStore>().*`; `Preset.TemplateSetID` (`line 375`) -> `SuggestedThemeID`; replace `CreateVersion` / `CompleteVersion` / `UpdateVersionStatus` with `SaveCompletedVersion` for completed fixtures and `memStore.Jobs().SubmitBranchTailor` (+ `ClaimNext` + `FailClaimed` for failed-version `422` fixture) for pending/failed versions; migrate legacy `tailoring_strategy_id` tests to `preset_id`. (Note: `core.Branch.TemplateSetID` and `CreateBranch_ForeignTemplateSetID_Returns403` / `UpdateBranch_ForeignTemplateSetID_Returns403` remain valid.)
- **`internal/api/branch_preview_dynamic_test.go` (`29-32`, `118`)**: consumer interfaces, `memStore.<SubStore>().*`, and `CreateVersion` -> `SaveCompletedVersion` (note `core.Branch.TemplateSetID` at `line 97` remains valid).
- **`internal/api/strategy_handlers_test.go` (`28-53`, `151`, `255`, `302`)**: consumer interfaces, `memStore.<SubStore>().*`, and `Preset.TemplateSetID` (`lines 151, 255, 302`) -> `SuggestedThemeID` / `suggested_theme_id`.
- **`internal/api/quota_handlers_test.go` (`100`, `154`, `220-222`, `301`, `385`, `459`, `476-478`)**: `singleUsageRecord` accepts a local quota reader interface (or `*store.Quotas`), and update all direct `memStore.<Quota/Branch/Preset/Profile>()` calls to sub-store accessors.
- **`internal/api/api_test.go`**:
  - Replace `core.*Store` and `ChannelJobEnqueuer` / `ChannelPDFJobEnqueuer` across `testEnv` (`33-65`, `1100-1125`, `2669`, `2975`, `3481`, `3829`) with `*store.Store`, consumer interfaces, `MaxPendingPerTenant`, and `core.JobNotifierFunc` / `*worker.Pool`.
  - Update all direct `memStore.*` calls to `memStore.<SubStore>().*`.
  - Update `TestProfilePDF_Parse_AsyncLifecycle` (`1360-1460`) to inspect `*core.Job` via `PDFParseJobResponse` (`Status == core.JobStatusPending` / `core.JobStatusCompleted`) and drive completion through `*store.Jobs` (`ClaimNext` + `CompletePDFParse`) / `*worker.Pool`.
  - Update `TestErrorEnvelopes/503_QUEUE_FULL` (`1477-1479`) to configure `MaxPendingPerTenant: 1` (triggering `core.ErrPendingLimitExceeded` -> `core.ErrQueueFull`) or inject a failing `JobStore` returning `core.ErrQueueFull`.
  - Update `deadlineSpyPDFJobStore` (`3801-3806`) to wrap `api.JobStore` rather than `core.PDFJobStore`.
  - Replace `CreateVersion` / `CompleteVersion` (`542`, `881`, `3667`) -> `SaveCompletedVersion` and `CreatePresetRequest.TemplateSetID` (`400`, `3188`) -> `SuggestedThemeID`.
- **`internal/mcp/http_test.go` (`201`, `437`, `514`)**: replace `core.ProfileStore` embed with `mcp.ProfileStore` and update direct `memStore.*` calls.
- **`internal/mcp/refresh_test.go` (`66-588`, `708-738`, `829`, `909`)**: replace `core.*Store` embeds with `mcp` consumer interfaces, update `memStore.<SubStore>().*` calls, and wire `branch.Config` with `Jobs` + `JobNotifier`.
- **`internal/mcp/mcp_test.go` (`92-175`, `564-653`, `828`, `926-928`, `1052`, `1132`, `1927-1931`)**: update stores to `mcp` consumer interfaces and `memStore.<SubStore>().*`; `TailoringStrategyID`/`CustomDirectives` assertions -> `PresetID`/`BranchInstructions`; `CreateVersion`/`CompleteVersion` -> `SaveCompletedVersion`. (Note: `core.Branch.TemplateSetID` assertions/fixtures remain valid.)

---

## 5. Storage Schema & Local/Dev Database Impact

With `resumeee-storage-sql#4` merged into `main`, persistence schema changes are greenfield (non-backwards-compatible):
- Adds `jobs` table + partial unique active-job index `idx_jobs_active` (replacing `pdf_parse_jobs` and inline `resume_versions` lease columns).
- Adds `target_resumes.epoch`, `resume_versions.branch_epoch`, `resume_versions.job_id`, and composite index `idx_branch_version`.

Existing local SQLite files or dev Postgres schemas created before `resumeee-storage-sql#4` must be recreated/reset cleanly when switching `resumeee-api` onto the greenfield pins.

---

## 6. Verification Checklist

After implementing the code changes above, all of the following checks must succeed before merging:

1. `GOWORK=off go build ./cmd/server ./cmd/mcp`
2. `GOWORK=off make verify` (covers `gofmt`, `go test -race`, and `golangci-lint`, including AST/spec checks `api/import_boundary_test.go`, `api/interface_assertion_test.go`, `api/godoc_test.go`, and `api/openapi_test.go`)
3. End-to-end check with SQLite storage (`resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba`) confirming startup stale-job recovery (`RecoverStaleJobs`), PDF parse submission/polling, and branch tailoring job lifecycle.
4. Once the `resumeee-api` implementation PR lands on `main`, re-verify with `go.work` enabled across `resumeee-core`, `resumeee-storage-sql`, and `resumeee-api`.
