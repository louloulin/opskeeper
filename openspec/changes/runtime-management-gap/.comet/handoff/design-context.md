# Comet Design Handoff

- Change: runtime-management-gap
- Phase: design
- Mode: compact
- Context hash: 4167d19323d5679bb894a45f4a5c93b741d1824d75cfcc7beb72efd5c4f4c38b

Generated-by: comet-handoff.sh

OpenSpec remains the canonical capability spec. This handoff is a deterministic, source-traceable context pack, not an agent-authored summary.

## openspec/changes/runtime-management-gap/proposal.md

- Source: openspec/changes/runtime-management-gap/proposal.md
- Lines: 1-79
- SHA256: 1fb4745b0594dbbc7c75c7611791f01ecbd4e214fd54ae68209e17586835498e

```md
# Proposal: runtime-management-gap

## Why

OpsKeeper has built an auditable incident loop: alert ingestion, diagnosis, review, human approval, controlled recovery, independent verification, and archived evidence. The latest `main@8774f7b3` also pins repair-preview results to scenario, target, workload, and candidate identities. These strengths solve the incident workflow, but they do not yet provide a common runtime control plane.

Code review confirms that runtime state is fragmented:

- Edge is a tunnel identity with binary `online/offline`, `last_seen_at`, and agent-version fields; it is not a general runtime registry.
- Edge plugin health is manager-memory-only and disappears on manager restart.
- Manager Chat Workers and AgentTeams execution are process/file-local and lack a shared claim/lease/checkpoint protocol.
- Repair preview is a one-shot CLI and pool-fixture restart marks manifests stale rather than resuming them.
- Session workspaces have no task-level lock, quota, TTL, GC, or disk accounting.
- Credentials are decrypted into process environment variables for the lifetime of an approved shell execution rather than brokered as task-scoped, revocable grants.
- Plugins are filesystem records plus an immediate push; they lack desired versions, immutable artifacts, readback, canary rollout, pause, and automatic rollback.
- Incident loop events and audit logs do not form a unified runtime timeline.

Multica's useful pattern is not its product surface but its proven control-plane primitives: a server-authoritative runtime registry, daemon heartbeat, slot-before-claim task queue, prepare lease, task-scoped token, per-task execution directory, disk reporting/GC, and immutable plugin execution snapshots. OpsKeeper should adopt those mechanisms while preserving its incident, HITL, and evidence-chain authority. It should not copy Multica's workspace-centric task model, cloud runtime proxying, legacy authentication, or profile-merge behavior.

## What Changes

1. Establish a server-authoritative Runtime Inventory.
   - Register Edge Agent, Manager Chat Worker, AgentTeams Worker, TeamHarness plugin runtime, plugin manager, and preview-pg fixture as runtime records.
   - Record runtime kind, profile, owner, deployment location, labels, capabilities, observed version, artifact digest, health, heartbeat, slots, CPU/memory/disk usage, and active task count.
   - Use manifest or release metadata as the only authoritative version source; runtime version SHALL NOT be inferred from hard-coded UI or script constants.

2. Standardize heartbeat and readback.
   - Carry liveness, capability digest, version/digest, resource snapshot, free slots, active tasks, pending command acknowledgements, plugin health, and workspace/disk summary.
   - Model health as `starting`, `online`, `degraded`, `recently_lost`, and `offline`, with grace windows rather than treating one missed beat as permanent failure.

3. Add a durable task lifecycle.
   - Define `queued`, `admitted`, `claimed`, `preparing`, `running`, `succeeded`, `failed`, and `cancelled` states.
   - Persist idempotency key, runtime claim, attempt, lease deadlines, checkpoint, resume pointer, failure classification, and terminal result.
   - Enforce slot-before-claim admission using server concurrency limits plus runtime-reported health and local capacity.
   - Recover after Manager, Worker, Edge, preview runner, or node-agent restart by resuming from checkpoint or terminating safely without duplicate side effects.

4. Govern task workspaces and disk.
   - Give every task distinct `workdir`, `output`, `logs`, `config`, and `temp` semantics plus a claim lock.
   - Preserve evidence and output by default; reclaim regenerable artifacts, stale temporary data, orphan preview environments, and expired workspaces through policy.
   - Expose usage by task, incident, workspace, runtime, node, and fleet.

5. Introduce task-scoped credential brokering.
   - Bind grants to incident/task/runtime/plugin/scope with expiry and revocation.
   - Avoid exporting durable vault secrets as broad environment credentials where a brokered short-lived credential or task token is available.
   - Audit issuance, use, renewal, revocation, denial, and destruction; sanitize errors and command output before persistence or display.

6. Build plugin and worker rollout management.
   - Store immutable plugin releases with version, manifest, artifact digest, compatibility matrix, and signature/verification state.
   - Maintain per-node and per-runtime desired versions.
   - Require observed readback before a rollout step is healthy.
   - Support canary, ordered batches, pause/resume, health gates, failure isolation, and rollback.
   - Let runtime plugins contribute ops skills, tools, probes, task handlers, and safety metadata without granting themselves permission.

7. Create a runtime event timeline.
   - Append registration, heartbeat loss, health transition, version drift, task claim/checkpoint/recovery, workspace GC, credential issuance/revocation, plugin rollout, rollback, and runtime lifecycle events.
   - Correlate every event with runtime ID, task ID, incident ID, workspace ID, approval ID, plugin release, trace ID, and initiating actor where applicable.

## Scope

- OpsKeeper Manager, Edge Agent, Manager Chat Worker, AgentTeams/TeamHarness worker runtimes, plugin manager, and repair-preview runtime.
- Runtime inventory, heartbeat/readback, durable task lifecycle, recovery, workspaces, disk governance, task credentials, plugin rollout, and runtime observability.
- Incremental adapters for existing incident loop and evidence archive APIs.

## Non-goals

- No replacement of the incident loop, HITL approval authority, or independent verification model.
- No general cloud node lifecycle manager, cloud runtime proxy, or Kubernetes scheduler.
- No first-phase microVM or hard-isolation claim; shell execution remains `IsolationNone` unless a stronger runner is explicitly implemented and verified.
- No claim that disposable preview-pg reproduces all active sessions of a production database.
- No migration of Multica code or its workspace-centric semantics.

## Impact

- Manager persistence, migrations, APIs, event timeline, scheduler, registry, and audit surfaces.
- Edge Agent registration, heartbeat, supervisor, control channel, workspace, disk, credential, and plugin reconciliation behavior.
- Manager Chat Worker and AgentTeams/TeamHarness task claim/report integration.
- Repair-preview runner and pool-fixture lifecycle integration.
- Plugin registry/release storage, rollout API, compatibility checks, and Dashboard runtime views.
- Security controls for task tokens, credential grants, output redaction, and runtime authentication.
```

## openspec/changes/runtime-management-gap/design.md

- Source: openspec/changes/runtime-management-gap/design.md
- Lines: 1-121
- SHA256: 4e19ac6bb4fbc12ade8d8a97557ad02cf676b2a14ff49179b379eeb3863db060

[TRUNCATED]

```md
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
```

Full source: openspec/changes/runtime-management-gap/design.md

## openspec/changes/runtime-management-gap/tasks.md

- Source: openspec/changes/runtime-management-gap/tasks.md
- Lines: 1-63
- SHA256: a9dbc23d6a79e90b3fd18146275128b347b87e80891038faf6f1c9209a91afce

```md
# Tasks: runtime-management-gap

## 1. Runtime Inventory and Readback

- [ ] 1.1 Define runtime identity, health, capability, capacity, version, artifact-digest, and deployment-location persistence models.
- [ ] 1.2 Add manager APIs for runtime registration, heartbeat, command acknowledgement, inventory, and health transition.
- [ ] 1.3 Implement read-only adapters for Edge Agent, Manager Chat Worker, AgentTeams Worker, TeamHarness/plugin manager, and preview-pg.
- [ ] 1.4 Make every runtime report version and artifact digest from manifest/release metadata and reject hard-coded authority.
- [ ] 1.5 Add heartbeat liveness grace, `starting`, `degraded`, `recently_lost`, and `offline` transitions.
- [ ] 1.6 Add focused model/API/adapter tests and a read-only runtime inventory view.

## 2. Durable Task Lifecycle

- [ ] 2.1 Define task state, idempotency, admission, claim, lease, checkpoint, attempt, failure classification, and terminal-result models.
- [ ] 2.2 Implement slot-before-claim admission using server limits, runtime health, free slots, and local capacity.
- [ ] 2.3 Add claim, prepare-lease renewal, start, checkpoint, progress, result, cancel, stale-recovery, and failure APIs.
- [ ] 2.4 Integrate one read-only diagnostic worker and repair-preview execution with the shared task lifecycle.
- [ ] 2.5 Integrate AgentTeams/TeamHarness task reporting while preserving the existing incident-phase ledger as evidence.
- [ ] 2.6 Add restart/failover tests proving no duplicate read-only or disposable-preview work.

## 3. Workspace and Disk Governance

- [ ] 3.1 Define per-task `workdir/output/logs/config/temp` layout, claim lock, GC metadata, retention class, and evidence policy.
- [ ] 3.2 Implement task workspace creation, ownership validation, lock hold/release, and fail-safe reset.
- [ ] 3.3 Add bounded disk scans and per-task/incident/workspace/runtime/node/fleet usage reporting.
- [ ] 3.4 Implement policy-driven reclaim for regenerable artifacts, temporary data, stale preview environments, and expired workspaces.
- [ ] 3.5 Add quota/TTL admission checks and crash/recovery/GC tests that preserve evidence.

## 4. Task Credential Broker

- [ ] 4.1 Define task token, external credential grant, scope, expiry, renewal, revocation, and destruction models.
- [ ] 4.2 Mint task-bound credentials at claim time and revoke them at terminal state, lease expiry, cancellation, or runtime loss.
- [ ] 4.3 Broker or narrowly scope cloud, database, plugin API, and SSH credentials instead of exporting broad long-lived environment secrets where feasible.
- [ ] 4.4 Audit credential issuance, use, renewal, denial, revocation, and destruction.
- [ ] 4.5 Add centralized output/error sanitization for task results, logs, evidence, telemetry, and UI.
- [ ] 4.6 Add token expiry, revocation, cross-task denial, audit, and redaction tests.

## 5. Plugin and Worker Rollout

- [ ] 5.1 Define immutable plugin release, artifact digest, signature/verification state, compatibility matrix, and capability manifest.
- [ ] 5.2 Add per-node/per-runtime desired version and observed readback state.
- [ ] 5.3 Implement plugin reconciliation on Node Runtime Agent with version, digest, capability, health, and compatibility readback.
- [ ] 5.4 Add canary, ordered batch, health gate, pause/resume, failure isolation, and rollback APIs.
- [ ] 5.5 Pin the plugin release snapshot used by each running task.
- [ ] 5.6 Add rollback and mixed-version compatibility tests.

## 6. Runtime Event Timeline and Operations UI

- [ ] 6.1 Define append-only runtime event schema and correlation IDs.
- [ ] 6.2 Emit registration, health, drift, task, workspace, credential, plugin, rollout, and rollback events.
- [ ] 6.3 Add timeline query APIs filtered by runtime, task, incident, plugin, approval, and trace.
- [ ] 6.4 Add runtime inventory, task recovery, capacity, disk, credential, rollout, and event-timeline UI.
- [ ] 6.5 Add retention, authorization, and event-completeness tests.

## 7. End-to-End Verification

- [ ] 7.1 Verify read-only inventory across all first-slice runtimes.
- [ ] 7.2 Simulate Manager, worker, edge, plugin, and preview-runner restarts and verify recovery semantics.
- [ ] 7.3 Simulate network partition and verify fail-closed new mutating work with bounded local autonomy.
- [ ] 7.4 Verify canary failure pauses rollout and automatically restores the previous healthy release.
- [ ] 7.5 Verify disk GC preserves evidence and only reclaims policy-approved artifacts.
- [ ] 7.6 Verify task credentials expire, revoke, audit, and redact as required.
- [ ] 7.7 Run security, migration, Go, Python, and end-to-end regression suites.
```

## openspec/changes/runtime-management-gap/specs/runtime-management/spec.md

- Source: openspec/changes/runtime-management-gap/specs/runtime-management/spec.md
- Lines: 1-140
- SHA256: 6beccdaed7c1419cadf69f03782bc53d3f1e0183dd8dd646d95292f01f0aeadd

[TRUNCATED]

```md
## ADDED Requirements

### Requirement: Runtime inventory is authoritative
OpsKeeper SHALL maintain a server-authoritative inventory of registered runtimes containing identity, kind, profile, owner, deployment location, labels, capabilities, version, artifact digest, health, heartbeat, capacity, resource usage, and active task count.

#### Scenario: Manager lists first-slice runtimes
- **WHEN** an administrator queries runtime inventory
- **THEN** Edge Agent, Manager Chat Worker, AgentTeams Worker, TeamHarness/plugin manager, and preview-pg runtimes are represented with stable runtime IDs and current readback

#### Scenario: Runtime reports a manifest version
- **WHEN** a runtime heartbeat contains version and artifact metadata
- **THEN** the stored observed version comes from manifest or release metadata rather than a hard-coded UI/script constant

### Requirement: Runtime health has bounded transitions
OpsKeeper SHALL distinguish `starting`, `online`, `degraded`, `recently_lost`, and `offline` and SHALL require configurable heartbeat/grace windows before control decisions.

#### Scenario: One heartbeat is missed
- **WHEN** an otherwise healthy runtime misses one heartbeat interval
- **THEN** it is not immediately classified as permanently offline

#### Scenario: Runtime stops reporting beyond the grace window
- **WHEN** no valid heartbeat or liveness record arrives before the reconnect deadline
- **THEN** the runtime transitions to `recently_lost` or `offline` and active task recovery begins according to task policy

### Requirement: Task lifecycle is durable and claim-safe
OpsKeeper SHALL persist task identity, idempotency, admission, runtime claim, lease, checkpoint, attempt, failure classification, workspace identity, and terminal result across Manager, worker, node-agent, and preview-runner restarts.

#### Scenario: Worker restarts after checkpoint
- **WHEN** a recoverable task worker restarts with a valid claim and checkpoint
- **THEN** the task resumes from the checkpoint or terminates safely without duplicating declared side effects

#### Scenario: No capacity is available
- **WHEN** either the Manager concurrency limit or the runtime-reported free slot/capacity limit is reached
- **THEN** no new task claim is created

### Requirement: Task workspaces are isolated and reclaimable
Every task SHALL have an explicit workspace identity with distinct work, output, logs, config, and temporary areas, a claim lock, retention policy, and disk accounting.

#### Scenario: Two tasks target the same workspace
- **WHEN** a second task cannot acquire the workspace claim lock
- **THEN** it waits, selects another workspace, or is rejected according to task policy instead of resetting live work

#### Scenario: Reclaimable workspace artifacts expire
- **WHEN** lifecycle policy marks regenerable or temporary data eligible for cleanup
- **THEN** it is reclaimed while default-preserved evidence and output remain available

### Requirement: Task credentials are scoped and revocable
OpsKeeper SHALL issue credentials to a task/runtime/plugin scope with expiry and revocation and SHALL audit credential lifecycle and sanitize task outputs.

#### Scenario: Task reaches terminal state
- **WHEN** a task succeeds, fails, is cancelled, or exceeds its lease
- **THEN** its task token and unresolved credential grants are revoked and scheduled for local destruction

#### Scenario: Credential-bearing output is persisted
- **WHEN** task output or errors contain configured secret patterns
- **THEN** the persisted/displayed representation is redacted before entering evidence, telemetry, logs, or UI

### Requirement: Plugin releases are immutable and rollout is gated
OpsKeeper SHALL store immutable plugin releases with version, manifest, artifact digest, compatibility metadata, and verification state, and SHALL reconcile per-runtime desired state against observed readback.

#### Scenario: Canary reports unhealthy readback
- **WHEN** a canary batch fails health, compatibility, digest, or capability checks
- **THEN** rollout pauses before broader deployment and can restore the previous healthy release

#### Scenario: Task starts with an active plugin snapshot
- **WHEN** a task uses plugin-provided capabilities
- **THEN** its task record references the exact plugin release snapshot used for execution

### Requirement: Runtime events form a correlated timeline
OpsKeeper SHALL append runtime lifecycle, health, drift, task, workspace, credential, and plugin rollout events with runtime, task, incident, workspace, approval, plugin, trace, and actor correlation IDs where applicable.

#### Scenario: Operator investigates an incident
- **WHEN** the operator opens runtime events for an incident
- **THEN** registration, claims, recovery, credential, workspace, plugin, and health events can be queried in one correlated timeline

### Requirement: Runtime version provenance and drift are manager-validated
OpsKeeper SHALL accept authoritative runtime versions only from `manifest` or `release_metadata` provenance. Manager policy SHALL validate reported version and artifact digest, while signature verification SHALL remain a separate release verification state.

#### Scenario: Runtime reports manifest provenance with matching digest
- **WHEN** a heartbeat reports `version_source = manifest` with a version and artifact digest that match Manager's expected release
```

Full source: openspec/changes/runtime-management-gap/specs/runtime-management/spec.md

