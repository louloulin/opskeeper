import {
  COMMAND_PHASES,
  COMMAND_PHASE_LABELS,
  normalizeIncidentOwner,
  resolveEvidenceCompleteness,
  resolveFreshness,
  selectNextAction,
} from './manager-loop.js';

const STATE_MAP = new Map([
  ['starting', { stage: 'detected', status: 'running', owner: { kind: 'manager', label: 'Manager' } }],
  ['awaiting_alert', { stage: 'detected', status: 'running', owner: { kind: 'system', label: 'Alertmanager' } }],
  ['alert_correlated', { stage: 'correlated', status: 'completed', owner: { kind: 'manager', label: 'Manager' } }],
  ['diagnosis_dispatched', { stage: 'investigated', status: 'running', owner: { kind: 'worker', role: 'opskeeper-investigator', label: 'Investigator' } }],
  ['preview_ready', { stage: 'approved', status: 'pending', owner: { kind: 'worker', role: 'opskeeper-reviewer', label: 'Reviewer' } }],
  ['awaiting_approval', { stage: 'approved', status: 'blocked', substate: 'awaiting_human', owner: { kind: 'human', label: 'Human' } }],
  ['repair_dispatched', { stage: 'recovered', status: 'running', substate: 'executing', owner: { kind: 'worker', role: 'opskeeper-repairer', label: 'Repairer' } }],
  ['verifying', { stage: 'recovered', status: 'running', substate: 'verifying', owner: { kind: 'verifier', role: 'opskeeper-verifier', label: 'Verifier' } }],
  ['recovered', { stage: 'recovered', status: 'completed', owner: { kind: 'verifier', role: 'opskeeper-verifier', label: 'Verifier' } }],
  ['closed', { stage: 'postmortem', status: 'completed', owner: { kind: 'manager', label: 'Manager' } }],
  ['start_failed', { stage: undefined, status: 'failed', owner: { kind: 'system', label: 'System failure' } }],
]);

function text(value) {
  if (typeof value === 'string' && value.trim()) return value.trim();
  return Number.isFinite(value) ? String(value) : '';
}

function object(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
}

function date(value) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function scopeState(snapshot) {
  if (snapshot.errorCode || snapshot.error_code) {
    return Number.isFinite(snapshot.latency_ms ?? snapshot.latencyMs) ? 'degraded' : 'unknown';
  }
  return Number.isFinite(snapshot.latency_ms ?? snapshot.latencyMs) ? 'healthy' : 'unknown';
}

function impact(snapshots) {
  const values = Array.isArray(snapshots) ? snapshots.map(object) : [];
  const affectedScopes = values.map((snapshot) => ({
    name: text(snapshot.section ?? snapshot.name ?? 'scope'),
    state: scopeState(snapshot),
  }));
  const latencies = values
    .map((snapshot) => snapshot.latency_ms ?? snapshot.latencyMs)
    .filter((value) => Number.isFinite(value));
  const average = latencies.length
    ? Math.round(latencies.reduce((total, value) => total + value, 0) / latencies.length)
    : undefined;
  const states = new Set(affectedScopes.map((scope) => scope.state));
  return {
    level: states.has('unknown') || states.has('degraded') ? 'severe' : values.length ? 'normal' : 'unknown',
    affectedScopes,
    indicators: [{
      name: 'average_latency_ms',
      value: average === undefined ? undefined : `${average} ms`,
      state: average === undefined ? 'unknown' : average >= 1000 ? 'degraded' : 'healthy',
    }],
  };
}

export function fromDemoScenario(input = {}) {
  const source = object(input);
  const scenario = object(source.scenario);
  const status = text(scenario.status);
  const mapped = STATE_MAP.get(status) || { stage: undefined, status: 'unknown', owner: undefined };
  const incidentId = text(scenario.incident_id ?? scenario.incidentId);
  const observedAt = text(scenario.updated_at ?? scenario.updatedAt) || undefined;
  const stageIndex = COMMAND_PHASES.indexOf(mapped.stage);
  const investigatedIndex = COMMAND_PHASES.indexOf('investigated');
  const approvedIndex = COMMAND_PHASES.indexOf('approved');
  const recoveredIndex = COMMAND_PHASES.indexOf('recovered');
  const causeObserved = stageIndex >= investigatedIndex;
  const previewObserved = [
    'preview_ready', 'awaiting_approval', 'repair_dispatched', 'verifying', 'recovered', 'closed',
  ].includes(status);
  const authorizationObserved = stageIndex >= approvedIndex;
  const recoveryObserved = stageIndex >= recoveredIndex;
  const recoveryComplete = ['recovered', 'closed'].includes(status);
  const owner = normalizeIncidentOwner(mapped.owner);
  const stageTimeline = COMMAND_PHASES.map((stage, index) => ({
    stage,
    status: stage === mapped.stage
      ? mapped.status
      : stageIndex >= 0 && index < stageIndex
        ? 'completed'
        : 'pending',
    ownerLabel: stage === mapped.stage ? owner?.label : undefined,
    evidenceRefs: [],
  }));
  const command = {
    incidentId,
    stage: mapped.stage,
    stageStatus: mapped.status,
    stageSubstate: mapped.substate,
    owner,
  };
  return {
    incidentId,
    scenario: text(scenario.scenario_id ?? scenario.scenarioId),
    ...command,
    freshness: resolveFreshness(observedAt, source.serverNow),
    observedAt,
    serverNow: text(source.serverNow) || undefined,
    businessImpact: impact(source.snapshots),
    nextAction: selectNextAction(command),
    stageTimeline,
    evidenceCompleteness: resolveEvidenceCompleteness({
      incident: { observed: Boolean(command.incidentId), complete: true },
      cause: {
        observed: causeObserved,
        complete: causeObserved && stageIndex > investigatedIndex,
      },
      preview: {
        observed: previewObserved,
        complete: previewObserved,
      },
      authorization: {
        observed: authorizationObserved,
        complete: ['repair_dispatched', 'verifying', 'recovered', 'closed'].includes(status),
      },
      execution: {
        observed: recoveryObserved,
        complete: recoveryObserved,
      },
      verification: {
        observed: recoveryObserved,
        complete: recoveryComplete,
      },
    }),
  };
}
