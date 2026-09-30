---
change: runtime-management-gap
design-doc: docs/superpowers/specs/2026-10-01-runtime-management-gap-design.md
base-ref: 8774f7b3576bbde9f41a4dab3941f76ca51f1b9e
---

# Runtime Inventory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Deliver the first independently testable runtime-control-plane slice: persistent runtime inventory, manifest/release readback, bounded health transitions, and a read-only operations view.

**Architecture:** Add a Manager-owned `runtime` domain with a GORM repository, business service, and chi HTTP handler. Existing Edge, Worker, TeamHarness/plugin, and preview-pg surfaces are wrapped by read-only adapters; no executor is rewritten and no scheduler is introduced. The existing `/admin/runtime` page gains an inventory table backed by the new API.

**Tech Stack:** Go 1.x, GORM with SQLite/MySQL compatibility, chi router, React/TypeScript existing web stack, `go test`.

**Spec:** `openspec/changes/runtime-management-gap/specs/runtime-management/spec.md`; design: `docs/superpowers/specs/2026-10-01-runtime-management-gap-design.md`.

## Global Constraints

- Manager is the only authority for registry facts, health decisions, timing thresholds, and admission.
- Runtime manifests report identity/profile/capacity but cannot override Manager policy.
- Accepted version provenance is only `manifest` or `release_metadata`; signature state is separate.
- Version or digest drift preserves liveness observation and yields `degraded`, not a rejected heartbeat.
- Health values are exactly `starting`, `online`, `degraded`, `recently_lost`, and `offline`.
- The first slice is read-only for non-Edge runtimes and must not change incident, HITL, or execution semantics.
- Existing tests, formatting, module boundaries, and AGENTS instructions remain in force.

---

### Task 1: Runtime Persistence Model

**Files:**
- Create: `internal/manager/model/runtime/model.go`
- Create: `internal/manager/biz/runtime/types.go`
- Create: `internal/manager/data/runtime/store/repo.go`
- Create: `internal/manager/data/runtime/store/migrate.go`
- Create: `internal/manager/data/runtime/store/repo_test.go`
- Modify: `cmd/opskeeper/main.go`

**Interfaces:**
- Produces `model.Runtime` with stable string `RuntimeID` and GORM table `runtimes`.
- Produces `runtime.Repo` methods used by later tasks:
  - `Upsert(ctx context.Context, item model.Runtime) (model.Runtime, error)`
  - `Get(ctx context.Context, runtimeID string) (model.Runtime, error)`
  - `List(ctx context.Context, filter runtime.ListFilter) ([]model.Runtime, error)`
  - `RecordHeartbeat(ctx context.Context, runtimeID string, beat runtime.ObservedReadback) (model.Runtime, error)`
  - `UpdateHealth(ctx context.Context, runtimeID string, health string, reason string, at time.Time) (model.Runtime, error)`

- [ ] **Step 1: Write repository and migration tests**

Create tests that open SQLite in memory, call `Migrate`, and prove:

```go
func TestRepo_UpsertIsIdempotentByID(t *testing.T) {
    repo := newTestRepo(t)
    first, err := repo.Upsert(context.Background(), validRuntime("edge-1"))
    if err != nil { t.Fatalf("first upsert: %v", err) }
    second, err := repo.Upsert(context.Background(), validRuntime("edge-1"))
    if err != nil { t.Fatalf("second upsert: %v", err) }
    if first.ID != second.ID || first.RuntimeID != second.RuntimeID {
        t.Fatalf("runtime identity changed: first=%+v second=%+v", first, second)
    }
}

func TestRepo_RecordHeartbeatPreservesDesiredFields(t *testing.T) {
    repo := newTestRepo(t)
    item, err := repo.Upsert(context.Background(), validRuntime("worker-1"))
    if err != nil { t.Fatalf("upsert: %v", err) }
    got, err := repo.RecordHeartbeat(context.Background(), item.RuntimeID, validReadback())
    if err != nil { t.Fatalf("heartbeat: %v", err) }
    if got.ObservedVersion != "2026.09.14-rc4" || got.VersionSource != "release_metadata" {
        t.Fatalf("version readback not persisted: %+v", got)
    }
    if got.LastHeartbeatAt.IsZero() { t.Fatal("last heartbeat is zero") }
}

func TestRepo_ListFiltersHealthAndKind(t *testing.T) {
    repo := newTestRepo(t)
    seedRuntimes(t, repo,
        runtimeWithHealth("edge-1", "edge", "online"),
        runtimeWithHealth("worker-1", "manager_chat_worker", "degraded"),
    )
    got, err := repo.List(context.Background(), runtime.ListFilter{Kind: "edge", Health: "online"})
    if err != nil { t.Fatalf("list: %v", err) }
    if len(got) != 1 || got[0].RuntimeID != "edge-1" {
        t.Fatalf("filtered runtimes = %+v", got)
    }
}
```

- [ ] **Step 2: Run the failing persistence tests**

Run: `go test ./internal/manager/data/runtime/store -run 'TestRepo_' -count=1`

Expected result: package does not exist or symbols are undefined.

- [ ] **Step 3: Implement the model and repository**

The model must persist these groups separately:

```go
type Runtime struct {
    ID uint64
    RuntimeID string
    Kind string
    Profile string
    OwnerID *uint64
    DeploymentLocation string
    LabelsJSON string
    CapabilityDigest string
    ConfiguredSlots int
    ConfiguredCPUMillis int64
    ConfiguredMemoryMB int64
    ConfiguredDiskMB int64
    Health string
    HealthReason string
    LastHeartbeatAt *time.Time
    AcknowledgementCursor string
    ObservedVersion string
    ObservedArtifactDigest string
    VersionSource string
    ObservedSlots int
    ActiveTasks int
    ResourceJSON string
    WorkspaceJSON string
    CreatedAt time.Time
    UpdatedAt time.Time
    DeletedAt *time.Time
}
```

Use unique index `runtime_id`, check constraints for health/kind/version source, and JSON text columns for labels/resources/workspace. `Upsert` must update only observed readback and mutable metadata fields; it must never replace `RuntimeID` or configured policy.

- [ ] **Step 4: Register the migration**

Add the runtime data import and put `managerruntimedata.Migrate` before `manageredgedata.Migrate` in `dbx.RunMigrations`. This permits a later Edge backfill to resolve persisted edge IDs while keeping runtime ownership in the new domain.

- [ ] **Step 5: Run persistence tests and Go formatting**

Run:

```bash
go test ./internal/manager/data/runtime/store -count=1
gofmt -w internal/manager/model/runtime/model.go internal/manager/biz/runtime/types.go internal/manager/data/runtime/store/*.go
```

Expected result: all tests pass.

### Task 2: Health and Readback Policy Service

**Files:**
- Create: `internal/manager/biz/runtime/service.go`
- Create: `internal/manager/biz/runtime/health.go`
- Create: `internal/manager/biz/runtime/service_test.go`
- Create: `internal/manager/biz/runtime/health_test.go`

**Interfaces:**
- Consumes `runtime.Repo` and `runtime.ReleaseCatalog`.
- Produces:
  - `Service.Register(ctx context.Context, input RegisterInput) (model.Runtime, error)`
  - `Service.Heartbeat(ctx context.Context, runtimeID string, beat ObservedReadback) (model.Runtime, error)`
  - `EvaluateHealth(current model.Runtime, now time.Time, policy HealthPolicy) HealthDecision`
- Produces `ReleaseCatalog.Lookup(ctx context.Context, runtimeKind string) (ReleaseFact, error)`.

- [ ] **Step 1: Write failing policy tests**

Cover at least:

```go
func TestEvaluateHealth_OneMissedBeatStaysOnline(t *testing.T) {
    decision := EvaluateHealth(onlineRuntime(), onlineRuntime().LastHeartbeatAt.Add(5*time.Second), defaultPolicy())
    if decision.Health != "online" || decision.Reason != "" {
        t.Fatalf("decision = %+v", decision)
    }
}

func TestEvaluateHealth_BeyondGraceBecomesRecentlyLost(t *testing.T) { /* assert recently_lost */ }
func TestEvaluateHealth_ReconnectFailureBecomesOffline(t *testing.T) { /* assert offline */ }
func TestHeartbeat_VersionDriftDegradesWithoutDroppingLiveness(t *testing.T) { /* assert heartbeat persisted then degraded */ }
func TestHeartbeat_RejectsHardcodedVersionSource(t *testing.T) {
    svc, repo := newTestService(t)
    item, err := svc.Register(context.Background(), validRegisterInput("worker-1"))
    if err != nil { t.Fatalf("register: %v", err) }
    beat := validReadback()
    beat.VersionSource = "hardcoded"
    got, err := svc.Heartbeat(context.Background(), item.RuntimeID, beat)
    if err != nil { t.Fatalf("heartbeat should preserve liveness: %v", err) }
    if got.Health != "degraded" || got.HealthReason != "version_source_rejected" {
        t.Fatalf("health = %+v", got)
    }
    if got.LastHeartbeatAt.IsZero() { t.Fatal("forbidden source erased heartbeat") }
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./internal/manager/biz/runtime -count=1`

Expected result: service and health functions are undefined.

- [ ] **Step 3: Implement validation and transitions**

Validation rules:

```go
var allowedVersionSources = map[string]bool{
    "manifest": true,
    "release_metadata": true,
}

var allowedHealth = map[string]bool{
    "starting": true,
    "online": true,
    "degraded": true,
    "recently_lost": true,
    "offline": true,
}
```

`Heartbeat` first persists the liveness/readback observation, then applies version policy. A digest mismatch or forbidden source records `degraded` and a reason such as `version_drift` or `version_source_rejected`; it never deletes the heartbeat timestamp. `ReleaseCatalog` returns expected version/digest from manifest/release metadata only.

- [ ] **Step 4: Run focused service tests**

Run: `go test ./internal/manager/biz/runtime -count=1`

Expected result: all tests pass.

### Task 3: Manager Runtime API

**Files:**
- Create: `internal/manager/server/runtime/http.go`
- Create: `internal/manager/server/runtime/http_test.go`
- Modify: `cmd/opskeeper/main.go`

**Interfaces:**
- Consumes `runtime.Service`.
- Produces protected routes:
  - `GET /v1/runtimes`
  - `GET /v1/runtimes/{runtimeId}`
  - `POST /v1/runtimes/register`
  - `POST /v1/runtimes/{runtimeId}/heartbeat`

- [ ] **Step 1: Write handler contract tests**

Use a fake service and chi router, as existing manager HTTP tests do. Assert:

- list returns `{ "items": [...] }` and never exposes labels/resources as unvalidated raw secrets;
- get returns 404 JSON for unknown runtime;
- register rejects empty runtime ID/kind and forbidden version source with 400;
- heartbeat accepts a drift heartbeat and returns `degraded`;
- acknowledgement cursor is persisted in the request payload;
- no route mutates incident, plugin, or execution state.

- [ ] **Step 2: Run handler tests before implementation**

Run: `go test ./internal/manager/server/runtime -count=1`

Expected result: package does not exist.

- [ ] **Step 3: Implement DTOs and routes**

DTO naming must match the API contract exactly: `runtime_id`, `kind`, `profile`, `deployment_location`, `labels`, `capabilities`, `version`, `version_source`, `artifact_digest`, `health`, `health_reason`, `last_heartbeat_at`, `capacity`, `resource_usage`, `active_tasks`, and `command_ack_cursor`.

Decode into a bounded request struct; reject unknown fields where the existing handler style supports strict decoding. Return `errs.ErrInvalid` mapping for malformed input and `errs.ErrNotFound` for missing rows.

- [ ] **Step 4: Wire Manager startup**

Instantiate the repository and service after the DB is opened, pass the service to the handler, and register it on the existing authenticated `protected` chi router near the edge handler. Add a read-only authz object name such as `runtime:*`.

- [ ] **Step 5: Run API and compilation tests**

Run:

```bash
go test ./internal/manager/server/runtime ./internal/manager/biz/runtime ./internal/manager/data/runtime/store -count=1
go test ./cmd/opskeeper -run TestNothing -count=1
```

The second command is a compile-oriented test invocation; it must exit zero.

### Task 4: Manifest Release Facts and Five Read-only Adapters

**Files:**
- Create: `internal/manager/biz/runtime/release.go`
- Create: `internal/manager/biz/runtime/release_test.go`
- Create: `internal/manager/service/runtimeinventory/provider.go`
- Create: `internal/manager/service/runtimeinventory/provider_test.go`
- Create: `internal/manager/service/runtimeinventory/edge.go`
- Create: `internal/manager/service/runtimeinventory/local.go`
- Create: `internal/manager/service/runtimeinventory/preview.go`
- Modify: `cmd/opskeeper/main.go`
- Modify: `scripts/check_release_version.py`
- Modify: `scripts/verify_release.py`

**Interfaces:**
- Produces `Provider.Snapshot(ctx context.Context) ([]runtime.RegisterInput, error)`.
- Produces release metadata field `runtime_artifacts`:

```json
{
  "manager": "sha256:<64-hex-package-digest>",
  "manager_chat_worker": "sha256:<64-hex-package-digest>",
  "agentteams_worker": "sha256:<64-hex-package-digest>",
  "teamharness": "sha256:<64-hex-plugin-artifact-digest>",
  "plugin_manager": "sha256:<64-hex-package-digest>",
  "preview_pg": "sha256:<64-hex-image-or-bundle-digest>"
}
```

- [ ] **Step 1: Write release metadata tests**

Assert the catalog:

- reads only `RELEASE_VERSION.json` or a release manifest provided by deployment configuration;
- rejects a missing/invalid `runtime_artifacts` entry for a required kind;
- rejects a digest that is not `sha256:` plus 64 lowercase hexadecimal characters;
- does not fall back to a Go constant, UI string, or script literal for version/digest authority.

- [ ] **Step 2: Extend release packaging validation**

Update release scripts so a signed release refuses to publish when any required runtime artifact digest is absent. In local development, absent metadata yields a `starting` runtime with `release_metadata_incomplete`; it must never synthesize a digest.

- [ ] **Step 3: Implement read-only adapters**

The provider returns five first-slice descriptors:

```go
var firstSliceKinds = []string{
    "manager_chat_worker",
    "agentteams_worker",
    "teamharness_plugin_runtime",
    "plugin_manager",
    "preview_pg",
}
```

Edge descriptors are generated from the existing edge repository and tunnel status; each Edge maps to one `node_runtime` row with stable ID `edge:<numeric-edge-id>`. Local descriptors use process liveness and release metadata. Preview-pg uses its existing configured fixture endpoint and returns degraded—not offline—when the endpoint is intentionally absent in local mode.

- [ ] **Step 4: Register and refresh adapters**

At startup, reconcile all descriptors with `Service.Register`. Start one bounded ticker using Manager policy; on each tick, call each provider and submit a heartbeat. Provider errors mark only that runtime degraded and never panic Manager startup.

- [ ] **Step 5: Run adapter and release tests**

Run:

```bash
go test ./internal/manager/biz/runtime ./internal/manager/service/runtimeinventory -count=1
python3 scripts/check_release_version.py
```

Expected result: all commands pass.

### Task 5: Runtime Inventory View and Slice Regression

**Files:**
- Create: `web/src/api/runtimes.ts`
- Modify: `web/src/pages/admin/Runtime.tsx`
- Modify: `openspec/changes/runtime-management-gap/tasks.md`

**Interfaces:**
- Produces `listRuntimes(): Promise<RuntimeListResponse>`.
- Produces a compact inventory table with kind, stable ID, profile, health, version, digest, heartbeat age, capacity, and active tasks.

- [ ] **Step 1: Add typed API wrapper and tests where the web test framework supports it**

The wrapper must mirror backend JSON names and expose:

```ts
export type RuntimeHealth = 'starting' | 'online' | 'degraded' | 'recently_lost' | 'offline';
export type RuntimeRecord = {
  runtime_id: string;
  kind: string;
  profile: string;
  deployment_location: string;
  health: RuntimeHealth;
  health_reason?: string;
  version: string;
  version_source: 'manifest' | 'release_metadata';
  artifact_digest: string;
  last_heartbeat_at?: string | null;
  active_tasks: number;
};
export function listRuntimes() { return request<RuntimeListResponse>('GET', '/runtimes'); }
```

- [ ] **Step 2: Extend the existing Runtime page**

Add an inventory section above the detailed deployment cards. Use the existing `Card`, `Row`, polling interval, bilingual copy, and status tone patterns. Digests are truncated for display but retain full text in the `title` attribute. No control actions are exposed in this slice.

- [ ] **Step 3: Run focused web checks**

Run from `web`:

```bash
pnpm typecheck
pnpm test
```

Expected result: TypeScript and Vitest pass. Do not add a new formatter or test framework.

- [ ] **Step 4: Check off only completed OpenSpec tasks**

Mark tasks `1.1` through `1.6` complete only after repository, service, API, adapters, metadata validation, health transitions, and inventory view all pass focused tests. Do not check off task groups 2–7.

- [ ] **Step 5: Run the slice regression**

Run:

```bash
go test ./internal/manager/biz/runtime ./internal/manager/data/runtime/store ./internal/manager/server/runtime ./internal/manager/service/runtimeinventory -count=1
openspec validate runtime-management-gap --strict --json
```

Expected result: all tests and strict validation pass.

## Plan Self-Review

- Covers OpenSpec tasks `1.1`–`1.6`: persistence, APIs/adapters, metadata authority, health transitions, tests, and read-only UI.
- Defers task groups 2–7 to later independently reviewable plans for durable task lifecycle, workspace, credentials, plugin rollout, timeline, and end-to-end verification.
- Uses exact Manager-owned policy correction from the confirmed design.
- No placeholder values are used for release digests; missing release metadata is explicitly degraded/starting rather than synthesized.
