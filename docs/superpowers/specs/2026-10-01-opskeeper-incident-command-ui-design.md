---
comet_change: opskeeper-incident-command-ui
role: technical-design
canonical_spec: openspec
---

# OpsKeeper Incident Command UI Technical Design

## Context

OpsKeeper has an authoritative seven-phase loop, durable loop events, repair-preview summaries, sanitized incident archives, and a public demo scenario API. The current TeamHarness plugin and public live page expose parts of this loop, but they organize the experience around features and maintain separate stage vocabularies. The technical design removes that ambiguity with one shared, presentation-only projection and two thin surface adapters.

This design does not change Manager authority, Worker roles, approval semantics, safety gates, execution behavior, or runtime desired state.

## Decision

Use a shared pure projection layer plus thin adapters:

1. `shared/incident-command` normalizes authoritative data into `IncidentCommandView`.
2. TeamHarness adapts Manager loop state/timeline, incident lists, archive, and repair-preview responses.
3. The public live demo adapts scenario status and business snapshots.
4. Visual components consume `IncidentCommandView`; they do not derive authority or infer progress.

Alternative approaches were considered and rejected:

- **Per-surface projection** is fastest for P0 but would let stage, freshness, owner, and next-action semantics diverge again.
- **A backend projection endpoint** centralizes normalization but prematurely turns the UI contract into a service API and expands backend scope. It can be reconsidered only if client aggregation becomes untenable after P2.

## Authoritative Stage Model

The command timeline uses the Manager loop phase identifiers exactly:

1. `detected`
2. `correlated`
3. `investigated`
4. `critiqued`
5. `approved`
6. `recovered`
7. `postmortem`

Worker roles are owner labels, not stage identifiers. The approved phase may expose `awaiting_human` or `approved`; the recovered phase may expose `executing` or `verifying` only when authoritative audit/tool evidence supports that distinction.

### Manager source

TeamHarness adds read-only API wrappers:

```js
getIncidentLoopState(incidentId)      // GET /loops/{incident_id}/state
getIncidentLoopTimeline(incidentId)   // GET /loops/{incident_id}/timeline
```

The existing Manager timeline response already contains:

- chronological loop events;
- per-phase status, duration, start/end time, and contract summary;
- worker role and skill version;
- tool calls, audit rows, knowledge references, and sub-phases;
- chain metadata with current/final phase and closure facts.

The projection uses `phases` for human-readable facts and raw `events` for authoritative source IDs and pause detection. It never uses array position alone as progress.

### Public demo source

The public page continues using `/api/demo/scenario` as its P0 authority. Its scenario states map explicitly to command phases:

| Demo state | Command projection |
|---|---|
| `starting`, `awaiting_alert` | `detected` |
| `alert_correlated` | `correlated` |
| `diagnosis_dispatched` | `investigated` |
| `preview_ready` | `critiqued` completed, `approved` pending |
| `awaiting_approval` | `approved` / `awaiting_human` / blocked |
| `repair_dispatched` | `recovered` / `executing` |
| `verifying` | `recovered` / `verifying` |
| `recovered` | `recovered` completed |
| `closed` | `postmortem` completed |
| `start_failed` | failed projection with unknown phase |

If the demo and a future loop read disagree, each source is displayed with its own observation boundary; the adapter does not silently choose one.

## Projection Layer

Create a repository-level shared module:

```text
shared/incident-command/
├── index.js
├── index.d.ts
├── manager-loop.js
├── demo-scenario.js
└── fixtures/
```

The implementation uses plain JavaScript plus JSDoc so it can run directly in TeamHarness Node tests, Vite, and Next without adding a workspace package manager. TypeScript consumers use the declarations.

### Public responsibilities

The module exports:

- command phase constants and status helpers;
- `fromManagerLoop({ state, timeline, incident, preview, archive, serverNow })`;
- `fromDemoScenario({ scenario, snapshots, serverNow })`;
- freshness calculation;
- next-action priority;
- evidence completeness calculation;
- deterministic fixtures for cross-surface golden tests.

### State rules

For each Manager phase:

- `phase_entered` without a terminal event → `running`;
- `phase_contract_written` → `completed`, with duration and compact outcome;
- `phase_failed` or retry exhaustion → `failed`;
- `phase_paused` → `blocked`; `approved:phase_paused` also sets `awaiting_human`;
- unknown phase/status, malformed payload, or missing required timing → `unknown` or `partial`;
- no events → current phase and stage status remain unknown, never `detected`.

Freshness uses state `updated_at` or response observation time. Event occurrence remains event `created_at`. Expiry decisions use authoritative server time when supplied; otherwise the UI says the boundary cannot be determined.

### Next-action priority

The projection emits at most one primary action:

1. retry a failed read or failed phase;
2. provide precise human approval when `approved` is blocked on a human;
3. inspect evidence for a proposal or verification decision;
4. verify recovery when awaiting verification;
5. archive/close after verified recovery;
6. wait for authoritative work;
7. inspect replay after closure.

Every action carries `kind`, `priority`, `label`, optional detail, and optional disabled reason. Multiple candidate actions are reduced deterministically; visual components cannot invent a higher-priority action.

### Completeness

Evidence completeness is calculated independently:

- incident facts;
- cause;
- repair preview;
- authorization;
- execution;
- verification.

Values are `complete`, `partial`, `missing`, or `legacy_not_applicable`. A legacy incident is not failed merely because preview or newer evidence fields did not exist when it was closed.

## TeamHarness Architecture

### Information architecture

Primary tabs become:

- `incident-command` — 事故指挥
- `evidence-approval` — 证据审批
- `archive-replay` — 复盘档案
- `system-status` — 系统状态

Compatibility aliases map existing IDs:

- `diagnostics` → `incident-command`
- `archive` → `archive-replay`
- `runtime` → `system-status`
- `integration` and `plugins` open `incident-command` and expose secondary diagnostics

### Components

New focused components live under `src/extensions/incident-command/`:

- `IncidentCommandRoute` — incident selection and read orchestration;
- `CommandBar` — incident, impact, stage, owner, freshness, elapsed time, next action;
- `StageTimeline` — seven phases with status, substate, owner, duration, outcome, and blocker;
- `EvidenceDrawer` — keyboard-accessible decision evidence;
- `ApprovalChecklist` — authoritative approval facts and exact instruction;
- `DiagnosticsMenu` — secondary plugin and integration recovery tools.

The current large diagnostic route is decomposed incrementally. P0 may wrap the existing diagnostic component under the new command route while preserving behavior; P1 replaces only the orientation and evidence portions. Existing archive, runtime, integration, and plugin capabilities remain reachable.

### Reads and caching

The command route reads:

1. incident list;
2. selected incident detail;
3. loop state and timeline;
4. repair-preview summary when available;
5. archive evidence lazily for replay or drawer deep inspection.

Reads are normalized independently. Partial failure marks only the affected projection section stale or unavailable; a preview failure does not invalidate authoritative loop phase data.

## Public Live Demo Architecture

The page adds focused components:

- `IncidentCommandBar`
- `IncidentNextAction`
- `CommandStageTimeline`

Business cards and preview decision cards remain supporting evidence. External dashboards and rooms move below the command summary. Existing scenario injection, polling, static-shell resilience, and localized copy remain unchanged.

The page uses the demo adapter in P0. A server-side loop read can be added later only as a read-only composition and must preserve source observation boundaries.

## Evidence and Approval Design

The evidence drawer groups material by decision:

- **Why this incident**: alert snapshot, affected business probes, incident labels.
- **Why this cause**: causal chain, corroborating monitoring evidence, counter-evidence.
- **Why this repair**: baseline and A/B candidate comparison, controlled-load boundary, rejection reasons.
- **Why it is safe**: target/workload fingerprints, expiry, rollback, approval record, execution identity.
- **Why it worked**: post-change metrics and independent verification.

Raw payloads remain behind secondary disclosures.

The approval checklist displays authoritative values only. It can render or copy the exact approval instruction and link to the existing channel, but it does not submit approval and cannot visually imply authority has been granted. Missing incident, candidate, target, expiry, rollback, or verification context is shown as incomplete.

## Archive Replay

Replay freezes projection at closure and separates:

- evidence available at decision time;
- post-incident knowledge and similarity enrichment;
- current bounded comparisons.

Timeline nodes retain source event IDs. Dense archive tables remain secondary. Controlled-drill actions are hidden unless scenario, manifest, target, workload, and safety identities are complete and explicitly supported.

## Visual and Accessibility System

OpsKeeper-scoped semantic tokens are added before broad component rewriting:

- impact/failure;
- waiting/HITL;
- active system work;
- verified recovery;
- unknown/stale;
- surface, border, and focus.

TeamHarness maps these tokens to AgentTeams host variables and preserves host-managed contrast. The public page may retain its dark command-console brand treatment. All status icons include text; timeline nodes and disclosures are keyboard reachable; the evidence drawer traps focus while open and closes with `Escape`; disabled controls expose reasons; narrow screens stack command, impact, timeline, and action in reading order.

## Runtime Boundary

Runtime-management data may be projected only after Manager provides authoritative readback. P3 can show:

- runtime health and capacity;
- claim/lease/checkpoint/recovery state;
- version and capability drift;
- preview fixture availability.

These facts explain blockers in command or system status. Fleet, rollout, disk, credential, and plugin administration remain secondary administrative surfaces and are not part of primary incident navigation.

## Error Handling

- Network or normalization failure → retry/stale state; existing operational paths continue.
- Permission failure → explicit readback unavailable message and recovery entry.
- Inconsistent sources → retain source IDs and observation boundaries; do not silently merge.
- Missing optional archive fields → `legacy_not_applicable`.
- Runtime gaps → unknown, not inferred failure.
- Projection bugs must not create mutating UI actions.

No UI path calls repair, approval, safety, plugin rollout, or runtime desired-state mutation APIs.

## Testing

### Shared projection

Node tests cover:

- all Manager phases and statuses;
- `approved:phase_paused` human blocking;
- recovered executing/verifying evidence;
- malformed and missing events;
- demo-to-command mappings;
- freshness and server-time boundaries;
- deterministic next-action priority;
- six completeness categories and legacy behavior;
- cross-surface golden fixtures.

### TeamHarness

Tests cover:

- tab normalization and compatibility aliases;
- Manager state/timeline API paths;
- command projection and stale/empty states;
- timeline labels and source references;
- evidence grouping;
- approval missing-context warning;
- static render semantics for accessibility attributes.

Run:

```bash
npm test
npm run build
```

### Public site

Run:

```bash
npm run typecheck
npm run build
```

Add focused component or adapter tests if the existing site test setup permits; otherwise the shared adapter tests and typecheck/build are the automated boundary and the public flow receives a smoke check.

### End-to-end smoke

One public-demo flow verifies:

1. scenario injection;
2. command summary visibility;
3. preview readiness;
4. precise-approval copy;
5. recovered/closed command state;
6. contextual external links remain available.

Manual review covers keyboard traversal, focus trapping, narrow-screen layout, contrast, and bilingual copy.

## OPC Squad Packets

| Packet | Primary scope | Handoff |
|---|---|---|
| Contract owner | shared projection, phase matrix, fixtures | golden tests and typed model |
| Command UI owner | TeamHarness IA, command bar, timeline | component tests/build |
| Live demo owner | demo adapter and public command components | site typecheck/build/smoke |
| Evidence/approval owner | drawer, candidate comparison, checklist | safety copy and focused tests |
| Archive owner | replay and legacy projection | replay tests |
| Runtime liaison | read-only blocker boundary | runtime mapping contract |
| Design/a11y owner | tokens, focus, contrast, responsive | accessibility checklist |
| Verification owner | test matrix and final review | validation report |

The contract packet is the dependency gate. Parallel UI packets start only after phase IDs, source mappings, and golden fixtures freeze. No packet may change authoritative APIs or add mutation behavior.

## Implementation Sequence

1. **P-1 contract**: shared model, phase mapping, fixtures, tests, aliases, tokens.
2. **P0 orientation**: TeamHarness IA and command summary; public demo command summary.
3. **P1 decision depth**: timeline, evidence drawer, approval checklist.
4. **P1.5 parity**: both surfaces consume the shared adapter and golden fixtures.
5. **P2 replay**: archive replay, completeness, bounded comparisons.
6. **P3 runtime-aware command**: authoritative blocker explanations only.
7. **P4 knowledge/drill**: gated reuse and controlled drill entry.

This sequence keeps every slice presentation-only and preserves the existing incident loop while moving OpsKeeper's first impression from a generic administrative console to an incident command surface.
