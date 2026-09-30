import { fromDemoScenario } from './demo-scenario.js';
import { fromManagerLoop } from './manager-loop.js';

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
