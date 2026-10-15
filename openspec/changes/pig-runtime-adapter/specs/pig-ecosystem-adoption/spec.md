# Specification: Pi/PiG Ecosystem Adoption

## Purpose

Adopt Pi/PiG packages, skills, prompts, agents, extensions, and Piglets through reviewed, immutable, phase-gated ingestion without making unreviewed ecosystem content executable.

## ADDED Requirements

### Requirement: Adoption follows explicit phases
OpsKeeper SHALL use ecosystem inventory, non-executable resources, read-only PiG execution, governed executable extensions, and gradual role migration as ordered adoption phases with promotion gates.

#### Scenario: Phase gate evidence is missing
- **WHEN** an operator requests executable extension admission before read-only runtime evidence is complete
- **THEN** Manager rejects the request and reports the missing gate evidence

#### Scenario: Phase gate evidence is complete
- **WHEN** all security, reliability, audit, and operational criteria pass
- **THEN** the operator may promote the next bounded phase

### Requirement: Ecosystem inventory is reviewed before execution
OpsKeeper SHALL record purpose, source, license, maintainers, dependencies, capabilities, access needs, release cadence, security posture, and review decision for every candidate Pi/PiG package before it becomes executable.

#### Scenario: Candidate lacks dependency review
- **WHEN** a package has no dependency or access review
- **THEN** it can appear only as a rejected inventory candidate and cannot be admitted to a tenant

#### Scenario: Useful read-only candidate is approved
- **WHEN** review confirms provenance, compatibility, bounded read-only behavior, and operational value
- **THEN** the package becomes eligible only for its reviewed resource types and phase

### Requirement: Non-executable resources import safely
OpsKeeper SHALL import only allowlisted skills, prompts, themes, and agent declarations in the non-executable phase and SHALL skip hooks, MCP definitions, and executable extensions by default.

#### Scenario: Package contains an extension
- **WHEN** a Phase 1 package includes an executable extension
- **THEN** the resource remains inert and the import result records it as skipped

#### Scenario: Skill is enabled
- **WHEN** an operator enables a reviewed skill for a tenant
- **THEN** runtime loading preserves source, version, digest, license, and conflict state without implicit code execution

### Requirement: Package releases are immutable
OpsKeeper SHALL store package source identity, version, artifact digest, resource inventory, capability manifest, compatibility matrix, and verification result as immutable release facts, and running tasks SHALL reference the exact release snapshot.

#### Scenario: Package is updated while a task runs
- **WHEN** a new package release becomes desired
- **THEN** the running task continues with its admitted release snapshot until terminal state

#### Scenario: Artifact changes under a fixed version
- **WHEN** a package artifact digest no longer matches its release record
- **THEN** admission and rollout fail closed with drift evidence

### Requirement: Existing runtimes remain selectable
Pi/PiG adoption SHALL NOT remove or degrade the existing OpsKeeper ChatRuntime or AgentTeams paths before role-specific parity, safety, rollback, and operational acceptance pass.

#### Scenario: PiG role validation fails
- **WHEN** PiG promotion evidence does not meet the role gate
- **THEN** the role remains on its current runtime and rollback remains available

#### Scenario: PiG role meets all gates
- **WHEN** canary incidents demonstrate parity or improvement without safety regression
- **THEN** the operator may increase PiG traffic for that role only
