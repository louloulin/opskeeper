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
