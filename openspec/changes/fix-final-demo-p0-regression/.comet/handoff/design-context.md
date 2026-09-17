# Comet Design Handoff

- Change: fix-final-demo-p0-regression
- Phase: design
- Mode: compact
- Context hash: af184a55d48c13cfffa5bf1bb10a10dec66c9733eaf2aeb69b37472dcc8e979f

Generated-by: comet-handoff.sh

OpenSpec remains the canonical capability spec. This handoff is a deterministic, source-traceable context pack, not an agent-authored summary.

## openspec/changes/fix-final-demo-p0-regression/proposal.md

- Source: openspec/changes/fix-final-demo-p0-regression/proposal.md
- Lines: 1-31
- SHA256: d2f72af0b1d160a69d40bc1cc3a2653297bbaa2e797522ed37773e1c94ddd355

```md
## Why

The public final-demo regression exposed two P0 blockers: the pool metrics exporter is pinned to a previous fixture manifest, and AgentTeams role contexts contaminate one another, producing duplicate execution and a Matrix message storm. These defects prevent a trustworthy end-to-end incident demonstration.

## What Changes

- Replace manifest-pinned demo metrics with a versioned exporter that discovers every fixture from the authoritative pool-fixture service and emits `pool_manifest_id`-labeled series.
- Isolate Manager and Worker instructions and continuation handling by runtime role.
- Prevent workers from consuming Manager dispatch state or handling messages addressed to another role.
- Strip reasoning/thinking content before replies reach Matrix.
- Add per-agent message-rate protection and a reliable admin incident STOP circuit breaker.
- Add focused Go and Python tests for dynamic metrics, role routing, reply sanitization, rate limiting, and STOP behavior.

## Capabilities

### New Capabilities

- `pool-metrics-discovery`: Dynamically expose metrics for every pool fixture without redeploying or manually pinning a manifest.
- `agentteams-dispatch-isolation`: Keep Manager and Worker contexts separated, make stage continuation idempotent, and provide fail-safe message controls.

### Modified Capabilities

- None. This repository has no existing OpenSpec capability specs.

## Impact

- `cmd/pool-fixture` and its Go tests.
- Public-demo monitoring scripts under `deploy/demo/monitoring/pgpool-fixture`.
- `plugins/opskeeper-teamharness/adapters/qwenpaw/plugin.py` and related Python tests.
- TeamHarness package version metadata and generated release artifacts during the later build phase.
- No shared PostgreSQL or Redis schema changes; pool fixtures remain incident-owned and disposable.
```

## openspec/changes/fix-final-demo-p0-regression/design.md

- Source: openspec/changes/fix-final-demo-p0-regression/design.md
- Lines: 1-63
- SHA256: ce164a9e0bfe1da1af053d36a1741eeb9aeae2acac804568693b67c0e3ac9eb5

```md
## Context

The final-demo environment runs an incident-owned PostgreSQL pool fixture and an authenticated Prometheus scraper. The current public exporter is an unversioned Python container whose `POOL_MANIFEST_ID` environment variable is manually replaced for each incident. The TeamHarness plugin is installed in Manager and standalone Worker runtimes, but Manager prompt and continuation registrations are not consistently filtered by role.

## Goals / Non-Goals

**Goals:**

- Make pool metric collection independent of a manually pinned manifest.
- Ensure only Manager runtimes execute Manager continuation and relay logic.
- Ensure Workers do not receive Manager-only prompt instructions.
- Suppress reasoning/thinking text in outbound replies.
- Bound runaway message generation and honor an admin STOP for an incident.
- Preserve OpsKeeper Manager as the authority for incident records, approval, recovery execution, permissions, and redaction.

**Non-Goals:**

- No PolarDB, HA, schema, or shared PostgreSQL/Redis topology change.
- No replacement of the AgentTeams Matrix transport or its room membership model.
- No signed dispatch envelope protocol rewrite in this hotfix.
- No change to HITL approval authority or backend recovery validation.

## Decisions

### Pool metrics use authoritative dynamic discovery

The pool fixture already owns all live manifests and exposes an authenticated aggregate `/metrics` endpoint. The Go endpoint will include `pool_manifest_id` on every series. The versioned demo exporter will proxy that endpoint with its bearer token instead of storing a manifest ID. This avoids a new list API, prevents stale pinning, and allows PromQL to select the exact current incident manifest.

Alternative rejected: recreating the exporter with a new environment variable for every incident. That is the current failure mode and leaves no durable artifact in the repository.

### Manager behavior is selected by explicit role

`_is_manager_agent` will treat explicit Worker/standalone roles as non-Manager even if a Manager runtime variable leaks into the environment. Manager prompt sections and the continuation/relay hook will be registered or executed only for Manager contexts. Worker contexts will not consume Manager pending markers.

Alternative rejected: relying on `AGENTTEAMS_MANAGER_RUNTIME` alone. That variable describes a runtime family and is not a safe role identity.

### Replies are sanitized at the middleware boundary

A QwenPaw `on_reply` middleware will remove thinking blocks and literal `<think>...</think>` spans from final text events. This covers direct room replies without altering model reasoning itself.

### Fail-safe controls are process-local and bounded

Each runtime gets a small process-local reply/message limiter and an incident STOP set. An admin STOP records the incident and skips further non-admin turns that mention it. Rate breaches raise a terminal boundary error instead of emitting another Matrix message. These controls are intentionally simple to deploy quickly and stop recursive feedback.

## Risks / Trade-offs

- [Prometheus retains recent stale series for a closed manifest] → PromQL must filter by the current `pool_manifest_id`, while old series naturally expire.
- [Aggregate metrics include multiple fixtures] → Every series is manifest-labeled, making incident selection explicit.
- [Role inference may encounter an unknown deployment] → Explicit `standalone`/worker roles take precedence; tests cover the public-demo Manager and Worker configurations.
- [A process-local STOP set does not survive process restart] → Restart remains an operator recovery action; STOP prevents runaway output during an active incident.
- [A low rate limit could interrupt a legitimate long transcript] → The default permits short bursts and is environment-configurable.

## Migration Plan

1. Update the fixture metrics and add the versioned exporter.
2. Tighten TeamHarness role routing, reply sanitization, rate limits, and STOP handling.
3. Run focused Go and Python tests.
4. Build locally on macOS, deploy only approved artifacts to the public host, and run a fresh-manifest E2E.
5. Roll back by restoring the prior TeamHarness package and pool fixture image; no database migration is required.

## Open Questions

- None blocking implementation. Deployment and public E2E still require explicit user approval.
```

## openspec/changes/fix-final-demo-p0-regression/tasks.md

- Source: openspec/changes/fix-final-demo-p0-regression/tasks.md
- Lines: 1-20
- SHA256: 7b441c105d8731b01e9eb1bb6c0e134cd14329d21f11d85555455a2f39633a43

```md
## 1. Pool Metrics Fix

- [ ] 1.1 Add `pool_manifest_id` labels to aggregate pool-fixture Prometheus series.
- [ ] 1.2 Add a versioned, manifest-agnostic public-demo pool metrics proxy script.
- [ ] 1.3 Add Go tests proving multi-fixture metrics are distinct and dynamically exposed.

## 2. AgentTeams Isolation Fix

- [ ] 2.1 Make Manager identity reject explicit Worker/standalone roles and isolate Manager continuation state.
- [ ] 2.2 Register Manager, Worker, and Team prompt sections only for their valid runtime roles.
- [ ] 2.3 Add outbound reply sanitization for thinking blocks and literal thinking spans.
- [ ] 2.4 Add configurable outbound burst limits and admin incident STOP circuit breaking.
- [ ] 2.5 Add Python tests for role identity, prompt gating, continuation isolation, sanitization, rate limits, and STOP.

## 3. Verification and Packaging

- [ ] 3.1 Run focused pool-fixture Go tests.
- [ ] 3.2 Run TeamHarness Python unit and package validation tests.
- [ ] 3.3 Update release version metadata and build local release artifacts on macOS.
- [ ] 3.4 Obtain deployment approval, deploy the verified artifacts, and run a fresh-manifest public E2E.
```

## openspec/changes/fix-final-demo-p0-regression/specs/agentteams-dispatch-isolation/spec.md

- Source: openspec/changes/fix-final-demo-p0-regression/specs/agentteams-dispatch-isolation/spec.md
- Lines: 1-40
- SHA256: 55a824f010c5a8dc311aacc648c052f50bde2e2f9534dec534d6aa7f19076faa

```md
## ADDED Requirements

### Requirement: Manager continuation is role-isolated
The TeamHarness continuation and result-relay logic SHALL execute only in a Manager runtime, and an explicit Worker or standalone role MUST be treated as non-Manager.

#### Scenario: Worker sees a Manager dispatch marker
- **WHEN** a standalone Worker receives text containing a pending Manager task marker
- **THEN** it does not consume the marker, relay a result, or enter Manager continuation logic

### Requirement: Role prompts do not cross contexts
Manager-specific prompt content SHALL NOT be injected into standalone Worker contexts, and Worker prompt content SHALL NOT be injected into Manager contexts.

#### Scenario: Standalone worker builds its system prompt
- **WHEN** TeamHarness registers prompt sections for `opskeeper-repairer`
- **THEN** Manager-only coordination instructions are absent from that Worker prompt

### Requirement: Matrix replies exclude reasoning content
Outbound AgentTeams replies MUST NOT expose model reasoning or thinking content.

#### Scenario: Final reply contains a thinking span
- **WHEN** a model reply includes `<think>private reasoning</think>` and a public result line
- **THEN** only the public result line is emitted

### Requirement: Runaway messages are bounded
TeamHarness SHALL enforce a configurable per-agent outbound reply/message burst limit and terminate rather than continue emitting when the limit is exceeded.

#### Scenario: Repeated replies exceed the configured boundary
- **WHEN** an agent exceeds the configured outbound burst limit
- **THEN** no additional Matrix message is emitted for that boundary and a terminal rate-limit error is raised

### Requirement: Admin incident STOP is fail-safe
An administrator SHALL be able to stop an incident by ID, and subsequent non-admin messages that reference that incident MUST skip agent execution.

#### Scenario: Admin issues STOP
- **WHEN** an admin sends `ADMIN STOP <incident-id>`
- **THEN** the incident is recorded as stopped in that runtime before the model runs

#### Scenario: A stopped worker receives another incident message
- **WHEN** a non-admin message references a stopped incident
- **THEN** the agent turn is skipped without dispatch, room access, or status forwarding
```

## openspec/changes/fix-final-demo-p0-regression/specs/pool-metrics-discovery/spec.md

- Source: openspec/changes/fix-final-demo-p0-regression/specs/pool-metrics-discovery/spec.md
- Lines: 1-23
- SHA256: 6f4b981ae843b73e08e0a9ffd153567c51f33caacef6cfcc9501197e752bee80

```md
## ADDED Requirements

### Requirement: Pool metrics are dynamically manifest-labeled
The pool fixture service SHALL expose aggregate Prometheus metrics for every loaded fixture, and each series MUST carry a `pool_manifest_id` label.

#### Scenario: A new fixture is created
- **WHEN** a pool fixture is created while the metrics endpoint is already running
- **THEN** subsequent aggregate metric responses include series for that fixture without restarting or reconfiguring the endpoint

#### Scenario: Multiple fixtures exist
- **WHEN** aggregate metrics are requested for more than one fixture
- **THEN** active and capacity series remain distinguishable by `pool_manifest_id`

### Requirement: Demo exporter is manifest-agnostic
The public-demo pool metrics exporter SHALL obtain metrics from the authoritative pool-fixture aggregate endpoint and SHALL NOT require a manifest ID in its deployment configuration.

#### Scenario: Exporter starts before a fixture exists
- **WHEN** the exporter starts and no fixture is active
- **THEN** it remains healthy and emits no stale pool series

#### Scenario: Pool fixture becomes unavailable
- **WHEN** the exporter cannot reach the authoritative pool-fixture endpoint
- **THEN** it returns an upstream failure rather than fabricating pool values
```

