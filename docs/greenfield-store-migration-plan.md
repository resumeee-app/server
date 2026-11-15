# Greenfield Store Architecture Migration Plan (`resumeee-api`)

## 1. Purpose & Context

This document specifies the migration required in `resumeee-api` to adopt the zero-backwards-compatibility greenfield persistence redesign across the workspace:

- **`resumeee-core` PR #26**: https://github.com/resumeee-app/resumeee-core/pull/26 (branch `refactor/greenfield-store-architecture`, commit `9453215a9d6cb16c2c09bae5b56c4db888e3af30`)
- **`resumeee-storage-sql` PR #4**: https://github.com/resumeee-app/resumeee-storage-sql/pull/4 (branch `feat/store-lease-queue`, commit `41126830eb90f16c2c09bae5b56c4bbccdc008e3af30`; related Issue #5)

Under the new architecture, storage adapters (`resumeee-storage-*`, `memory`) implement only durable storage, atomicity, and transaction primitives via `store.Backend`, `store.ReadTx`, and `store.Tx`, while all persistence domain logic, normalization, quota coordination, job leasing, and retry/cancellation semantics live in `resumeee-core/store`, `resumeee-core/branch`, and `resumeee-core/worker`.

### Execution Order & Module Pinning

Execute migrations in dependency order:
1. Merge `resumeee-core#26` into `main` (or pin directly to commit `9453215a9d6cb16c2c09bae5b56c4db888e3af30`).
2. Merge `resumeee-storage-sql#4` into `main` (re-pinning `resumeee-core` to `main` if desired; commit `41126830eb90f16c2c09bae5b56c4bbccdc008e3af30`).
3. Update `resumeee-api` module pins using `GOWORK=off` and `GOPRIVATE=github.com/resumeee-app/*`:

```bash
GOWORK=off GOPRIVATE=github.com/resumeee-app/* go get \
  github.com/resumeee-app/resumeee-core@9453215a9d6cb16c2c09bae5b56c4db888e3af30 \
  github.com/resumeee-app/resumeee-storage-sql@41126830eb90f16c2c09bae5b56c4bbccdc008e3af30
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
  - Bump `resumeee-core` and `resumeee-storage-sql` to the greenfield commits above and run `GOWORK=off go mod tidy`.
- **`internal/config/config.go` (`28`, `75-76`, `168`, `242`)**
  - Remove `WorkerQueueSize`, `DefaultWorkerQueueSize`, and the `WORKER_QUEUE_SIZE` env binding, since durable admission control (`MaxPendingPerTenant` on `branch.Config` / `worker.PDFSubmitConfig`) replaces bounded Go channels.
  - Evaluate whether to expose durable worker/queue settings (`MaxPendingPerTenant`, `MaxConcurrentPerTenant`, `PollInterval`, `LeaseDuration`, `MaxAttempts`) in `Config` or rely on `branch` / `worker` package defaults initially.
- **`.env.example` (`81`), `Dockerfile` (`92`), `README.md` (`47`, `73`)**
  - Remove `WORKER_QUEUE_SIZE` documentation/defaults (and document any newly added durable worker env vars if exposed).

### Phase 2: Server Composition & Binary Entrypoints

- **`server/options.go` (`94`, `190`, `297-306`)**
  - Change `WithStore(store core.Store)` to `WithStore(store *store.Store)`.
  - Update `Config` / `WithWorkerPoolConfig` to drop `WorkerQueueSize` / `queueSize` (avoid leaving dead channel queue parameters behind).
- **`server/server.go` (`4`, `52`, `80`, `112`, `144`, `206`, `241`, `262`, `280-294`, `349-433`)**
  - Change `Server.store` from `core.Store` to `*store.Store` and update log/error/doc references.
  - In startup recovery (`line 262`), remove `branchStore.FailInterruptedVersions(...)`; retain `workerPool.RecoverStaleJobs(ctx, "server restarted while generation job was in flight")` so both branch tailoring and PDF parse jobs are interrupted and quota reservations refunded in one atomic path.
  - Construct `worker.Pool` with `Jobs: s.store.Jobs()` instead of `QueueSize` / `PDFJobs`.
  - Construct `branch.Service` with `Jobs: s.store.Jobs()` and `JobNotifier: workerPool` instead of `JobEnqueuer: workerPool`.
  - Update `api.NewRouterConfigFromStore(s.store)` + `JobNotifier: deps.workerPool`, and pass `*store.Store` sub-stores + `Jobs: s.store.Jobs(), JobNotifier: deps.workerPool` to `mcp.ServerConfig`.
- **`cmd/server/main.go` (`23-40`, `91`, `138`)**
  - Change `StoreInitializer`, `defaultSQLiteInitializer`, and `closeStore` from `core.Store` to `*store.Store`.
  - Remove `WorkerQueueSize: cfg.WorkerQueueSize` from `server.Config` literal.
- **`cmd/mcp/main.go` (`31-34`, `152`, `178-189`, `267`)**
  - Change `StoreInitializer`, `defaultSQLiteInitializer`, `buildMCPServerConfig`, and `closeMCPStore` from `core.Store` to `*store.Store`.
  - Remove `queueSize` / `QueueSize` in `worker.Config`; pass `Jobs: st.Jobs()` to `worker.New` and `Jobs: st.Jobs(), JobNotifier: pool` to `branch.New`.
  - Add `pool.RecoverStaleJobs(...)` on standalone MCP server startup so interrupted jobs and reserved quotas are recovered cleanly, matching `server/server.go`.

### Phase 3: Consumer-Side Store Interfaces & Queue Removal

- **Delete `internal/api/queue.go`**
  - Remove `ChannelQueue`, `ChannelJobEnqueuer`, and `ChannelPDFJobEnqueuer` completely.
- **`internal/access/access.go` (`50`, `62`)**
  - Replace `core.TemplateStore` and `core.PresetStore` parameters on `Template(...)` and `Preset(...)` with narrow consumer-defined reader interfaces requiring only the template/preset lookup method used.
- **`internal/api/provisioner.go` (`13-37`)**
  - Replace `core.ProfileStore`, `core.BranchStore`, `core.TemplateStore`, `core.UserStore` fields with narrow consumer interfaces matching `seed.ProfileSeeder`, `seed.BranchSeeder`, `seed.TemplateSeeder`, `seed.UserSeeder` so `seed.EnsureTenantProvisionedStores` compiles cleanly without root `core` store interfaces.
- **`internal/api/api.go` (`51-58`, `65`, `71`, `86-87`, `171`)**
  - Replace `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `QuotaStore`, and `UserStore` field types on `RouterConfig` with consumer-defined interfaces (or sub-package store interfaces where already defined) satisfied by `*store.Store` sub-stores.
  - Replace `PDFJobStore core.PDFJobStore`, `PDFJobEnqueuer core.PDFJobEnqueuer`, and `JobEnqueuer branch.JobEnqueuer` with:
    - `JobStore` (consumer interface satisfied by `*store.Jobs`, supplying `worker.JobStore` / `branch.JobStore` and API job queries/cancellations)
    - `JobNotifier core.JobNotifier`
  - Update `NewRouterConfigFromStore(st *store.Store)` to populate every sub-store field from `st.Profiles()`, `st.Career()`, `st.Presets()`, `st.Templates()`, `st.Branches()`, `st.Jobs()`, `st.Quotas()`, `st.Users()`.
  - Update `branch.Config` wiring (`line 171`) to pass `Jobs: cfg.JobStore` and `JobNotifier: cfg.JobNotifier`.
- **`internal/mcp/config.go` (`39-53`)**
  - Replace `core.*Store` fields on `ServerConfig` with narrow consumer interfaces matching MCP tool/resource usage.
  - Remove `JobEnqueuer branch.JobEnqueuer` (MCP `BranchService` receives job submission via `branch.Service`, or pass `Jobs`/`JobNotifier` only if needed directly).

### Phase 4: REST Handlers, MCP Tools & OpenAPI Contracts

- **`internal/api/profile_handlers.go` (`25`, `34-36`) & `internal/api/pdf_handlers.go` (`36`, `377-423`)**
  - Update route guards (`cfg.PDFJobStore != nil` -> `cfg.JobStore != nil`).
  - Replace manual `core.PDFParseJob` creation, `PDFJobStore.CreateJob`, and `EnqueuePDFJob` with `worker.SubmitPDFJob(ctx, worker.PDFSubmitConfig{...}, worker.PDFJobSubmission{...})`, returning `*core.Job`.
  - Update `getPDFParseJob` (`pdf_handlers.go`) to read `*core.Job` via `JobStore` and project/return job status (`core.JobStatus`: `pending`, `processing`, `completed`, `failed`, `cancelled`) and parsed result / failure details cleanly.
  - Optional/recommended: expose `DELETE` / `POST .../cancel` routes for PDF jobs (`worker.CancelPDFJob`) and branch tailors (`(*branch.Service).CancelVersion`).
- **`internal/api/branch_handlers.go` (`64-85`, `144-183`, `232-235`, `330-349`) & `internal/mcp/branch_tools.go` (`308-310`, `409-443`, `503-504`)**
  - Remove all assignments and reads against deleted `core.Branch.CustomDirectives` and `core.Branch.TailoringStrategyID`.
  - **Greenfield HTTP/MCP wire decision**: drop legacy `custom_directives` and `tailoring_strategy_id` request/response fields from `branch_handlers.go` (and `api/openapi.yaml`), standardizing on `branch_instructions` and `preset_id`. Expose `epoch` on `Branch` responses. Keep `TemplateSetID` on `core.Branch` (`template_set_id`), which remains canonical on branches.
- **`internal/api/version_handlers.go` (`418`)**
  - Replace deleted `ver.ResumeID` with `ver.BranchID` in `loadRenderableVersion` ownership verification (`ver.BranchID != b.ID`).
  - Verify `errors.Is(err, core.ErrQueueFull)` (`line 61`) continues mapping `core.ErrPendingLimitExceeded` -> HTTP `503`.
- **`internal/api/preset_handlers.go` (`42`, `52`, `98-131`, `226-235`) & `internal/mcp/preset_tools.go` (`156`, `224`)**
  - Remove all reads/writes against deleted `core.Preset.TemplateSetID`.
  - Drop `template_set_id` from `Preset` HTTP request/response structs and MCP tool normalization in favor of canonical `suggested_theme_id` (`SuggestedThemeID *string`).
- **`api/openapi.yaml` & `api/openapi_test.go`**
  - Update `Branch` schema (`-custom_directives`, `-tailoring_strategy_id`, `+epoch`).
  - Update `BranchVersion` schema (`-resume_id` if present, `+branch_epoch`, `+job_id`).
  - Update `Preset` schema (`-template_set_id`; `suggested_theme_id` canonical).
  - Update PDF job status/schema (`core.Job` status enum including `cancelled`, or keep unified `Job` schema) and update `api/openapi_test.go` expectations if schema names/required properties change.

### Phase 5: Test Suite & Mock Updates

Every test file below fails compilation or assertions until updated:

- **`cmd/server/main_test.go` (`46`, `107-113`, `488`, `511`)**: `core.Store` -> `*store.Store`; replace `memStore.CreateVersion` + `UpdateVersionStatus` setup with a submitted `core.Job` + claim (or `SaveCompletedVersion`), verifying `RecoverStaleJobs`. (Note: `line 96` sets `TemplateSetID` on `core.Branch`, which remains valid.)
- **`cmd/mcp/main_test.go` (`102`, `215`, `245`, `272`, `283`, `314`)**: `core.Store` -> `*store.Store`.
- **`server/server_test.go`**: align `WithStore` / `server.Config` tests with `*store.Store`, and remove `TestServer_OptionValidation/invalid_worker_queueSize` / `WorkerQueueSize` checks.
- **`internal/config/config_test.go` (`39-40`, `125-126`)**: remove `WorkerQueueSize` / `WORKER_QUEUE_SIZE` assertions (add assertions for any new worker/job env settings if introduced).
- **`internal/access/access_test.go` (`14`, `24`)**: embed `access` consumer interfaces instead of `core.TemplateStore` / `core.PresetStore`.
- **`internal/api/middleware_test.go` (`431`)**: `noopProfileStore` embeds `api`'s consumer profile interface instead of `core.ProfileStore`.
- **`internal/api/preset_validation_test.go` (`17`, `27-28`, `126`, `156-157`)**: update store types to `api` consumer interfaces and `api.CreatePresetRequest.TemplateSetID` / `core.Preset.TemplateSetID` -> `SuggestedThemeID` (`suggested_theme_id`).
- **`internal/api/resume_handlers_test.go` (`34-54`, `148-151`, `267`, `304`, `375`, `416`, `482`, `550-553`, `660-663`, `756`, `910-914`)**: consumer interfaces; `Preset.TemplateSetID` (`line 375`) -> `SuggestedThemeID`; replace `CreateVersion` / `CompleteVersion` fixture helpers with `SaveCompletedVersion` (or real `*store.Jobs` entries for non-completed versions); remove or migrate legacy `tailoring_strategy_id` tests (`CreateBranch_ForeignTailoringStrategyID_Returns403`, `UpdateBranch_ForeignTailoringStrategyID_Returns403`) to `preset_id`. (Note: `core.Branch.TemplateSetID` and `CreateBranch_ForeignTemplateSetID_Returns403` / `UpdateBranch_ForeignTemplateSetID_Returns403` remain valid.)
- **`internal/api/branch_preview_dynamic_test.go` (`29-32`, `118`)**: consumer interfaces and `CreateVersion` -> `SaveCompletedVersion` (note `core.Branch.TemplateSetID` at `line 97` remains valid).
- **`internal/api/strategy_handlers_test.go` (`28-53`, `151`, `255`, `302`)**: consumer interfaces; `Preset.TemplateSetID` (`lines 255, 302`) and foreign preset template test (`line 151`) -> `SuggestedThemeID` / `suggested_theme_id`.
- **`internal/api/quota_handlers_test.go` (`220`)**: `singleUsageRecord` accepts `api`'s quota reader interface instead of `core.QuotaStore`.
- **`internal/api/api_test.go`**:
  - Replace `core.*Store` and `ChannelJobEnqueuer` / `ChannelPDFJobEnqueuer` across `testEnv` (`33-65`, `1100-1125`, `2669`, `2975`, `3481`, `3829`) with `*store.Store`, consumer interfaces, and `core.JobNotifierFunc` / `*worker.Pool`.
  - Update `TestProfilePDF_Parse_AsyncLifecycle` (`1360-1460`) to inspect `*core.Job` (`Status == core.JobStatusPending` / `core.JobStatusCompleted`) and drive completion through `*worker.Pool` / `*store.Jobs` instead of `UpdateJobStatus`.
  - Update `TestErrorEnvelopes/503_QUEUE_FULL` (`1477-1479`) so instead of filling a capacity-1 channel enqueuer, it exercises `MaxPendingPerTenant` (`core.ErrPendingLimitExceeded` -> `core.ErrQueueFull`) or an injected failing job store returning `core.ErrQueueFull`.
  - Update `deadlineSpyPDFJobStore` (`3801-3806`) to wrap `api`'s consumer `JobStore` interface rather than `core.PDFJobStore`.
  - Replace `CreateVersion` / `CompleteVersion` (`542`, `881`, `3667`) -> `SaveCompletedVersion` and `CreatePresetRequest.TemplateSetID` (`400`, `3188`) -> `SuggestedThemeID`.
- **`internal/mcp/http_test.go` (`201`)**: replace `core.ProfileStore` embed with `mcp` consumer interface.
- **`internal/mcp/refresh_test.go` (`709-726`)**: replace `core.BranchStore` / `ProfileStore` / `PresetStore` / `TemplateStore` with `mcp` consumer interfaces and wire `branch.Config` with `Jobs` + `JobNotifier`.
- **`internal/mcp/mcp_test.go` (`92-120`, `598-599`, `926-928`, `1052`, `1132`, `1927-1931`)**: update stores to `mcp` consumer interfaces; `TailoringStrategyID`/`CustomDirectives` assertions -> `PresetID`/`BranchInstructions`; `CreateVersion`/`CompleteVersion` -> `SaveCompletedVersion`. (Note: `core.Branch.TemplateSetID` assertions/fixtures remain valid.)

---

## 5. Storage Schema & Local/Dev Database Impact

With `resumeee-storage-sql#4`, persistence schema changes are greenfield (non-backwards-compatible):
- Adds `jobs` table + partial unique active-job index `idx_jobs_active` (replacing `pdf_parse_jobs` and inline `resume_versions` lease columns).
- Adds `target_resumes.epoch`, `resume_versions.branch_epoch`, `resume_versions.job_id`, and composite index `idx_branch_version`.

Existing local SQLite files or dev Postgres schemas created before `resumeee-storage-sql#4` must be recreated/reset cleanly when switching `resumeee-api` onto the greenfield pins.

---

## 6. Verification Checklist

After implementing the code changes above, all of the following checks must succeed before merging:

1. `GOWORK=off go build ./cmd/server ./cmd/mcp`
2. `GOWORK=off make verify` (covers `gofmt`, `go test -race`, and `golangci-lint`, including AST/spec checks `api/import_boundary_test.go`, `api/interface_assertion_test.go`, `api/godoc_test.go`, and `api/openapi_test.go`)
3. End-to-end check with SQLite storage (`resumeee-storage-sql#4`) confirming startup stale-job recovery (`RecoverStaleJobs`), PDF parse submission/polling, and branch tailoring job lifecycle.
4. Once `resumeee-core#26`, `resumeee-storage-sql#4`, and the `resumeee-api` implementation PR land on `main`, re-verify with `go.work` enabled across all three repositories.
