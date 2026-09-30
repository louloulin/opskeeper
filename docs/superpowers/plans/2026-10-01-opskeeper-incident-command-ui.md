---
change: opskeeper-incident-command-ui
design-doc: docs/superpowers/specs/2026-10-01-opskeeper-incident-command-ui-design.md
base-ref: db9d5b56031d8ac8c413fede85c55b8ed0226b4c
---

# OpsKeeper Incident Command UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a shared, authoritative incident-command projection and use it to orient TeamHarness and the public live demo around evidence, approval, recovery, and replay.

**Architecture:** A repository-level pure projection module normalizes Manager loop and demo scenario inputs. TeamHarness and the public site own thin adapters and presentation components; no UI path mutates execution, approval, safety, or runtime state.

**Tech Stack:** JavaScript ES modules with JSDoc and TypeScript declarations, React 18/19, Next.js 14, Vite 6 library mode, Node `node:test`, OpenSpec.

**Spec:** `openspec/changes/opskeeper-incident-command-ui/specs/incident-command-ui/spec.md` and `docs/superpowers/specs/2026-10-01-opskeeper-incident-command-ui-design.md`.

## Global Constraints

- Command phase IDs are exactly `detected`, `correlated`, `investigated`, `critiqued`, `approved`, `recovered`, `postmortem`.
- Worker roles are owner labels, never phase IDs.
- Missing, malformed, unauthorized, or stale authoritative data renders `unknown` or `stale`; it must never render inferred running progress.
- `approved:phase_paused` projects to `approved`, `stageSubstate: awaiting_human`, `stageStatus: blocked`, and a human owner.
- The UI is presentation-only and must not call repair, approval, safety, plugin rollout, or runtime desired-state mutation APIs.
- TeamHarness uses OpsKeeper-scoped tokens compatible with AgentTeams host theme variables.
- Existing plugin, integration, runtime, archive, and external recovery capabilities remain reachable.
- Approval requires exact incident/candidate context; a bare affirmative is insufficient.
- Legacy missing optional evidence is `legacy_not_applicable`, not failure.
- Chinese-facing product copy uses generic operations-platform wording and does not reference another product.

---

### Task 1: Freeze the shared projection contract

**Files:**

- Create: `shared/incident-command/index.js`
- Create: `shared/incident-command/index.d.ts`
- Create: `shared/incident-command/manager-loop.js`
- Create: `shared/incident-command/demo-scenario.js`
- Create: `shared/incident-command/fixtures.js`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command-contract.test.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/package.json`
- Modify: `plugins/opskeeper-teamharness/dashboard/vite.config.mjs`
- Modify: `site/tsconfig.json`
- Modify: `openspec/changes/opskeeper-incident-command-ui/tasks.md`

**Interfaces:**

- Produces `COMMAND_PHASES`, `COMMAND_PHASE_LABELS`, `fromManagerLoop(input)`, `fromDemoScenario(input)`, `resolveFreshness(observedAt, serverNow)`, `selectNextAction(view)`, and fixture objects used by both surfaces.
- `fromManagerLoop({ state, timeline, incident, preview, archive, serverNow })` returns `IncidentCommandView`.
- `fromDemoScenario({ scenario, snapshots, serverNow })` returns the same public shape.

- [ ] **Step 1: Add failing contract tests**

Create tests for:

```js
test('uses Manager loop phase identifiers as command stages', () => {
  assert.deepEqual(COMMAND_PHASES, [
    'detected', 'correlated', 'investigated', 'critiqued',
    'approved', 'recovered', 'postmortem',
  ]);
});

test('projects approved pause as human blocking', () => {
  const view = fromManagerLoop({ state, timeline: approvedPauseTimeline });
  assert.equal(view.stage, 'approved');
  assert.equal(view.stageStatus, 'blocked');
  assert.equal(view.stageSubstate, 'awaiting_human');
  assert.equal(view.owner.kind, 'human');
});

test('keeps missing loop data unknown instead of detected', () => {
  const view = fromManagerLoop({ state: null, timeline: null });
  assert.equal(view.stage, undefined);
  assert.equal(view.stageStatus, 'unknown');
  assert.equal(view.freshness, 'unknown');
});
```

Also test all Manager phase mappings, `phase_failed`, retry exhaustion, malformed phase/status, recovered execution/verification evidence, demo state mapping, deterministic next-action priority, six completeness categories, and legacy archive fields.

- [ ] **Step 2: Verify the tests fail**

Run from `plugins/opskeeper-teamharness/dashboard`:

```bash
npm test -- src/extensions/incident-command-contract.test.js
```

Expected: the new test file fails because `shared/incident-command` does not exist.

- [ ] **Step 3: Implement the pure projection**

Implement exact defensive normalization:

```js
export const COMMAND_PHASES = Object.freeze([
  'detected', 'correlated', 'investigated', 'critiqued',
  'approved', 'recovered', 'postmortem',
]);

export function fromManagerLoop(input = {}) {
  const state = normalizeLoopState(input.state);
  const timeline = normalizeLoopTimeline(input.timeline);
  const stages = projectManagerStages(state, timeline);
  const command = selectCurrentStage(state, timeline, stages);
  return {
    incidentId: normalizeIncidentId(input),
    scenario: normalizeScenario(input.incident),
    ...command,
    businessImpact: projectIncidentImpact(input.incident),
    nextAction: selectNextAction({ ...command, preview: input.preview }),
    stageTimeline: stages,
    evidenceCompleteness: projectCompleteness(input),
  };
}
```

Use `timeline.phases` for human facts, raw `timeline.events` for IDs and pause detection, and `state.updated_at` for freshness. Do not use array index as authority.

- [ ] **Step 4: Add cross-build resolution**

- Add the test file to the TeamHarness `test` script.
- Add a Vite alias `@opskeeper/incident-command` to `../../shared/incident-command/index.js`.
- Add a TypeScript path alias `@opskeeper/incident-command` to `../shared/incident-command/index.d.ts`.

- [ ] **Step 5: Run focused tests and typecheck**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test
cd ../../../site && npm run typecheck
```

- [ ] **Step 6: Update OpenSpec task status and commit**

Mark completed discovery/contract tasks in `openspec/changes/opskeeper-incident-command-ui/tasks.md`, then commit only files belonging to this change:

```bash
git add shared/incident-command plugins/opskeeper-teamharness/dashboard/package.json plugins/opskeeper-teamharness/dashboard/package-lock.json plugins/opskeeper-teamharness/dashboard/vite.config.mjs site/tsconfig.json openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: add incident command projection contract"
```

### Task 2: Establish tab compatibility and scoped design tokens

**Files:**

- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/tabs.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/plugin-theme.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/unified-route.jsx`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/runtime.test.js`

**Interfaces:**

- Produces primary tab IDs `incident-command`, `evidence-approval`, `archive-replay`, `system-status`.
- Produces legacy aliases `diagnostics`, `integration`, `plugins`, `archive`, `runtime`.
- Produces `opskeeperCommandThemeStyle` with scoped status/surface/focus CSS variables.

- [ ] **Step 1: Add failing normalization and token tests**

```js
test('normalizes legacy incident command tab aliases', () => {
  assert.equal(normalizeOpskeeperTab('diagnostics'), 'incident-command');
  assert.equal(normalizeOpskeeperTab('integration'), 'incident-command');
  assert.equal(normalizeOpskeeperTab('plugins'), 'incident-command');
  assert.equal(normalizeOpskeeperTab('archive'), 'archive-replay');
  assert.equal(normalizeOpskeeperTab('runtime'), 'system-status');
  assert.equal(normalizeOpskeeperTab('unknown'), 'incident-command');
});

test('defines scoped incident command semantic tokens', () => {
  assert.match(opskeeperCommandThemeStyle['--ops-status-active'], /var\(--/);
  assert.match(opskeeperCommandThemeStyle['--ops-status-waiting'], /var\(--/);
  assert.match(opskeeperCommandThemeStyle['--ops-status-unknown'], /var\(--/);
});
```

- [ ] **Step 2: Implement aliases, primary tabs, and tokens**

Keep existing component rendering reachable behind a diagnostics state while Task 3 adds the new route. Apply `opskeeperCommandThemeStyle` only to the OpsKeeper extension root.

- [ ] **Step 3: Run focused tests**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test -- src/extensions/runtime.test.js
```

- [ ] **Step 4: Commit**

```bash
git add plugins/opskeeper-teamharness/dashboard/src/extensions/tabs.js plugins/opskeeper-teamharness/dashboard/src/extensions/plugin-theme.js plugins/opskeeper-teamharness/dashboard/src/extensions/unified-route.jsx plugins/opskeeper-teamharness/dashboard/src/extensions/runtime.test.js
git commit -m "feat: orient TeamHarness tabs around incident command"
```

### Task 3: Add TeamHarness command readback and P0 command surface

**Files:**

- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/api.js`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/IncidentCommandRoute.jsx`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/CommandBar.jsx`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/NextActionCard.jsx`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/DiagnosticsMenu.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/unified-route.jsx`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command.test.js`

**Interfaces:**

- `opskeeperApi.getIncidentLoopState(incidentId)` calls `GET /loops/{id}/state`.
- `opskeeperApi.getIncidentLoopTimeline(incidentId)` calls `GET /loops/{id}/timeline`.
- `IncidentCommandRoute` accepts `{ api, initialIncidentId, onOpenDiagnostics }`.
- `CommandBar` accepts `{ view, locale, onOpenEvidence }`.
- `NextActionCard` accepts `{ action, locale }`.

- [ ] **Step 1: Add failing API and component tests**

Test exact XHR paths, selected-incident polling, partial-error independence, stale/unknown render text, next-action priority, and diagnostics reachability.

- [ ] **Step 2: Implement read APIs**

Use the existing `jsonFetch` helper and encode incident IDs. Both methods are GET-only.

- [ ] **Step 3: Implement the P0 route**

Read incident list and selected incident state/timeline. Normalize through `fromManagerLoop`. Keep the existing diagnostic report below the command summary until Task 5 replaces evidence sections.

- [ ] **Step 4: Implement accessible compact components**

Use semantic `<section>`, `<time>`, status text, explicit disabled reasons, tabular numbers, and scoped tokens. Do not use emoji as the only status signal.

- [ ] **Step 5: Run plugin tests and build**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test && npm run build
```

- [ ] **Step 6: Mark P0 TeamHarness tasks complete and commit**

```bash
git add plugins/opskeeper-teamharness/dashboard/src/extensions openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: add TeamHarness incident command surface"
```

### Task 4: Add public live-demo command orientation

**Files:**

- Create: `site/components/demo/incident-command-bar.tsx`
- Create: `site/components/demo/incident-next-action.tsx`
- Create: `site/components/demo/command-stage-timeline.tsx`
- Modify: `site/app/live-incident/page.tsx`
- Modify: `site/lib/demo-types.ts`
- Modify: `site/tsconfig.json`

**Interfaces:**

- Consumes `fromDemoScenario({ scenario, snapshots, serverNow })`.
- `IncidentCommandBar` accepts `{ view, locale }`.
- `IncidentNextAction` accepts `{ action, locale }`.
- `CommandStageTimeline` accepts `{ stages, locale }`.

- [ ] **Step 1: Add adapter-focused tests if site test tooling supports them**

If no site test runner exists, put adapter assertions in the shared contract tests and use TypeScript as the component contract.

- [ ] **Step 2: Implement localized command components**

Render incident ID, impact level, healthy/degraded cards, latency, current stage, owner, elapsed time, freshness, expiry boundary, and one next action. Keep business cards, preview card, and links secondary.

- [ ] **Step 3: Wire the live page**

Construct snapshots from the existing three business snapshot states. Use `scenario.updated_at` as observation input and preserve existing polling/injection behavior.

- [ ] **Step 4: Run site checks**

```bash
cd site && npm run typecheck && npm run build
```

- [ ] **Step 5: Mark public P0 tasks complete and commit**

```bash
git add site/components/demo site/app/live-incident/page.tsx site/lib/demo-types.ts site/tsconfig.json openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: add live demo incident command orientation"
```

### Task 5: Implement timeline, evidence drawer, and precise approval checklist

**Files:**

- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/StageTimeline.jsx`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/EvidenceDrawer.jsx`
- Create: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/ApprovalChecklist.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/IncidentCommandRoute.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/api.js`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command.test.js`

**Interfaces:**

- `StageTimeline` accepts `{ stages, locale, onOpenEvidence }`.
- `EvidenceDrawer` accepts `{ open, groups, onClose }`.
- `ApprovalChecklist` accepts `{ facts, locale }`.

- [ ] **Step 1: Add failing timeline, evidence, and approval tests**

Cover phase source references, completed/current/future distinctions, approved human block, focus semantics, five evidence groups, raw payload secondary disclosure, missing approval context, and no submit endpoint.

- [ ] **Step 2: implement the timeline**

Render one row when space permits and stack on narrow screens. Include status text, owner, duration, compact outcome, blocking reason, source reference, and evidence action.

- [ ] **Step 3: Implement evidence projection**

Normalize existing archive and preview data into five decision groups. Keep source IDs and `legacy_not_applicable` states.

- [ ] **Step 4: Implement the drawer**

Use a dialog-like section with focus trapping, initial focus, `Escape`, and restore focus on close. Raw JSON/details remain nested disclosures.

- [ ] **Step 5: Implement the checklist**

Display authoritative facts and exact instruction only. Include a copy action if safe; do not include an approval submission action.

- [ ] **Step 6: Run tests and build**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test && npm run build
```

- [ ] **Step 7: Mark P1 tasks complete and commit**

```bash
git add plugins/opskeeper-teamharness/dashboard/src/extensions openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: add incident evidence and approval experience"
```

### Task 6: Enforce cross-surface projection parity

**Files:**

- Modify: `shared/incident-command/fixtures.js`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command-contract.test.js`
- Modify: `site/app/live-incident/page.tsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/IncidentCommandRoute.jsx`
- Modify: `openspec/changes/opskeeper-incident-command-ui/tasks.md`

**Interfaces:**

- Golden input pairs produce identical `stage`, `stageStatus`, `stageSubstate`, owner kind/label, freshness, next-action kind, impact level, and completeness.

- [ ] **Step 1: Add golden equivalence tests**

Use deterministic Manager loop and demo scenario fixtures representing detected, approval wait, executing recovery, verifying recovery, closed, failed, and unknown states.

- [ ] **Step 2: Remove surface-local interpretation**

Both components must consume the shared result. Visual labels may differ by locale/surface; semantic fields may not.

- [ ] **Step 3: Run shared, plugin, and site checks**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test && npm run build
cd ../../../site && npm run typecheck && npm run build
```

- [ ] **Step 4: Mark parity tasks complete and commit**

```bash
git add shared/incident-command plugins/opskeeper-teamharness/dashboard/src/extensions site/app/live-incident/page.tsx openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "test: enforce incident command projection parity"
```

### Task 7: Implement archive replay and legacy compatibility

**Files:**

- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive.js`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive-route.jsx`
- Modify: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/StageTimeline.jsx`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/archive.test.js`

**Interfaces:**

- Produces `projectArchiveReplay(archive)`, returning closure projection, timeline, selected/rejected candidate comparison, rollback result, verification result, completeness, similarities, and enrichment provenance.

- [ ] **Step 1: Add failing replay tests**

Cover chronological transition replay, source event links, decision-time versus post-incident evidence, selected/rejected candidates, legacy empty preview fields, bounded similarities, and no automatic drill creation.

- [ ] **Step 2: Implement replay-first archive presentation**

Lead with replay summary and timeline. Move dense tables below. Hide controlled-drill action when identity support is incomplete.

- [ ] **Step 3: Run archive and plugin tests**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test -- src/extensions/archive.test.js src/extensions/incident-command.test.js
```

- [ ] **Step 4: Mark replay tasks complete and commit**

```bash
git add plugins/opskeeper-teamharness/dashboard/src/extensions/archive.js plugins/opskeeper-teamharness/dashboard/src/extensions/archive-route.jsx plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command/StageTimeline.jsx plugins/opskeeper-teamharness/dashboard/src/extensions/archive.test.js openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: add incident archive replay"
```

### Task 8: Define runtime-aware read-only boundary

**Files:**

- Modify: `shared/incident-command/manager-loop.js`
- Test: `plugins/opskeeper-teamharness/dashboard/src/extensions/incident-command-contract.test.js`
- Modify: `openspec/changes/opskeeper-incident-command-ui/tasks.md`

**Interfaces:**

- Extend projection with optional `runtimeBlockers` only when authoritative runtime/task readback exists.
- Each blocker contains `{ kind, runtimeId, taskId?, state, observedAt, freshness, detail }`.

- [ ] **Step 1: Add failing runtime projection tests**

Verify authoritative health/task data becomes a bounded explanation and missing runtime data remains `unknown`. Assert no desired-state field and no administrative action is generated.

- [ ] **Step 2: Implement optional projection only**

Do not add runtime administration navigation or mutation APIs. Existing System Status remains the only runtime surface in this change.

- [ ] **Step 3: Run focused tests**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test -- src/extensions/incident-command-contract.test.js
```

- [ ] **Step 4: Mark runtime boundary tasks complete and commit**

```bash
git add shared/incident-command openspec/changes/opskeeper-incident-command-ui/tasks.md
git commit -m "feat: project runtime incident blockers"
```

### Task 9: Add OPC packet governance and final validation

**Files:**

- Create: `openspec/changes/opskeeper-incident-command-ui/README.md`
- Modify: `openspec/changes/opskeeper-incident-command-ui/tasks.md`
- Modify: `openspec/changes/opskeeper-incident-command-ui/design.md`
- Modify: `docs/superpowers/specs/2026-10-01-opskeeper-incident-command-ui-design.md`

**Interfaces:**

- Produces the OPC packet matrix, dependency gate, review checklist, and validation result references.

- [ ] **Step 1: Document packet ownership and gates**

Record contract, command UI, live demo, evidence/approval, archive, runtime, a11y, and verification packets with authoritative sources, forbidden mutations, changed files, tests, and handoffs.

- [ ] **Step 2: Run strict OpenSpec validation**

```bash
openspec validate opskeeper-incident-command-ui --type change --strict --no-interactive
```

- [ ] **Step 3: Run complete validation matrix**

```bash
cd plugins/opskeeper-teamharness/dashboard && npm test && npm run build
cd ../../../site && npm run typecheck && npm run build
```

Then perform or record the public-demo smoke and keyboard/narrow-screen review. Do not mark the final validation tasks complete until all checks pass.

- [ ] **Step 4: Mark governance/validation tasks complete and commit**

```bash
git add openspec/changes/opskeeper-incident-command-ui docs/superpowers/specs/2026-10-01-opskeeper-incident-command-ui-design.md
git commit -m "docs: document incident command OPC delivery"
```

## Plan Self-Review

- Covers authoritative projection, TeamHarness IA, public demo, timeline/evidence/approval, archive replay, runtime boundary, OPC governance, and validation from the delta spec.
- Uses exact Manager phase IDs and preserves presentation-only boundaries.
- Avoids unspecified dependencies or mutation APIs.
- Keeps legacy compatibility and accessibility as independently testable requirements.
