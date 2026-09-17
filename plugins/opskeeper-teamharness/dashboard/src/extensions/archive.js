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
  return normalizeIncidentList(response)
    .map((incident) => ({
      id: String(incident.id ?? incident.incident_id ?? '').trim(),
      summary: incident.summary || incident.title || incident.rule_key || incident.alertname || '',
      status: incident.status || '',
    }))
    .filter((incident) => incident.id);
}
