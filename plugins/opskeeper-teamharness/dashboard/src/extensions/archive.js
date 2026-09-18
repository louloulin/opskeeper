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
