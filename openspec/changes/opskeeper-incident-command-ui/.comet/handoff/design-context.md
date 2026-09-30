# Comet Design Handoff

- Change: opskeeper-incident-command-ui
- Phase: design
- Mode: full
- Context hash: cd7217cd70437111d6d9205a3fc44fb2c4e3e9f5f33d3da044613059291d4339

Generated-by: comet-handoff.sh

OpenSpec remains the canonical capability spec. This handoff is a deterministic, source-traceable context pack, not an agent-authored summary.

## openspec/changes/opskeeper-incident-command-ui/proposal.md

- Source: openspec/changes/opskeeper-incident-command-ui/proposal.md
- Lines: 1-75
- SHA256: 8f6701b1421ce99a74e6098b6d9734d9006eb8fe626037f54eeeaa86d68f98b5

```md
# Proposal: opskeeper-incident-command-ui

## Why

OpsKeeper's differentiator is the auditable incident loop: real business degradation, multi-agent diagnosis, controlled repair preview, precise human approval, narrowly authorized repair, independent verification, and replayable archive evidence. The current public demo and TeamHarness plugin expose these capabilities, but their information architecture remains feature-oriented and uses two separate stage vocabularies. This makes the product feel like a generic operations console and obscures its decision-safety value.

OpsKeeper should orient every incident-facing surface around three questions:

1. What is happening to the business?
2. Which actor or safeguard is currently blocking progress?
3. What evidence justifies the next action?

This change establishes an incident-command presentation model, reorganizes TeamHarness around the incident workflow, upgrades the public live demo, and creates a staged path toward runtime-aware incident command without changing Manager execution authority.

## What Changes

### 1. Establish an authoritative presentation contract

Create a shared, presentation-only `IncidentCommandView` projection used by the TeamHarness plugin and public live demo. The projection will explicitly carry incident identity, scenario, generalized business impact, authoritative current stage, freshness and observation time, owner, blocking reason, prioritized next action, per-stage timeline facts, decision-category evidence completeness, and approval, rollback, and verification safeguards.

The contract will distinguish `fresh`, `stale`, and `unknown` observations. Missing authoritative data will never be projected as running progress.

### 2. Define an explicit stage mapping matrix

Map Manager loop events, RCA/task phases, live-demo scenario states, and the seven authoritative OpsKeeper command stages without inferring progress. The stage IDs will match Manager loop phases (`detected`, `correlated`, `investigated`, `critiqued`, `approved`, `recovered`, `postmortem`); worker roles remain owner labels rather than stage IDs. The matrix will identify the source event/task for every displayed stage, duration, owner, outcome, and blocking reason.

The repair stage will support `awaiting_human`, `approved`, and `executing` sub-states so a human approval block is not mislabeled as Worker execution.

### 3. Reorganize TeamHarness around incident command

Change the primary plugin navigation to `事故指挥`, `证据审批`, `复盘档案`, and `系统状态`. Plugin management and integration self-check will move to secondary diagnostics. Existing tab IDs will receive compatibility aliases so old deep links remain useful.

### 4. Add command orientation to the live demo

After scenario injection, the public live page will show a persistent command summary containing incident identity, business impact, current stage, owner, elapsed time, expiry boundary, and the single next action. External dashboards and rooms will remain contextual recovery links rather than the primary orientation.

### 5. Build decision-depth surfaces

Implement a seven-stage command timeline, grouped evidence drawer, A/B preview comparison, rollback plan, and precise-approval checklist. Evidence will be grouped by the decision it supports rather than by source system. The approval checklist will remain presentation-only and will expose the exact approval channel or command; it will never imply that reviewing the checklist grants authority.

### 6. Make archive replay the learning surface

Archive will lead with replay and closure summary before dense tables. Replay will distinguish evidence visible at decision time from post-incident enrichment. Legacy incidents without newer optional fields will receive explicit compatibility states rather than false failure states.

### 7. Establish a scoped visual and accessibility system

Define OpsKeeper-scoped semantic tokens for status, impact, surfaces, focus, spacing, and data density before the main component rewrite. The public demo may use a command-console brand treatment; the TeamHarness plugin will map scoped tokens to the AgentTeams host theme. Status icons, keyboard operation, focus traps, narrow-screen layouts, and stale/empty/error states are first-class requirements.

### 8. Stage runtime-aware evolution

Runtime inventory, task claims, leases, checkpoints, worker recovery, and plugin drift will initially remain outside primary incident navigation. Once the runtime-management change provides authoritative readback, those facts will explain incident blockers and system status. OpsKeeper will not become a general node, plugin, disk, or workflow administration console in this change.

### 9. Establish the OPC squad delivery track

Create role-scoped execution packets and review gates for the OPC squad so contract, UI, safety, archive, runtime, and validation work can proceed in bounded increments. Each packet will define owner, scope, authoritative data source, tests, and handoff artifact to avoid parallel UI work creating a second execution authority.

## Impact

- `site/app/live-incident/page.tsx`
- `site/lib/demo-types.ts`
- `site/components/demo/*`
- `plugins/opskeeper-teamharness/dashboard/src/extensions/tabs.js`
- `plugins/opskeeper-teamharness/dashboard/src/extensions/unified-route.jsx`
- diagnostic, archive, runtime, and integration extension components
- shared projection/token helpers and focused tests
- OPC squad execution and review documentation

## Non-Goals

- No change to Manager authority, Worker roles, approval semantics, safety gates, or repair execution.
- No new authoritative workflow-state database contract.
- No replacement for AgentTeams Dashboard navigation or host theming.
- No generic monitoring, topology, device, service-desk, or broad IT administration surface.
- No uncontrolled drill creation from archived incidents.
- No rebranding or affiliation with another product.
```

## openspec/changes/opskeeper-incident-command-ui/design.md

- Source: openspec/changes/opskeeper-incident-command-ui/design.md
- Lines: 1-196
- SHA256: a950b1bae44177b83dccd9ab07b086c2fde027cef91598bc9b30170589d80dfd

```md
# Design: opskeeper-incident-command-ui

## Positioning

OpsKeeper is an incident command and evidence-audit surface for AI-assisted operations. Its primary object is an incident moving through a governed loop from impact to verified recovery and reusable knowledge, not a dashboard, node, plugin, or workflow definition.

The interface has four persistent levels:

1. **Command** — what is affected, where the incident is, who owns the current step, and what happens next.
2. **Evidence** — the minimum authoritative material needed to trust the current state or decision.
3. **Authorization** — precise human approval, target identity, rollback, execution identity, and safeguards.
4. **Archive** — replayable history, decision-time evidence, similarities, and reusable operational knowledge.

Generic runtime administration remains contextual. It explains blockers after authoritative readback exists; it does not compete with the incident loop as the first impression.

## Presentation Contract

All incident-facing surfaces consume the same normalized projection. This is a UI contract, not an execution authority.

```ts
type StageId =
  | 'detected'
  | 'correlated'
  | 'investigated'
  | 'critiqued'
  | 'approved'
  | 'recovered'
  | 'postmortem';

type StageStatus =
  | 'pending'
  | 'running'
  | 'blocked'
  | 'completed'
  | 'failed'
  | 'unknown';

type ObservationFreshness = 'fresh' | 'stale' | 'unknown';

type Completeness = 'complete' | 'partial' | 'missing' | 'legacy_not_applicable';

type IncidentCommandView = {
  incidentId: string;
  scenario?: string;
  stage?: StageId;
  stageStatus: StageStatus;
  stageSubstate?: 'awaiting_human' | 'approved' | 'executing' | 'verifying';
  freshness: ObservationFreshness;
  observedAt?: string;
  serverNow?: string;
  sourceEventId?: string;
  sourceTaskId?: string;
  owner?: {
    kind: 'manager' | 'worker' | 'human' | 'verifier' | 'system';
    role?: string;
    label: string;
  };
  businessImpact: {
    level: 'unknown' | 'normal' | 'degraded' | 'severe';
    affectedScopes: Array<{ name: string; state: 'healthy' | 'degraded' | 'unknown' }>;
    indicators: Array<{ name: string; value?: string; state: 'healthy' | 'degraded' | 'unknown' }>;
  };
  nextAction?: {
    kind: 'wait' | 'inspect-evidence' | 'approve' | 'reject' | 'verify' | 'archive' | 'retry';
    priority: number;
    label: string;
    detail?: string;
    disabledReason?: string;
  };
  stageTimeline: Array<{
    stage: StageId;
    status: StageStatus;
    ownerLabel?: string;
    startedAt?: string;
    durationMs?: number;
    outcome?: string;
    blockingReason?: string;
    evidenceRefs: string[];
    sourceEventId?: string;
    sourceTaskId?: string;
  }>;
  evidenceCompleteness: {
    incident: Completeness;
    cause: Completeness;
    preview: Completeness;
    authorization: Completeness;
    execution: Completeness;
    verification: Completeness;
  };
};
```

Projection rules:

- A stage without an authoritative source is `unknown`, not `running`.
- An observation past its allowed age is `stale`.
- Expiry uses authoritative server time rather than client clock skew.
- UI animation may indicate refresh activity, never inferred incident progress.
- Every displayed stage fact retains a source event/task reference when available.

## Stage Mapping

The change will maintain one mapping matrix before timeline implementation:

| Command stage | Manager loop event | Loop/task phase | Public demo state |
|---|---|---|---|
| detected | `phase_entered:detected` and related detection events | alerter detection | `starting`, `awaiting_alert` |
| correlated | `phase_entered:correlated` / `phase_contract_written:correlated` | investigator correlation | `alert_correlated` |
| investigated | `phase_entered:investigated` / `phase_contract_written:investigated` | investigator diagnosis | `diagnosis_dispatched` |
| critiqued | `phase_entered:critiqued` / `phase_contract_written:critiqued` | critic review | diagnosis-to-preview transition |
| approved | `phase_entered:approved`, `phase_paused:approved`, and approval audit | reviewer proposal and HITL approval | `preview_ready`, `awaiting_approval` |
| recovered | `phase_entered:recovered` / `phase_contract_written:recovered`, execution and verification audit | repairer execution plus independent verifier | `repair_dispatched`, `verifying`, `recovered` |
| postmortem | `phase_entered:postmortem` / `phase_contract_written:postmortem` | postmortem reporter | `closed` |

The command stage IDs are the authoritative Manager loop phase IDs. Role names such as alerter, investigator, critic, reviewer, repairer, verifier, and postmortem reporter are owner labels, not stage IDs. The matrix is normative for implementation, but each source column must list exact event/task names during contract discovery. If a mapping cannot be proven, the corresponding UI state stays unknown or partial.

## TeamHarness Information Architecture

Primary tabs are `事故指挥`, `证据审批`, `复盘档案`, and `系统状态`. Secondary diagnostics retain plugin installation and integration self-check.

Compatibility aliases map:

- `diagnostics` → `incident-command`;
- `integration` → `incident-command` with diagnostics drawer;
- `archive` → `archive-replay`;
- `runtime` → `system-status`;
- `plugins` → `incident-command` with diagnostics drawer.

## Command and Evidence Layout

The first screen follows this reading order: incident identity and severity, business impact and duration, authoritative current stage and owner, one prioritized next action, compact seven-stage timeline, then contextual external links.

The evidence drawer groups material by decision:

- **Why this incident** — alert snapshot and affected probes.
- **Why this cause** — causal chain and corroborating signals.
- **Why this repair** — A/B preview, workload boundary, and rejected candidates.
- **Why it is safe** — target fingerprint, expiry, rollback, approval, and execution identity.
- **Why it worked** — post-change metrics and independent verification.

Raw payloads are secondary disclosures. The first evidence layer remains decision-oriented.

## Precise Approval

The approval checklist displays incident and candidate identity, execution identity, target and workload fingerprints, impact scope and parameters, expiry and server time, rollback plan, preview eligibility, verification criteria, and authoritative approval status.

The checklist can prefill or copy the exact approval instruction and can link to the existing approval channel. It cannot execute, bypass, or visually grant Manager authority. A bare affirmative remains insufficient and is shown as missing precise context.

## Archive Replay

Archive replay uses the same stage projection fixed at closure. It separates evidence available at decision time from post-incident enrichment, current similarity comparisons, and optional future drill actions.

Legacy incidents retain their original decision context. Missing optional newer fields render as `legacy_not_applicable`, not failure.

## Visual System

Semantic tokens precede component rewriting:

- impact/failure — red family;
- waiting/HITL — amber family;
- active system/agent work — cyan family;
- verified recovery — green family;
- evidence/history — neutral surfaces;
- unknown/stale — explicit neutral-plus-border treatment.

Tokens are OpsKeeper-scoped. The public page may use a dark command-console brand theme. TeamHarness maps tokens to host variables and preserves host-managed contrast and density.

Accessibility and responsiveness requirements include accessible status text, keyboard-focusable timeline nodes, focus-managed evidence dismissal, explicit disabled reasons, visible loading/stale/empty/retry/failure states, and narrow-screen stacking in reading order.

## Runtime Evolution Boundary

The runtime-management change remains a separate authority track. Incident Command UI consumes only authoritative Manager readback for runtime health and capacity, task claim/lease/checkpoint/recovery state, plugin version/capability drift, and workspace or preview fixture availability. These facts appear as incident blockers or system-status evidence. Detailed fleet, rollout, disk, credential, and plugin administration remain secondary administrative surfaces outside this change.

## OPC Squad Execution Model

The OPC squad uses bounded role packets: contract owner, command UI owner, live demo owner, evidence/approval owner, archive owner, runtime liaison, design/accessibility owner, and verification owner. Every packet states its authoritative data source, forbidden mutations, changed files, tests, and handoff artifact. Cross-packet changes require an explicit review gate.

## Rollout

1. **P-1 — Contract and design foundation:** inventory sources, define the mapping matrix, projection helpers, unknown/stale behavior, tokens, and compatibility aliases.
2. **P0 — Incident-first orientation:** rename tabs, move diagnostics secondary, add command bar and next action, and preserve existing capabilities.
3. **P1 — Decision depth:** timeline, evidence drawer, candidate comparison, rollback, precise approval, and accessibility/error-state tests.
4. **P1.5 — Shared projection:** extract shared helpers and add cross-surface golden tests.
5. **P2 — Replay and reuse:** archive replay, decision-time evidence, completeness, historical comparison, and gated drills.
6. **P3 — Runtime-aware command:** project authoritative runtime/task blockers while keeping runtime administration secondary.
7. **P4 — Knowledge and drill loop:** connect similarities to reusable knowledge and gate drill creation.

## Risks / Trade-offs

- **UI invents progress** → mapping matrix and source references are prerequisites.
- **Two surfaces diverge again** → shared projection helpers and golden tests precede visual polish.
- **Approval appears executable** → checklist is presentation-only and names the authoritative approval channel.
- **Evidence overwhelms operators** → first layer summarizes decisions; raw payloads remain secondary.
- **Plugin fights host theme** → scoped tokens map to host variables.
- **Runtime scope expands into generic administration** → runtime facts enter as incident explanations only.
- **Live demo regresses** → P0 remains presentation-only and preserves routes, recovery links, and existing APIs.
```

## openspec/changes/opskeeper-incident-command-ui/tasks.md

- Source: openspec/changes/opskeeper-incident-command-ui/tasks.md
- Lines: 1-71
- SHA256: 90205f625d5f37056333a76e15691c50acc0e31ffc15eff5f99613110b589070

```md
# Tasks

## 1. Discovery, Contract, and Design Foundation

- [ ] 1.1 Inventory live-demo, Manager incident, RCA task, approval, repair-preview, verification, archive, runtime, and plugin data dependencies.
- [ ] 1.2 Build the normative stage-mapping matrix using Manager loop phase IDs (`detected`, `correlated`, `investigated`, `critiqued`, `approved`, `recovered`, `postmortem`) and exact event/task names plus source identifiers.
- [ ] 1.3 Define `IncidentCommandView`, stage substates, generalized impact, freshness, completeness, source references, and next-action priority rules.
- [ ] 1.4 Define projection behavior for missing, partial, stale, malformed, and legacy data.
- [ ] 1.5 Audit current labels, tab IDs, status colors, emoji, inline styles, keyboard behavior, focus behavior, and responsive breakpoints.
- [ ] 1.6 Define scoped semantic tokens, status iconography, focus states, contrast requirements, and data-density rules.
- [ ] 1.7 Add compatibility aliases for all existing TeamHarness tab IDs and deep-link states.
- [ ] 1.8 Confirm UI copy uses the approved generic operations-platform wording and does not reference another product.

## 2. P0 Incident-First Orientation

- [ ] 2.1 Rename and reorder primary TeamHarness tabs to incident command, evidence approval, archive replay, and system status.
- [ ] 2.2 Move plugin management and integration self-check into secondary diagnostics without removing capability.
- [ ] 2.3 Implement the Incident Command Bar with incident identity, impact, stage, owner, elapsed time, freshness, and expiry boundary.
- [ ] 2.4 Implement the prioritized next-action card for wait, inspect, approve, reject, verify, archive, and retry states.
- [ ] 2.5 Add the same command summary to the public live page after successful injection.
- [ ] 2.6 Preserve contextual external links and make them secondary to the incident summary.
- [ ] 2.7 Add focused tests for tab aliases, projection, command summary, next-action copy, empty/stale state, and legacy input.

## 3. P1 Command Timeline and Decision Evidence

- [ ] 3.1 Implement the seven-stage timeline with status, substate, owner, duration, outcome, blocking reason, and source reference.
- [ ] 3.2 Implement the grouped evidence drawer with focus trapping, `Escape` dismissal, and keyboard-operable disclosures.
- [ ] 3.3 Project A/B candidate comparison, controlled workload boundary, target/workload identity, and preview eligibility.
- [ ] 3.4 Project rollback plan, verification criteria, approval record, execution identity, and audit trail.
- [ ] 3.5 Implement the precise-approval checklist, exact approval instruction/channel, and missing-context warning.
- [ ] 3.6 Ensure the UI cannot execute repair, bypass approval, imply granted authority, or mutate safety state.
- [ ] 3.7 Add keyboard, focus, responsive, loading, retry, stale, empty, and failure-state tests.

## 4. P1.5 Shared Projection

- [ ] 4.1 Extract shared projection, stage mapping, freshness, completeness, and formatting helpers.
- [ ] 4.2 Adapt TeamHarness and the public live demo to shared helpers while preserving host/demo visual differences.
- [ ] 4.3 Add cross-surface golden tests for stage, owner, freshness, next action, impact, and completeness.

## 5. P2 Archive Replay and Knowledge

- [ ] 5.1 Design and implement replay from authoritative archived transitions.
- [ ] 5.2 Group replay evidence by decision and retain links to source events.
- [ ] 5.3 Distinguish decision-time evidence from post-incident enrichment and current knowledge references.
- [ ] 5.4 Add evidence completeness and bounded historical-comparison summaries.
- [ ] 5.5 Preserve explicit `legacy_not_applicable` states for optional newer fields.
- [ ] 5.6 Add a visually secondary controlled-drill action only for scenarios with complete scenario, manifest, target, workload, and safety identity support.

## 6. P3 Runtime-Aware Command

- [ ] 6.1 Define read-only projections from authoritative runtime inventory, task, health, and plugin-readback data.
- [ ] 6.2 Show runtime and task blockers as incident/system-status explanations with claim, lease, checkpoint, recovery, and drift context.
- [ ] 6.3 Keep detailed node, rollout, disk, credential, and plugin administration outside primary incident navigation.
- [ ] 6.4 Add tests proving runtime data gaps produce unknown/stale states rather than inferred failures.

## 7. OPC Squad Delivery and Governance

- [ ] 7.1 Create role-scoped OPC execution packets for contract, command UI, live demo, evidence/approval, archive, runtime liaison, design/accessibility, and verification.
- [ ] 7.2 Define each packet's scope, owner, authoritative sources, changed files, tests, forbidden mutations, and handoff artifact.
- [ ] 7.3 Establish review gates for projection contract, safety copy, accessibility, runtime boundary, and archive replay.
- [ ] 7.4 Maintain a dependency board separating P-1/P0/P1/P2/P3/P4 work and preventing duplicate parallel edits.
- [ ] 7.5 Record decisions, open questions, and cross-change dependencies in the change handoff notes.

## 8. Validation and Completion

- [ ] 8.1 Validate strict OpenSpec change artifacts.
- [ ] 8.2 Run focused TeamHarness tests/build.
- [ ] 8.3 Run public live-demo tests/build and one end-to-end smoke flow.
- [ ] 8.4 Validate Chinese/English copy, color contrast, tabular readability, keyboard paths, and narrow-screen behavior.
- [ ] 8.5 Review final surfaces against incident-command positioning and remove residual generic-administrative framing.
- [ ] 8.6 Verify no UI path mutates approval, safety, repair, runtime, or authoritative incident state.
```

## openspec/changes/opskeeper-incident-command-ui/specs/incident-command-ui/spec.md

- Source: openspec/changes/opskeeper-incident-command-ui/specs/incident-command-ui/spec.md
- Lines: 1-124
- SHA256: cd87564e6b3b44469765b1c2cb57a3579ce2272d3d02093926fa9d00c472a8c2

```md
## ADDED Requirements

### Requirement: Present incidents through a command-first architecture
OpsKeeper incident-facing surfaces SHALL orient operators around current incident identity, business impact, authoritative stage, owner, blocking reason, freshness, elapsed time, and prioritized next action before presenting administrative capabilities.

#### Scenario: Operator opens an active incident
- **WHEN** an active incident is selected
- **THEN** the first screen identifies the incident, impact, stage, owner, duration, freshness, blocker, and one prioritized next action
- **AND** plugin administration and integration self-check remain available from secondary diagnostics

#### Scenario: Authoritative stage data is absent
- **WHEN** stage data is missing, malformed, expired, or cannot be mapped
- **THEN** the UI displays an unknown or stale state
- **AND** it does not infer running or completed progress

### Requirement: Normalize an authoritative presentation projection
OpsKeeper SHALL provide a presentation-only incident-command projection that normalizes stage, substate, owner, generalized impact, freshness, observation time, source reference, stage timeline, evidence completeness, and next action for incident-facing surfaces.

#### Scenario: Projection receives authoritative transition data
- **WHEN** an authoritative event or task maps to a command stage
- **THEN** the projection preserves stage, status, owner, timing, outcome, and source reference
- **AND** it does not create execution authority

#### Scenario: Public demo and plugin receive equivalent source data
- **WHEN** both surfaces normalize equivalent authoritative inputs
- **THEN** they produce equivalent stage, owner, freshness, next action, impact, and completeness projections
- **AND** visual chrome may remain surface-specific

### Requirement: Map stages only from authoritative sources
OpsKeeper SHALL maintain an explicit mapping among command stages, Manager loop events, RCA/task phases, and public-demo states. Command stage identifiers SHALL use the authoritative Manager loop phases `detected`, `correlated`, `investigated`, `critiqued`, `approved`, `recovered`, and `postmortem`; worker roles SHALL be owner labels rather than stage identifiers. Unmapped or unverifiable states SHALL remain unknown or partial.

#### Scenario: Repair waits for a human decision
- **WHEN** repair has not started and approval is required
- **THEN** the `approved` stage identifies the `awaiting_human` substate and human owner
- **AND** it does not label the Worker as executing repair

#### Scenario: A source event name changes
- **WHEN** an authoritative event or task kind no longer matches the normative mapping matrix
- **THEN** the affected stage becomes unknown or stale
- **AND** a focused mapping test fails

### Requirement: Render a decision-oriented command timeline
Incident-facing surfaces SHALL render the seven OpsKeeper stages with status, substate, owner, duration, compact outcome, blocking reason, evidence link, and source reference where available.

#### Scenario: Operator waits for precise approval
- **WHEN** the incident reaches the human approval transition
- **THEN** the approval state is visually dominant
- **AND** the interface names the waiting owner and exact decision required
- **AND** completed stages remain inspectable without exposing every raw payload initially

#### Scenario: A stage transition completes
- **WHEN** an authoritative transition is recorded
- **THEN** the timeline shows compact outcome and duration
- **AND** future stages remain visually distinct without implying speculative progress

### Requirement: Keep decision evidence one action away
OpsKeeper SHALL provide an evidence view that groups incident facts, causal chain, affected scope, monitoring evidence, candidate comparison, rollback plan, approval record, execution identity, and verification result by supported decision.

#### Scenario: Operator inspects a repair proposal
- **WHEN** the evidence view opens from the repair or approval state
- **THEN** it presents candidate comparison, controlled workload boundary, target/workload identity, rollback plan, and verification criteria
- **AND** raw payloads remain secondary disclosures

#### Scenario: Legacy archive evidence is incomplete
- **WHEN** optional newer evidence fields are absent on a legacy incident
- **THEN** the UI shows an explicit legacy-not-applicable or partial state
- **AND** it does not mark the incident failed solely for that absence

### Requirement: Present precise approval without granting authority
The approval surface SHALL expose candidate identity, execution identity, target fingerprint, workload fingerprint, impact scope, parameters, expiry, authoritative server time, rollback plan, preview eligibility, verification criteria, and authoritative approval status before an approval action appears visually actionable.

#### Scenario: Operator supplies an ambiguous affirmative
- **WHEN** approval input lacks incident or candidate context
- **THEN** the UI identifies the missing precise instruction
- **AND** it does not imply that Manager authority has been granted or bypassed

#### Scenario: All authoritative approval facts are present
- **WHEN** the checklist is complete
- **THEN** the operator can inspect each fact and reach the existing precise approval channel or command
- **AND** Manager remains the sole validator and recorder of the authoritative decision

### Requirement: Replay closed incidents without rewriting history
Archive SHALL support selecting a closed incident and replaying its timeline, decision-time evidence, selected and rejected candidates, rollback outcome, verification result, completeness, and bounded historical similarities without changing authoritative incident history.

#### Scenario: Reviewer opens a closed incident
- **WHEN** a closed incident has complete evidence
- **THEN** archive presents replay and decision summary before dense tables
- **AND** individual transitions link to their source evidence

#### Scenario: Archive contains post-incident enrichment
- **WHEN** knowledge or similarity data was created after closure
- **THEN** replay identifies it as post-incident enrichment
- **AND** it is not presented as evidence that was available at decision time

#### Scenario: Reviewer compares similar incidents
- **WHEN** bounded similarity data is available
- **THEN** archive exposes comparison provenance and limits
- **AND** it does not create a drill unless all controlled-drill identities and safeguards are supported

### Requirement: Preserve accessibility, responsiveness, and host integration
Incident Command UI SHALL keep status, timeline, evidence, disclosure, and approval controls accessible and responsive. TeamHarness SHALL use OpsKeeper-scoped semantic tokens that remain compatible with AgentTeams host theme variables.

#### Scenario: Keyboard-only operator reviews an incident
- **WHEN** the surface is used without a pointer device
- **THEN** timeline nodes, evidence disclosures, and checklist controls are reachable and operable
- **AND** the evidence drawer supports focus management and `Escape` dismissal

#### Scenario: Plugin runs under a light host theme
- **WHEN** TeamHarness renders inside AgentTeams
- **THEN** OpsKeeper-scoped tokens map to host-compatible contrast and foreground values
- **AND** the plugin does not impose a global OpsKeeper page theme

### Requirement: Preserve operational and runtime authority
Incident Command UI SHALL be presentation-only and MUST NOT execute repairs, bypass approval, mutate safety state, change runtime desired state, or replace AgentTeams Dashboard navigation.

#### Scenario: Projection fails
- **WHEN** normalization or projection read fails
- **THEN** the surface shows retry or stale-data state
- **AND** existing Manager, Worker, safety, approval, repair, verification, and runtime paths continue unchanged

#### Scenario: Runtime information explains an incident blocker
- **WHEN** authoritative runtime/task readback is available
- **THEN** command or system status can show health, claim, lease, checkpoint, recovery, or version-drift context
- **AND** detailed runtime administration remains outside primary incident navigation
```

