import * as React from 'react';
import { fromManagerLoop } from '@opskeeper/incident-command';
import { normalizeIncidentList } from '../runtime.js';
import { opskeeperApi } from '../api.js';
import CommandBar from './CommandBar.jsx';
import NextActionCard from './NextActionCard.jsx';

const DEFAULT_POLL_INTERVAL_MS = 5000;

function errorFrom(result) {
  return result.status === 'rejected' ? (result.reason || new Error('读取失败')) : null;
}

export async function fetchIncidentCommand(api, incidentId, serverNow, incident) {
  const [stateResult, timelineResult] = await Promise.allSettled([
    api.getIncidentLoopState(incidentId),
    api.getIncidentLoopTimeline(incidentId),
  ]);
  const state = stateResult.status === 'fulfilled' ? stateResult.value : null;
  const timeline = timelineResult.status === 'fulfilled' ? timelineResult.value : null;
  const authoritativeNow = serverNow || state?.server_now;

  return {
    state: stateResult.status === 'fulfilled' ? state : null,
    timeline: timelineResult.status === 'fulfilled' ? timeline : null,
    stateError: errorFrom(stateResult),
    timelineError: errorFrom(timelineResult),
    view: fromManagerLoop({
      incidentId,
      incident,
      state,
      timeline,
      serverNow: authoritativeNow,
    }),
  };
}

export function startIncidentCommandPolling({
  api,
  incidentId,
  incident,
  intervalMs = DEFAULT_POLL_INTERVAL_MS,
  onData,
}) {
  let active = true;
  let timer;

  const run = async () => {
    const result = await fetchIncidentCommand(api, incidentId, undefined, incident);
    if (!active) return;
    onData?.(result);
    timer = setTimeout(run, intervalMs);
  };
  run();

  return () => {
    active = false;
    clearTimeout(timer);
  };
}

export function selectIncidentCommandIncident(event, {
  setSelectedIncidentId,
  setCommand,
}) {
  const incidentId = event?.target?.value ?? '';
  setSelectedIncidentId(incidentId);
  setCommand(null);
}

export default function IncidentCommandRoute({
  api = opskeeperApi,
  initialIncidentId = '',
  onOpenDiagnostics,
}) {
  const [incidents, setIncidents] = React.useState([]);
  const [listError, setListError] = React.useState(null);
  const [selectedIncidentId, setSelectedIncidentId] = React.useState(initialIncidentId);
  const [command, setCommand] = React.useState(null);
  const selectedIncident = incidents.find((incident) => incident.id === selectedIncidentId);

  React.useEffect(() => {
    let cancelled = false;
    api.listIncidents({ limit: 50 })
      .then((response) => {
        if (cancelled) return;
        const list = normalizeIncidentList(response);
        setIncidents(list);
        setSelectedIncidentId((current) => current || list[0]?.id || '');
      })
      .catch((error) => {
        if (!cancelled) setListError(error?.message || '事故列表读取失败');
      });
    return () => {
      cancelled = true;
    };
  }, [api]);

  React.useEffect(() => {
    if (!selectedIncidentId) return undefined;
    return startIncidentCommandPolling({
      api,
      incidentId: selectedIncidentId,
      incident: selectedIncident,
      onData: setCommand,
    });
  }, [api, selectedIncident, selectedIncidentId]);

  const view = command?.view || fromManagerLoop({ incidentId: selectedIncidentId });
  const stateError = command?.stateError?.message;
  const timelineError = command?.timelineError?.message;

  return (
    <section aria-label="事故指挥" style={{ display: 'grid', gap: 12, padding: '14px 24px 0' }}>
      <div style={{ display: 'grid', gap: 8 }}>
        <label style={{ fontSize: 12, fontWeight: 600 }} htmlFor="opskeeper-incident-command-id">
          当前事故
        </label>
        <select
          id="opskeeper-incident-command-id"
          value={selectedIncidentId}
          onChange={(event) => selectIncidentCommandIncident(event, {
            setSelectedIncidentId,
            setCommand,
          })}
          style={{
            border: '1px solid var(--ops-surface-border)',
            background: 'var(--ops-surface)',
            color: 'var(--ops-surface-foreground)',
            borderRadius: 6,
            padding: '6px 8px',
            maxWidth: 420,
          }}
        >
          {selectedIncidentId && !selectedIncident && (
            <option value={selectedIncidentId}>{selectedIncidentId}</option>
          )}
          {incidents.map((incident) => (
            <option key={incident.id} value={incident.id}>
              {incident.id}{incident.summary ? ` · ${incident.summary}` : ''}
            </option>
          ))}
        </select>
        {listError && <p role="status" style={{ margin: 0, fontSize: 12 }}>事故列表读取失败：{listError}</p>}
      </div>
      <CommandBar
        view={{ ...view, incidentId: selectedIncidentId || view.incidentId }}
        onOpenEvidence={() => onOpenDiagnostics?.('diagnostics')}
      />
      <NextActionCard action={view.nextAction} />
      {stateError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>阶段状态读取失败：{stateError}；保留可用读back。</p>
      )}
      {timelineError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>时间线读取失败：{timelineError}；保留可用读back。</p>
      )}
    </section>
  );
}
