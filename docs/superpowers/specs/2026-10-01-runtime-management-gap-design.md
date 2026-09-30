---
comet_change: runtime-management-gap
role: technical-design
canonical_spec: openspec
---

# Runtime Management Gap Technical Design

## Architecture

OpsKeeper adds a Manager-owned runtime control plane without replacing its incident loop. Manager remains authoritative for runtime identity, admission, claims, leases, task tokens, workspace policy, plugin desired state, rollout, audit, and the correlated runtime timeline. Existing Edge Agent evolves incrementally into Node Runtime Agent; Manager Chat Worker, AgentTeams/TeamHarness, plugin manager, and repair-preview remain independent executors behind read-only or controlled adapters.

The first slice intentionally follows **registry before scheduler**. It standardizes inventory, heartbeat, readback, local capacity, durable claims, and recovery semantics before considering a global scheduler. Per-runtime slot admission provides enough ordering for the first slice while preserving incident-loop authority.

### Control-plane components

- **Runtime Registry**: persistent, server-authoritative runtime records and observed readback.
- **Task Lifecycle**: durable task requests, safety classification, admission, claims, leases, checkpoints, recovery, and terminal results.
- **Workspace Governor**: per-task workspace layout, claim locks, quotas, bounded disk accounting, retention, and reclaim.
- **Credential Broker**: task-bound tokens and narrowly scoped, expiring external grants with lifecycle audit.
- **Plugin Rollout Controller**: immutable releases, compatibility metadata, desired/observed state, canary batches, pause, and rollback.
- **Runtime Timeline**: append-only `runtime_events` rows with correlated incident, task, workspace, plugin, approval, trace, and actor IDs.

Node Runtime Agent reports facts and executes Manager-authorized work. It never self-authorizes mutating work, changes safety policy, or treats local manifest declarations as authority.

## Corrected Design Decisions

1. **Manager owns timing policy.** Heartbeat interval, prepare-lease duration, running lease, grace, and reconnect windows are configured in Manager policy by runtime profile or kind. A runtime may report its profile and observed capacity, but its manifest cannot override Manager safety thresholds.
2. **Version provenance is explicit.** Accepted `version_source` values are only `manifest` and `release_metadata`. Signature verification is a separate release verification state, not a version source. Hard-coded or script constants are anomalies without authority.
3. **Metadata drift does not discard liveness.** A heartbeat with a version or digest mismatch is still accepted as a health observation. Manager marks the runtime degraded, emits a drift event, and blocks unsuitable admission; it does not reject the heartbeat and lose observability.
4. **Credential issuance and terminal audit are separate.** Issuance requires task/runtime/plugin scope, expiry, and issuance audit fields. `revoked_at`, revocation reason, or denial reason belongs to later lifecycle events and is never a prerequisite for creating an otherwise valid grant.
5. **No second requirements source.** This document defines implementation mechanics; the OpenSpec delta remains canonical for acceptance.

## Runtime Domain Model

`Runtime` is identified by a stable Manager-assigned `runtime_id` and records:

- tenant, runtime kind, profile, owner, deployment location, node identity, and labels;
- capability manifest digest, observed version, artifact digest, and `version_source`;
- health, last valid heartbeat, command acknowledgement cursor, and health transition time;
- configured and observed slot/capacity limits, active task count, and resource snapshot;
- workspace/disk summary when the runtime owns a workspace;
- immutable creation metadata plus mutable observed readback fields.

Health is `starting`, `online`, `degraded`, `recently_lost`, or `offline`. A delayed but in-grace heartbeat remains online. Beyond grace, Manager marks `recently_lost` and starts task recovery according to task policy. Only a configured reconnect failure or operator decision produces `offline`.

The first adapters are read-only:

- Edge Agent reports existing tunnel, plugin, local command, and host capability state.
- Manager Chat Worker reports process identity, version metadata, active sessions, and local capacity.
- AgentTeams/TeamHarness worker reports execution identity, plugin health, task reporting, and capacity.
- Plugin manager reports installed releases, digests, signatures, capabilities, and compatibility readback.
- repair-preview runner reports fixture identity, version metadata, run state, disposable environment, and capacity.

## Task Lifecycle

Task state is `queued`, `admitted`, `claimed`, `preparing`, `running`, `succeeded`, `failed`, or `cancelled`. Every task stores tenant, incident/task correlation, kind, safety class, idempotency key, requested capability/plugin snapshot, admission result, runtime claim, attempt, lease deadlines, checkpoint pointer, failure classification, workspace identity, and terminal result.

Admission is slot-before-claim:

1. Manager validates tenant, task identity, authorization, plugin compatibility, credential policy, workspace quota, and safety class.
2. Manager checks fleet concurrency, runtime health, runtime-reported free slots, local capacity, and configured bounds.
3. Only after all checks pass does Manager atomically reserve a slot and create a claim.
4. Claim creation returns one task token and the exact plugin release snapshot pointer.

`preparing` may hold a short lease while the runtime resolves inputs, workspace, plugins, and credentials. `running` uses a separately configured lease and requires liveness plus progress/checkpoint updates. Read-only tasks may automatically resume from a checkpoint. Mutating and destructive tasks require a persisted idempotency contract fingerprint before automatic retry and never bypass the existing HITL approval boundary.

Recovery choices are explicit:

- valid lease and checkpoint: resume;
- valid lease without checkpoint: continue bounded local autonomy;
- expired lease: revoke credentials, release the slot, and recover according to safety class;
- no idempotency contract on mutating work: terminal failure `no_idempotency_contract`;
- duplicate terminal submission: return the stored terminal result.

## Workspace and Disk Governance

Each task workspace has distinct `workdir`, `output`, `logs`, `config`, and `temp` roots. The workspace record stores task, incident, runtime, node, claim lock, retention class, quota, GC metadata, and evidence policy. A live workspace is never reset unless the caller holds its claim lock.

Disk scans operate under a workspace root and do not follow symlinks that exit that root. They report bounded usage by task, incident, workspace, runtime, node, and fleet. Reclaim is policy-driven and defaults to preserving evidence and output; only regenerable artifacts, temporary data, stale disposable preview environments, and expired non-evidence workspaces are eligible.

## Credential Broker

Task identity is the credential boundary. Manager mints a short-lived task token bound to task, runtime, incident, plugin release digest, scope, and expiry. External grants are brokered where possible; otherwise a narrowly scoped runner secret is injected only into the authorized execution context.

An issuance audit row must contain issued-at time, scope hash, runtime ID, task ID, plugin release digest, expiry, and issuer/actor identity. Denial, renewal, use, revocation, and destruction are separate append-only lifecycle events. Terminal task state, cancellation, lease expiry, or runtime loss triggers revocation and scheduled local destruction. All output, errors, logs, evidence, telemetry, and UI projections pass centralized sanitization.

## Plugin Releases and Rollout

A plugin release is immutable and stores version, manifest, artifact digest, capability manifest digest, compatibility matrix, publisher identity, and verification state. Desired state points a node or runtime to one release digest. Observed readback reports loaded version, artifact digest, capability digest, health, and compatibility outcome.

Rollout uses canary and ordered batches with health gates, pause/resume, failure isolation, and rollback to the previous healthy release. A batch succeeds only after readback matches desired version, digest, capability, health, and compatibility. A task admitted with a plugin capability stores the immutable `plugin_release_digest` and `capability_digest`; later rollout does not mutate that running task.

## Runtime Events

`runtime_events` is an append-only table, not a mutable status log. Events carry event class, retention class, event time, actor, payload digest, and correlation IDs for runtime, task, incident, workspace, credential grant, plugin release, rollout, approval, and trace. Timeline queries project these rows and never update them. Existing incident evidence and AgentTeams ledgers remain evidence sources; runtime events explain control-plane decisions.

## API Boundaries

Manager exposes internal versioned APIs for registration, heartbeat/readback, acknowledgement, inventory, admission, claim, lease renewal, checkpoint/progress, result, cancel, workspace report/reclaim, credential lifecycle, plugin desired/readback, rollout control, and timeline query. Runtime adapters translate local process or node APIs into those contracts.

Automatic mutation remains fail-closed. TeamHarness and plugins can contribute skills, probes, tools, handlers, and safety metadata, but cannot grant themselves runtime permission, alter desired release state, or bypass Manager/HITL authority.

## Testing Strategy

- **Registry**: registration idempotency, manifest/release provenance, drift degradation, heartbeat grace transitions, restart persistence, and inventory authorization.
- **Lifecycle**: state-machine property tests, slot-before-claim races, lease expiry, checkpoint resume, duplicate result, cancellation, and no-idempotency failure.
- **Workspace**: lock races, fail-safe reset, external symlink traversal denial, quota admission, concurrent GC, and evidence preservation.
- **Credentials**: issuance validation, expiry, renewal, revocation, cross-task denial, audit completeness, and redaction fuzzing.
- **Plugins**: immutable-release updates, digest/capability mismatch, mixed-version compatibility, canary failure pause, and automatic rollback.
- **Recovery**: Manager, worker, edge, node-agent, plugin, and preview-runner restarts plus network-partition scenarios.
- **Regression**: focused Go/Python tests followed by migration, security, and end-to-end incident-loop suites.

## Delivery Sequence

1. Persist runtime inventory and read-only adapters.
2. Add task lifecycle APIs and integrate one read-only diagnostic worker plus repair preview.
3. Add workspace locks, disk accounting, policy reclaim, and preview cleanup.
4. Add task tokens, credential grants, revocation, audit, and sanitization.
5. Add immutable plugin releases and gated rollout.
6. Add runtime timeline and operations views.
7. Run end-to-end recovery, rollout, GC, credential, and incident-loop regressions.
