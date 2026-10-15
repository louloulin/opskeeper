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
