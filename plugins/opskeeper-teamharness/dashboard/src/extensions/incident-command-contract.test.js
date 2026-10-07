import assert from 'node:assert/strict';
import test from 'node:test';

import {
  COMMAND_PHASES,
  COMMAND_PHASE_LABELS,
  demoAwaitingApproval,
  emptyManagerLoop,
  fromDemoScenario,
  fromManagerLoop,
  incidentCommandGoldenInputPairs,
  managerApprovedPause,
  resolveFreshness,
  selectNextAction,
} from '../../../../../shared/incident-command/index.js';

const serverNow = '2026-10-01T12:00:30.000Z';

const REQUIRED_PARITY_FIELDS = [
  'stage',
  'stageStatus',
  'stageSubstate',
  'freshness',
  'evidenceCompleteness',
];

function event(id, phase, eventType, createdAt = '2026-10-01T12:00:00.000Z') {
  return { id, phase, event_type: eventType, created_at: createdAt };
}

function timelineWithPhases(phases, events = []) {
  return {
    incident_id: 'inc-1',
    phases,
    events,
    chain: {
      current_phase: phases.find((phase) => phase.status !== 'pending')?.phase || '',
      phases_observed: phases.filter((phase) => phase.status !== 'pending').length,
      phases_expected: 7,
    },
  };
}

test('uses Manager loop phase identifiers as command stages', () => {
  assert.deepEqual(COMMAND_PHASES, [
    'detected', 'correlated', 'investigated', 'critiqued',
    'approved', 'recovered', 'postmortem',
  ]);
  assert.equal(COMMAND_PHASE_LABELS.approved, '人工审批');
});

test('projects every authoritative Manager phase through the same identifier', () => {
  for (const [index, phase] of COMMAND_PHASES.entries()) {
    const view = fromManagerLoop({
      state: { incident_id: 'inc-1', current_phase: phase, updated_at: serverNow },
      timeline: timelineWithPhases(
        [{ phase, status: 'pending', worker_role: `opskeeper-${phase}` }],
        [event(index + 1, phase, 'phase_entered')],
      ),
      serverNow,
    });
    assert.equal(view.stage, phase);
    assert.equal(view.stageStatus, 'running');
  }
});

test('does not infer a current stage from an unrecognized event', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'investigated', updated_at: serverNow },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(61, 'investigated', 'renamed_event')],
    ),
    serverNow,
  });

  assert.equal(view.stage, undefined);
  assert.equal(view.stageStatus, 'unknown');
  assert.equal(view.stageTimeline[2].status, 'unknown');
});

test('projects approved pause as human blocking', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'approved', updated_at: serverNow },
    timeline: timelineWithPhases([
      { phase: 'approved', status: 'running', worker_role: 'opskeeper-reviewer' },
    ], [event(21, 'approved', 'phase_paused')]),
    serverNow,
  });

  assert.equal(view.stage, 'approved');
  assert.equal(view.stageStatus, 'blocked');
  assert.equal(view.stageSubstate, 'awaiting_human');
  assert.equal(view.owner.kind, 'human');
  assert.equal(view.owner.label, 'Human');
  assert.equal(view.sourceEventId, '21');
  assert.equal(view.nextAction.kind, 'approve');
});

test('keeps missing loop data unknown instead of detected', () => {
  const view = fromManagerLoop({ state: null, timeline: null });

  assert.equal(view.incidentId, '');
  assert.equal(view.stage, undefined);
  assert.equal(view.stageStatus, 'unknown');
  assert.equal(view.freshness, 'unknown');
  assert.deepEqual(view.stageTimeline.map((stage) => stage.status), Array(7).fill('unknown'));
});

test('ignores Manager pending placeholders when no authoritative events exist', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'detected', updated_at: serverNow },
    timeline: {
      incident_id: 'inc-1',
      phases: [{ phase: 'detected', status: 'pending', worker_role: 'opskeeper-detector' }],
      events: [],
      chain: { current_phase: 'detected', phases_observed: 0, phases_expected: 7 },
    },
    serverNow,
  });

  assert.equal(view.stage, undefined);
  assert.equal(view.stageStatus, 'unknown');
  assert.equal(view.stageTimeline[0].status, 'unknown');
});

test('clears approval blocking after a later resume event', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'approved', updated_at: serverNow },
    timeline: timelineWithPhases([
      { phase: 'approved', status: 'running', worker_role: 'opskeeper-reviewer' },
    ], [
      event(21, 'approved', 'phase_paused', '2026-10-01T11:59:00Z'),
      event(22, 'approved', 'phase_resumed', '2026-10-01T12:00:00Z'),
    ]),
    serverNow,
  });

  assert.equal(view.stageStatus, 'running');
  assert.equal(view.stageSubstate, undefined);
  assert.equal(view.owner.kind, 'worker');
  assert.notEqual(view.nextAction.kind, 'approve');
});

test('derives stage facts from authoritative events and timeline phases', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'investigated', updated_at: serverNow },
    timeline: timelineWithPhases([
      { phase: 'detected', status: 'success', started_at: '2026-10-01T11:59:00Z', ended_at: '2026-10-01T11:59:01Z', contract_summary: 'alert accepted' },
      { phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator', contract_summary: 'pool exhausted' },
    ], [
      event(1, 'detected', 'phase_entered', '2026-10-01T11:59:00Z'),
      event(2, 'detected', 'phase_contract_written', '2026-10-01T11:59:01Z'),
      event(3, 'investigated', 'phase_entered', '2026-10-01T11:59:02Z'),
    ]),
    serverNow,
  });

  assert.equal(view.stage, 'investigated');
  assert.equal(view.stageStatus, 'running');
  assert.equal(view.owner.kind, 'worker');
  assert.equal(view.owner.role, 'opskeeper-investigator');
  assert.equal(view.sourceEventId, '3');
  assert.equal(view.stageTimeline[0].status, 'completed');
  assert.equal(view.stageTimeline[0].durationMs, 1000);
  assert.equal(view.stageTimeline[0].outcome, 'alert accepted');
  assert.equal(view.stageTimeline[0].sourceEventId, '2');
});

test('keeps an absent Manager owner undefined', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'detected', updated_at: serverNow },
    timeline: timelineWithPhases([
      { phase: 'detected', status: 'running' },
    ], [event(71, 'detected', 'phase_entered')]),
    serverNow,
  });

  assert.equal(view.owner, undefined);
  assert.equal(view.stageTimeline.find((stage) => stage.stage === 'detected').ownerLabel, undefined);
});

test('marks failure and retry exhaustion without inferring progress', () => {
  for (const eventType of ['phase_failed', 'retry_exhausted']) {
    const view = fromManagerLoop({
      state: { incident_id: 'inc-1', current_phase: 'recovered', updated_at: serverNow, state_error: 'failed' },
      timeline: timelineWithPhases([
        { phase: 'recovered', status: 'failed' },
      ], [event(31, 'recovered', eventType)]),
      serverNow,
    });
    assert.equal(view.stage, 'recovered');
    assert.equal(view.stageStatus, 'failed');
    assert.equal(view.nextAction.kind, 'retry');
  }
});

test('keeps malformed phase data unknown', () => {
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'not-a-phase', updated_at: serverNow },
    timeline: { phases: [{ phase: 'not-a-phase', status: 'running' }], events: [] },
    serverNow,
  });

  assert.equal(view.stage, undefined);
  assert.equal(view.stageStatus, 'unknown');
});

test('projects recovered execution and verification from audit evidence', () => {
  const execution = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'recovered', updated_at: serverNow },
    timeline: {
      phases: [{ phase: 'recovered', status: 'running', worker_role: 'opskeeper-repairer', audit: [{ kind: 'execution' }] }],
      events: [event(41, 'recovered', 'phase_entered')],
    },
    serverNow,
  });
  assert.equal(execution.stageSubstate, 'executing');
  assert.equal(execution.owner.role, 'opskeeper-repairer');

  const verification = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'recovered', updated_at: serverNow },
    timeline: {
      phases: [{ phase: 'recovered', status: 'running', worker_role: 'opskeeper-verifier', audit: [{ kind: 'verification' }] }],
      events: [event(42, 'recovered', 'phase_entered')],
    },
    serverNow,
  });
  assert.equal(verification.stageSubstate, 'verifying');
  assert.equal(verification.nextAction.kind, 'verify');
});

test('maps every demo state to the authoritative command phases', () => {
  const cases = [
    ['starting', 'detected', 'running', undefined],
    ['awaiting_alert', 'detected', 'running', undefined],
    ['alert_correlated', 'correlated', 'completed', undefined],
    ['diagnosis_dispatched', 'investigated', 'running', undefined],
    ['preview_ready', 'approved', 'pending', undefined],
    ['awaiting_approval', 'approved', 'blocked', 'awaiting_human'],
    ['repair_dispatched', 'recovered', 'running', 'executing'],
    ['verifying', 'recovered', 'running', 'verifying'],
    ['recovered', 'recovered', 'completed', undefined],
    ['closed', 'postmortem', 'completed', undefined],
    ['start_failed', undefined, 'failed', undefined],
  ];

  for (const [status, stage, stageStatus, substate] of cases) {
    const view = fromDemoScenario({
      scenario: { incident_id: 77, scenario_id: 'pg-pool', status, updated_at: serverNow },
      snapshots: [
        { section: 'orders', latency_ms: 100, error_code: undefined },
        { section: 'inventory', latency_ms: 3000, error_code: 'degraded' },
      ],
      serverNow,
    });
    assert.equal(view.stage, stage, status);
    assert.equal(view.stageStatus, stageStatus, status);
    assert.equal(view.stageSubstate, substate, status);
  }
});

test('exposes deterministic fixtures without fabricating demo source identity', () => {
  assert.equal(emptyManagerLoop.stageStatus, 'unknown');
  assert.equal(managerApprovedPause.nextAction.kind, 'approve');
  assert.equal(demoAwaitingApproval.stageSubstate, 'awaiting_human');
  assert.equal(demoAwaitingApproval.sourceEventId, undefined);
  assert.equal(demoAwaitingApproval.stageTimeline.filter((stage) => stage.ownerLabel).length, 1);
});

test('projects deterministic golden Manager and demo input pairs to identical command semantics', () => {
  assert.deepEqual(
    incidentCommandGoldenInputPairs.map((pair) => pair.name),
    [
      'detected',
      'approval_wait',
      'executing_recovery',
      'verifying_recovery',
      'closed',
      'failed',
      'unknown',
    ],
  );

  for (const pair of incidentCommandGoldenInputPairs) {
    const manager = fromManagerLoop(pair.manager);
    const demo = fromDemoScenario(pair.demo);
    for (const field of REQUIRED_PARITY_FIELDS) {
      assert.deepEqual(manager[field], demo[field], `${pair.name}: ${field}`);
    }
    assert.equal(manager.nextAction?.kind, demo.nextAction?.kind, `${pair.name}: nextAction.kind`);
    assert.equal(manager.businessImpact.level, demo.businessImpact.level, `${pair.name}: businessImpact.level`);
    assert.deepEqual(
      manager.owner && { kind: manager.owner.kind, label: manager.owner.label },
      demo.owner && { kind: demo.owner.kind, label: demo.owner.label },
      `${pair.name}: owner`,
    );
  }
});

test('computes generic business impact from demo snapshots', () => {
  const view = fromDemoScenario({
    scenario: { incident_id: 77, status: 'awaiting_approval', updated_at: serverNow },
    snapshots: [
      { section: 'orders', latency_ms: 100 },
      { section: 'inventory', latency_ms: 3000, error_code: 'degraded' },
      { section: 'audit', error_code: 'unavailable' },
    ],
    serverNow,
  });

  assert.equal(view.businessImpact.level, 'severe');
  assert.deepEqual(view.businessImpact.affectedScopes.map((scope) => scope.state), [
    'healthy', 'degraded', 'unknown',
  ]);
  assert.equal(view.businessImpact.indicators[0].name, 'average_latency_ms');
  assert.equal(view.businessImpact.indicators[0].value, '1550 ms');
});

test('uses server time and explicit thresholds for freshness', () => {
  assert.equal(resolveFreshness(serverNow, serverNow), 'fresh');
  assert.equal(resolveFreshness('2026-10-01T11:59:30.000Z', serverNow), 'fresh');
  assert.equal(resolveFreshness('2026-10-01T11:59:29.999Z', serverNow), 'stale');
  assert.equal(resolveFreshness('2026-10-01T12:00:30.001Z', serverNow), 'unknown');
  assert.equal(resolveFreshness('2026-10-01T11:00:00Z', serverNow), 'stale');
  assert.equal(resolveFreshness(serverNow, undefined), 'unknown');
  assert.equal(resolveFreshness('invalid', serverNow), 'unknown');
});

test('projects only authoritative runtime blocker readback without administrative actions', () => {
  const view = fromManagerLoop({
    state: {
      incident_id: 'inc-1',
      current_phase: 'investigated',
      status: 'running',
      updated_at: serverNow,
    },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(71, 'investigated', 'phase_entered')],
    ),
    runtimeReadback: {
      blockers: [
        {
          kind: 'health',
          runtime_id: 'runtime-1',
          state: 'degraded',
          observed_at: serverNow,
          detail: 'Runtime health is degraded',
        },
        {
          kind: 'claim',
          runtime_id: 'runtime-1',
          task_id: 'task-1',
          state: 'unclaimed',
          observed_at: serverNow,
          detail: 'Task claim is absent',
        },
        {
          kind: 'lease',
          runtime_id: 'runtime-1',
          task_id: 'task-1',
          state: 'expired',
          observed_at: '2026-10-01T11:00:00.000Z',
          detail: 'Lease expired before checkpoint',
        },
        {
          kind: 'checkpoint',
          runtimeId: 'runtime-2',
          taskId: 'task-2',
          state: 'behind',
          observedAt: serverNow,
          detail: 'Checkpoint lags the observed task stream',
        },
        {
          kind: 'recovery',
          runtimeId: 'runtime-2',
          taskId: 'task-2',
          state: 'waiting',
          observedAt: serverNow,
          detail: 'Worker recovery is waiting for a lease',
        },
        {
          kind: 'drift',
          runtimeId: 'runtime-3',
          state: 'plugin-version-mismatch',
          observedAt: serverNow,
          detail: 'Plugin version differs from the authoritative inventory',
        },
      ],
    },
    serverNow,
  });

  assert.deepEqual(view.runtimeBlockers, [
    {
      kind: 'health', runtimeId: 'runtime-1', taskId: undefined, state: 'degraded',
      observedAt: serverNow, freshness: 'fresh', detail: 'Runtime health is degraded',
    },
    {
      kind: 'claim', runtimeId: 'runtime-1', taskId: 'task-1', state: 'unclaimed',
      observedAt: serverNow, freshness: 'fresh', detail: 'Task claim is absent',
    },
    {
      kind: 'lease', runtimeId: 'runtime-1', taskId: 'task-1', state: 'expired',
      observedAt: '2026-10-01T11:00:00.000Z', freshness: 'stale',
      detail: 'Lease expired before checkpoint',
    },
    {
      kind: 'checkpoint', runtimeId: 'runtime-2', taskId: 'task-2', state: 'behind',
      observedAt: serverNow, freshness: 'fresh', detail: 'Checkpoint lags the observed task stream',
    },
    {
      kind: 'recovery', runtimeId: 'runtime-2', taskId: 'task-2', state: 'waiting',
      observedAt: serverNow, freshness: 'fresh', detail: 'Worker recovery is waiting for a lease',
    },
    {
      kind: 'drift', runtimeId: 'runtime-3', taskId: undefined, state: 'plugin-version-mismatch',
      observedAt: serverNow, freshness: 'fresh',
      detail: 'Plugin version differs from the authoritative inventory',
    },
  ]);
  assert.equal('desiredState' in view, false);
  assert.equal(view.nextAction.kind, 'wait');
  assert.deepEqual(
    Object.keys(view).filter((key) => /desired|admin|mutate|rollout|credential/i.test(key)),
    [],
  );
});

test('keeps absent or malformed runtime readback unknown rather than inferring failure', () => {
  const absent = fromManagerLoop({
    state: {
      incident_id: 'inc-1',
      current_phase: 'investigated',
      status: 'running',
      updated_at: serverNow,
    },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(72, 'investigated', 'phase_entered')],
    ),
    serverNow,
  });
  assert.equal(absent.runtimeBlockers, undefined);
  assert.equal('desiredState' in absent, false);
  assert.equal(absent.nextAction.kind, 'wait');

  const incomplete = fromManagerLoop({
    state: {
      incident_id: 'inc-1',
      current_phase: 'investigated',
      status: 'running',
      updated_at: serverNow,
    },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(74, 'investigated', 'phase_entered')],
    ),
    runtimeReadback: {},
    serverNow,
  });
  assert.deepEqual(incomplete.runtimeBlockers, [{
    kind: 'unknown',
    runtimeId: undefined,
    taskId: undefined,
    state: 'unknown',
    observedAt: undefined,
    freshness: 'unknown',
    detail: 'Runtime blocker readback is incomplete',
  }]);

  const malformed = fromManagerLoop({
    state: {
      incident_id: 'inc-1',
      current_phase: 'investigated',
      status: 'running',
      updated_at: serverNow,
    },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(73, 'investigated', 'phase_entered')],
    ),
    runtimeReadback: { blockers: [{ runtime_id: 'runtime-unknown' }] },
    serverNow,
  });
  assert.deepEqual(malformed.runtimeBlockers, [{
    kind: 'unknown',
    runtimeId: 'runtime-unknown',
    taskId: undefined,
    state: 'unknown',
    observedAt: undefined,
    freshness: 'unknown',
    detail: 'Runtime blocker readback is incomplete',
  }]);
  assert.equal(malformed.stageStatus, 'running');
  assert.equal(malformed.nextAction.kind, 'wait');
  assert.equal('desiredState' in malformed, false);
});

test('normalizes unsupported or incomplete runtime blocker data while preserving evidence', () => {
  const view = fromManagerLoop({
    state: {
      incident_id: 'inc-1',
      current_phase: 'investigated',
      status: 'running',
      updated_at: serverNow,
    },
    timeline: timelineWithPhases(
      [{ phase: 'investigated', status: 'running', worker_role: 'opskeeper-investigator' }],
      [event(75, 'investigated', 'phase_entered')],
    ),
    runtimeReadback: {
      blockers: [
        {
          kind: 'credential',
          runtimeId: 'runtime-unsupported',
          taskId: 'task-unsupported',
          state: 'expired',
          observedAt: serverNow,
        },
        {
          kind: 'health',
          runtimeId: 'runtime-empty-state',
          state: '',
          observedAt: serverNow,
        },
        {
          kind: 'drift',
          taskId: 'task-missing-runtime',
          state: 'version-mismatch',
          observedAt: serverNow,
        },
      ],
    },
    serverNow,
  });

  assert.deepEqual(view.runtimeBlockers, [
    {
      kind: 'unknown',
      runtimeId: 'runtime-unsupported',
      taskId: 'task-unsupported',
      state: 'unknown',
      observedAt: serverNow,
      freshness: 'fresh',
      detail: 'Runtime blocker readback is incomplete',
    },
    {
      kind: 'unknown',
      runtimeId: 'runtime-empty-state',
      taskId: undefined,
      state: 'unknown',
      observedAt: serverNow,
      freshness: 'fresh',
      detail: 'Runtime blocker readback is incomplete',
    },
    {
      kind: 'unknown',
      runtimeId: undefined,
      taskId: 'task-missing-runtime',
      state: 'unknown',
      observedAt: serverNow,
      freshness: 'fresh',
      detail: 'Runtime blocker readback is incomplete',
    },
  ]);
});

test('selects one deterministic next action by priority', () => {
  const action = selectNextAction({
    stage: 'approved',
    stageStatus: 'blocked',
    stageSubstate: 'awaiting_human',
    candidates: [
      { kind: 'wait', priority: 60, label: 'Wait' },
      { kind: 'approve', priority: 20, label: 'Approve' },
    ],
  });
  assert.equal(action.kind, 'approve');
});

test('does not mutate candidate order while selecting a next action', () => {
  const candidates = [
    { kind: 'wait', priority: 60, label: 'Wait' },
    { kind: 'approve', priority: 20, label: 'Approve' },
  ];
  selectNextAction({ candidates });
  assert.deepEqual(candidates, [
    { kind: 'wait', priority: 60, label: 'Wait' },
    { kind: 'approve', priority: 20, label: 'Approve' },
  ]);
});

test('calculates completeness by decision and preserves legacy archive semantics', () => {
  const archive = {
    timeline: [{ event_type: 'incident_closed' }],
    repair_previews: [],
  };
  const view = fromManagerLoop({
    state: { incident_id: 'inc-1', current_phase: 'postmortem', updated_at: serverNow },
    timeline: {
      phases: [
        { phase: 'investigated', status: 'success', contract_summary: 'cause found', evidence_ref: 'cause-1' },
        { phase: 'approved', status: 'success', audit: [{ kind: 'approval' }] },
        { phase: 'recovered', status: 'success', audit: [{ kind: 'execution' }, { kind: 'verification' }] },
        { phase: 'postmortem', status: 'success' },
      ],
      events: [
        event(51, 'investigated', 'phase_entered', '2026-10-01T11:58:00Z'),
        event(52, 'investigated', 'phase_contract_written', '2026-10-01T11:59:00Z'),
        event(53, 'approved', 'phase_entered', '2026-10-01T11:59:01Z'),
        event(54, 'approved', 'phase_contract_written', '2026-10-01T11:59:02Z'),
        event(55, 'recovered', 'phase_entered', '2026-10-01T11:59:03Z'),
        event(56, 'recovered', 'phase_contract_written', '2026-10-01T11:59:04Z'),
        event(57, 'postmortem', 'phase_entered', '2026-10-01T11:59:05Z'),
        event(58, 'postmortem', 'phase_contract_written', '2026-10-01T11:59:06Z'),
      ],
    },
    incident: { id: 'inc-1', labels: { incident_id: 'inc-1' } },
    preview: { run_id: 'run-1', passing: { candidate_id: 'A' } },
    archive,
    serverNow,
  });

  assert.deepEqual(view.evidenceCompleteness, {
    incident: 'complete',
    cause: 'complete',
    preview: 'complete',
    authorization: 'complete',
    execution: 'complete',
    verification: 'complete',
  });

  const legacy = fromManagerLoop({
    state: { incident_id: 'inc-legacy', current_phase: 'postmortem', updated_at: serverNow },
    timeline: { phases: [{ phase: 'postmortem', status: 'success' }], events: [] },
    incident: { id: 'inc-legacy' },
    archive: { ...archive, closed: true },
    serverNow,
  });
  assert.equal(legacy.evidenceCompleteness.preview, 'legacy_not_applicable');
  assert.equal(legacy.evidenceCompleteness.authorization, 'missing');
  assert.equal(legacy.evidenceCompleteness.execution, 'missing');
  assert.equal(legacy.evidenceCompleteness.verification, 'missing');
});
