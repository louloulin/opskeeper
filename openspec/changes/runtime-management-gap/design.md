# Design: runtime-management-gap

## Context

OpsKeeper already has several runtime islands. Edge owns node telemetry and tunnel identity. Manager Chat Runtime owns in-process worker state. AgentTeams owns a local phase ledger. TeamHarness and the plugin manager own plugin installation and worker discovery. Repair preview owns disposable PostgreSQL execution. None of these systems currently share authoritative runtime identity, capacity, task state, workspace lifecycle, credentials, or events.

The target design is a Manager-owned control plane with a node-local execution plane. It borrows Multica's operational primitives but applies them to OpsKeeper's incident loop and safety ladder.

## Goals / Non-Goals

**Goals**

- Make every executable surface discoverable, observable, recoverable, and governable.
- Preserve Manager authority for identity, authorization, incident state, approval, evidence, and rollout decisions.
- Let a node agent continue already-authorized local work during transient disconnection without accepting new high-risk work.
- Make task side effects idempotent and safely recoverable across process restarts.
- Make plugin capability and version deterministic at task execution time.
- Keep runtime telemetry, evidence, approvals, and security events correlated.

**Non-Goals**

- No general-purpose multi-cloud node manager.
- No rewrite of AgentTeams, Dashboard, Matrix, or the incident domain.
- No assumption that a plugin can elevate its own permissions.
- No claim of hard execution isolation from a shell runner.

## Architecture

### Manager control plane

The Manager owns durable runtime records, task queue and claim state, desired plugin versions, rollout state, credential grants, and append-only runtime events. It is the only component that can create a new claim, approve a rollout step, mint a task credential, or move shared task state.

The control plane exposes:

- runtime registration, heartbeat, and command acknowledgement;
- task claim, lease renewal, checkpoint, result, and failure APIs;
- workspace and disk report ingestion;
- credential issuance, proxying/revocation, and audit events;
- plugin release, desired-state, readback, rollout, pause, and rollback APIs;
- runtime inventory and timeline queries.

### Node Runtime Agent

The existing Edge Agent is the incremental path to a Node Runtime Agent. It remains the node-local supervisor and runs with node context. It registers the node runtime and child plugin/task runtimes, reports capacity and health, executes or supervises authorized tasks, holds task workspace locks, enforces local resource ceilings, reports disk usage, destroys task credentials, and reconciles plugin desired state.

The node agent is autonomous only for work already claimed and authorized before a disconnect. If the Manager is unreachable, it must not accept new mutating work or change desired plugin versions. It reports deferred commands and local terminal decisions after reconnect.

### Runtime adapter model

Each existing runtime gets an adapter rather than an immediate rewrite:

- Edge Agent becomes a first-class node runtime.
- Manager Chat Worker exposes its worker map through durable task state.
- AgentTeams/TeamHarness tasks use the shared task lifecycle while retaining their local phase ledger as incident-phase evidence.
- Plugin manager becomes a runtime plugin supervisor and rollout target.
- Repair-preview runner and pool fixture become task-owned disposable runtimes with manifests, leases, and GC.

This allows read-only inventory first and avoids a big-bang scheduler migration.

### Durable task model

A task row is the authority for cross-process state. It stores immutable task identity plus mutable claim and execution state:

- tenant, incident, task kind, payload digest, safety level, and idempotency key;
- desired runtime selector and actual runtime ID;
- admission result and capacity snapshot;
- claim generation, claimed-by, claim time, prepare-lease deadline, start time, and heartbeat/lease deadline;
- checkpoint/ref, resume hint, attempt, max attempts, parent retry, and failure reason;
- workspace identity and output/evidence references;
- terminal result, sanitizer status, and completion time.

The state machine is:

`queued -> admitted -> claimed -> preparing -> running -> succeeded | failed | cancelled`

`preparing` may extend a short lease while inputs, workspaces, plugins, and credentials are resolved. `running` relies on runtime liveness and task checkpoint/progress. Stale claims are retried or safely failed according to task kind and safety classification. Mutating tasks require explicit idempotency behavior before they can be automatically retried.

### Workspace and disk model

Every task gets a root with explicit `workdir`, `output`, `logs`, `config`, and `temp` entries. The root carries a claim lock and GC metadata. Work is never silently reset without holding that lock. Output and evidence remain default-preserved; regenerable artifacts can be policy-reclaimed. Disk scans are bounded, avoid symlink traversal, separate repository/cache/artifact accounting, and report by task, incident, workspace, runtime, and node.

### Credential model

Task identity, not the Manager process environment, is the security boundary. A task token binds task, runtime, plugin release, incident, scope, and expiry. External credentials are obtained through a broker, wrapped as short-lived grants, or delivered to a narrowly scoped runner secret. Token and grant use are audited. Terminal or expired task state triggers revocation and local destruction. Errors and output pass a sanitizer before entering evidence, telemetry, logs, or UI.

### Plugin rollout model

A plugin release is immutable: version, manifest, artifact digest, compatibility matrix, and verification state cannot be updated in place. Desired state points a node/runtime to one release. Readback must report loaded version, digest, capability digest, health, and compatibility result before a rollout step succeeds. Rollouts support canary and ordered batches with health gates, automatic pause, and rollback to the previous desired release. A running task records the release snapshot it used, preserving replay and audit semantics.

## Key Decisions

1. **Registry before scheduler.** First make all runtimes visible and honest. Avoid introducing a generic scheduler before inventory, health, capacity, and readback are reliable.
2. **Adapters before rewrites.** Preserve the existing incident loop and TeamHarness behavior while adding shared lifecycle boundaries.
3. **Manager authority with node autonomy.** The Manager decides claims and desired state; the node agent can complete already-authorized work during a partition.
4. **Safety-aware recovery.** Read-only work can resume automatically; mutating work requires an idempotency contract and must respect existing HITL/approval boundaries.
5. **Immutable plugin releases.** Version drift and mutable package replacement are unacceptable for audited operations.
6. **Event-first observability.** Every runtime control decision emits append-only, correlated events; incident-specific logs remain evidence, not the control plane.

## Rollout Plan

1. **Read-only inventory:** register existing runtimes, standardize heartbeat/readback, and render the runtime inventory without changing execution.
2. **Task lifecycle foundation:** add durable queue/claim/lease/checkpoint APIs and integrate one read-only diagnostic task plus repair preview.
3. **Node governance:** add task workspaces, claim locks, disk reports, bounded GC, and preview-environment cleanup.
4. **Credential broker:** add task tokens, scoped external grants, revocation, access audit, and output sanitization.
5. **Plugin rollout:** add immutable releases, desired/readback state, compatibility gates, canary batches, pause, and rollback.
6. **Runtime operations surface:** add timeline, drift, capacity, recovery, disk, rollout, and credential views; then broaden worker integration.

## Risks / Trade-offs

- **Registry can become stale** → heartbeat, readback, sweeper, and version/digest comparison must converge; stale records are explicitly marked, never silently trusted.
- **Task state migration is invasive** → phase it by task kind, starting with read-only and disposable preview work.
- **Automatic retry can duplicate mutation** → classify safety and require task-specific idempotency before enabling retry.
- **Node autonomy can outlive authorization** → leases and credential expiry bound disconnected execution; new mutating work remains fail-closed.
- **Disk scans can damage performance** → scans are sampled/bounded, incremental where possible, and never block task admission without configured thresholds.
- **Plugin rollout can regress capability** → compatibility matrices, canary health gates, readback, and rollback are mandatory before broad deployment.

## Open Questions

1. Which external systems require brokered credentials in the first production slice versus scoped runner secrets?
2. What default prepare-lease, running-liveness, reconnect-grace, workspace-retention, and disk-report intervals are appropriate for the supported node profiles?
3. Which existing database backend should own the append-only runtime event stream in production?
