export const COMMAND_PHASES = Object.freeze([
  'detected',
  'correlated',
  'investigated',
  'critiqued',
  'approved',
  'recovered',
  'postmortem',
]);

export const COMMAND_PHASE_LABELS = Object.freeze({
  detected: '告警确认',
  correlated: '关联聚类',
  investigated: '深度诊断',
  critiqued: '自批评审',
  approved: '人工审批',
  recovered: '修复验证',
  postmortem: '复盘归档',
});

const TERMINAL_EVENT_TYPES = new Set(['phase_failed', 'retry_exhausted']);
const FRESHNESS_LIMIT_MS = 60_000;
const FAILURE_STATES = new Set(['failed', 'aborted']);

function object(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
}

function text(value) {
  if (typeof value === 'string' && value.trim()) return value.trim();
  return Number.isFinite(value) ? String(value) : '';
}

function identity(value) {
  const source = object(value);
  return text(source.id ?? source.incident_id ?? source.incidentId);
}

function date(value) {
  const parsed = value instanceof Date ? value : new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function milliseconds(start, end) {
  const startTime = date(start);
  const endTime = date(end);
  return startTime && endTime && endTime >= startTime ? endTime - startTime : undefined;
}

function isCommandPhase(value) {
  return COMMAND_PHASES.includes(value);
}

function normalizePhase(value) {
  const source = object(value);
  const phase = text(source.phase);
  return isCommandPhase(phase) ? { ...source, phase } : null;
}

function normalizeEvent(value) {
  const source = object(value);
  const phase = text(source.phase);
  const eventType = text(source.event_type ?? source.eventType);
  const id = text(source.id ?? source.event_id ?? source.eventId);
  if (!phase || !eventType) return null;
  return {
    id,
    phase,
    eventType,
    createdAt: source.created_at ?? source.createdAt,
    taskId: text(source.task_id ?? source.taskId),
  };
}

function normalizeTimeline(value) {
  const source = object(value);
  const phases = Array.isArray(source.phases)
    ? source.phases.map(normalizePhase).filter(Boolean)
    : [];
  const events = Array.isArray(source.events)
    ? source.events.map(normalizeEvent).filter(Boolean)
    : [];
  return {
    incidentId: identity(source),
    phases,
    events,
    chain: object(source.chain),
  };
}

function evidenceRefs(phase) {
  const refs = [];
  const add = (value) => {
    const ref = text(value);
    if (ref && !refs.includes(ref)) refs.push(ref);
  };
  add(phase.evidence_ref ?? phase.evidenceRef);
  for (const call of Array.isArray(phase.tool_calls) ? phase.tool_calls : []) {
    add(object(call).evidence_ref ?? object(call).evidenceRef);
  }
  for (const row of Array.isArray(phase.audit) ? phase.audit : []) {
    add(object(row).evidence_ref ?? object(row).evidenceRef);
  }
  return refs;
}

function auditKinds(phase) {
  const rows = Array.isArray(phase.audit) ? phase.audit.map(object) : [];
  return new Set(rows.map((row) => text(row.kind).toLowerCase()));
}

export function normalizeIncidentOwner(input) {
  const source = object(input);
  const kind = text(source.kind).toLowerCase();
  const role = text(source.role);
  const label = text(source.label);
  if (kind === 'human') return { kind: 'human', label: label || 'Human' };
  const identity = `${role} ${label}`.toLowerCase();
  if (kind === 'verifier' || /verifier/.test(identity)) {
    return { kind: 'verifier', role: role || undefined, label: 'Verifier' };
  }
  if (kind === 'manager' || /(^|\b)(manager|orchestrator)/.test(identity) || label === 'Manager') {
    return { kind: 'manager', role: role || undefined, label: 'Manager' };
  }
  if (kind === 'system' || /system failure|scenario runner/.test(identity)) {
    return { kind: 'system', role: role || undefined, label: 'System failure' };
  }
  if (/alertmanager/.test(identity)) {
    return { kind: 'system', role: role || undefined, label: 'Alertmanager' };
  }
  if (role || label) {
    const displayRole = (role || label)
      .replace(/^opskeeper[-_]/, '')
      .split(/[-_\s]+/)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join(' ');
    return { kind: 'worker', role: role || undefined, label: label || displayRole };
  }
  return undefined;
}

export function resolveEvidenceCompleteness(input = {}) {
  const source = object(input);
  const project = (name) => {
    const evidence = object(source[name]);
    if (!evidence.observed) return 'missing';
    return evidence.complete ? 'complete' : 'partial';
  };
  return {
    incident: project('incident'),
    cause: project('cause'),
    preview: source.preview?.legacyNotApplicable && !source.preview?.observed
      ? 'legacy_not_applicable'
      : project('preview'),
    authorization: project('authorization'),
    execution: project('execution'),
    verification: project('verification'),
  };
}

export function resolveFreshness(observedAt, serverNow) {
  const observed = date(observedAt);
  const now = date(serverNow);
  if (!observed || !now) return 'unknown';
  if (observed > now) return 'unknown';
  return now - observed <= FRESHNESS_LIMIT_MS ? 'fresh' : 'stale';
}

function projectStages(timeline) {
  const byPhase = new Map(timeline.phases.map((phase) => [phase.phase, phase]));
  const eventsByPhase = new Map();
  for (const event of timeline.events) {
    if (!eventsByPhase.has(event.phase)) eventsByPhase.set(event.phase, []);
    eventsByPhase.get(event.phase).push(event);
  }

  return COMMAND_PHASES.map((phase) => {
    const source = byPhase.get(phase) || {};
    const events = eventsByPhase.get(phase) || [];
    const pauseControls = events.filter((event) => (
      event.eventType === 'phase_paused' || event.eventType === 'phase_resumed'
    ));
    const pauseControl = pauseControls[pauseControls.length - 1];
    const paused = pauseControl?.eventType === 'phase_paused';
    const failed = events.some((event) => TERMINAL_EVENT_TYPES.has(event.eventType));
    const entered = events.find((event) => event.eventType === 'phase_entered');
    const contract = events.find((event) => event.eventType === 'phase_contract_written');
    const terminal = events.find((event) => TERMINAL_EVENT_TYPES.has(event.eventType));
    const resumed = events.find((event) => event.eventType === 'phase_resumed');
    const status = paused
      ? 'blocked'
      : failed
        ? 'failed'
        : contract
          ? 'completed'
          : entered || resumed
            ? 'running'
            : 'unknown';
    const sourceEvent = contract || terminal || pauseControl || entered || resumed;
    const role = text(source.worker_role ?? source.workerRole);
    const owner = normalizeIncidentOwner({
      role,
      label: paused && phase === 'approved' ? 'Human' : role ? undefined : 'Manager',
    });
    return {
      stage: phase,
      status,
      ownerLabel: owner?.label,
      startedAt: entered?.createdAt || text(source.started_at ?? source.startedAt) || undefined,
      durationMs: milliseconds(
        text(source.started_at ?? source.startedAt),
        text(source.ended_at ?? source.endedAt),
      ),
      outcome: text(source.contract_summary ?? source.contractSummary) || undefined,
      blockingReason: paused
        ? (phase === 'approved' ? '等待精准人工审批' : '阶段暂停')
        : failed
          ? (text(source.contract_summary ?? source.contractSummary) || '阶段失败')
          : undefined,
      evidenceRefs: evidenceRefs(source),
      sourceEventId: sourceEvent?.id || undefined,
      sourceTaskId: sourceEvent?.taskId || undefined,
      audit: auditKinds(source),
      workerRole: role,
    };
  });
}

function ownerFor(stage, record) {
  if (stage === 'approved' && record.status === 'blocked') {
    return normalizeIncidentOwner({ kind: 'human', label: 'Human' });
  }
  return normalizeIncidentOwner({
    role: record.workerRole,
    label: record.workerRole ? undefined : 'Manager',
  });
}

function recoveredSubstate(record) {
  if (record.audit.has('verification')) return 'verifying';
  if (record.audit.has('execution')) return 'executing';
  return undefined;
}

function approvedSubstate(record) {
  if (record.status === 'blocked') return 'awaiting_human';
  if (record.status === 'completed') return 'approved';
  return undefined;
}

function currentStage(state, timeline, stages) {
  const statePhase = text(state.current_phase ?? state.currentPhase);
  const chainPhase = text(timeline.chain.current_phase ?? timeline.chain.currentPhase);
  const selected = [statePhase, chainPhase].find(isCommandPhase);
  const record = selected
    ? stages.find((item) => item.stage === selected)
    : undefined;
  const terminalOutsideCommandPhase = timeline.events.find((event) => (
    TERMINAL_EVENT_TYPES.has(event.eventType) && !isCommandPhase(event.phase)
  ));
  const stateStatus = text(state.status ?? state.state_status ?? state.loop_status).toLowerCase();
  const authoritativeFailure = Boolean(terminalOutsideCommandPhase) || FAILURE_STATES.has(stateStatus);
  if (!selected || !record || record.status === 'unknown') {
    if (authoritativeFailure) {
      return {
        stage: undefined,
        stageStatus: 'failed',
        stageSubstate: undefined,
        owner: normalizeIncidentOwner({ kind: 'system', label: 'System failure' }),
        sourceEventId: terminalOutsideCommandPhase?.id || undefined,
        sourceTaskId: terminalOutsideCommandPhase?.taskId || undefined,
      };
    }
    return {
      stage: undefined,
      stageStatus: 'unknown',
      stageSubstate: undefined,
      owner: undefined,
      sourceEventId: undefined,
      sourceTaskId: undefined,
    };
  }
  return {
    stage: selected,
    stageStatus: record.status,
    stageSubstate: selected === 'approved'
      ? approvedSubstate(record)
      : selected === 'recovered'
        ? recoveredSubstate(record)
        : undefined,
    owner: ownerFor(selected, record),
    sourceEventId: record.sourceEventId,
    sourceTaskId: record.sourceTaskId,
  };
}

function candidateFor(command) {
  if (command.stageStatus === 'failed' || command.stageStatus === 'unknown') {
    return {
      kind: command.stageStatus === 'failed' ? 'retry' : 'inspect-evidence',
      priority: 10,
      label: command.stageStatus === 'failed' ? '重试或检查失败原因' : '检查权威状态',
      detail: command.stageStatus === 'unknown' ? '权威阶段数据缺失或无法映射' : undefined,
    };
  }
  if (command.stage === 'approved' && command.stageSubstate === 'awaiting_human') {
    return {
      kind: 'approve',
      priority: 20,
      label: '提供精准人工审批',
      detail: '审批必须包含 incident 与 candidate 精确上下文',
    };
  }
  if (command.stage === 'recovered' && command.stageSubstate === 'verifying') {
    return { kind: 'verify', priority: 30, label: '等待或检查独立验证' };
  }
  if ((command.stage === 'recovered' || command.stage === 'postmortem') && command.stageStatus === 'completed') {
    return { kind: 'archive', priority: 40, label: '归档并回放事故' };
  }
  return { kind: 'wait', priority: 60, label: '等待权威流程推进' };
}

export function selectNextAction(input = {}) {
  const candidates = Array.isArray(input.candidates)
    ? input.candidates.map(object).filter((item) => text(item.kind))
    : [candidateFor(input)];
    return [...candidates].sort((left, right) => Number(left.priority ?? 99) - Number(right.priority ?? 99))[0];
}

function incidentId(input, state, timeline) {
  return identity(input.incident) || identity(state) || timeline.incidentId || text(input.incidentId);
}

function incidentImpact(incident) {
  const source = object(incident);
  const severity = text(source.severity).toLowerCase();
  const level = ['critical', 'blocker'].includes(severity)
    ? 'severe'
    : ['warning', 'degraded'].includes(severity)
      ? 'degraded'
      : 'unknown';
  const target = text(source.target ?? source.name ?? source.summary);
  return {
    level,
    affectedScopes: target ? [{ name: target, state: 'unknown' }] : [],
    indicators: [],
  };
}

function completeness(input, stages) {
  const byStage = new Map(stages.map((stage) => [stage.stage, stage]));
  const incident = object(input.incident);
  const archive = object(input.archive);
  const preview = object(input.preview);
  const cause = byStage.get('investigated');
  const approved = byStage.get('approved');
  const recovered = byStage.get('recovered');
  const legacyPreview = archive.legacy_preview_not_applicable === true
    || (archive.closed === true && Array.isArray(archive.repair_previews) && archive.repair_previews.length === 0);
  const previewObserved = Boolean(preview.run_id || preview.runId);
  return resolveEvidenceCompleteness({
    incident: {
      observed: Boolean(identity(incident) || identity(input.state) || identity(input.timeline)),
      complete: true,
    },
    cause: {
      observed: Boolean(cause && cause.status !== 'unknown'),
      complete: cause?.status === 'completed' && Boolean(cause.outcome || cause.evidenceRefs.length),
    },
    preview: {
      observed: previewObserved,
      complete: previewObserved && Boolean(preview.passing || preview.passing_candidate),
      legacyNotApplicable: legacyPreview,
    },
    authorization: {
      observed: Boolean(approved && approved.status !== 'unknown'),
      complete: Boolean(approved?.audit?.has('approval')),
    },
    execution: {
      observed: Boolean(recovered && recovered.status !== 'unknown'),
      complete: Boolean(recovered?.audit?.has('execution')),
    },
    verification: {
      observed: Boolean(recovered && recovered.status !== 'unknown'),
      complete: Boolean(
        recovered?.audit?.has('verification')
          && (recovered.status === 'completed' || stages.find((stage) => stage.stage === 'postmortem')?.status === 'completed'),
      ),
    },
  });
}

export function fromManagerLoop(input = {}) {
  const source = object(input);
  const state = object(source.state);
  const timeline = normalizeTimeline(source.timeline);
  const stages = projectStages(timeline);
  const command = currentStage(state, timeline, stages);
  const observedAt = text(state.updated_at ?? state.updatedAt) || undefined;
  const earliest = timeline.events
    .map((event) => date(event.createdAt))
    .filter(Boolean)
    .sort((left, right) => left - right)[0];
  const now = date(source.serverNow);
  const latest = timeline.events
    .map((event) => date(event.createdAt))
    .filter(Boolean)
    .sort((left, right) => right - left)[0];
  const view = {
    incidentId: incidentId(source, state, timeline),
    scenario: text(object(source.incident).scenario ?? object(source.incident).scenario_id),
    ...command,
    freshness: resolveFreshness(observedAt, source.serverNow),
    observedAt,
    serverNow: text(source.serverNow) || undefined,
    elapsedMs: earliest && (now || latest) ? Math.max(0, (now || latest) - earliest) : undefined,
    businessImpact: incidentImpact(source.incident),
    nextAction: selectNextAction({ ...command, preview: source.preview }),
    stageTimeline: stages.map(({ audit, workerRole, ...stage }) => stage),
    evidenceCompleteness: completeness(source, stages),
  };
  return view;
}
