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
- **THEN** Manager records the observation without downgrading the runtime

#### Scenario: Runtime reports drift or a forbidden source
- **WHEN** a heartbeat reports an unexpected artifact digest, `version_source = hardcoded`, or `version_source = script_constant`
- **THEN** Manager accepts the liveness observation, records the anomaly, marks the runtime `degraded`, and emits a version-drift event

### Requirement: Mutating tasks declare idempotency before automatic retry
OpsKeeper SHALL NOT automatically retry a task classified as mutating or destructive unless its idempotency contract fingerprint was persisted before the first claim, and SHALL preserve existing human approval boundaries during recovery.

#### Scenario: A mutating task restarts with an idempotency contract
- **WHEN** a claimed mutating task restarts or loses its lease after a checkpoint
- **THEN** Manager resumes from the checkpoint or terminates safely without duplicating declared side effects

#### Scenario: A mutating task has no idempotency contract
- **WHEN** a mutating task requires automatic recovery but no idempotency contract fingerprint exists
- **THEN** the task terminates with `no_idempotency_contract` instead of retrying

### Requirement: Workspace disk scans stay bounded and owner-correlated
OpsKeeper SHALL prevent workspace disk scans from following symbolic links outside the workspace root and SHALL report usage by task, incident, workspace, runtime, node, and fleet.

#### Scenario: Workspace contains an external symlink
- **WHEN** a scan encounters a symbolic link whose target exits the workspace root
- **THEN** the scan records only link metadata and does not traverse or count the external target

#### Scenario: An operator requests fleet usage
- **WHEN** an operator queries disk usage grouped by fleet
- **THEN** the response includes fleet totals plus node, runtime, workspace, task, and incident correlations

### Requirement: Credential issuance and terminal lifecycle audits are complete
OpsKeeper SHALL refuse credential issuance unless the grant records task, runtime, plugin release digest, scope hash, issuance time, and expiry. Revocation, denial, and destruction SHALL be recorded as later lifecycle events.

#### Scenario: A grant lacks issuance audit fields
- **WHEN** Manager attempts to issue a grant without a required issuance field
- **THEN** issuance fails with `grant_audit_incomplete` and no usable grant is returned

#### Scenario: A task reaches a terminal or expired state
- **WHEN** a task succeeds, fails, is cancelled, loses its lease, or expires
- **THEN** Manager revokes unresolved grants, records the terminal audit event, and schedules local destruction

### Requirement: Tasks pin immutable plugin execution snapshots
OpsKeeper SHALL persist the plugin release digest and capability digest used at task admission and SHALL NOT resolve a running task to a mutable plugin version.

#### Scenario: A task uses a plugin capability
- **WHEN** Manager admits a task that requires a plugin capability
- **THEN** the task records the exact plugin release and capability digests used for execution

#### Scenario: Rollout advances while the task runs
- **WHEN** a new plugin release becomes desired after task admission
- **THEN** the running task continues against its pinned release snapshot

### Requirement: Runtime events are immutable and retention-classified
OpsKeeper SHALL store runtime events through an append-only write path. Stored events SHALL NOT be updated and SHALL carry an event class and retention class.

#### Scenario: An operator queries the timeline
- **WHEN** an operator filters events by runtime, task, incident, plugin, approval, or trace
- **THEN** Manager returns correlated append-only events with event and retention classifications

#### Scenario: A stored event is updated
- **WHEN** a process attempts to update an existing runtime event row
- **THEN** the write is rejected with `event_immutable`
