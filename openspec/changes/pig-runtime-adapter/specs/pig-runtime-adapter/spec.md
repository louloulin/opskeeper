# Specification: PiG Runtime Adapter

## Purpose

Run a pinned, isolated PiG/Piglet runtime as an optional OpsKeeper agent runtime while preserving Manager authority over admission, capability, tools, credentials, events, approval, and audit.

## ADDED Requirements

### Requirement: PiG is an explicitly selected runtime
OpsKeeper SHALL launch PiG only when a tenant policy, runtime selector, incident, and role explicitly admit a registered PiG release.

#### Scenario: Investigator role selects PiG
- **WHEN** an investigator task is admitted with `runtime_kind = pig`
- **THEN** Manager launches the exact registered PiG release and records its artifact and capability digests on the task

#### Scenario: PiG is not enabled
- **WHEN** a tenant or role has no PiG runtime policy
- **THEN** the task selects an existing supported runtime and no PiG process starts

### Requirement: PiG releases are immutable and verified
OpsKeeper SHALL launch PiG only from a release record containing source identity, version, artifact digest, compatibility result, capability manifest, and verification state, and SHALL reject mutable or drifted artifacts.

#### Scenario: Artifact digest drifts
- **WHEN** the resolved PiG or Piglet artifact digest differs from the registered release
- **THEN** startup fails with a drift error and the runtime is not admitted

#### Scenario: Release is valid
- **WHEN** digest, compatibility, and verification checks pass
- **THEN** the task execution snapshot records the exact release and capability digests

### Requirement: Runtime capability is Manager-authoritative
OpsKeeper SHALL expose a PiG-registered capability only when it also exists in the immutable release capability manifest, runtime allowlist, tenant policy, role policy, and incident scope.

#### Scenario: Runtime registers an undeclared tool
- **WHEN** PiG reports a tool absent from the release manifest or allowlist
- **THEN** the tool remains invisible, the observation is recorded as drift, and startup is marked degraded or fails according to policy

#### Scenario: Capability passes every policy
- **WHEN** a read-only diagnostic capability is declared, observed, allowlisted, and incident-scoped
- **THEN** Manager exposes only the stable OpsKeeper tool identity to the agent

### Requirement: Tool calls pass the governed gateway
OpsKeeper SHALL route every PiG extension tool call through the Governed Tool Gateway and SHALL enforce read/write/destructive class, tenant, incident, role, resource, approval, rate limit, redaction, and audit before dispatch.

#### Scenario: Read-only diagnostic call is allowed
- **WHEN** an allowed PiG tool invokes a read-only OpsKeeper diagnostic capability for its own incident
- **THEN** the gateway executes the bounded operation and records a correlated tool event

#### Scenario: Repair candidate requests execution
- **WHEN** a PiG tool requests a mutating repair
- **THEN** the gateway accepts at most a proposal and does not execute the mutation without the existing human-approved exact payload path

### Requirement: PiG processes are isolated and observable
OpsKeeper SHALL run PiG extensions in an isolated execution environment, disable ambient discovery, expose only authorized task context, and support heartbeat, cancellation, crash, and terminal handling.

#### Scenario: Operator cancels task
- **WHEN** a task is cancelled while a PiG tool is running
- **THEN** the adapter cancels owned work, terminates the isolated process if needed, records the cancellation, and does not replay the interrupted operation automatically

#### Scenario: Process loses heartbeat
- **WHEN** the PiG process misses its configured grace window
- **THEN** Manager marks it degraded or offline, emits an event, and follows task recovery policy without duplicating side effects

### Requirement: PiG does not receive durable production credentials
OpsKeeper SHALL NOT expose host secrets or durable production credentials to a PiG process; task access SHALL be represented by narrowly scoped task identity and gateway authorization.

#### Scenario: Adapter environment is assembled
- **WHEN** a PiG process starts
- **THEN** its environment contains task and runtime identity and configuration but no durable database, cloud, Kubernetes, or vault credential

#### Scenario: Extension requests a secret
- **WHEN** an extension attempts to read or request a disallowed secret
- **THEN** the request fails closed and is audited without revealing secret material
