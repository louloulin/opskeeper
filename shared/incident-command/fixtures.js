import { fromDemoScenario } from './demo-scenario.js';
import { fromManagerLoop } from './manager-loop.js';

const serverNow = '2026-10-01T12:00:30.000Z';
const observedAt = serverNow;
const severeSnapshot = { section: 'orders', latency_ms: 1500, error_code: 'degraded' };

function managerInput(currentPhase, phases, events, extra = {}) {
  return {
    state: {
      incident_id: 'inc-golden',
      current_phase: currentPhase,
      updated_at: observedAt,
      ...extra.state,
    },
    timeline: {
      incident_id: 'inc-golden',
      phases,
      events,
      chain: { current_phase: currentPhase, phases_observed: phases.length, phases_expected: 7 },
    },
    incident: {
      id: 'inc-golden',
      severity: 'critical',
      ...extra.incident,
    },
    preview: extra.preview,
    serverNow,
  };
}

function demoInput(status, extra = {}) {
  return {
    scenario: {
      incident_id: 'inc-golden',
      scenario_id: 'pg-pool-exhaustion',
      status,
      updated_at: observedAt,
      ...extra.scenario,
    },
    snapshots: extra.snapshots ?? [severeSnapshot],
    serverNow,
  };
}

function phaseRecord(phase, status, extra = {}) {
  return { phase, status, ...extra };
}

function phaseEvent(id, phase, eventType, createdAt = observedAt) {
  return { id, phase, event_type: eventType, created_at: createdAt };
}

function completedPreRecoveryPhases(approvedAudit = [{ kind: 'approval' }]) {
  return [
    phaseRecord('detected', 'success', { worker_role: 'opskeeper-manager', contract_summary: 'alert observed' }),
    phaseRecord('investigated', 'success', {
      worker_role: 'opskeeper-investigator',
      contract_summary: 'pool exhausted',
      evidence_ref: 'archive:cause',
    }),
    phaseRecord('critiqued', 'success', { worker_role: 'opskeeper-critic', contract_summary: 'reviewed' }),
    phaseRecord('approved', 'success', { worker_role: 'opskeeper-reviewer', audit: approvedAudit }),
  ];
}

function completedPreRecoveryEvents(offset = 0) {
  return [
    phaseEvent(offset + 1, 'detected', 'phase_entered', '2026-10-01T11:59:50.000Z'),
    phaseEvent(offset + 2, 'detected', 'phase_contract_written', '2026-10-01T11:59:51.000Z'),
    phaseEvent(offset + 3, 'investigated', 'phase_entered', '2026-10-01T11:59:52.000Z'),
    phaseEvent(offset + 4, 'investigated', 'phase_contract_written', '2026-10-01T11:59:53.000Z'),
    phaseEvent(offset + 5, 'critiqued', 'phase_entered', '2026-10-01T11:59:54.000Z'),
    phaseEvent(offset + 6, 'critiqued', 'phase_contract_written', '2026-10-01T11:59:55.000Z'),
    phaseEvent(offset + 7, 'approved', 'phase_entered', '2026-10-01T11:59:56.000Z'),
    phaseEvent(offset + 8, 'approved', 'phase_contract_written', '2026-10-01T11:59:57.000Z'),
  ];
}

const readyPreview = { run_id: 'run-golden', passing: { candidate_id: 'candidate-a' } };

export const incidentCommandGoldenInputPairs = Object.freeze([
  Object.freeze({
    name: 'detected',
    manager: managerInput('detected', [
      phaseRecord('detected', 'running', { worker_role: 'opskeeper-manager' }),
    ], [phaseEvent(1, 'detected', 'phase_entered')]),
    demo: demoInput('starting'),
  }),
  Object.freeze({
    name: 'approval_wait',
    manager: managerInput('approved', [
      ...completedPreRecoveryPhases([]),
      phaseRecord('approved', 'running', { worker_role: 'opskeeper-reviewer' }),
    ], [
      ...completedPreRecoveryEvents(),
      phaseEvent(9, 'approved', 'phase_paused'),
    ], { preview: readyPreview }),
    demo: demoInput('awaiting_approval'),
  }),
  Object.freeze({
    name: 'executing_recovery',
    manager: managerInput('recovered', [
      ...completedPreRecoveryPhases(),
      phaseRecord('recovered', 'running', {
        worker_role: 'opskeeper-repairer',
        audit: [{ kind: 'execution' }],
      }),
    ], [
      ...completedPreRecoveryEvents(),
      phaseEvent(9, 'recovered', 'phase_entered'),
    ], { preview: readyPreview }),
    demo: demoInput('repair_dispatched'),
  }),
  Object.freeze({
    name: 'verifying_recovery',
    manager: managerInput('recovered', [
      ...completedPreRecoveryPhases(),
      phaseRecord('recovered', 'running', {
        worker_role: 'opskeeper-verifier',
        audit: [{ kind: 'execution' }, { kind: 'verification' }],
      }),
    ], [
      ...completedPreRecoveryEvents(),
      phaseEvent(9, 'recovered', 'phase_entered'),
    ], { preview: readyPreview }),
    demo: demoInput('verifying'),
  }),
  Object.freeze({
    name: 'closed',
    manager: managerInput('postmortem', [
      ...completedPreRecoveryPhases(),
      phaseRecord('recovered', 'success', {
        worker_role: 'opskeeper-verifier',
        audit: [{ kind: 'execution' }, { kind: 'verification' }],
        contract_summary: 'verified recovered',
      }),
      phaseRecord('postmortem', 'success', { worker_role: 'opskeeper-manager', contract_summary: 'closed' }),
    ], [
      ...completedPreRecoveryEvents(),
      phaseEvent(9, 'recovered', 'phase_entered'),
      phaseEvent(10, 'recovered', 'phase_contract_written'),
      phaseEvent(11, 'postmortem', 'phase_entered'),
      phaseEvent(12, 'postmortem', 'phase_contract_written'),
    ], { preview: readyPreview }),
    demo: demoInput('closed'),
  }),
  Object.freeze({
    name: 'failed',
    manager: managerInput('scenario_start', [], [phaseEvent(1, 'scenario_start', 'phase_failed')], {
      state: { status: 'failed' },
    }),
    demo: demoInput('start_failed'),
  }),
  Object.freeze({
    name: 'unknown',
    manager: { state: null, timeline: null, incident: null },
    demo: demoInput('unrecognized', {
      scenario: { incident_id: '', updated_at: undefined },
      snapshots: [],
    }),
  }),
]);

export const emptyManagerLoop = fromManagerLoop({});

export const managerApprovedPause = fromManagerLoop({
  state: { incident_id: 'inc-1', current_phase: 'approved', updated_at: '2026-10-01T12:00:30Z' },
  timeline: {
    phases: [{ phase: 'approved', status: 'running', worker_role: 'opskeeper-reviewer' }],
    events: [{ id: 21, phase: 'approved', event_type: 'phase_paused', created_at: '2026-10-01T12:00:00Z' }],
  },
  serverNow: '2026-10-01T12:00:30Z',
});

export const demoAwaitingApproval = fromDemoScenario({
  scenario: { incident_id: '77', scenario_id: 'pg-pool-exhaustion', status: 'awaiting_approval', updated_at: '2026-10-01T12:00:30Z' },
  snapshots: [],
  serverNow: '2026-10-01T12:00:30Z',
});
