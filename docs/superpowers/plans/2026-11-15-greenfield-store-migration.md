# Greenfield Store Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` (recommended) or `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate `resumeee-api` to the greenfield composite `*store.Store`, unified `store.Jobs` durable lease queue, `worker.Pool`, and streamlined domain models merged on `resumeee-core@main` and `resumeee-storage-sql@main`.

**Architecture:** `cmd/*` and `server` inject a concrete `*store.Store` (`memory` or `sql.Open*`), passing typed sub-stores (`Profiles()`, `Career()`, `Presets()`, `Templates()`, `Branches()`, `Jobs()`, `Quotas()`, `Users()`) into consumer-defined interfaces declared in domain `store.go` files (`internal/api/store.go`, `internal/mcp/store.go`, and `internal/access/access.go`). Bounded Go channel queues (`internal/api/queue.go`) are deleted in favor of `store.Jobs` + `*worker.Pool` (`core.JobNotifier`) with atomic admission limits (`MaxPendingPerTenant`), legacy branch/preset wire fields (`custom_directives`, `tailoring_strategy_id`, preset `template_set_id`) are dropped cleanly, and flat `PDFParseJobResponse` is preserved at HTTP boundaries.

**Tech Stack:** Go (`GOWORK=off`), `github.com/resumeee-app/resumeee-core@21d6fa985d331313cf0b8fd65c0d27dfa968fce9`, `github.com/resumeee-app/resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba`, `chi`, `mcp-go`, `kin-openapi`, `golangci-lint`.

**Spec:** [greenfield-store-migration-plan.md](file:///home/null/projects/resumeee/resumeee-api/docs/greenfield-store-migration-plan.md)

**Global Constraints:**
- Obey `AGENTS.md`: document every exported and unexported symbol with a Go doc comment starting with its exact identifier name; keep package doc comments in `<pkg>.go`; use domain file names (`store.go`, never `interfaces.go`); zero runtime interface assertions (`v.(T)` — enforced by `api/interface_assertion_test.go`); zero `resumeee-storage-sql` imports outside `cmd/*` (enforced by `api/import_boundary_test.go`).
- Run Go commands with `GOWORK=off GOPRIVATE=github.com/resumeee-app/*` until the implementation PR lands and sibling `go.work` checkouts align.
- Pin both `resumeee-core@21d6fa985d331313cf0b8fd65c0d27dfa968fce9` and `resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba` explicitly (`resumeee-storage-sql`'s `go.mod` points to an earlier `resumeee-core` pseudo-version).
- Preserve the 36 existing REST endpoints checked by `api/openapi_test.go`; defer `/cancel` REST endpoints and MCP cancel tools to a follow-up PR while keeping `api.JobStore` cancel-ready (`branch.JobStore` + `worker.JobStore`).
- Create a dedicated implementation branch (e.g., `refactor/greenfield-store-migration`) before executing Task 1 rather than committing code onto `docs/greenfield-store-migration-plan`.
- Keep every task package-compilable for its own package test suite (`go test -race ./<pkg>/...`), running repo-wide `make verify` and binary builds in Task 7 once all upstream dependents (`server`, `cmd/*`, `internal/mcp`) have migrated.

**Review Focus (Spec-Implied Failure Modes & Owners):**
1. **Cross-Kind Job Leak on `GET /profile/pdf/jobs/{id}`:** `store.Jobs()` stores both `pdf_parse` and `branch_tailor` jobs. Returning or dereferencing a non-PDF job causes nil `job.PDF` panics or leaks `branch_tailor` state, and marshaling `*core.Job` directly exposes `PDF.RawText`, `ReservationID`, and `ClaimedBy`.
   - *Owner:* **Task 3** (`newPDFParseJobResponse` in `internal/api/pdf_handlers.go` guarding `job != nil && job.Kind == core.JobKindPDFParse && job.PDF != nil`) and **Task 4** (`TestGetPDFParseJob_NonPDFJobReturns404` + response field assertions in `internal/api/api_test.go`).
2. **Async vs Sync PDF Route & Nil-Notifier Error Precedence:** Without a worker (`JobNotifier == nil`), `POST /profile/pdf/parse` must stay synchronous unless `?async=true` is set, and `enqueueAsyncPDFJob` must return `500` for missing `JobStore` before `503` for missing `JobNotifier`, then forward `MaxPendingPerTenant` so `core.ErrPendingLimitExceeded` maps to `503 QUEUE_FULL`.
   - *Owner:* **Task 3** (`internal/api/pdf_handlers.go`) and **Task 4** (`TestProfilePDF_Parse_SyncByDefaultWithoutNotifier`, `TestProfilePDF_Parse_AsyncWithoutNotifier_Returns503`, `TestErrorEnvelopes/503_QUEUE_FULL` via `MaxPendingPerTenant: 1`).
3. **AST Guardrails & OpenAPI Drift:** Missing doc comments on `internal/api/store.go` / `internal/mcp/store.go`, stale `api/openapi.yaml` schemas (`VersionStatus`, `PDFParseJobStatus`, `Branch`, `BranchVersion`, `Preset`, `PromptPreviewRequest`), or stray `interface{}` / `any` assertions fail `api/*_test.go`.
   - *Owner:* **Task 3** (`internal/api/store.go`), **Task 4** (`api/openapi.yaml` + `go test -race ./api/...`), **Task 5** (`internal/mcp/store.go`), **Task 7** (`make verify`).
4. **MCP Server Crash Leaving Stranded `processing` Jobs & Quotas:** `cmd/mcp/main.go` starts an embedded `*worker.Pool`; omitting `pool.RecoverStaleJobs(ctx, ...)` before `pool.Start(ctx)` strands interrupted jobs and locks tenant quotas after ungraceful shutdown.
   - *Owner:* **Task 7** (`cmd/mcp/main.go` + recovery test in `cmd/mcp/main_test.go` + SQLite E2E check).
5. **Module Pin Skew Under `go.work`:** Omitting `GOWORK=off` or forgetting to pin `resumeee-core` explicitly resolves an older `resumeee-core` pseudo-version from `resumeee-storage-sql/go.mod`.
   - *Owner:* **Task 2** (`go.mod` / `go.sum` dual pin) and **Task 7** (`GOWORK=off make verify` + SQLite E2E verification).

---

### Task 1: Remove Channel Queue Configuration & Documentation

**Interfaces:**
- Removes `WorkerQueueSize`, `DefaultWorkerQueueSize`, and `WORKER_QUEUE_SIZE` from `internal/config`.

**Files:**
- Modify: `internal/config/config.go` (`28`, `75-76`, `168`, `242`)
- Modify: `internal/config/config_test.go` (`39-40`, `89`, `125-126`, `202-214`)
- Modify: `.env.example` (`81`)
- Modify: `Dockerfile` (`92`)
- Modify: `README.md` (`47`, `73`)

**Steps:**
- [ ] **1. Update `internal/config/config_test.go` first to remove obsolete `WorkerQueueSize` / `WORKER_QUEUE_SIZE` assertions**
  - Remove `WorkerQueueSize` default assertions (`lines 39-40`), env override fixture/assertions (`lines 89, 125-126`), and `WORKER_QUEUE_SIZE` validation subtests (`lines 202-214`).
  - Keep existing validation tests for `WorkerConcurrency` and `MaxConcurrentPerTenant` intact.

- [ ] **2. Remove channel queue config from `internal/config/config.go`**
  - Delete `DefaultWorkerQueueSize` (`line 28`).
  - Delete `WorkerQueueSize int` from `Config` (`lines 75-76`).
  - Delete `WORKER_QUEUE_SIZE` parsing in `Load` (`line 168`) and `WorkerQueueSize <= 0` validation (`line 242`).
  - Rely on `branch` / `worker` / `core` built-in durable defaults (`core.DefaultMaxPendingPerTenant`, `core.DefaultJobTTL`); do not add new queue env vars to `config.Config`.

- [ ] **3. Remove `WORKER_QUEUE_SIZE` references from env/container/docs files**
  - Remove `WORKER_QUEUE_SIZE` lines from `.env.example` (`line 81`), `Dockerfile` (`line 92`), and `README.md` (`lines 47, 73`).

- [ ] **4. Verify config tests and search for lingering `WORKER_QUEUE_SIZE` / `WorkerQueueSize` outside pending `server`/`cmd` files**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./internal/config/...
    ```
    Expected: `PASS`.

- [ ] **5. Commit Task 1**
  - Run:
    ```bash
    git add internal/config/config.go internal/config/config_test.go .env.example Dockerfile README.md
    git commit -m "chore(config): remove obsolete channel WorkerQueueSize config"
    ```

---

### Task 2: Pin Upstream Greenfield Modules & Migrate `internal/access`

**Interfaces:**
- Consumes: `store.Templates.Template`, `store.Presets.Preset` (structural match; no `resumeee-core/store` import needed in `internal/access`).
- Produces: `access.TemplateReader`, `access.PresetReader`.

**Files:**
- Modify: `go.mod`, `go.sum`
- Modify: `internal/access/access.go` (`1-88`)
- Modify: `internal/access/access_test.go` (`14`, `24`)

**Steps:**
- [ ] **1. Pin merged `main` commits in `go.mod` and observe expected `internal/access` failure**
  - Run:
    ```bash
    GOWORK=off GOPRIVATE=github.com/resumeee-app/* go get \
      github.com/resumeee-app/resumeee-core@21d6fa985d331313cf0b8fd65c0d27dfa968fce9 \
      github.com/resumeee-app/resumeee-storage-sql@421b71937a9e2fce455f3a9ff0a4e3c0e22b80ba
    GOWORK=off go mod tidy
    ```
  - Run test to confirm compile error on removed `core.TemplateStore` / `core.PresetStore`:
    ```bash
    GOWORK=off go test ./internal/access/...
    ```
    Expected: FAIL (`undefined: core.TemplateStore`, `undefined: core.PresetStore`).

- [ ] **2. Define consumer interfaces in `internal/access/access.go`**
  - Add documented consumer interfaces matching `*store.Templates` and `*store.Presets`:
    ```go
    // TemplateReader retrieves a template set for ownership and access checks.
    type TemplateReader interface {
        Template(ctx context.Context, userID, id string) (*core.TemplateSet, error)
    }

    // PresetReader retrieves a tailoring preset for ownership and access checks.
    type PresetReader interface {
        Preset(ctx context.Context, userID, id string) (*core.Preset, error)
    }
    ```
  - Update `Template(ctx context.Context, store TemplateReader, userID, templateID string) (*core.TemplateSet, error)` and `Preset(ctx context.Context, store PresetReader, userID, presetID string) (*core.Preset, error)`.

- [ ] **3. Update `internal/access/access_test.go`**
  - Replace embedded `core.TemplateStore` (`line 14`) with `access.TemplateReader` (or `TemplateReader`), and `core.PresetStore` (`line 24`) with `PresetReader`.

- [ ] **4. Verify `internal/access`, `internal/config`, and `auth`**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./internal/access/... ./internal/config/... ./auth/...
    ```
    Expected: `PASS`.

- [ ] **5. Commit Task 2**
  - Run:
    ```bash
    git add go.mod go.sum internal/access/access.go internal/access/access_test.go
    git commit -m "build: pin greenfield core/sql commits and migrate access interfaces"
    ```

---

### Task 3: Migrate `internal/api` Store Interfaces, Queue Removal, Handlers & Existing Tests

**Interfaces:**
- Consumes: `store.*` sub-stores via concrete constructor `NewRouterConfigFromStore(st *store.Store)`, plus `branch.Store`, `branch.JobStore`, `worker.JobStore`, `profile.Store`, `seed.*Seeder`, `core.JobNotifier`.
- Produces (`internal/api/store.go`): `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `JobStore`, `QuotaStore`, `UserStore`; `RouterConfig.MaxPendingPerTenant int`; `PDFParseJobResponse`.

**Files:**
- Create: `internal/api/store.go`
- Delete: `internal/api/queue.go`, `internal/api/queue_test.go`
- Modify: `internal/api/provisioner.go` (`1-37`), inspect `internal/api/middleware.go` (`195-261`)
- Modify: `internal/api/api.go` (`1-268`)
- Modify: `internal/api/profile_handlers.go` (`25`, `34-36`), `internal/api/pdf_handlers.go` (`36`, `270-454`)
- Modify: `internal/api/branch_handlers.go` (`1-411`), `internal/api/response.go` (`140-210`)
- Modify: `internal/api/version_handlers.go` (`418`), `internal/api/preset_handlers.go` (`42`, `52`, `98-131`, `226-271`)
- Modify existing tests for compile/API signature alignment:
  - `internal/api/middleware_test.go`
  - `internal/api/branch_model_selection_test.go`
  - `internal/api/prompt_preview_test.go`
  - `internal/api/preset_validation_test.go`
  - `internal/api/branch_preview_dynamic_test.go`
  - `internal/api/strategy_handlers_test.go`
  - `internal/api/quota_handlers_test.go`
  - `internal/api/resume_handlers_test.go`
  - `internal/api/api_test.go`

**Steps:**
- [ ] **1. Run `go test ./internal/api/...` to record initial compile errors against greenfield `resumeee-core`**
  - Run:
    ```bash
    GOWORK=off go test ./internal/api/...
    ```
    Expected: FAIL with missing `core.*Store`, `queue.go`, `branch.JobEnqueuer`, and removed struct fields.

- [ ] **2. Delete `internal/api/queue.go` and `internal/api/queue_test.go`**
  - Run:
    ```bash
    rm internal/api/queue.go internal/api/queue_test.go
    ```

- [ ] **3. Create `internal/api/store.go` with documented consumer interfaces**
  - Include `package api` doc compliance (keep `Package api ...` in `api.go`; every interface and method in `store.go` has a doc comment starting with its exact identifier name):
    - `ProfileStore`: `Profile`, `UpsertProfile`, `Snapshot`, and `ReplaceCareerGraph` (compatible with `profile.Store`, `seed.ProfileSeeder`, `branch.ProfileReader`, and `*store.Profiles`). Inspect `api.NewRouter` first to confirm whether `profile.New` is called there when `cfg.ProfileService == nil`; embedding `profile.Store` satisfies both paths cleanly.
    - `CareerStore`: `ListSkills`, `ListGlobalSkills`, `Skill`, `CreateSkill`, `UpdateSkill`, `DeleteSkill`, `ListExperiences`, `Experience`, `CreateExperience`, `UpdateExperience`, `DeleteExperience`, `ListProjects`, `Project`, `CreateProject`, `UpdateProject`, `DeleteProject`, `ListEducation`, `Education`, `CreateEducation`, `UpdateEducation`, `DeleteEducation`, `ListCertifications`, `Certification`, `CreateCertification`, `UpdateCertification`, `DeleteCertification`, `ListFactsByOwner`, `Fact`, `CreateFact`, `UpdateFact`, `DeleteFact`.
    - `PresetStore`: `ListPresets`, `Preset`, `CreatePreset`, `UpdatePreset`, `DeletePreset`.
    - `TemplateStore`: `ListTemplates`, `Template`, `CreateTemplate`, `UpdateTemplate`, `DeleteTemplate`.
    - `BranchStore`: embeds `branch.Store`.
    - `JobStore`: embeds `branch.JobStore` and `worker.JobStore` (cancel-ready for future endpoints; satisfies `worker.SubmitPDFJob` and handler reads).
    - `QuotaStore`: `Quota`, `ReserveQuota`, `CommitUsage`, `ReleaseQuota`.
    - `UserStore`: `User`, `UpsertUser`.

- [ ] **4. Update `internal/api/provisioner.go` and `internal/api/api.go`**
  - In `internal/api/provisioner.go`: update store fields to narrow consumer interfaces compatible with `seed.ProfileSeeder`, `seed.BranchSeeder`, `seed.TemplateSeeder`, and `seed.UserSeeder`. Confirm `internal/api/middleware.go` continues passing `*core.User` cleanly.
  - In `internal/api/api.go`:
    - Update `RouterConfig` store fields to `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `QuotaStore`, `UserStore`, plus `JobStore JobStore`, `JobNotifier core.JobNotifier`, and `MaxPendingPerTenant int`.
    - Remove `PDFJobStore`, `PDFJobEnqueuer`, and `JobEnqueuer`.
    - Update `NewRouterConfigFromStore(st *store.Store)` to populate `ProfileStore: st.Profiles()`, `CareerStore: st.Career()`, `PresetStore: st.Presets()`, `TemplateStore: st.Templates()`, `BranchStore: st.Branches()`, `JobStore: st.Jobs()`, `QuotaStore: st.Quotas()`, `UserStore: st.Users()`, and build `ProfileService: profile.New(st.Profiles(), st.Career(), st.Branches())`.
    - Update `branch.New(branch.Config{...})` (`line 171`) to pass `Jobs: cfg.JobStore`, `JobNotifier: cfg.JobNotifier`, and `MaxPendingPerTenant: cfg.MaxPendingPerTenant`.
    - Ensure route guards in `api.go` / `profile_handlers.go` check `cfg.JobStore != nil` instead of `cfg.PDFJobStore != nil`.

- [ ] **5. Migrate `internal/api/profile_handlers.go` and `internal/api/pdf_handlers.go`**
  - Update PDF route guards (`cfg.JobStore != nil`).
  - Keep sync-by-default behavior when `JobNotifier == nil`:
    ```go
    runAsync := strings.EqualFold(r.URL.Query().Get("async"), "true") ||
        (h.cfg.JobNotifier != nil && !strings.EqualFold(r.URL.Query().Get("sync"), "true"))
    ```
  - In `enqueueAsyncPDFJob`:
    1. If `h.cfg.JobStore == nil`, respond `500` (`"pdf job store unavailable"`).
    2. If `h.cfg.JobNotifier == nil`, respond `503` (`"PDF worker queue not configured on server"`).
    3. Call:
       ```go
       job, err := worker.SubmitPDFJob(r.Context(), worker.PDFSubmitConfig{
           Jobs:                h.cfg.JobStore,
           Quotas:              h.cfg.QuotaStore,
           Notifier:            h.cfg.JobNotifier,
           MaxPendingPerTenant: h.cfg.MaxPendingPerTenant,
       }, worker.PDFJobSubmission{
           UserID:       userID,
           Filename:     filename,
           Provider:     provider,
           Model:        model,
           RawText:      rawText,
           DailyLimit:   h.cfg.DailyRequestLimit,
           MonthlyLimit: h.cfg.MonthlyTokenLimit,
       })
       ```
    4. Keep existing error mapping (`errors.Is(err, core.ErrQueueFull)` -> `503 QUEUE_FULL`, `core.ErrQuotaExceeded` -> `429`, etc.) and return `202` with `newPDFParseJobResponse(job)`.
  - Add documented `PDFParseJobResponse` and `newPDFParseJobResponse(job *core.Job) (*PDFParseJobResponse, bool)` projecting only: `id`, `user_id`, `status`, `filename`, `provider`, `model`, `result`, `error_message`, `created_at`, `updated_at`, `completed_at`.
  - Guard `if job == nil || job.Kind != core.JobKindPDFParse || job.PDF == nil { return nil, false }` so `getPDFParseJob` returns `404` for non-PDF jobs (or `core.ErrNotFound`) and never leaks `PDF.RawText`, `ReservationID`, or `ClaimedBy`.

- [ ] **6. Migrate `internal/api/branch_handlers.go`, `response.go`, `version_handlers.go`, `preset_handlers.go`**
  - In `branch_handlers.go` & `response.go`: remove `CustomDirectives` (`custom_directives`) and `TailoringStrategyID` (`tailoring_strategy_id`) from `CreateBranchRequest`, `UpdateBranchRequest`, `PromptPreviewRequest`, handler reads/writes, and `branchResponse` (`branchToResponse`). Delete dead helper `firstNonNilStringPtr`. Expose `Epoch` (`json:"epoch"`) on `branchResponse` and `BranchEpoch` (`json:"branch_epoch"`), `JobID` (`json:"job_id,omitempty"`) on `branchVersionResponse` (preserving `TemplateSetID` on branches and `RenderPreviewRequest`).
  - In `version_handlers.go` (`line 418`): change `ver.ResumeID != b.ID` to `ver.BranchID != b.ID` in `loadRenderableVersion`.
  - In `preset_handlers.go`: remove `TemplateSetID` (`template_set_id`) from Preset & Strategy request/response structs and handler assignments, keeping `SuggestedThemeID` (`suggested_theme_id`) canonical.

- [ ] **7. Update existing `internal/api/*_test.go` fixtures and mocks to new APIs**
  - Update store types & `memStore.<SubStore>().*` accessors across:
    - `middleware_test.go` (`noopProfileStore` embeds `ProfileStore`)
    - `branch_model_selection_test.go` (`memStore.<SubStore>().*`)
    - `prompt_preview_test.go` (`PromptPreviewRequest` and `core.Branch` using `BranchInstructions`/`PresetID`; `memStore.<SubStore>().*`)
    - `preset_validation_test.go` (`SuggestedThemeID`)
    - `branch_preview_dynamic_test.go` (`SaveCompletedVersion`)
    - `strategy_handlers_test.go` (`SuggestedThemeID`)
    - `quota_handlers_test.go` (`singleUsageRecord` takes a local interface `{ ListUsageRecords(context.Context, string, int) ([]core.LLMUsageRecord, error) }`; update `memStore.Quotas()` / `Branches()` / `Presets()` / `Profiles()` calls)
    - `resume_handlers_test.go`:
      - Update `SuggestedThemeID` (`line 375`), `memStore.<SubStore>().*`, and migrate legacy `tailoring_strategy_id` tests to `preset_id` (keeping `TemplateSetID` branch tests).
      - Replace `CreateVersion` / `CompleteVersion` completed fixtures with `SaveCompletedVersion`.
      - Replace pending (`409`) version fixture with `memStore.Jobs().SubmitBranchTailor(ctx, pendingJob, pendingVer, core.PendingJobOptions{})` and failed (`422` nil `TailoredContent`) fixture with `SubmitBranchTailor` + `ClaimNext` + `FailClaimed`.
    - `api_test.go`:
      - Replace `core.*Store` & `Channel*Enqueuer` in `testEnv` with `*store.Store`, `MaxPendingPerTenant`, and `core.JobNotifierFunc` / `*worker.Pool`.
      - Update `memStore.<SubStore>().*`, `CreateVersion` -> `SaveCompletedVersion`, `CreatePresetRequest.TemplateSetID` -> `SuggestedThemeID`, `deadlineSpyPDFJobStore` -> wraps `api.JobStore`.
      - Update `TestProfilePDF_Parse_AsyncLifecycle` to drive job status through `*store.Jobs` (`ClaimNext` + `CompletePDFParse`) / `*worker.Pool` and assert `PDFParseJobResponse` fields.
      - Update `TestErrorEnvelopes/503_QUEUE_FULL` to configure `MaxPendingPerTenant: 1`, submit 1 pending job (so the 2nd hits `core.ErrPendingLimitExceeded` -> `core.ErrQueueFull` -> `503`), and confirm `503 QUEUE_FULL`.

- [ ] **8. Verify `internal/api` tests pass under `-race`**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./internal/api/...
    ```
    Expected: `PASS`.

- [ ] **9. Commit Task 3**
  - Run:
    ```bash
    git add -A internal/api
    git commit -m "refactor(api): migrate internal/api to composite store, durable jobs, and greenfield models"
    ```

---

### Task 4: Add Greenfield API Regression Tests & Update OpenAPI Specification

**Interfaces:**
- Produces updated OpenAPI schemas matching `internal/api` wire responses (`Branch`, `BranchVersion`, `Preset`, `TailoringStrategy`, `PromptPreviewRequest`, `PDFParseJob`, `VersionStatus`, `PDFParseJobStatus`) and verified by `api/openapi_test.go`.

**Files:**
- Modify: `internal/api/api_test.go`
- Modify: `internal/api/resume_handlers_test.go`
- Modify: `api/openapi.yaml` (`1404-1428`, `2208-2216`, `2871-3024`, `3097-3315`, `3653-3792`)
- Check: `api/openapi_test.go`, `api/godoc_test.go`, `api/import_boundary_test.go`, `api/interface_assertion_test.go`

**Steps:**
- [ ] **1. Add failing/targeted greenfield regression tests in `internal/api/api_test.go` & `resume_handlers_test.go`**
  - Add `TestGetPDFParseJob_NonPDFJobReturns404`: submit a `branch_tailor` job into `memStore.Jobs()`, call `GET /profile/pdf/jobs/{branchTailorJobID}`, assert HTTP `404` and zero information leak.
  - Add `TestProfilePDF_Parse_ResponseOmitsInternalJobFields`: assert neither `POST /profile/pdf/parse` nor `GET /profile/pdf/jobs/{id}` JSON contains `"raw_text"`, `"reservation_id"`, or `"claimed_by"`.
  - Add `TestProfilePDF_Parse_SyncByDefaultWithoutNotifier_AndAsync503`: confirm `JobNotifier == nil` executes sync (`200`), `?async=true` with `JobNotifier == nil` returns `503`, and `?async=true` with `JobStore == nil` returns `500`.
  - Assert in branch/version/preset tests that `Branch` responses contain `"epoch"` and omit `"custom_directives"`/`"tailoring_strategy_id"`, `BranchVersion` responses contain `"branch_epoch"` (and `"job_id"` on job-backed versions), and `Preset`/`Strategy` responses omit `"template_set_id"` while preserving `"suggested_theme_id"`.

- [ ] **2. Run `internal/api` tests and observe OpenAPI drift on `api/...`**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./internal/api/...
    GOWORK=off go test -race -v ./api/...
    ```
    Expected: `./internal/api/...` `PASS`; check `api/openapi_test.go` coverage and contract behavior.

- [ ] **3. Update `api/openapi.yaml` for greenfield contracts**
  - `/branches/{id}/generate` (`1412-1413`): update description wording from `"in-memory worker pool"` to `"durable job queue"`.
  - `VersionStatus` (`2208-2216`): add `cancelled`.
  - `PDFParseJobStatus` (`3740-3747`): add `cancelled`.
  - `Preset`, `CreatePresetRequest`, `UpdatePresetRequest`, `TailoringStrategy`, `CreateStrategyRequest`, `UpdateStrategyRequest` (`2871-3024`): remove `template_set_id`; ensure `suggested_theme_id` is canonical.
  - `Branch`, `CreateBranchRequest`, `UpdateBranchRequest` (`3097-3265`): remove `custom_directives`, `tailoring_strategy_id`; add `epoch` (`type: integer`, `format: int64`) to `Branch`. Keep `template_set_id` on `Branch` / `CreateBranchRequest` / `UpdateBranchRequest`.
  - `PromptPreviewRequest` (`3279-3315`): remove `custom_directives`, `tailoring_strategy_id`.
  - `BranchVersion` (`3653-3708`): add `branch_epoch` (`type: integer`, `format: int64`) and `job_id` (`type: string`).
  - Verify `PDFParseJob` (`3749-3792`) preserves flat fields (`filename`, `provider`, `model`, `result`) and uses updated `PDFParseJobStatus`.

- [ ] **4. Verify all `api` AST guardrails and OpenAPI suite pass**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./api/... ./internal/api/...
    ```
    Expected: `PASS` across all `api/*_test.go` (36 endpoints, `godoc_test.go`, `import_boundary_test.go`, `interface_assertion_test.go`) and `internal/api/...`.

- [ ] **5. Commit Task 4**
  - Run:
    ```bash
    git add api/openapi.yaml internal/api/api_test.go internal/api/resume_handlers_test.go
    git commit -m "test(api): add greenfield job/wire tests and update OpenAPI schemas"
    ```

---

### Task 5: Migrate `internal/mcp` Consumer Interfaces, Tools & Tests

**Interfaces:**
- Produces (`internal/mcp/store.go`): `ProfileStore`, `CareerStore`, `PresetStore`, `TemplateStore`, `BranchStore`, `UserStore` (every exported and unexported type/method documented for `godoc_test.go`).

**Files:**
- Create: `internal/mcp/store.go`
- Modify: `internal/mcp/config.go` (`39-53`)
- Modify: `internal/mcp/branch_tools.go` (`308-310`, `409-443`, `503-504`)
- Modify: `internal/mcp/preset_tools.go` (`156`, `224`)
- Modify (test alignment): `internal/mcp/http_test.go` (`201`, `437`, `514`), `internal/mcp/refresh_test.go` (`66-588`, `708-738`, `829`, `909`), `internal/mcp/mcp_test.go` (`92-175`, `564-653`, `828`, `926-928`, `1052`, `1132`, `1927-1931`)
- Inspect for any remaining references: `internal/mcp/mcp.go`, `internal/mcp/prompts.go`, `internal/mcp/resources.go`, `internal/mcp/tools.go`, `internal/mcp/profile_tools.go`

**Steps:**
- [ ] **1. Run `go test ./internal/mcp/...` to record baseline compile failures**
  - Run:
    ```bash
    GOWORK=off go test ./internal/mcp/...
    ```
    Expected: FAIL on `core.*Store`, `branch.JobEnqueuer`, and removed `Branch`/`Preset` fields.

- [ ] **2. Create `internal/mcp/store.go` and update `internal/mcp/config.go`**
  - Define documented consumer interfaces in `internal/mcp/store.go`:
    - `ProfileStore`: `Profile`, `UpsertProfile`, `Snapshot` (satisfies `seed.ProfileSeeder`).
    - `CareerStore`: `ListSkills`, `ListGlobalSkills`, `Skill`, `CreateSkill`, `UpdateSkill`, `Experience`, `CreateExperience`, `Project`, `Education`, `CreateFact`.
    - `PresetStore`: `ListPresets`, `Preset`, `CreatePreset`, `UpdatePreset` (satisfies `access.PresetReader`).
    - `TemplateStore`: `Template`, `CreateTemplate` (satisfies `access.TemplateReader` and `seed.TemplateSeeder`).
    - `BranchStore`: `ListBranches`, `Branch`, `CreateBranch`, `UpdateBranch`, `Version`, `VersionByNumber`, `SaveCompletedVersion` (satisfies `seed.BranchSeeder`).
    - `UserStore`: `User`, `UpsertUser` (satisfies `seed.UserSeeder`).
  - Update `ServerConfig` in `internal/mcp/config.go` (`lines 39-53`) to use the `mcp` consumer interfaces and remove `JobEnqueuer branch.JobEnqueuer` (inspect `tools.go`/`resources.go`/`mcp.go` to confirm any other store fields; `MCP` delegates branch tailoring to `BranchService`).

- [ ] **3. Migrate `internal/mcp/branch_tools.go` and `internal/mcp/preset_tools.go`**
  - In `branch_tools.go`: drop `tailoring_strategy_id` and `custom_directives` MCP arguments/schema descriptions and reads/writes on `core.Branch`; keep `branch_instructions`, `preset_id`, and `template_set_id` (on branches / `render_resume`).
  - In `preset_tools.go`: drop `template_set_id` MCP arguments, schema descriptions, and `core.Preset.TemplateSetID` normalization/writes; keep `suggested_theme_id` canonical.

- [ ] **4. Migrate `internal/mcp/*_test.go` suites**
  - Update `http_test.go` (`failingProfileStore` embeds `ProfileStore`, sub-store accessors on `memStore`).
  - Update `refresh_test.go` (`branch.Config` wired with `Jobs: memStore.Jobs()` and `JobNotifier`, sub-store accessors on `memStore`, embed `mcp` consumer interfaces at lines `708-738`).
  - Update `mcp_test.go` (embed `mcp` consumer interfaces, update `memStore.<SubStore>().*`, replace `CreateVersion`/`CompleteVersion` with `SaveCompletedVersion`, update `TailoringStrategyID`/`CustomDirectives`/`TemplateSetID` (preset) assertions to `PresetID`/`BranchInstructions`/`SuggestedThemeID`, and verify `create_branch`/`update_branch`/`create_preset` MCP tool calls).

- [ ] **5. Verify `internal/mcp` and `api/godoc_test.go` pass**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./internal/mcp/... ./api/...
    ```
    Expected: `PASS`.

- [ ] **6. Commit Task 5**
  - Run:
    ```bash
    git add -A internal/mcp
    git commit -m "refactor(mcp): migrate internal/mcp to consumer store interfaces and greenfield fields"
    ```

---

### Task 6: Migrate `server` Composition, Options & Tests

**Interfaces:**
- Updates `server.WithStore(*store.Store)` and `server.WithWorkerPoolConfig(concurrency, maxPerTenant int)`.

**Files:**
- Modify: `server/options.go` (`94`, `190`, `297-306`)
- Modify: `server/server.go` (`4`, `52`, `80`, `112`, `144`, `206`, `241`, `262`, `280-294`, `349-433`)
- Modify: `server/server_test.go` (`138`, `160`, `403-405`, `443`)

**Steps:**
- [ ] **1. Run `go test ./server/...` to confirm baseline compile errors**
  - Run:
    ```bash
    GOWORK=off go test ./server/...
    ```
    Expected: FAIL on `core.Store`, `WithWorkerPoolConfig`, `QueueSize`/`PDFJobs`, `FailInterruptedVersions`, `JobEnqueuer`.

- [ ] **2. Update `server/options.go`**
  - Import `github.com/resumeee-app/resumeee-core/store`.
  - Change `WithStore(st *store.Store)` (`Config.Store *store.Store`), remove `Config.WorkerQueueSize`, and change `WithWorkerPoolConfig(concurrency, maxPerTenant int)` to drop `queueSize`.

- [ ] **3. Update `server/server.go`**
  - Change `Server.store` from `core.Store` to `*store.Store` and update log/doc strings.
  - On startup recovery (`line 262`), remove `branchStore.FailInterruptedVersions(...)`; retain `workerPool.RecoverStaleJobs(ctx, "server restarted while generation job was in flight")`.
  - Wire `worker.New(worker.Config{..., Jobs: s.store.Jobs(), Branches: s.store.Branches(), Profiles: s.store.Profiles(), Presets: s.store.Presets(), Templates: s.store.Templates(), Quotas: s.store.Quotas()})` (no `QueueSize`, no `PDFJobs`).
  - Wire `branch.New(branch.Config{..., Branches: s.store.Branches(), Jobs: s.store.Jobs(), Profiles: s.store.Profiles(), Presets: s.store.Presets(), Templates: s.store.Templates(), Quotas: s.store.Quotas(), JobNotifier: workerPool})`.
  - Wire `api.NewRouterConfigFromStore(s.store)` with `JobNotifier: deps.workerPool`.
  - Wire `mcp.ServerConfig` from `s.store` sub-stores (`Branches()`, `Profiles()`, `Career()`, `Presets()`, `Templates()`, `Users()`) and `BranchService`.

- [ ] **4. Update `server/server_test.go`**
  - Update `memStore.UpsertProfile` -> `memStore.Profiles().UpsertProfile` (`lines 138, 443`).
  - Update `WithWorkerPoolConfig(4, 200, 3)` -> `WithWorkerPoolConfig(4, 3)` (`lines 160, 403, 405`) and remove `invalid worker queueSize` subtest (`line 404`).

- [ ] **5. Verify `server` tests pass**
  - Run:
    ```bash
    GOWORK=off go test -race -v ./server/...
    ```
    Expected: `PASS`.

- [ ] **6. Commit Task 6**
  - Run:
    ```bash
    git add server/options.go server/server.go server/server_test.go
    git commit -m "refactor(server): wire composite *store.Store, worker.Pool, and RecoverStaleJobs"
    ```

---

### Task 7: Migrate `cmd/server` & `cmd/mcp`, Add MCP Startup Recovery, Run SQLite E2E & `make verify`

**Interfaces:**
- Updates `StoreInitializer` in `cmd/server` and `cmd/mcp` to `func(ctx context.Context, cfg *config.Config) (*store.Store, error)`.

**Files:**
- Modify: `cmd/server/main.go` (`23-40`, `91`, `138`), `cmd/server/main_test.go` (`46`, `75-137`, `488`, `511`)
- Modify: `cmd/mcp/main.go` (`31-34`, `152`, `178-189`, `267`), `cmd/mcp/main_test.go` (`102`, `215`, `245`, `272`, `283`, `314`)

**Steps:**
- [ ] **1. Run `go test ./cmd/...` to confirm baseline compile failures**
  - Run:
    ```bash
    GOWORK=off go test ./cmd/...
    ```
    Expected: FAIL on `core.Store`, `WorkerQueueSize`, `QueueSize`, `memStore.CreateVersion`.

- [ ] **2. Migrate `cmd/server/main.go` and `cmd/server/main_test.go`**
  - Update `StoreInitializer`, `defaultSQLiteInitializer`, and `closeStore` in `cmd/server/main.go` from `core.Store` to `*store.Store`, and remove `WorkerQueueSize: cfg.WorkerQueueSize`.
  - Update `cmd/server/main_test.go` (`*store.Store`, `memStore.<SubStore>().*`, and in `TestRecoverStaleJobs` replace `CreateVersion`/`UpdateVersionStatus` with `memStore.Jobs().SubmitBranchTailor` + `ClaimNext` before invoking `RecoverStaleJobs`).

- [ ] **3. Migrate `cmd/mcp/main.go` and add stale-job recovery**
  - Update `StoreInitializer`, `defaultSQLiteInitializer`, `buildMCPServerConfig`, and `closeMCPStore` from `core.Store` to `*store.Store`.
  - Update `worker.New` to pass `Jobs: st.Jobs()` (no `QueueSize`) and `branch.New` to pass `Jobs: st.Jobs(), JobNotifier: pool`.
  - Call `pool.RecoverStaleJobs(ctx, "mcp server restarted during job processing")` (logging warning/error if recovery fails) immediately before `pool.Start(ctx)`.
  - Update `cmd/mcp/main_test.go` (`*store.Store`, `memStore.<SubStore>().*`) and add a unit test confirming `cmd/mcp` startup recovers interrupted `processing` jobs via `RecoverStaleJobs` and releases reserved quota.

- [ ] **4. Add SQLite Storage End-to-End Integration Check**
  - In `cmd/server/main_test.go` (guarded with `sql.OpenStore(ctx, filepath.Join(t.TempDir(), "e2e.db"))`), verify end-to-end against real `resumeee-storage-sql`:
    1. Seed tenant + profile + branch + quota.
    2. Submit and claim a `processing` job, create a fresh `worker.Pool` and call `RecoverStaleJobs`, asserting job status transitions to `failed` and quota reservation is released (`ReservedTokens == 0`).
    3. Start `*worker.Pool` + router (`NewRouterConfigFromStore` + `JobNotifier: pool`), submit an async `POST /profile/pdf/parse?async=true` job, poll `GET /profile/pdf/jobs/{id}` until `completed`, and verify response fields (`filename`, `provider`, `model`, `result`, omitting `raw_text`, `reservation_id`, `claimed_by`).
    4. Trigger an async `POST /branches/{id}/generate` job, poll `GET /branches/{id}/versions/{v}` until `completed`, and verify `branch_epoch`, `job_id`, and rendered/tailored content.

- [ ] **5. Run full binary build & `make verify`**
  - Run:
    ```bash
    GOWORK=off GOPRIVATE=github.com/resumeee-app/* go build ./cmd/server ./cmd/mcp
    GOWORK=off GOPRIVATE=github.com/resumeee-app/* make verify
    ```
    Expected: `gofmt`, `go test -race ./...`, `golangci-lint run ./...`, `api/*_test.go`, `cmd/*`, `server/*`, and all `internal/*` tests exit `0` with zero failures or skipped assertions.

- [ ] **6. Verify post-commit cleanliness & finalize Task 7**
  - Run:
    ```bash
    git add cmd/server/main.go cmd/server/main_test.go cmd/mcp/main.go cmd/mcp/main_test.go
    git commit -m "refactor(cmd): migrate entrypoints, add MCP RecoverStaleJobs, and verify SQLite E2E"
    git status -sb
    ```
    Expected: clean working tree with all 7 tasks committed.
