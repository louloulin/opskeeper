---
change: prepare-final-demo-main-flow
design-doc: docs/superpowers/specs/2026-09-18-prepare-final-demo-main-flow-design.md
base-ref: ea57acc6fcdea6864d4c194ae4eea05a9cdce459
---

# Prepare Final Demo Main Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a reproducible PostgreSQL pool-exhaustion final-demo flow in which home shows real business impact, Manager controls injection and progression, monitoring confirms pressure, Element shows coordination, and Archive retains preview and recovery evidence.

**Architecture:** Manager owns the allowlisted demo scenario, incident binding, alert correlation, service-to-service business-query proxy, and persisted progress. pool-fixture owns the saturating application pool and three real SQL-backed business snapshots on that same pool. The Next.js home site is a browser-facing console and server-side proxy; it never receives database or fixture credentials. `repair-preview-readback` remains the authority for structured preview evidence, and this change only consumes its API/readback contract.

**Tech Stack:** Go 1.23+ with `net/http`, GORM, chi, PostgreSQL/pgx; Next.js 14 App Router, TypeScript, Tailwind CSS; existing Prometheus/Alertmanager, AgentTeams Element, AgentTeams Dashboard, and TeamHarness plugin.

**Spec:** `openspec/changes/prepare-final-demo-main-flow/specs/final-demo-reliability/spec.md` and `docs/superpowers/specs/2026-09-18-prepare-final-demo-main-flow-design.md`

## Global Constraints

- Home must not connect directly to PostgreSQL or pool-fixture and must not store database credentials in the browser.
- Repeated scenario submission must return the same incident and fixture manifest, not start a second scenario.
- Prometheus alerting remains in the path; home injection must not bypass monitoring.
- Order, inventory, and audit cards must use real SQL queries through the same application pool that is saturated.
- Business API responses must set `Cache-Control: no-store`; the browser must use bounded independent requests.
- Preview PASS only creates HITL eligibility; it is not human approval.
- Preview results with differing replay profiles are `NOT COMPARABLE` and cannot support approval.
- Repair execution is bound to incident, candidate, execution ID, target fingerprint, scope, and expiry.
- Manager restart during pending approval/verification must preserve scenario and incident state.
- Do not modify AgentTeams Controller or Dashboard host source.
- Do not introduce PolarDB HA and do not claim active-session cloning; use controlled-load wording.
- `repair-preview-readback` owns preview persistence and Archive authority; this plan must not create a second fact source.

---

### Task 1: Add real business snapshots to the saturated pool

**Files:**
- Modify: `cmd/pool-fixture/main.go`
- Test: `cmd/pool-fixture/main_test.go`

**Interfaces:**
- Consumes: existing `PoolRuntime`, `ownedPool`, authenticated handler, and PostgreSQL DSN.
- Produces:
  - `type BusinessSection string` with constants `BusinessSectionOrders`, `BusinessSectionInventory`, and `BusinessSectionAudit`.
  - `type BusinessSnapshot struct { Section, Value, Detail string; LatencyMS int64; GeneratedAt time.Time }`
  - `func (r *postgresRuntime) BusinessSnapshot(ctx context.Context, section BusinessSection) (BusinessSnapshot, error)`
  - `GET /v1/business-snapshots/{section}` with the existing `X-Opskeeper-Version: v1` + bearer authorization.

- [ ] **Step 1: Write failing controller and HTTP tests**

Add a fake-runtime business method and these test cases to `cmd/pool-fixture/main_test.go`:

```go
func TestBusinessSnapshotUsesSharedSaturatedPool(t *testing.T) {
	controller, runtime, _ := newTestController(t)
	runtime.businessErr = context.DeadlineExceeded
	manifest, err := controller.Start(context.Background(), StartRequest{
		CaseID: "pg-pool-exhaustion", IncidentID: "incident-business",
		InitialCapacity: 4, TargetCapacity: 8, TTLSeconds: 60,
	})
	if err != nil { t.Fatal(err) }
	if _, err := controller.BusinessSnapshot(context.Background(), manifest.ManifestID, BusinessSectionOrders); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected saturated-pool error, got %v", err)
	}
	if runtime.businessSection != BusinessSectionOrders {
		t.Fatalf("unexpected section %q", runtime.businessSection)
	}
}

func TestBusinessSnapshotHandlerCoversAllSections(t *testing.T) {
	controller, _, _ := newTestController(t)
	server := httptest.NewServer(NewHandler(controller))
	defer server.Close()
	for _, section := range []string{"orders", "inventory", "audit"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/business-snapshots/"+section, nil)
		addPoolAuth(request)
		response, err := server.Client().Do(request)
		if err != nil { t.Fatal(err) }
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", section, response.StatusCode)
		}
	}
}
```

Adjust `fakeRuntime` with fields `businessSection BusinessSection` and `businessErr error`; return a deterministic snapshot unless `businessErr` is set.

- [ ] **Step 2: Run the failing tests**

Run: `go test ./cmd/pool-fixture -run 'TestBusinessSnapshot' -v`

Expected: compile failure because `BusinessSnapshot`, section constants, and handler routes do not exist.

- [ ] **Step 3: Implement schema, queries, and endpoint**

In `postgresRuntime`, initialize three small tables during `newPostgresRuntime` before any saturation:

```sql
CREATE TABLE IF NOT EXISTS demo_business_orders (
  id bigint PRIMARY KEY,
  status text NOT NULL,
  amount_cents bigint NOT NULL,
  created_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS demo_business_inventory (
  warehouse text PRIMARY KEY,
  available integer NOT NULL,
  total integer NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS demo_business_audit_events (
  id bigserial PRIMARY KEY,
  event_type text NOT NULL,
  created_at timestamptz NOT NULL
);
```

Seed each table only when empty. Use the existing `POOL_FIXTURE_DB_DSN`; do not create a second pool.

Implement section queries:

```sql
-- orders
SELECT count(*)::text, coalesce(sum(amount_cents),0)::text FROM demo_business_orders WHERE status='paid';
-- inventory
SELECT warehouse, available, total FROM demo_business_inventory ORDER BY updated_at DESC LIMIT 1;
-- audit
SELECT count(*)::text, max(created_at) FROM demo_business_audit_events;
```

All calls use the request context and the same `r.db` limited by `SetMaxOpenConns`. Map `context.DeadlineExceeded` and pgx pool-wait errors to HTTP 503 with `error_code="pool_exhausted"`. Unknown sections return 400. Successful responses are `no-store`.

Extend `Handler.ServeHTTP` before the generic authenticated dispatch:

```go
if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/business-snapshots/") {
    h.businessSnapshot(writer, request)
    return
}
```

- [ ] **Step 4: Verify focused tests and race behavior**

Run:

```bash
go test ./cmd/pool-fixture -run 'TestBusinessSnapshot|TestHandlerProtectsAndExposesPoolLifecycle' -v
go test ./cmd/pool-fixture -race
```

Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/pool-fixture/main.go cmd/pool-fixture/main_test.go
git commit -m "feat(pool-fixture): expose real business snapshots"
```

### Task 2: Add Manager scenario storage and control API

**Files:**
- Create: `internal/manager/model/demo/model.go`
- Create: `internal/manager/data/demo/store.go`
- Create: `internal/manager/biz/demo/scenario.go`
- Create: `internal/manager/service/demo/scenario.go`
- Create: `internal/manager/server/demo/http.go`
- Modify: `cmd/opskeeper/main.go`
- Tests: `internal/manager/data/demo/store_test.go`, `internal/manager/biz/demo/scenario_test.go`, `internal/manager/server/demo/http_test.go`

**Interfaces:**
- Consumes: alert incident repository/event creation, pool-fixture HTTP API, tenant context, and existing JWT/bearer middleware patterns.
- Produces:

```go
type StartScenarioInput struct {
    IdempotencyKey    string `json:"idempotency_key"`
    ScenarioID        string `json:"scenario_id"`
    Target            string `json:"target"`
    TargetFingerprint string `json:"target_fingerprint"`
    AlertFingerprint  string `json:"alert_fingerprint"`
    DurationSeconds   int    `json:"duration_seconds"`
}

type ScenarioStatus struct {
    IncidentID        uint64 `json:"incident_id"`
    ScenarioID        string `json:"scenario_id"`
    Status            string `json:"status"`
    PoolManifestID    string `json:"pool_manifest_id"`
    TargetFingerprint string `json:"target_fingerprint"`
    AlertFingerprint  string `json:"alert_fingerprint"`
    UpdatedAt         string `json:"updated_at"`
}
```

HTTP:

```text
POST /api/v1/demo/scenarios/pg-pool-exhaustion/start
GET  /api/v1/demo/scenarios/{idempotency_key}
GET  /api/v1/demo/scenarios/{idempotency_key}/business/{section}
```

Manager-side demo service authentication uses `Authorization: Bearer $OPSKEEPER_DEMO_API_TOKEN` and `X-Opskeeper-Version: v1`. The token is never returned to the browser.

- [ ] **Step 1: Write storage tests**

Use SQLite/GORM in `store_test.go` and cover:

```go
func TestScenarioStoreIsIdempotent(t *testing.T)
func TestScenarioStoreUpdatesAlertAndManifest(t *testing.T)
func TestScenarioStoreRejectsFingerprintMismatch(t *testing.T)
```

Model fields must include tenant ID, scenario ID, idempotency key, incident ID, pool manifest ID, target, target fingerprint, alert fingerprint, status, expiry, and timestamps. Use a unique index on `(tenant_id, scenario_id, idempotency_key)`.

- [ ] **Step 2: Write business tests**

Cover in `scenario_test.go`:

```go
func TestStartCreatesIncidentBeforeFixture(t *testing.T)
func TestRepeatedStartReturnsSameIncident(t *testing.T)
func TestStartValidatesAllowlistedTargetAndDuration(t *testing.T)
func TestFixtureStartFailureLeavesRestartableScenario(t *testing.T)
func TestBusinessSnapshotReturnsPoolExhausted(t *testing.T)
```

Use fake interfaces for incident creation and fixture calls. Assert the fake incident recorder is appended before the fixture start recorder. Assert a second `Start` does not call fixture start twice.

- [ ] **Step 3: Implement model, store, and migration registration**

Create a GORM `ScenarioRun` model and repository methods:

```go
CreateOrUpdate(ctx context.Context, run *model.ScenarioRun) error
GetByIdempotencyKey(ctx context.Context, tenantID uint64, scenarioID, key string) (*model.ScenarioRun, error)
UpdateStatus(ctx context.Context, id uint64, status string, mutation func(*model.ScenarioRun) error) error
```

Register `managerdemodata.Migrate` in `cmd/opskeeper/main.go` immediately after `manageralertdata.Migrate`. Statuses are exactly:

```text
starting
awaiting_alert
alert_correlated
diagnosis_dispatched
preview_ready
awaiting_approval
repair_dispatched
verifying
recovered
closed
start_failed
```

- [ ] **Step 4: Implement service and HTTP handler**

Validation:

- scenario ID must equal `pg-pool-exhaustion`;
- target must equal `pg:pool-fixture`;
- duration must be 60–600 seconds;
- fingerprints must be 16–128 hexadecimal or `sha256:`-prefixed characters;
- idempotency key must be 12–128 characters.

Start sequence:

1. load existing run by idempotency key and return it if active;
2. create an `alert_incidents` row with dedupe key `demo-scenario:<idempotency-key>`;
3. persist scenario `starting`;
4. call pool-fixture `POST /v1/pool-fixtures`;
5. persist manifest ID and `awaiting_alert`;
6. record a scenario-start incident event with sanitized target and no credentials.

If step 4 fails, retain the incident and mark the scenario `start_failed`; retry with the same key reuses the incident. Add `GET` status aggregation from scenario, incident, and fixture state. Business proxy forwards section and maps 503 pool exhaustion without leaking fixture internals.

- [ ] **Step 5: Write HTTP tests**

Cover bearer rejection, successful start, idempotent replay, status readback, and three business-section proxies.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/manager/data/demo ./internal/manager/biz/demo ./internal/manager/server/demo -v
go test ./cmd/opskeeper -run TestNothing
```

Expected: all pass and command compiles.

- [ ] **Step 7: Commit**

```bash
git add internal/manager/model/demo internal/manager/data/demo internal/manager/biz/demo internal/manager/service/demo internal/manager/server/demo cmd/opskeeper/main.go
git commit -m "feat(manager): control final-demo pool scenario"
```

### Task 3: Correlate Prometheus alerts and preserve incident identity

**Files:**
- Modify: `internal/manager/biz/alert/webhook.go`
- Modify: `internal/manager/biz/alert/usecase.go`
- Modify: `internal/manager/biz/alert/repo.go`
- Modify: `internal/manager/data/alert/store/repo.go`
- Tests: `internal/manager/biz/alert/webhook_test.go`, `internal/manager/data/alert/store/store_test.go`

**Interfaces:**
- Consumes: Manager scenario store and existing `IngestAlertmanager` path.
- Produces: `CorrelateDemoScenario(ctx context.Context, fingerprint string, labels map[string]string) (*model.Incident, bool, error)` on the alert usecase/repository seam.

- [ ] **Step 1: Write failing tests**

```go
func TestAlertmanagerCorrelatesActiveDemoScenario(t *testing.T)
func TestAlertmanagerDoesNotCreateDuplicateDemoIncident(t *testing.T)
func TestUnrelatedAlertKeepsNormalIngestPath(t *testing.T)
```

Assertions:

- scenario alert uses the existing scenario incident ID;
- no second incident is created;
- an `alert_received` event is appended to the scenario incident;
- scenario status becomes `alert_correlated`;
- unrelated alerts retain current behavior.

- [ ] **Step 2: Implement correlation**

Before normal `RecordFiring`, derive the Alertmanager dedupe key. Look up an active scenario by alert fingerprint and, when absent, by the exact labels configured for the demo rule (`alertname`, `instance`, `job`, `pool_manifest_id`). If found, update that incident's firing fields, append the webhook receipt event, update scenario status, and trigger the existing investigator path using the already-wired alert pipeline.

Do not synthesize an alert in home or Manager. The public Alertmanager route remains the only alert ingress.

- [ ] **Step 3: Test restart retention**

In the store test, create scenario/incident rows, close and reopen the SQLite DB, and assert correlation still finds them by fingerprint and idempotency key.

- [ ] **Step 4: Run focused tests**

```bash
go test ./internal/manager/biz/alert -run 'TestAlertmanager.*DemoScenario' -v
go test ./internal/manager/data/alert/store -v
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/manager/biz/alert internal/manager/data/alert/store
git commit -m "fix(alert): correlate demo scenario alerts"
```

### Task 4: Build the home live-incident console and server proxy

**Files:**
- Create: `site/app/live-incident/page.tsx`
- Create: `site/components/demo/business-card.tsx`
- Create: `site/components/demo/stage-rail.tsx`
- Create: `site/components/demo/preview-decision-card.tsx`
- Create: `site/app/api/demo/scenario/route.ts`
- Create: `site/app/api/demo/business/[section]/route.ts`
- Create: `site/lib/demo-manager.ts`
- Create: `site/lib/demo-types.ts`
- Modify: `site/app/site-header-zh.tsx` only if navigation discoverability requires it

**Interfaces:**
- Consumes: Manager HTTP APIs from Task 2.
- Produces browser-facing types:

```ts
export type BusinessSection = 'orders' | 'inventory' | 'audit';
export type BusinessSnapshot = {
  section: BusinessSection;
  value: string;
  detail: string;
  latency_ms: number;
  generated_at: string;
};
export type ScenarioStatus = {
  incident_id: number;
  scenario_id: string;
  status: ScenarioStage;
  pool_manifest_id: string;
  target_fingerprint: string;
  alert_fingerprint: string;
  updated_at: string;
};
```

- [ ] **Step 1: Add server-side Manager client**

`site/lib/demo-manager.ts` reads only server env:

```ts
const MANAGER_URL = process.env.OPSKEEPER_MANAGER_URL;
const MANAGER_TOKEN = process.env.OPSKEEPER_DEMO_API_TOKEN;
```

Reject startup requests when either value is missing. Set `X-Opskeeper-Version: v1`, use bearer auth, enforce a 2-second `AbortSignalTimeout`, and return typed errors. Do not expose the token through `NEXT_PUBLIC_*`.

- [ ] **Step 2: Add API routes**

`site/app/api/demo/scenario/route.ts`:

- `POST`: start scenario with server-generated key `final-demo-<UTC-yyyyMMdd-HHmm>`;
- `GET`: read current scenario;
- responses: `Cache-Control: no-store`.

`site/app/api/demo/business/[section]/route.ts`:

- validate section against the three-value union;
- proxy Manager;
- map timeout/503 to `{ error_code: 'pool_exhausted' }` with HTTP 503;
- responses: `Cache-Control: no-store`.

- [ ] **Step 3: Build card components**

`BusinessCard` accepts section, snapshot/error state, latency, and loading flag. Each card:

- independently polls every 3 seconds;
- aborts after 2 seconds;
- shows last successful value plus current degraded state;
- never hides the page shell;
- exposes accessible status text (`正常`, `查询超时`, `降级`);
- displays latency in tabular numerals.

- [ ] **Step 4: Build stage and preview components**

`StageRail` renders the exact statuses from Task 2 and marks current, completed, and failed states. `PreviewDecisionCard` accepts:

```ts
type PreviewDecision = {
  replay_profile_id: string;
  boundary: string;
  baseline: PreviewMetrics;
  candidates: Array<{ id: 'A' | 'B'; decision: 'PASS' | 'FAIL'; metrics: PreviewMetrics }>;
};
```

It always renders the controlled-load boundary and marks differing replay profiles `NOT COMPARABLE`.

- [ ] **Step 5: Build `/live-incident` page**

Page sections:

1. header with environment and service status;
2. one-click injection button with duration/target readback;
3. order/inventory/audit cards;
4. business impact metrics;
5. stage rail;
6. compact preview decision card;
7. links to monitoring, Element room, Dashboard, Archive, and preview report;
8. manual prompt panel only for human approval and archive closure.

Use client-side polling and disabled injection while a scenario is active. Do not add a static fallback data path.

- [ ] **Step 6: Validate frontend**

```bash
cd site
pnpm typecheck
pnpm lint
pnpm build
```

Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add site/app/live-incident site/components/demo site/app/api/demo site/lib/demo-manager.ts site/lib/demo-types.ts site/app/site-header-zh.tsx
git commit -m "feat(site): add final-demo incident console"
```

### Task 5: Integrate preview gate and workflow progression

**Files:**
- Modify: `internal/manager/biz/demo/scenario.go`
- Modify: `internal/manager/service/demo/scenario.go`
- Modify: `site/components/demo/preview-decision-card.tsx`
- Modify: `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py`
- Tests: `internal/manager/biz/demo/scenario_test.go`, `plugins/opskeeper-teamharness/tests/test_workflow_projection.py`

**Interfaces:**
- Consumes: `repair-preview-readback` Manager archive response containing `repair_previews`.
- Produces scenario status transitions to `preview_ready` and `awaiting_approval`, plus a compact decision summary:

```go
type PreviewDecisionSummary struct {
    ReplayProfileID string
    BoundaryText    string
    CandidateA      string
    CandidateB      string
    EligibleForHITL bool
}
```

- [ ] **Step 1: Add failing gate tests**

Cover:

```go
func TestPreviewPASSCreatesOnlyHITLEligibility(t *testing.T)
func TestPreviewFAILCannotReachApproval(t *testing.T)
func TestMismatchedReplayProfileIsNotComparable(t *testing.T)
```

- [ ] **Step 2: Implement Manager gating**

Only a `PASS` candidate with complete baseline metrics and matching replay profile can transition the scenario to `awaiting_approval`. Candidate B remains persisted as rejected and is never sent to HITL. Approval continues through the existing approval path and must reference candidate ID and execution ID.

- [ ] **Step 3: Emit workflow transitions**

Extend existing `agentteams.workflow` emission for:

- `preview_ready`;
- `awaiting_approval`;
- `repair_dispatched`;
- `verifying`;
- `recovered`.

Messages remain projections only; a Matrix failure cannot block authoritative Manager state.

- [ ] **Step 4: Run tests**

```bash
go test ./internal/manager/biz/demo -run 'TestPreview' -v
python3 -m pytest plugins/opskeeper-teamharness/tests/test_workflow_projection.py -q
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add internal/manager/biz/demo internal/manager/service/demo site/components/demo/preview-decision-card.tsx plugins/opskeeper-teamharness
git commit -m "feat(demo): gate repair approval on preview evidence"
```

### Task 6: Add reliability and E2E verification

**Files:**
- Create: `scripts/verify-final-demo.sh`
- Create: `FINAL_DEMO_SCRIPT.md`
- Modify: `openspec/changes/prepare-final-demo-main-flow/tasks.md`

**Interfaces:**
- Consumes all previous tasks plus public environment variables/endpoints.
- Produces repeatable local/public verification commands and the presenter script.

- [ ] **Step 1: Write reliability assertions**

`scripts/verify-final-demo.sh` must implement:

```bash
set -euo pipefail
```

Checks:

1. `/readyz` returns 200;
2. domains return 200;
3. Manager/plugin version readback matches env `EXPECTED_MANAGER_VERSION` and `EXPECTED_PLUGIN_VERSION`;
4. three business APIs return 200 before injection;
5. scenario start is idempotent;
6. after injection, at least one business API returns 503 or exceeds threshold;
7. scenario and incident IDs remain equal across repeated reads;
8. after approval/repair, all business APIs return 200;
9. monitoring query confirms active/capacity recovery;
10. Archive includes preview A/B and evidence-complete events.

- [ ] **Step 2: Write presenter script**

`FINAL_DEMO_SCRIPT.md` contains timing, screen order, exact talking points, expected values, recovery actions, and links:

- 30-second opening;
- 30-second injection/body impact;
- 3-minute collaboration/approval;
- 30-second recovery and Archive close.

- [ ] **Step 3: Run local suites**

```bash
go test ./cmd/pool-fixture ./internal/manager/data/demo ./internal/manager/biz/demo ./internal/manager/server/demo ./internal/manager/biz/alert
cd site && pnpm typecheck && pnpm lint && pnpm build
```

Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add scripts/verify-final-demo.sh FINAL_DEMO_SCRIPT.md openspec/changes/prepare-final-demo-main-flow/tasks.md
git commit -m "test(demo): verify final main flow"
```

### Task 7: Restore and validate the public environment

**Files:**
- Modify: `deploy/docker-compose.final-demo.yml` or the active public compose file only if new env vars/services are required
- Modify: `FINAL_DEMO_SCRIPT.md`
- Modify: `openspec/changes/prepare-final-demo-main-flow/tasks.md`

**Interfaces:**
- Consumes locally built Manager/site/plugin artifacts and the existing Aliyun host.
- Produces a validated public environment with readback evidence.

- [ ] **Step 1: Restore workers and fixtures**

On the Aliyun host:

```bash
docker start agentteams-worker-opskeeper-investigator
docker inspect --format '{{.Name}} {{.State.Status}}' agentteams-worker-opskeeper-investigator
docker logs --tail 100 agentteams-worker-opskeeper-investigator
```

Resolve the QwenPaw timeout from logs/config, then verify all seven RCA worker containers are running.

- [ ] **Step 2: Deploy locally built artifacts**

Build on the local Mac, copy artifacts to Aliyun, and restart only the affected services. Do not build on Aliyun. Record Manager commit, plugin version, site build ID, and fixture image digest.

- [ ] **Step 3: Execute public E2E**

Run `scripts/verify-final-demo.sh` with public URLs and expected versions. Manually verify:

- Element and Dashboard show the same stage transitions;
- home cards degrade and recover;
- monitoring shows 4/4 then recovered capacity;
- Archive shows complete A/B preview and evidence chain;
- Manager restart during awaiting approval preserves progress;
- retry does not execute repair twice.

- [ ] **Step 4: Record readback and close tasks**

Append actual public version matrix, incident ID, replay profile, screenshots/evidence links, and elapsed timings to `FINAL_DEMO_SCRIPT.md`. Mark all completed OpenSpec tasks.

- [ ] **Step 5: Commit**

```bash
git add deploy FINAL_DEMO_SCRIPT.md openspec/changes/prepare-final-demo-main-flow/tasks.md
git commit -m "chore(demo): validate public final flow"
```

## Execution Notes

- Task 5 requires the `repair-preview-readback` API contract to be present. If it is not merged, stop after Task 4 and report the missing dependency rather than inventing a parallel preview model.
- Operational checks in Task 7 are part of build completion; record evidence even when they do not produce a large source diff.
- Do not skip the public E2E in favor of local tests.
