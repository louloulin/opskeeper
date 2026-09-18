## ADDED Requirements

### Requirement: Initiate scenarios through the Manager control plane
The home demo console SHALL start a controlled PostgreSQL pool-exhaustion scenario through an OpsKeeper Manager API rather than directly operating the pool fixture or storing database credentials.

#### Scenario: One-click injection is requested
- **WHEN** the presenter clicks the injection action on `home.yueming.xin`
- **THEN** Manager validates the scenario, target fingerprint, duration, and blast radius
- **AND** Manager creates or returns the idempotent incident before starting the fixture

#### Scenario: Injection is repeated
- **WHEN** the same scenario is requested again while it is active
- **THEN** Manager returns the existing incident without creating a duplicate injection or incident

### Requirement: Produce comparable remediation evidence
The final demo SHALL separate business incident observation from controlled remediation rehearsal, and every candidate comparison SHALL be bound to a replay profile containing the workload model, concurrency, duration, SQL distribution, timeout, random seed, target version, and candidate version.

#### Scenario: Candidate comparison is comparable
- **WHEN** a preview candidate reports baseline and candidate metrics
- **THEN** both runs reference the same `replay_profile_id`
- **AND** the comparison may be used in the approval evidence

#### Scenario: Replay profiles differ
- **WHEN** baseline and candidate runs do not share the same replay profile
- **THEN** the comparison is marked `NOT COMPARABLE`
- **AND** it cannot be presented as approval evidence

### Requirement: Show business impact and recovery across demo surfaces
The final demo SHALL make database-dependent home queries visibly fail or degrade during injection while preserving the page shell, and SHALL use monitoring and the collaboration room to explain diagnosis and remediation before the same home queries are rechecked after recovery.

#### Scenario: Injection impacts database-backed widgets
- **WHEN** the pool-exhaustion scenario is active
- **THEN** order, inventory, and audit query areas show a bounded error, timeout, or degraded state
- **AND** static page navigation remains usable without a full-page failure

#### Scenario: Recovery is confirmed
- **WHEN** the approved remediation finishes
- **THEN** monitoring shows pool capacity and utilization recovering
- **AND** the home database queries return normal data without bypassing the same application connection path

### Requirement: Demonstrate single-host control-plane resilience
OpsKeeper SHALL keep incident alerting, diagnosis evidence, candidate rehearsal, approval state, execution state, and verification results persisted by incident ID while running the public demo without database high availability.

#### Scenario: Manager restarts while approval is pending
- **WHEN** Manager is restarted while an incident is waiting for approval or verification
- **THEN** the incident and its progress remain visible after restart
- **AND** no in-flight remediation is silently duplicated

#### Scenario: Remediation is retried
- **WHEN** a remediation request is retried after restart
- **THEN** OpsKeeper validates its execution ID and target fingerprint first
- **AND** it rejects execution when the target fingerprint does not match the approved target

### Requirement: Present honest reliability boundaries
The demo SHALL describe the current deployment as isolated single-host Docker reliability hardening and SHALL NOT claim automatic failover, cross-availability-zone recovery, or equivalence to production database high availability.

#### Scenario: Reliability boundary is displayed
- **WHEN** the preview page or presentation explains control-plane reliability
- **THEN** it identifies isolation, persistence, backup, restart retention, and idempotency as implemented mechanisms
- **AND** it names managed PostgreSQL or PolarDB high availability as the production evolution path rather than a current capability
