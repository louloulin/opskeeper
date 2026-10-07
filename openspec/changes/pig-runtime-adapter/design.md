# Design: PiG Runtime Adapter and Pi/PiG Ecosystem Adoption

## Context

OpsKeeper already has an in-process ChatRuntime, AgentTeams/TeamHarness integration, and an MCP tool boundary. PiG provides a Pi-compatible Go harness with subprocess extensions, multi-language SDKs, packages, Piglets, package installation, and lifecycle primitives. PiG is useful as an ecosystem host, but it is not an OpsKeeper control plane and its subprocess boundary is not a permission sandbox.

The separate active `runtime-management-gap` change defines the future common runtime inventory, durable task lifecycle, credential broker, immutable plugin releases, and event timeline. This change integrates PiG with those target semantics without duplicating them.

## Goals / Non-Goals

**Goals**

- Make PiG an opt-in agent runtime behind an `AgentRuntimeAdapter` port.
- Support a read-only investigator-style PiG execution slice first.
- Make Pi/PiG capabilities explicit, versioned, allowlisted, and auditable.
- Reuse OpsKeeper approval, incident, recovery, and audit authority.
- Define a staged adoption path and measurable promotion gates.
- Leave ChatRuntime and AgentTeams operational and selectable.

**Non-Goals**

- No wholesale replacement of the current agent engine.
- No direct PiG extension access to production databases, cloud credentials, Kubernetes, nodes, or repair execution.
- No ambient PiG extension discovery from user or project directories.
- No public marketplace hosting or unreviewed third-party plugin execution.
- No new generic runtime inventory, task lease, credential broker, or plugin rollout system; those belong to `runtime-management-gap`.
- No mutating PiG tool execution in the first implementation slice.

## Target Architecture

```text
Web Console / API
  └─ Manager incident, proposal, approval, audit, knowledge
      └─ Agent Runtime Orchestrator
          ├─ OpsKeeper ChatRuntime adapter
          ├─ AgentTeams/TeamHarness adapter
          └─ PiG Runtime Adapter
                ├─ pinned PiG executable or Piglet release
                ├─ isolated extension/Piglet process
                ├─ normalized lifecycle and capability events
                └─ Governed Tool Gateway
                     ├─ read-only diagnostic tools
                     ├─ proposal-only repair tools
                     └─ approved execution tools
```

The Manager remains the only authority for incident state, admission, tool capability, credentials, approval, recovery, verification, and audit. PiG hosts code and extension protocol behavior; it does not own security policy.

## Key Decisions

### 1. Adapter before replacement

The first boundary is an `AgentRuntimeAdapter` port, not a rewrite. Existing runtimes keep their behavior while PiG becomes an opt-in implementation. This avoids coupling OpsKeeper's governance to PiG's pre-stable evolution.

### 2. PiG processes are ecosystem hosts, not security boundaries

Every PiG extension or Piglet runs in an isolated worker or constrained OS/container environment. It receives a task token and narrow tool access, not durable host secrets. Subprocess isolation is used for crash containment and cancellation only.

### 3. Capability visibility is server-authoritative

Manager stores an allowlisted capability manifest per immutable PiG release. PiG runtime registration is evidence, not authority. A tool is exposed only if its release, identity, compatibility, capability class, tenant policy, incident scope, and role policy permit it.

### 4. All side-effect-capable calls pass through the Governed Tool Gateway

Read-only diagnostic tools can execute after allowlist checks. Repair or destructive actions can only create proposals through OpsKeeper APIs. Actual execution remains bound to approved incident, resource, command, and payload identities. PiG extensions cannot bypass the approval ledger or direct execution API.

### 5. Packages are immutable and reproducible

Pi/PiG packages and Piglets are represented by immutable source identity, version, digest, compatibility matrix, capability manifest, and verification result. Running work records the exact release snapshot. Mutable package replacement is forbidden once a task is admitted.

### 6. Adoption advances only through promotion gates

Each phase has explicit security and reliability evidence. Missing evidence blocks promotion rather than narrowing policy automatically.

## Data Flow

1. Operator registers a reviewed PiG executable or Piglet release.
2. Manager validates manifest, digest, compatibility, resource inventory, capability declarations, and policy mapping.
3. Admission selects PiG only for an allowed tenant, incident scope, and role.
4. Adapter launches an isolated PiG process with exact release, task identity, locale, model policy, and no ambient discovery.
5. Adapter normalizes registration, heartbeat, start, progress, tool call, cancellation, failure, and completion events.
6. Extension tool calls are translated to OpsKeeper tool IDs and sent to the Governed Tool Gateway.
7. Gateway enforces read/write/destructive class, incident scope, resource identity, rate limits, redaction, approval state, and audit.
8. Manager persists terminal output and immutable evidence with the release and capability digests.

## Pi/PiG Adoption Roadmap

### Phase 0 — Ecosystem and dependency inventory

- Inventory Pi/PiG packages relevant to diagnostics, observation, Kubernetes, databases, network, logs, cloud APIs, and runbooks.
- Record license, source, maintainers, dependencies, capabilities, network access, filesystem access, secrets, release cadence, and security posture.
- Produce a shortlist of first read-only candidates and explicit rejected packages.
- Promotion gate: reviewed inventory and at least one useful read-only candidate.

### Phase 1 — Non-executable resource ingestion

- Import allowlisted skills, prompts, themes, and agent declarations from Pi/PiG packages.
- Skip hooks, MCP definitions, and executable extensions by default.
- Preserve source, version, digest, license, and tenant visibility.
- Promotion gate: deterministic import, conflict handling, provenance display, and no implicit execution.

### Phase 2 — Read-only PiG Runtime Adapter

- Implement the runtime port and a PiG driver for one investigator-style role.
- Pin one immutable PiG/Piglet release.
- Expose only read-only diagnostic tools through the gateway.
- Run without ambient discovery, host writes, provider overrides, or production credentials.
- Promotion gate: launch, heartbeat, event normalization, cancellation, isolation, audit completeness, and E2E read-only incident success.

### Phase 3 — Governed executable extensions

- Add private package admission, signature and digest verification, compatibility checks, capability allowlists, sandbox profiles, and rollout readback.
- Permit read-only executable extensions after review.
- Allow proposal generation but not direct repair execution.
- Promotion gate: security review, sandbox escape tests, rollback, drift detection, and no audit bypass.

### Phase 4 — Gradual role migration

- Extend PiG to reporter, critic, reviewer, verifier, and finally repairer.
- Repairer remains proposal-only from PiG; execution stays in the existing approved backend path.
- Use canary tenants and incidents, compare quality and latency with current runtimes, and retain rollback to ChatRuntime or AgentTeams.
- Promotion gate: role-specific benchmark parity or improvement with no safety regression.

## OPC Squad Execution Model

- **Architecture owner:** runtime port, target-state contracts, and dependency boundary with `runtime-management-gap`.
- **Runtime engineer:** PiG process lifecycle, driver, health, cancellation, and event normalization.
- **Plugin/ecosystem engineer:** package parsing, immutable release inventory, resource import, compatibility validation, and Phase 0 catalog.
- **Security engineer:** capability policy, tool gateway, sandbox profile, secrets, redaction, audit, and abuse tests.
- **Verification engineer:** unit, integration, E2E, recovery, rollout, rollback, and role-parity suites.
- **Product/operator representative:** tenant policy, migration gates, dashboard inventory, and operational runbooks.

Implementation tasks remain independently reviewable so the squad can progress in parallel without changing the safety contract.

## Risks / Trade-offs

- **PiG is pre-stable** → isolate behind the adapter port and pin exact releases.
- **Ecosystem quality varies** → private allowlist and Phase 0 review precede execution.
- **Generic runtime management is not complete yet** → implement a read-only self-contained slice and migrate to shared models later.
- **Extension capability can be underdeclared** → runtime behavior tests and sandbox constraints are mandatory; declarations alone do not grant trust.
- **Dual runtime paths add complexity** → adapter metrics and promotion gates decide when older paths are retired.

## Migration Plan

1. Add the runtime port and policy-neutral event and capability models.
2. Implement PiG adapter lifecycle with a test driver and a pinned real PiG release.
3. Connect only read-only tools to the governed gateway.
4. Add immutable Pi/PiG release inventory and non-executable resource import.
5. Add E2E investigator validation and operational runbooks.
6. Use the runtime-management plugin and task models when available; do not fork a second control-plane implementation.

## Open Questions

- None blocking the read-only first slice. The PiG executable distribution and exact subprocess protocol are implementation details to be pinned during build.
