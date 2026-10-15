import { normalizeIncidentList } from './runtime.js';

export const INCIDENT_EVENT_TYPES = Object.freeze({
  alertReceived: 'alert.received',
  rootCauseConfirmed: 'root_cause.confirmed',
  evidenceRefreshed: 'evidence.refreshed',
  recommendationApproved: 'recommendation.approved',
  actionExecuted: 'action.executed',
  recoverySignalObserved: 'recovery_signal.observed',
  incidentClosed: 'incident.closed',
  incidentReopened: 'incident.reopened',
});

const COMMAND_STAGES = Object.freeze([
  'detected',
  'correlated',
  'investigated',
  'critiqued',
  'approved',
  'recovered',
  'postmortem',
]);

const ARCHIVE_EVENT_STAGE = Object.freeze(new Map([
  [INCIDENT_EVENT_TYPES.alertReceived, ['detected']],
  [INCIDENT_EVENT_TYPES.evidenceRefreshed, ['correlated']],
  [INCIDENT_EVENT_TYPES.rootCauseConfirmed, ['investigated']],
  [INCIDENT_EVENT_TYPES.recommendationApproved, ['critiqued', 'approved']],
  [INCIDENT_EVENT_TYPES.actionExecuted, ['recovered']],
  [INCIDENT_EVENT_TYPES.recoverySignalObserved, ['recovered']],
  [INCIDENT_EVENT_TYPES.incidentClosed, ['postmortem']],
]));

const LEGACY_EVENT_TYPE_ALIASES = new Map([
  ['alert_received', INCIDENT_EVENT_TYPES.alertReceived],
  ['root_cause', INCIDENT_EVENT_TYPES.rootCauseConfirmed],
  ['approved', INCIDENT_EVENT_TYPES.recommendationApproved],
  ['action', INCIDENT_EVENT_TYPES.actionExecuted],
  ['recovery', INCIDENT_EVENT_TYPES.recoverySignalObserved],
  ['closed', INCIDENT_EVENT_TYPES.incidentClosed],
  ['reopened', INCIDENT_EVENT_TYPES.incidentReopened],
]);

function normalizeWireBoolean(value) {
  return value === true;
}

export function normalizeArchiveResponse(response) {
  const archive = response?.data && typeof response.data === 'object'
    ? response.data
    : response?.incident_id
      ? response
      : null;
  if (!archive) return null;
  return {
    ...archive,
    timeline: Array.isArray(archive.timeline) ? archive.timeline : [],
    trace_ids: Array.isArray(archive.trace_ids) ? archive.trace_ids : [],
    required_event_types: Array.isArray(archive.required_event_types) ? archive.required_event_types : [],
    missing_event_types: Array.isArray(archive.missing_event_types) ? archive.missing_event_types : [],
    similar_incidents: Array.isArray(archive.similar_incidents) ? archive.similar_incidents : [],
    postmortem_refs: Array.isArray(archive.postmortem_refs) ? archive.postmortem_refs : [],
    repair_previews: normalizeRepairPreviews(archive.repair_previews),
  };
}

export function normalizeRepairPreviews(value) {
  const source = value?.data && typeof value.data === 'object'
    ? value.data.repair_previews
    : value?.repair_previews || value;
  if (!Array.isArray(source)) return [];
  return source
    .filter((run) => run && typeof run === 'object' && (run.run_id || run.id))
    .map((run) => ({
      ...run,
      id: run.run_id || run.id,
      candidates: Array.isArray(run.candidates)
        ? run.candidates.filter((candidate) => candidate && typeof candidate === 'object').map(normalizeRepairPreviewCandidate)
        : [],
    }));
}

function normalizeRepairPreviewCandidate(candidate) {
  return {
    ...candidate,
    id: candidate.id || '',
    candidate_id: candidate.candidate_id || '',
    name: candidate.name || '',
    action: candidate.action || '',
    change_summary: candidate.change_summary || '',
    consistent: normalizeWireBoolean(candidate.consistent),
    average_latency_ms: candidate.average_latency_ms ?? null,
    median_latency_ms: candidate.median_latency_ms ?? null,
    p95_latency_ms: candidate.p95_latency_ms ?? null,
    sample_count: candidate.sample_count ?? 0,
    tps: candidate.tps ?? null,
    error_count: candidate.error_count ?? 0,
    write_impact: candidate.write_impact || '',
    storage_delta_bytes: candidate.storage_delta_bytes ?? null,
    business_probe_pass: normalizeWireBoolean(candidate.business_probe_pass),
    decision: candidate.decision || 'UNKNOWN',
    rejection_reason: candidate.rejection_reason || '',
  };
}

export function normalizeRepairPreviewSummary(response) {
  const summary = response?.data && typeof response.data === 'object'
    ? response.data
    : response?.run_id || response?.incident_id
      ? response
      : null;
  if (!summary) {
    return {
      incidentId: '', runId: '', seedFingerprint: '', workloadFingerprint: '',
      controlledLoad: false, isolationBoundary: '', baseline: null, passing: null, rejected: null,
    };
  }
  return {
    incidentId: summary.incident_id || summary.incidentId || '',
    runId: summary.run_id || summary.runId || '',
    seedFingerprint: summary.seed_fingerprint || summary.seedFingerprint || '',
    workloadFingerprint: summary.workload_fingerprint || summary.workloadFingerprint || '',
    controlledLoad: normalizeWireBoolean(summary.controlled_load ?? summary.controlledLoad),
    isolationBoundary: summary.isolation_boundary || summary.isolationBoundary || '',
    targetFingerprint: summary.target_fingerprint || summary.targetFingerprint || '',
    status: summary.status || '',
    parameters: summary.parameters && typeof summary.parameters === 'object' ? summary.parameters : null,
    impactScope: summary.impact_scope || summary.impactScope || '',
    expiresAt: summary.expires_at || summary.expiresAt || summary.approval_expires_at || '',
    rollbackPlan: summary.rollback_plan || summary.rollbackPlan || '',
    verificationCriteria: summary.verification_criteria || summary.verificationCriteria || '',
    approvalInstruction: summary.approval_command || summary.approvalCommand || summary.approval_instruction || '',
    approvalChannel: summary.approval_channel || summary.approvalChannel || '',
    baseline: summary.baseline && typeof summary.baseline === 'object'
      ? normalizeRepairPreviewCandidate(summary.baseline)
      : null,
    passing: summary.passing && typeof summary.passing === 'object'
      ? normalizeRepairPreviewCandidate(summary.passing)
      : null,
    rejected: summary.rejected && typeof summary.rejected === 'object'
      ? normalizeRepairPreviewCandidate(summary.rejected)
      : null,
  };
}

function projectionObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
}

function projectionText(value) {
  if (typeof value === 'string' && value.trim()) return value.trim();
  return Number.isFinite(value) ? String(value) : '';
}

function projectionEventId(event) {
  return projectionText(projectionObject(event).id ?? projectionObject(event).event_id);
}

function eventsByType(archive) {
  const groups = new Map();
  for (const event of Array.isArray(archive.timeline) ? archive.timeline : []) {
    const source = projectionObject(event);
    const rawEventType = projectionText(source.event_type ?? source.eventType);
    const eventType = INCIDENT_EVENT_TYPES[rawEventType]
      ?? LEGACY_EVENT_TYPE_ALIASES.get(rawEventType)
      ?? rawEventType;
    if (!eventType) continue;
    if (!groups.has(eventType)) groups.set(eventType, []);
    groups.get(eventType).push(source);
  }
  return groups;
}

function eventOfType(groups, type) {
  return (groups.get(type) || [])[0] || null;
}

function fact(id, label, value, sourceIds = []) {
  return {
    id,
    label,
    value,
    sourceIds: sourceIds.filter(Boolean),
  };
}

function firstPreviewRun(archive) {
  return projectionObject((Array.isArray(archive.repair_previews) ? archive.repair_previews : [])[0]);
}

function compactSummaryCandidates(preview) {
  return [preview.baseline, preview.passing, preview.rejected]
    .filter(Boolean)
    .map(projectionObject);
}

function mergedPreviewCandidates(preview, run) {
  const candidates = [...compactSummaryCandidates(preview)];
  const existing = new Set(candidates.map((candidate) => projectionText(
    candidate.candidate_id ?? candidate.candidateId ?? candidate.id,
  )));
  for (const candidate of Array.isArray(run.candidates) ? run.candidates : []) {
    const source = projectionObject(candidate);
    const identity = projectionText(source.candidate_id ?? source.candidateId ?? source.id);
    if (identity && existing.has(identity)) continue;
    if (identity) existing.add(identity);
    candidates.push(source);
  }
  return candidates;
}

function candidateComparison(candidates) {
  return candidates.map((candidate) => {
    const id = projectionText(candidate.candidate_id ?? candidate.candidateId ?? candidate.id);
    const name = projectionText(candidate.name);
    const decision = projectionText(candidate.decision).toLowerCase();
    const rejection = projectionText(candidate.rejection_reason ?? candidate.rejectionReason);
    const latency = Number(candidate.average_latency_ms ?? candidate.averageLatencyMs);
    const identity = [
      id,
      Number.isFinite(latency) ? `${Math.round(latency)}ms` : '',
      name,
    ].filter(Boolean).join(' ');
    const outcome = decision === 'pass'
      ? '通过'
      : decision
        ? `${decision}${rejection ? `：${rejection}` : ''}`
        : '结果未知';
    return identity ? `${identity} → ${outcome}` : '';
  }).filter(Boolean).join('；');
}

function completenessFor(facts, requiredFactIds = [], fallback = 'missing') {
  const required = requiredFactIds.length
    ? facts.filter((item) => requiredFactIds.includes(item.id))
    : facts;
  if (!required.length) return fallback;
  const populated = (item) => item.value !== '' && item.value !== false && item.value != null;
  if (required.every(populated)) return 'complete';
  return required.some(populated) ? 'partial' : fallback;
}

function projectionDate(value) {
  if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value;
  if (typeof value !== 'string' && typeof value !== 'number') return null;
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? null : parsed;
}

function sortedArchiveEvents(archive) {
  const events = (Array.isArray(archive.timeline) ? archive.timeline : [])
    .filter((event) => event && typeof event === 'object')
    .map(normalizeArchiveReplayEvent);
  return events.sort((left, right) => {
    const leftTime = projectionDate(left.occurredAt)?.getTime();
    const rightTime = projectionDate(right.occurredAt)?.getTime();
    if (leftTime == null && rightTime == null) return 0;
    if (leftTime == null) return 1;
    if (rightTime == null) return -1;
    return leftTime - rightTime || left.sourceEventId.localeCompare(right.sourceEventId);
  });
}

function normalizeArchiveReplayEvent(event) {
  const source = projectionObject(event);
  const rawType = projectionText(source.event_type ?? source.eventType);
  const eventType = INCIDENT_EVENT_TYPES[rawType]
    ?? LEGACY_EVENT_TYPE_ALIASES.get(rawType)
    ?? rawType;
  const evidenceRef = projectionText(source.evidence_ref ?? source.evidenceRef);
  const traceId = projectionText(source.trace_id ?? source.traceId);
  const id = projectionEventId(source);
  return {
    id,
    incidentId: projectionText(source.incident_id ?? source.incidentId),
    occurredAt: projectionText(source.occurred_at ?? source.occurredAt),
    phase: projectionText(source.phase),
    eventType,
    actorType: projectionText(source.actor_type ?? source.actorType),
    actor: projectionText(source.actor),
    status: projectionText(source.status),
    actionFingerprint: projectionText(source.action_fingerprint ?? source.actionFingerprint),
    evidenceRef,
    evidenceRefs: [evidenceRef, traceId].filter(Boolean),
    traceId,
    recoverySignal: normalizeWireBoolean(source.recovery_signal ?? source.recoverySignal),
    sourceEventId: id,
    sourceUrl: projectionText(source.source_url ?? source.sourceUrl),
  };
}

function archiveReplayStages(events) {
  const eventsByStage = new Map(COMMAND_STAGES.map((stage) => [stage, []]));
  for (const event of events) {
    for (const stage of ARCHIVE_EVENT_STAGE.get(event.eventType) || []) {
      eventsByStage.get(stage).push(event);
    }
  }

  return COMMAND_STAGES.map((stage, index) => {
    const candidates = eventsByStage.get(stage);
    const source = stage === 'recovered'
      ? candidates.find((event) => event.eventType === INCIDENT_EVENT_TYPES.recoverySignalObserved)
        || candidates[0]
      : candidates[0];
    const previous = COMMAND_STAGES
      .slice(0, index)
      .map((priorStage) => eventsByStage.get(priorStage)[0])
      .filter(Boolean).pop();
    const startedAt = projectionDate(source?.occurredAt);
    const previousAt = projectionDate(previous?.occurredAt);
    return {
      stage,
      status: source
        ? stage === 'recovered' && source.eventType === INCIDENT_EVENT_TYPES.actionExecuted
          ? 'running'
          : 'completed'
        : 'unknown',
      ownerLabel: source?.actor || undefined,
      workerRole: source?.actor || undefined,
      startedAt: source?.occurredAt,
      durationMs: startedAt && previousAt ? Math.max(0, startedAt - previousAt) : undefined,
      outcome: [source?.status, source?.evidenceRef].filter(Boolean).join(' · ') || undefined,
      evidenceRefs: source?.evidenceRefs || [],
      sourceEventId: source?.sourceEventId,
      sourceUrl: source?.sourceUrl,
    };
  });
}

function normalizedReplayCandidate(candidate, run) {
  const source = projectionObject(candidate);
  const id = projectionText(source.candidate_id ?? source.candidateId ?? source.id);
  if (!id) return null;
  const decision = projectionText(source.decision).toUpperCase();
  return {
    id,
    name: projectionText(source.name),
    decision: decision || 'UNKNOWN',
    latencyMs: Number.isFinite(Number(source.average_latency_ms ?? source.averageLatencyMs))
      ? Number(source.average_latency_ms ?? source.averageLatencyMs)
      : null,
    businessProbePass: normalizeWireBoolean(source.business_probe_pass ?? source.businessProbePass),
    reason: projectionText(source.rejection_reason ?? source.rejectionReason),
    sourceEventIds: [
      projectionText(run?.run_id ?? run?.id),
      projectionText(source.source_event_id ?? source.sourceEventId),
    ].filter(Boolean),
  };
}

function replayCandidateComparison(archive, events) {
  const run = firstPreviewRun(archive);
  const previewPresent = Boolean(projectionText(run.run_id ?? run.id));
  const candidates = (Array.isArray(run.candidates) ? run.candidates : [])
    .map((candidate) => normalizedReplayCandidate(candidate, run))
    .filter(Boolean);
  const explicitSelection = projectionText(
    run.selected_candidate_id
    ?? run.selectedCandidateId
    ?? archive.selected_candidate_id
    ?? archive.selectedCandidateId,
  );
  const selected = explicitSelection
    ? candidates.find((candidate) => candidate.id === explicitSelection) || null
    : null;
  const rejected = candidates.filter((candidate) => (
    candidate.id !== selected?.id
      && ['FAIL', 'FAILED', 'REJECTED', 'REJECTED_BY_PREVIEW'].includes(candidate.decision)
  ));
  const completeness = !previewPresent && archive.closed === true
    ? 'legacy_not_applicable'
    : selected && rejected.length
      ? 'complete'
      : previewPresent
        ? 'partial'
        : 'missing';
  return {
    selected,
    selectedCandidateId: explicitSelection,
    rejected,
    baseline: candidates.find((candidate) => candidate.id === 'baseline') || null,
    provenance: previewPresent ? 'decision_time' : '',
    runId: projectionText(run.run_id ?? run.id),
    completeness,
    sourceEventIds: [
      projectionText(run.run_id ?? run.id),
      projectionEventId(events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.recommendationApproved)),
    ].filter(Boolean),
  };
}

function replayRollback(archive, events) {
  const action = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.actionExecuted);
  const result = projectionText(
    archive.rollback_result ?? archive.rollbackResult,
  );
  const evidenceRef = projectionText(
    archive.rollback_evidence_ref
    ?? archive.rollbackEvidenceRef
    ?? archive.rollback_evidence
    ?? archive.rollbackEvidence,
  );
  const plan = projectionText(archive.rollback_plan ?? archive.rollbackPlan);
  return {
    result,
    evidenceRef,
    plan,
    provenance: 'decision_time',
    sourceEventIds: [action?.sourceEventId].filter(Boolean),
    completeness: result && plan && action ? 'complete' : result || plan || action ? 'partial' : 'missing',
  };
}

function replayVerification(archive, events) {
  const recovery = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.recoverySignalObserved);
  const result = projectionText(
    archive.verification_result ?? archive.verificationResult ?? recovery?.status,
  );
  const criteria = projectionText(
    archive.verification_criteria ?? archive.verificationCriteria,
  );
  return {
    result,
    criteria,
    provenance: 'decision_time',
    sourceEventIds: [recovery?.sourceEventId].filter(Boolean),
    completeness: result && criteria && recovery ? 'complete' : result || criteria || recovery ? 'partial' : 'missing',
  };
}

function sourceEventIds(value) {
  const source = projectionObject(value);
  const raw = source.source_event_ids ?? source.sourceEventIds ?? [source.source_event_id ?? source.sourceEventId];
  return (Array.isArray(raw) ? raw : [raw]).map(projectionText).filter(Boolean);
}

function replayEnrichment(archive, events) {
  const closure = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.incidentClosed);
  const closureAt = projectionDate(closure?.occurredAt);
  const rawPostIncident = [
    ...(Array.isArray(archive.post_incident_evidence) ? archive.post_incident_evidence : []),
    ...(Array.isArray(archive.enrichment?.post_incident) ? archive.enrichment.post_incident : []),
  ];
  const postmortems = (Array.isArray(archive.postmortem_refs) ? archive.postmortem_refs : [])
    .map(projectionObject);
  const normalizeEnrichment = (item) => {
    const source = projectionObject(item);
    return {
      id: projectionText(source.id ?? source.incident_id ?? source.url),
      kind: projectionText(source.kind ?? source.root_cause ?? 'postmortem'),
      value: projectionText(source.value ?? source.root_cause ?? source.url),
      occurredAt: projectionText(source.created_at ?? source.createdAt ?? source.confirmed_at ?? source.confirmedAt),
      provenance: 'post_incident_enrichment',
      sourceEventIds: sourceEventIds(source),
    };
  };
  const currentKnowledge = [
    ...(Array.isArray(archive.current_knowledge_refs) ? archive.current_knowledge_refs : []),
    ...(Array.isArray(archive.enrichment?.current_knowledge_refs) ? archive.enrichment.current_knowledge_refs : []),
  ].map((item) => {
    const source = projectionObject(item);
    return {
      id: projectionText(source.id ?? source.url ?? source),
      value: projectionText(source.value ?? source.title ?? source),
      provenance: 'current_knowledge',
      sourceEventIds: sourceEventIds(source),
    };
  }).filter((item) => item.id);

  return {
    postIncident: [...rawPostIncident, ...postmortems].map(normalizeEnrichment).filter((item) => item.id),
    currentKnowledge,
    closureAt: closure?.occurredAt || '',
    provenance: 'archive enrichment is excluded from frozen decision-time evidence',
  };
}

function replaySimilarities(archive) {
  const source = Array.isArray(archive.similar_incidents) ? archive.similar_incidents : [];
  const requestedLimit = Number(archive.similarity_limit ?? archive.similarityLimit ?? 5);
  const limit = Number.isInteger(requestedLimit) && requestedLimit > 0 ? requestedLimit : 5;
  const items = source.slice(0, limit).map((item) => {
    const sourceItem = projectionObject(item);
    const id = projectionText(sourceItem.incident_id ?? sourceItem.id);
    return id ? {
      id,
      score: Number.isFinite(Number(sourceItem.similarity_score ?? sourceItem.score))
        ? Number(sourceItem.similarity_score ?? sourceItem.score)
        : null,
      closed: sourceItem.closed === true,
      generatedAt: projectionText(sourceItem.generated_at ?? sourceItem.generatedAt),
      provenance: 'post_incident_enrichment',
      sourceEventIds: sourceEventIds(sourceItem),
    } : null;
  }).filter(Boolean);
  const generated = items
    .map((item) => projectionDate(item.generatedAt))
    .filter(Boolean);
  return {
    items,
    total: source.length,
    limit,
    truncated: source.length > items.length,
    provenance: {
      kind: 'post_incident_enrichment',
      source: projectionText(archive.similarity_source ?? archive.similaritySource ?? 'archive.similar_incidents'),
      generatedAt: generated.length ? projectionText(archive.similarity_generated_at) || items.find((item) => item.generatedAt)?.generatedAt : '',
    },
    completeness: source.length ? 'complete' : 'missing',
  };
}

function replayControlledDrill(archive) {
  const source = projectionObject(archive.controlled_drill ?? archive.controlledDrill);
  const identitySource = projectionObject(
    source.identity_support
      ?? source.identitySupport
      ?? source.identities
      ?? archive.controlled_drill_identity_support
      ?? archive.controlledDrillIdentitySupport,
  );
  const identityValue = (aliases) => projectionText(
    aliases.map((key) => source[key] ?? identitySource[key]).find(Boolean),
  );
  const identities = {
    scenario: identityValue(['scenario_id', 'scenarioId', 'scenario']),
    manifest: identityValue(['manifest_id', 'manifestId', 'pool_manifest_id', 'poolManifestId']),
    target: identityValue(['target_fingerprint', 'targetFingerprint', 'target']),
    workload: identityValue(['workload_fingerprint', 'workloadFingerprint', 'workload']),
    safety: identityValue(['safety_identity', 'safetyIdentity', 'safety_fingerprint', 'safetyFingerprint', 'safety']),
  };
  const complete = Object.values(identities).every(Boolean);
  const action = projectionObject(source.action);
  const supported = source.supported === true
    && complete
    && action.type === 'read_only'
    && Boolean(projectionText(action.href));
  return {
    supported,
    identities,
    missingIdentities: Object.entries(identities).filter(([, value]) => !value).map(([key]) => key),
    action: supported && action.type === 'read_only'
      ? {
        type: 'read_only',
        href: projectionText(action.href),
      }
      : undefined,
  };
}

function combinedReplayCompleteness(sections, archive) {
  const values = sections.map((section) => section.completeness);
  if (values.includes('missing') && !values.some((value) => value === 'complete' || value === 'partial')) return 'missing';
  if (values.includes('partial') || archive.evidence_complete !== true) return 'partial';
  return values.every((value) => value === 'complete' || value === 'legacy_not_applicable') ? 'complete' : 'partial';
}

export function projectArchiveReplay(input = {}) {
  const archive = normalizeArchiveResponse(input) || { timeline: [] };
  const events = sortedArchiveEvents(archive);
  const candidateComparison = replayCandidateComparison(archive, events);
  const rollback = replayRollback(archive, events);
  const verification = replayVerification(archive, events);
  const enrichment = replayEnrichment(archive, events);
  const similarities = replaySimilarities(archive);
  const controlledDrill = replayControlledDrill(archive);
  const closureAt = projectionDate(
    archive.closed_at ?? archive.closedAt ?? enrichment.closureAt,
  );
  const decisionEvidence = events
    .filter((event) => (!closureAt || projectionDate(event.occurredAt) <= closureAt))
    .map((event) => ({
      id: event.sourceEventId,
      kind: event.eventType,
      value: event.evidenceRef || event.status,
      occurredAt: event.occurredAt,
      provenance: 'decision_time',
      sourceEventIds: [event.sourceEventId],
    }));
  const timelineCompleteness = events.length && events.every((event) => event.sourceEventId)
    ? 'complete'
    : events.length
      ? 'partial'
      : 'missing';
  const completeness = combinedReplayCompleteness(
    [{ completeness: timelineCompleteness }, candidateComparison, rollback, verification],
    archive,
  );
  const alert = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.alertReceived);
  const cause = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.rootCauseConfirmed);
  const action = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.actionExecuted);
  const recovery = events.find((event) => event.eventType === INCIDENT_EVENT_TYPES.recoverySignalObserved);

  return {
    closure: {
      incidentId: projectionText(archive.incident_id ?? archive.incidentId),
      closed: archive.closed === true,
      closedAt: enrichment.closureAt,
      evidenceComplete: archive.evidence_complete === true,
      recoveryObserved: archive.recovery_observed === true || Boolean(recovery),
      localizationSeconds: Number.isFinite(Number(archive.localization_seconds)) ? Number(archive.localization_seconds) : null,
      recoverySeconds: Number.isFinite(Number(archive.recovery_seconds)) ? Number(archive.recovery_seconds) : null,
      firstEventAt: projectionText(archive.first_event_at ?? events[0]?.occurredAt),
      lastEventAt: projectionText(archive.last_event_at ?? events[events.length - 1]?.occurredAt),
      sourceEventIds: [alert?.sourceEventId, cause?.sourceEventId, recovery?.sourceEventId].filter(Boolean),
    },
    timeline: events,
    stageTimeline: archiveReplayStages(events),
    decisionEvidence,
    candidateComparison,
    rollback,
    verification,
    enrichment,
    similarities,
    controlledDrill,
    completeness,
  };
}

export function projectIncidentEvidence(input = {}) {
  const source = projectionObject(input);
  const archive = normalizeArchiveResponse(source.archive) || {
    incident_id: projectionText(source.incidentId),
    timeline: [],
    repair_previews: [],
    postmortem_refs: [],
  };
  const previewSummary = normalizeRepairPreviewSummary(source.preview);
  const previewRun = firstPreviewRun(archive);
  const groups = eventsByType(archive);
  const alert = eventOfType(groups, INCIDENT_EVENT_TYPES.alertReceived);
  const evidenceRefreshed = eventOfType(groups, INCIDENT_EVENT_TYPES.evidenceRefreshed);
  const cause = eventOfType(groups, INCIDENT_EVENT_TYPES.rootCauseConfirmed);
  const postmortem = projectionObject((Array.isArray(archive.postmortem_refs) ? archive.postmortem_refs : [])[0]);
  const approval = eventOfType(groups, INCIDENT_EVENT_TYPES.recommendationApproved);
  const action = eventOfType(groups, INCIDENT_EVENT_TYPES.actionExecuted);
  const recovery = eventOfType(groups, INCIDENT_EVENT_TYPES.recoverySignalObserved);
  const previewCandidate = previewSummary.passing || previewRun.candidates?.find?.((candidate) => (
    candidate.candidate_id !== 'baseline' && candidate.decision === 'PASS'
  ));
  const targetFingerprint = projectionText(
    previewSummary.targetFingerprint || previewRun.target_fingerprint || previewRun.targetFingerprint,
  );
  const workloadFingerprint = projectionText(
    previewSummary.workloadFingerprint || previewRun.workload_fingerprint || previewRun.workloadFingerprint,
  );
  const isolationBoundary = projectionText(
    previewSummary.isolationBoundary || previewRun.isolation_boundary || previewRun.isolationBoundary,
  );
  const controlledLoad = normalizeWireBoolean(
    previewSummary.controlledLoad ?? previewRun.controlled_load ?? previewRun.controlledLoad,
  );
  const previewPresent = Boolean(previewSummary.runId || previewRun.id);
  const candidates = mergedPreviewCandidates(previewSummary, previewRun);
  const previewReady = Boolean((previewSummary.runId || previewRun.id) && previewCandidate);
  const provenanceSourceIds = [
    previewSummary.runId || projectionText(previewRun.id),
    ...candidates.map((candidate) => projectionText(candidate.id ?? candidate.candidate_id ?? candidate.candidateId)).filter(Boolean),
  ];
  const expiry = projectionText(previewSummary.expiresAt || archive.approval_expires_at || archive.approvalExpiresAt);
  const legacyPreview = archive.legacy_preview_not_applicable === true
    || (archive.closed === true
      && !(Array.isArray(archive.repair_previews) ? archive.repair_previews : []).length
      && !previewReady);

  const incidentFacts = [
    fact('incidentId', '事故标识', projectionText(archive.incident_id ?? archive.incidentId), [archive.incident_id]),
    fact('alert', '告警快照', projectionText(alert?.evidence_ref ?? alert?.evidenceRef), [projectionEventId(alert)]),
    fact('scope', '受影响范围', projectionText(previewSummary.impactScope || archive.impact_scope || archive.impactScope), [projectionEventId(alert)]),
  ];
  const causeFacts = [
    fact('rootCause', '根因', projectionText(postmortem.root_cause || cause?.evidence_ref || cause?.evidenceRef), [
      projectionText(postmortem.id), projectionEventId(cause),
    ]),
    fact('corroboration', '佐证链路', [
      projectionText(evidenceRefreshed?.evidence_ref ?? evidenceRefreshed?.evidenceRef),
      ...(Array.isArray(archive.trace_ids) ? archive.trace_ids : []),
    ].filter(Boolean).join('，'), [projectionEventId(evidenceRefreshed)]),
  ];
  const repairFacts = [
    fact('comparison', 'A/B 候选对比', candidateComparison(candidates), provenanceSourceIds),
    fact('boundary', '受控工作负载边界', previewPresent ? [
      controlledLoad ? '受控负载：是' : '受控负载：未知',
      isolationBoundary ? `隔离边界：${isolationBoundary}` : '',
    ].filter(Boolean).join('；') : '', [projectionText(previewRun.id)]),
    fact('identity', '目标与负载身份', [
      targetFingerprint ? `目标：${targetFingerprint}` : '',
      workloadFingerprint ? `负载：${workloadFingerprint}` : '',
    ].filter(Boolean).join('；'), [projectionText(previewRun.id)]),
    fact('provenance', '预览来源', [
      previewSummary.runId ? `运行：${previewSummary.runId}` : projectionText(previewRun.id),
      previewSummary.seedFingerprint ? `种子：${previewSummary.seedFingerprint}` : '',
    ].filter(Boolean).join('；'), provenanceSourceIds),
    fact('eligibility', '预览资格', previewReady ? '预览已完成' : ''),
  ];
  const safetyFacts = [
    fact('targetFingerprint', '目标指纹', targetFingerprint, [projectionText(previewRun.id)]),
    fact('workloadFingerprint', '负载指纹', workloadFingerprint, [projectionText(previewRun.id)]),
    fact('rollbackPlan', '回滚计划', projectionText(previewSummary.rollbackPlan || archive.rollback_plan || archive.rollbackPlan), [projectionEventId(action)]),
    fact('expiry', '审批有效期', expiry, [previewSummary.runId || projectionText(previewRun.id)]),
    fact('approval', '审批记录', projectionText(approval?.status || approval?.actor), [projectionEventId(approval)]),
    fact('executionIdentity', '执行身份', projectionText(action?.actor || action?.action_fingerprint || action?.actionFingerprint), [projectionEventId(action)]),
    fact('auditTrail', '审计轨迹', (Array.isArray(archive.timeline) ? archive.timeline : []).map(projectionEventId).filter(Boolean).join('，'), []),
  ];
  const verificationFacts = [
    fact('result', '独立验证结果', projectionText(recovery?.status), [projectionEventId(recovery)]),
    fact('criteria', '验证标准', projectionText(previewSummary.verificationCriteria || archive.verification_criteria || archive.verificationCriteria), [projectionEventId(recovery)]),
    fact('metrics', '变更后指标', projectionText(recovery?.evidence_ref ?? recovery?.evidenceRef), [projectionEventId(recovery)]),
  ];

  return [
    {
      id: 'incident',
      title: '为什么是这起事故',
      completeness: completenessFor(incidentFacts, ['incidentId', 'alert', 'scope']),
      facts: incidentFacts,
      rawPayload: { archive: archive.incident_id, alert },
    },
    {
      id: 'cause',
      title: '为什么是这个根因',
      completeness: completenessFor(causeFacts, ['rootCause', 'corroboration']),
      facts: causeFacts,
      rawPayload: { postmortem, cause },
    },
    {
      id: 'repair',
      title: '为什么选择这个修复',
      completeness: legacyPreview
        ? 'legacy_not_applicable'
        : completenessFor(repairFacts, ['comparison', 'boundary', 'identity', 'provenance', 'eligibility']),
      facts: repairFacts,
      rawPayload: previewRun,
    },
    {
      id: 'safety',
      title: '为什么它是安全的',
      completeness: completenessFor(safetyFacts, [
        'targetFingerprint', 'workloadFingerprint', 'expiry', 'rollbackPlan', 'approval', 'executionIdentity', 'auditTrail',
      ]),
      facts: safetyFacts,
      rawPayload: { approval, action },
    },
    {
      id: 'verification',
      title: '为什么确认有效',
      completeness: completenessFor(verificationFacts, ['result', 'criteria', 'metrics']),
      facts: verificationFacts,
      rawPayload: recovery,
    },
  ];
}

export function projectApprovalFacts(input = {}) {
  const source = projectionObject(input);
  const archive = normalizeArchiveResponse(source.archive) || { timeline: [] };
  const preview = normalizeRepairPreviewSummary(source.preview);
  const previewRun = firstPreviewRun(archive);
  const groups = eventsByType(archive);
  const approval = eventOfType(groups, INCIDENT_EVENT_TYPES.recommendationApproved);
  const action = eventOfType(groups, INCIDENT_EVENT_TYPES.actionExecuted);
  const recovery = eventOfType(groups, INCIDENT_EVENT_TYPES.recoverySignalObserved);
  const candidate = projectionObject(preview.passing || previewRun.candidates?.find?.((item) => (
    projectionObject(item).candidate_id !== 'baseline' && projectionObject(item).decision === 'PASS'
  )));
  const incidentId = projectionText(archive.incident_id ?? archive.incidentId ?? preview.incidentId);
  const candidateId = projectionText(candidate.candidate_id ?? candidate.candidateId ?? candidate.id);
  const instructionSource = projectionText(preview.approvalInstruction || archive.approval_command || archive.approvalCommand);
  const targetFingerprint = projectionText(preview.targetFingerprint || previewRun.target_fingerprint || previewRun.targetFingerprint);
  const rollbackPlan = projectionText(preview.rollbackPlan || archive.rollback_plan || archive.rollbackPlan);
  const verificationCriteria = projectionText(preview.verificationCriteria || archive.verification_criteria || archive.verificationCriteria);
  const previewReady = Boolean((preview.runId || previewRun.id) && candidateId);
  const approvalStatus = projectionText(approval?.status || source.approvalStatus);
  return {
    incidentId,
    candidateId,
    executionId: projectionEventId(action),
    targetFingerprint,
    workloadFingerprint: projectionText(preview.workloadFingerprint || previewRun.workload_fingerprint || previewRun.workloadFingerprint),
    impactScope: projectionText(preview.impactScope || archive.impact_scope || archive.impactScope),
    parameters: preview.parameters || previewRun.parameters || null,
    expiresAt: projectionText(preview.expiresAt || archive.approval_expires_at || archive.approvalExpiresAt),
    serverNow: projectionText(source.serverNow),
    rollbackPlan,
    previewEligibility: previewReady ? '预览已完成' : '预览结果缺失',
    verificationCriteria,
    approvalStatus,
    instruction: instructionSource,
    channel: projectionText(preview.approvalChannel || archive.approval_channel || archive.approvalChannel),
    sourceIds: [projectionEventId(approval), projectionEventId(action), projectionEventId(recovery)].filter(Boolean),
  };
}

export function normalizeArchiveIncidentList(response) {
  const incidents = Array.isArray(response?.items)
    ? response.items
    : normalizeIncidentList(response);
  return incidents
    .map((incident) => ({
      id: String(incident.incident_id ?? incident.id ?? '').trim(),
      summary: incident.summary || incident.title || incident.rule_key || incident.incident_id || '',
      status: incident.closed ? 'closed' : (incident.status || 'open'),
      evidenceComplete: normalizeWireBoolean(incident.evidence_complete),
      eventCount: Number(incident.event_count ?? 0),
    }))
    .filter((incident) => incident.id);
}


export function normalizeIncidentSummary(response) {
  const incident = response?.data && typeof response.data === 'object'
    ? response.data
    : response?.id || response?.incident_id
      ? response
      : null;
  if (!incident) return null;
  const labels = incident.labels && typeof incident.labels === 'object' ? incident.labels : {};
  return {
    id: String(incident.id ?? incident.incident_id ?? '').trim(),
    ruleKey: incident.rule_key || labels.rule || '',
    ruleName: incident.rule_name || '',
    severity: incident.severity || '',
    status: incident.status || 'open',
    summary: incident.summary || incident.title || incident.rule_key || '',
    eventCount: Number(incident.event_count ?? 0),
    targetType: incident.target_type || '',
    dedupeKey: incident.dedupe_key || '',
    labels,
    firedAt: incident.fired_at || incident.last_fired_at || null,
    resolvedAt: incident.resolved_at || null,
    updatedAt: incident.updated_at || null,
    value: incident.value ?? null,
  };
}
