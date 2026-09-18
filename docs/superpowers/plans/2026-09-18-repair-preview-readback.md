---
change: repair-preview-readback
design-doc: docs/superpowers/specs/2026-09-18-repair-preview-readback-design.md
base-ref: e863f9e04c2d145c37e42d14114e7e697ff2fe6b
---

# Repair Preview Readback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build incident-bound, controlled repair-preview evaluation, HITL eligibility gating, and TeamHarness Archive readback.

**Architecture:** Manager owns durable preview runs/candidates and exposes sanitized compact/archive summaries. A standalone runner executes deterministic baseline/A/B workloads in disposable PostgreSQL branches and submits persisted results. TeamHarness remains read-only and renders either the approval-gate summary or the full post-closure table.

**Tech Stack:** Go 1.24, GORM/PostgreSQL, pgx v5, Chi HTTP, React 19 Dashboard plugin, Vitest-style Node test runner used by the existing plugin package.

**Spec:** `openspec/changes/repair-preview-readback/specs/repair-preview-readback/spec.md` and `docs/superpowers/specs/2026-09-18-repair-preview-readback-design.md`.

## Global Constraints

- Do not modify AgentTeams or Dashboard host source.
- Manager is the only repair-preview authority; TeamHarness is read-only.
- A preview PASS only confers HITL eligibility and never records human approval.
- Preview branches reconstruct runtime faults with bounded controlled load and must state that original active sessions are not copied.
- API output must be tenant-isolated, bounded, and must not expose credentials, raw SQL parameters, production data, or unbounded artifacts.
- Legacy incidents must still load; missing optional preview evidence must not make their existing archive completeness fail.
- All code edits use `apply_patch`; commit after each completed task.

---

### Task 0: Commit OpenSpec and Design Baseline

**Files:**
- Commit: `openspec/changes/repair-preview-readback/`
- Commit: `docs/superpowers/specs/2026-09-18-repair-preview-readback-design.md`

**Interfaces:**
- Consumes: the user-confirmed OpenSpec artifacts and Design Doc.
- Produces: a clean baseline containing only this change's planning artifacts.

- [ ] **Step 1: Validate the baseline**

Run:

```bash
openspec validate repair-preview-readback --strict
git diff --check
```

Expected: both commands exit 0.

- [ ] **Step 2: Commit the baseline**

Run:

```bash
git add openspec/changes/repair-preview-readback docs/superpowers/specs/2026-09-18-repair-preview-readback-design.md docs/superpowers/plans/2026-09-18-repair-preview-readback.md
git commit -m "docs: design repair preview readback"
```

Expected: one commit containing the OpenSpec change, Design Doc, and this plan.

### Task 1: Repair-Preview Domain and Storage

**Files:**
- Create: `internal/control/repairpreview/model.go`
- Create: `internal/control/repairpreview/model_test.go`
- Create: `internal/control/repairpreview/repository.go`
- Create: `internal/control/repairpreview/repository_test.go`
- Create: `internal/control/repairpreview/migrate.go`
- Modify: `deploy/postgres-init.sql`
- Create: `deploy/postgres-migration-repair-preview.sql`
- Modify: `cmd/opskeeper/main.go` migration list

**Interfaces:**
- Consumes: existing GORM `*gorm.DB` and the incident timeline repository.
- Produces:

```go
package repairpreview

type Decision string

const (
	DecisionPass   Decision = "PASS"
	DecisionFail   Decision = "FAIL"
	DecisionReject Decision = "REJECTED_BY_PREVIEW"
)

type Run struct {
	ID                 string
	TenantID           string
	IncidentID         string
	BranchPrefix       string
	SeedFingerprint    string
	WorkloadFingerprint string
	WorkloadRevision   string
	ControlledLoad     bool
	IsolationBoundary  string
	Status             string
	StartedAt          time.Time
	FinishedAt         time.Time
	ErrorSummary       string
	ArtifactRef        string
	Candidates         []Candidate
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Candidate struct {
	ID                 string
	RunID              string
	TenantID           string
	IncidentID         string
	CandidateID        string
	Name               string
	Kind               string
	Action             string
	ChangeSummary      string
	Branch             string
	ResultChecksum     string
	Consistent         bool
	AverageLatencyMS   float64
	MedianLatencyMS    float64
	P95LatencyMS       float64
	SampleCount        int
	TPS                float64
	ErrorCount         int
	WriteImpact        string
	StorageDeltaBytes  int64
	BusinessProbePass  bool
	Decision           Decision
	RejectionReason    string
}

type Repository interface {
	Save(ctx context.Context, run Run) error
	ListByIncident(ctx context.Context, tenantID, incidentID string, limit int) ([]Run, error)
	FindEligible(ctx context.Context, tenantID, incidentID, runID, candidateID, action string) (Candidate, error)
}
```

- [ ] **Step 1: Write model tests**

Create tests named:
  - `TestRunValidate_BindsTenantAndIncident`
  - `TestEvaluateCandidate_PassRequiresCompleteMetrics`
  - `TestEvaluateCandidate_RejectsChecksumDivergence`
  - `TestEvaluateCandidate_RejectsWriteLossOrFailedBusinessProbe`
  - `TestEvaluateCandidate_DoesNotRecordHumanApproval`

Use fixed values: seed fingerprint `sha256:seed-v1`, workload fingerprint `sha256:workload-v1`, Candidate A action `resize_pool`, Candidate B action `reset_pool`.

- [ ] **Step 2: Verify tests fail**

Run:

```bash
go test ./internal/control/repairpreview -run 'Test(RunValidate|EvaluateCandidate)' -count=1
```

Expected: package does not exist or tests fail.

- [ ] **Step 3: Implement model and decision policy**

Implement `Validate`, `Evaluate`, and `SanitizeErrorSummary`.

PASS requires:

- non-empty checksum and consistency metrics;
- `SampleCount > 0`;
- non-negative latency, throughput, errors, and storage delta fields that were measured for the candidate kind;
- successful business probe;
- write impact in `none`, `preview_only`, or an explicitly allowed bounded value;
- no checksum divergence from baseline.

The model must not contain approval actor/time fields.

- [ ] **Step 4: Run focused model tests**

```bash
go test ./internal/control/repairpreview -count=1
```

Expected: all package tests pass.

- [ ] **Step 5: Add repository tests**

Use the existing in-memory SQLite setup pattern from `internal/control/incident/repository_test.go`. Create `repair_preview_runs` and `repair_preview_candidates` manually with SQLite-compatible types.

Cover:

- `Save` persists a run and candidates in one transaction;
- duplicate `run_id` or duplicate `(run_id,candidate_id)` is rejected;
- `ListByIncident` is tenant/incident scoped and bounded;
- `FindEligible` returns only same tenant/incident/run/candidate/action with `PASS`;
- deleted/stale candidate IDs return not-found rather than another tenant's candidate.

- [ ] **Step 6: Implement repository and migrations**

Use two PostgreSQL tables:

```sql
CREATE TABLE IF NOT EXISTS repair_preview_runs (
    id UUID PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    incident_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    branch_prefix TEXT NOT NULL,
    seed_fingerprint TEXT NOT NULL,
    workload_fingerprint TEXT NOT NULL,
    workload_revision TEXT NOT NULL,
    controlled_load BOOLEAN NOT NULL,
    isolation_boundary TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    error_summary TEXT NOT NULL DEFAULT '',
    artifact_ref TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, incident_id, run_id)
);

CREATE TABLE IF NOT EXISTS repair_preview_candidates (
    id UUID PRIMARY KEY,
    run_id UUID NOT NULL,
    tenant_id TEXT NOT NULL,
    incident_id TEXT NOT NULL,
    candidate_id TEXT NOT NULL,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    action TEXT NOT NULL,
    change_summary TEXT NOT NULL,
    branch TEXT NOT NULL,
    result_checksum TEXT NOT NULL,
    consistent BOOLEAN NOT NULL,
    average_latency_ms DOUBLE PRECISION NOT NULL,
    median_latency_ms DOUBLE PRECISION NOT NULL,
    p95_latency_ms DOUBLE PRECISION NOT NULL,
    sample_count INTEGER NOT NULL,
    tps DOUBLE PRECISION NOT NULL,
    error_count INTEGER NOT NULL,
    write_impact TEXT NOT NULL,
    storage_delta_bytes BIGINT NOT NULL,
    business_probe_pass BOOLEAN NOT NULL,
    decision TEXT NOT NULL,
    rejection_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, candidate_id)
);

CREATE INDEX idx_repair_preview_runs_incident
    ON repair_preview_runs (tenant_id, incident_id, created_at DESC);
CREATE INDEX idx_repair_preview_candidates_run
    ON repair_preview_candidates (run_id, candidate_id);
```

Mirror the definitions in `deploy/postgres-init.sql` and the standalone migration. Add the new migrator beside `incidentcontrol.Migrate` in `cmd/opskeeper/main.go`.

- [ ] **Step 7: Run storage validation**

```bash
go test ./internal/control/repairpreview -count=1
go test ./internal/control/incident -count=1
git diff --check
```

Expected: all pass.

- [ ] **Step 8: Commit Task 1**

```bash
git add internal/control/repairpreview deploy/postgres-init.sql deploy/postgres-migration-repair-preview.sql cmd/opskeeper/main.go
git commit -m "feat: persist repair preview evidence"
```

### Task 2: Deterministic Preview Runner

**Files:**
- Create: `internal/control/repairpreview/workload.go`
- Create: `internal/control/repairpreview/workload_test.go`
- Create: `internal/control/repairpreview/runner.go`
- Create: `internal/control/repairpreview/runner_test.go`
- Create: `cmd/repair-preview-runner/main.go`
- Create: `deploy/repair-preview/seed.sql`
- Create: `deploy/repair-preview/pg-pool-workload.yaml`

**Interfaces:**
- Consumes:

```go
type WorkloadSpec struct {
	Revision string
	Seed string
	WarmupQueries int
	Samples int
	Concurrency int
	QueryTimeoutMS int
	BusinessProbe BusinessProbeSpec
	Candidates []CandidateSpec
}

type CandidateSpec struct {
	CandidateID string
	Name string
	Kind string
	Action string
	ChangeSummary string
}
```

- Produces: `func Execute(ctx context.Context, db *sql.DB, spec WorkloadSpec) (Run, error)` and CLI flags `--dsn`, `--tenant-id`, `--incident-id`, `--workload`, `--run-id`, `--dry-run`.

- [ ] **Step 1: Add workload fingerprint tests**

Cover:

- seed and workload canonical JSON hash is stable across map ordering;
- changing query text, sample count, concurrency, timeout, or candidate action changes the fingerprint;
- missing revision/seed/sample/concurrency values are invalid.

- [ ] **Step 2: Verify workload tests fail**

```bash
go test ./internal/control/repairpreview -run TestWorkload -count=1
```

Expected: tests fail before implementation.

- [ ] **Step 3: Implement workload parsing and fingerprinting**

Canonicalize the YAML into sorted JSON, hash with SHA-256, and prefix with `sha256:`. Never embed credentials in the persisted workload or fingerprint input.

- [ ] **Step 4: Add runner integration tests**

Use a temporary Postgres database only when `OPSKEEPER_REPAIR_PREVIEW_TEST_DSN` is set; otherwise skip with a clear message. Cover:

- baseline and candidate branches receive the same seed;
- deterministic checksums match for Candidate A;
- Candidate B's business probe fails and decision is `REJECTED_BY_PREVIEW`;
- latency samples and p95 are populated;
- controlled saturation uses finite clients and exits by timeout;
- branch cleanup removes run-scoped schemas.

- [ ] **Step 5: Implement runner**

Use `database/sql` with pgx stdlib. For each branch:

1. create schema `rp_<run_hash>_<candidate>`;
2. apply `seed.sql`;
3. warm up;
4. replay the fixed query set under bounded concurrency;
5. hash ordered canonical query result rows;
6. run business probes;
7. collect timing and storage metrics;
8. evaluate decision;
9. drop the schema in `defer`.

Candidate A is `resize_pool` and must remain checksum-consistent. Candidate B is `reset_pool`, fails its business probe, and is rejected. Persisted boundary text is exactly: `Controlled fixed-workload reconstruction in disposable preview-pg; original active sessions are not copied.`

- [ ] **Step 6: Add CLI**

The CLI loads YAML, computes fingerprints, executes the run, opens Manager's control-plane DB using the provided DSN, calls `Repository.Save`, and prints sanitized JSON. `--dry-run` prints the run without saving.

- [ ] **Step 7: Run runner validation**

```bash
go test ./internal/control/repairpreview ./cmd/repair-preview-runner -count=1
go build ./cmd/repair-preview-runner
git diff --check
```

Expected: all pass; optional integration tests run when the DSN is supplied.

- [ ] **Step 8: Commit Task 2**

```bash
git add internal/control/repairpreview cmd/repair-preview-runner deploy/repair-preview
git commit -m "feat: run controlled repair previews"
```

### Task 3: Compact API, Archive Aggregation, and HITL Gate

**Files:**
- Create: `internal/control/repairpreview/readback.go`
- Create: `internal/control/repairpreview/readback_test.go`
- Create: `internal/control/repairpreview/gate.go`
- Create: `internal/control/repairpreview/gate_test.go`
- Modify: `internal/manager/server/incident/http.go`
- Modify: `internal/manager/server/incident/http_test.go`
- Modify: `internal/manager/model/hitl/model.go`
- Modify: `internal/manager/biz/aiops/tools/recovery_execute_basetool.go`
- Modify: `internal/manager/biz/aiops/tools/recovery_execute_basetool_test.go`
- Modify: `internal/manager/biz/aiops/tools/registry_basetool.go`
- Modify: `cmd/opskeeper/main.go`
- Modify: `plugins/opskeeper-teamharness/prompts/manager/AGENTS.md`
- Modify: `internal/manager/biz/knowledge/builtin_vault/diagnostics/pg-pool-exhaustion-resize-pool-v1.md`

**Interfaces:**
- Consumes: `Repository.FindEligible`.
- Produces:

```go
type CompactSummary struct {
	IncidentID string
	RunID string
	SeedFingerprint string
	WorkloadFingerprint string
	ControlledLoad bool
	IsolationBoundary string
	Baseline Candidate
	Passing Candidate
	Rejected Candidate
}

type Gate interface {
	Eligible(ctx context.Context, tenantID, incidentID, runID, candidateID, action string) error
}
```

Archive gains `RepairPreviews []Run` capped to the latest three runs and twelve candidates. The compact route is `GET /v1/incidents/{incident_id}/repair-preview-summary`.

- [ ] **Step 1: Add readback tests**

Cover compact summary selection, bounded output, missing preview empty state, and no raw artifact content.

- [ ] **Step 2: Add gate tests**

Cover missing IDs, tenant/incident mismatch, stale workload, FAIL/REJECTED candidate, exact action match, and PASS eligibility.

- [ ] **Step 3: Implement readback and gate**

Return `ErrNotFound`, `ErrStaleWorkload`, and `ErrNotEligible` as sentinel errors. Never expose another tenant's candidate existence.

- [ ] **Step 4: Extend incident API tests**

Add cases:

- archive includes `repair_previews`;
- compact summary returns baseline/A/B;
- legacy archive returns an empty array and unchanged `evidence_complete`;
- tenant mismatch is 404;
- repository error does not leak SQL details.

- [ ] **Step 5: Implement incident API dependency and routes**

Change constructor to `NewHandler(repository Repository, previews PreviewReadRepository)` while retaining a nil-safe seam for existing tests if needed. Register:

```go
router.Get("/v1/incidents/{incident_id}/repair-preview-summary", h.repairPreviewSummary)
```

- [ ] **Step 6: Extend HITL payload and recovery gate**

Add to `RecoveryExecutionParameters` and recovery args:

```go
PreviewRunID string `json:"preview_run_id,omitempty"`
PreviewCandidateID string `json:"preview_candidate_id,omitempty"`
```

For AgentTeams `resize_pool`, `kill_process`, and `restart_service`, require both IDs. Before `ReserveApprovedProposal`, call the preview gate using tenant context and exact incident/run/candidate/action. Candidate B fails because its action is not eligible. The approved proposal canonical hash includes the preview IDs.

Update Manager prompt/SOP to require the compact A/B card and forbid proposing `reset_pool` after preview rejection.

- [ ] **Step 7: Run Manager validation**

```bash
go test ./internal/control/repairpreview ./internal/manager/server/incident ./internal/manager/biz/aiops/tools -count=1
go test ./internal/manager/model/hitl ./internal/manager/biz/hitl ./internal/manager/data/hitl -count=1
go build ./...
git diff --check
```

Expected: all pass.

- [ ] **Step 8: Commit Task 3**

```bash
git add internal/control/repairpreview internal/manager/server/incident internal/manager/model/hitl internal/manager/biz/aiops/tools internal/manager/biz/hitl internal/manager/data/hitl cmd/opskeeper/main.go plugins/opskeeper-teamharness/prompts/manager/AGENTS.md internal/manager/biz/knowledge/builtin_vault/diagnostics/pg-pool-exhaustion-resize-pool-v1.md
git commit -m "feat: gate recovery on repair preview evidence"
```

### Task 4: TeamHarness Archive and Approval-Gate Projection

**Files:**
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive-route.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/route.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/api.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive.test.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/runtime.test.js`
- Modify: `plugins/opskeeper-teamharness/CHANGELOG.md`
- Modify: `plugins/opskeeper-teamharness/plugin.yaml`

**Interfaces:**
- Consumes Manager fields `repair_previews` and compact summary endpoint.
- Produces normalized:

```js
export function normalizeRepairPreviews(value) {
  return Array.isArray(value) ? value.map((run) => ({
    ...run,
    candidates: Array.isArray(run.candidates) ? run.candidates : [],
  })).filter((run) => run.run_id || run.id) : [];
}
```

- [ ] **Step 1: Add frontend tests**

Cover wrapper unwrapping, missing array fallback, compact endpoint URL, PASS/FAIL styling strings, controlled-load boundary copy, legacy empty state, and no `/preview/` link replacing authoritative data.

- [ ] **Step 2: Verify frontend tests fail**

```bash
pnpm --dir plugins/opskeeper-teamharness/dashboard test
```

Expected: new tests fail.

- [ ] **Step 3: Implement API and normalization**

Add `getIncidentRepairPreviewSummary(incidentId)` and normalize snake_case/camelCase variants without mutating the original response.

- [ ] **Step 4: Implement compact and full views**

In diagnostics route, render the approval-gate card after RCA and before HITL: baseline metric, A PASS/eligible, B FAIL/rejected, fingerprint, and boundary. In Archive, render the complete responsive comparison table and keep `/preview/` as a secondary link.

- [ ] **Step 5: Run frontend validation**

```bash
pnpm --dir plugins/opskeeper-teamharness/dashboard test
pnpm --dir plugins/opskeeper-teamharness/dashboard build
git diff --check
```

Expected: all pass.

- [ ] **Step 6: Commit Task 4**

```bash
git add plugins/opskeeper-teamharness/dashboard/src/plugins/opskeeper-teamharness/CHANGELOG.md plugins/opskeeper-teamharness/plugin.yaml
git commit -m "feat: project repair preview decisions"
```

### Task 5: Real Run, Public Deployment, and E2E

**Files:**
- Modify: `plugins/opskeeper-teamharness/dashboard/plugin.json`
- Modify: `plugins/opskeeper-teamharness/dashboard/package.json`
- Modify: `plugins/opskeeper-teamharness/dashboard/package-lock.json`
- Modify: `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.json`
- Modify: `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py`
- Modify: `openspec/changes/repair-preview-readback/tasks.md`
- Create: `docs/superpowers/reports/2026-09-18-repair-preview-readback-e2e.md`

**Interfaces:**
- Consumes all prior tasks.
- Produces: TeamHarness `1.0.65` or the next unreused version, public readback, and real E2E evidence.

- [ ] **Step 1: Bump all TeamHarness version references together**

Use one version in plugin YAML/JSON, Python constant, package manifests, dashboard entry filename, and changelog. Do not reuse a browser-cached version.

- [ ] **Step 2: Run full local validation**

```bash
go test ./internal/control/repairpreview ./internal/manager/server/incident ./internal/manager/biz/aiops/tools -count=1
go build ./...
./plugins/opskeeper-teamharness/scripts/build-package.sh
pnpm --dir plugins/opskeeper-teamharness/dashboard test
git diff --check
```

Expected: all pass.

- [ ] **Step 3: Execute a real preview run**

Use the public control-plane DB through an approved local tunnel and disposable preview-pg. Save the run with the selected public incident ID. Record run ID, candidate IDs, workload fingerprint, seed fingerprint, A/B decisions, and DB readback.

- [ ] **Step 4: Build and deploy locally**

Build Go and plugin artifacts on the local Mac. Upload artifacts to Aliyun; do not build on Aliyun. Restart only the required Manager/Dashboard/Worker containers after readback.

- [ ] **Step 5: Run public E2E**

Validate:

1. old incident Archive still loads;
2. selected incident shows baseline/A/B;
3. approval gate shows compact A/B card;
4. only Candidate A reaches HITL;
5. human approves A;
6. recovery verification passes;
7. incident closes;
8. Archive retains full table and evidence chain;
9. `/preview/` deep link remains secondary.

- [ ] **Step 6: Write evidence and complete tasks**

Record public version readbacks, API responses with sensitive fields removed, E2E timing, and rollback status in the report. Mark every OpenSpec task complete.

- [ ] **Step 7: Commit Task 5**

```bash
git add plugins/opskeeper-teamharness docs/superpowers/reports/2026-09-18-repair-preview-readback-e2e.md openspec/changes/repair-preview-readback/tasks.md
git commit -m "test: verify repair preview readback e2e"
```

## Final Verification

- [ ] Run `openspec validate repair-preview-readback --strict`
- [ ] Run `go test ./...`
- [ ] Run `pnpm --dir plugins/opskeeper-teamharness/dashboard test`
- [ ] Run `./plugins/opskeeper-teamharness/scripts/build-package.sh`
- [ ] Run `git diff --check`
- [ ] Confirm all OpenSpec task checkboxes are checked
- [ ] Confirm public version/API/E2E report is complete
