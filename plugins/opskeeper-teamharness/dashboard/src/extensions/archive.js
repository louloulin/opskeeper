import { normalizeIncidentList } from './runtime.js';

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
    consistent: Boolean(candidate.consistent),
    average_latency_ms: candidate.average_latency_ms ?? null,
    median_latency_ms: candidate.median_latency_ms ?? null,
    p95_latency_ms: candidate.p95_latency_ms ?? null,
    sample_count: candidate.sample_count ?? 0,
    tps: candidate.tps ?? null,
    error_count: candidate.error_count ?? 0,
    write_impact: candidate.write_impact || '',
    storage_delta_bytes: candidate.storage_delta_bytes ?? null,
    business_probe_pass: Boolean(candidate.business_probe_pass),
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
    controlledLoad: Boolean(summary.controlled_load ?? summary.controlledLoad),
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
    const eventType = projectionText(source.event_type ?? source.eventType);
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

function candidateText(run) {
  return (Array.isArray(run.candidates) ? run.candidates : [])
    .map(projectionObject)
    .map((candidate) => {
      const id = projectionText(candidate.candidate_id ?? candidate.candidateId ?? candidate.id);
      const name = projectionText(candidate.name);
      const decision = projectionText(candidate.decision).toLowerCase();
      const rejection = projectionText(candidate.rejection_reason ?? candidate.rejectionReason);
      const identity = [id, name].filter(Boolean).join(' · ');
      const outcome = decision === 'pass'
        ? '通过'
        : decision
          ? `${decision}${rejection ? `：${rejection}` : ''}`
          : '结果未知';
      return identity ? `${identity} → ${outcome}` : '';
    })
    .filter(Boolean)
    .join('；');
}

function completenessFor(facts, fallback = 'missing') {
  return facts.some((item) => item.value !== '' && item.value !== false && item.value != null) ? 'complete' : fallback;
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
  const alert = eventOfType(groups, 'alert_received');
  const cause = eventOfType(groups, 'root_cause');
  const postmortem = projectionObject((Array.isArray(archive.postmortem_refs) ? archive.postmortem_refs : [])[0]);
  const approval = eventOfType(groups, 'approved');
  const action = eventOfType(groups, 'action');
  const recovery = eventOfType(groups, 'recovery');
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
  const controlledLoad = Boolean(previewSummary.controlledLoad ?? previewRun.controlled_load ?? previewRun.controlledLoad);
  const previewReady = Boolean((previewSummary.runId || previewRun.id) && previewCandidate);
  const legacyPreview = archive.legacy_preview_not_applicable === true
    || (archive.closed === true && !(Array.isArray(archive.repair_previews) ? archive.repair_previews : []).length);

  const incidentFacts = [
    fact('incidentId', '事故标识', projectionText(archive.incident_id ?? archive.incidentId), [archive.incident_id]),
    fact('alert', '告警快照', projectionText(alert?.evidence_ref ?? alert?.evidenceRef), [projectionEventId(alert)]),
    fact('scope', '受影响范围', projectionText(previewSummary.impactScope || archive.impact_scope || archive.impactScope), [projectionEventId(alert)]),
  ];
  const causeFacts = [
    fact('rootCause', '根因', projectionText(postmortem.root_cause || cause?.evidence_ref || cause?.evidenceRef), [
      projectionText(postmortem.id), projectionEventId(cause),
    ]),
    fact('corroboration', '佐证链路', (Array.isArray(archive.trace_ids) ? archive.trace_ids : []).join('，'), []),
  ];
  const repairFacts = [
    fact('comparison', 'A/B 候选对比', candidateText(previewRun), [projectionText(previewRun.id)]),
    fact('boundary', '受控工作负载边界', [
      controlledLoad ? '受控负载：是' : '受控负载：未知',
      isolationBoundary ? `隔离边界：${isolationBoundary}` : '',
    ].filter(Boolean).join('；'), [projectionText(previewRun.id)]),
    fact('identity', '目标与负载身份', [
      targetFingerprint ? `目标：${targetFingerprint}` : '',
      workloadFingerprint ? `负载：${workloadFingerprint}` : '',
    ].filter(Boolean).join('；'), [projectionText(previewRun.id)]),
    fact('eligibility', '预览资格', previewReady ? '预览已完成' : '预览结果缺失'),
  ];
  const safetyFacts = [
    fact('targetFingerprint', '目标指纹', targetFingerprint, [projectionText(previewRun.id)]),
    fact('workloadFingerprint', '负载指纹', workloadFingerprint, [projectionText(previewRun.id)]),
    fact('rollbackPlan', '回滚计划', projectionText(previewSummary.rollbackPlan || archive.rollback_plan || archive.rollbackPlan), [projectionEventId(action)]),
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
    { id: 'incident', title: '为什么是这起事故', completeness: completenessFor(incidentFacts), facts: incidentFacts, rawPayload: { archive: archive.incident_id, alert } },
    { id: 'cause', title: '为什么是这个根因', completeness: completenessFor(causeFacts), facts: causeFacts, rawPayload: { postmortem, cause } },
    {
      id: 'repair',
      title: '为什么选择这个修复',
      completeness: legacyPreview ? 'legacy_not_applicable' : (previewReady ? 'complete' : 'missing'),
      facts: repairFacts,
      rawPayload: previewRun,
    },
    { id: 'safety', title: '为什么它是安全的', completeness: completenessFor(safetyFacts), facts: safetyFacts, rawPayload: { approval, action } },
    { id: 'verification', title: '为什么确认有效', completeness: completenessFor(verificationFacts), facts: verificationFacts, rawPayload: recovery },
  ];
}

export function projectApprovalFacts(input = {}) {
  const source = projectionObject(input);
  const archive = normalizeArchiveResponse(source.archive) || { timeline: [] };
  const preview = normalizeRepairPreviewSummary(source.preview);
  const previewRun = firstPreviewRun(archive);
  const groups = eventsByType(archive);
  const approval = eventOfType(groups, 'approved');
  const action = eventOfType(groups, 'action');
  const recovery = eventOfType(groups, 'recovery');
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
    instruction: instructionSource || (incidentId && candidateId
      ? `审批 incident=${incidentId} candidate=${candidateId}`
      : ''),
    channel: projectionText(preview.approvalChannel || archive.approval_channel || archive.approvalChannel) || 'Manager 人工审批中心',
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
      evidenceComplete: Boolean(incident.evidence_complete),
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
