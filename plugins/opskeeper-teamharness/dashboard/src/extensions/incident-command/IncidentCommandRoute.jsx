import * as React from 'react';
import { fromManagerLoop } from '@opskeeper/incident-command';
import { normalizeIncidentList } from '../runtime.js';
import { opskeeperApi } from '../api.js';
import { projectApprovalFacts, projectIncidentEvidence } from '../archive.js';
import CommandBar from './CommandBar.jsx';
import NextActionCard from './NextActionCard.jsx';
import StageTimeline from './StageTimeline.jsx';
import EvidenceDrawer from './EvidenceDrawer.jsx';
import ApprovalChecklist from './ApprovalChecklist.jsx';

const DEFAULT_POLL_INTERVAL_MS = 5000;

function errorFrom(result) {
  return result.status === 'rejected' ? (result.reason || new Error('读取失败')) : null;
}

export async function fetchIncidentCommand(api, incidentId, serverNow, incident) {
  const [stateResult, timelineResult, archiveResult, previewResult] = await Promise.allSettled([
    api.getIncidentLoopState(incidentId),
    api.getIncidentLoopTimeline(incidentId),
    api.getIncidentArchive?.(incidentId) ?? Promise.resolve(null),
    api.getIncidentRepairPreviewSummary?.(incidentId) ?? Promise.resolve(null),
  ]);
  const state = stateResult.status === 'fulfilled' ? stateResult.value : null;
  const timeline = timelineResult.status === 'fulfilled' ? timelineResult.value : null;
  const archive = archiveResult.status === 'fulfilled' ? archiveResult.value : null;
  const preview = previewResult.status === 'fulfilled' ? previewResult.value : null;
  const authoritativeNow = serverNow || state?.server_now;
  const view = fromManagerLoop({
    incidentId,
    incident,
    state,
    timeline,
    serverNow: authoritativeNow,
  });

  return {
    state: stateResult.status === 'fulfilled' ? state : null,
    timeline: timelineResult.status === 'fulfilled' ? timeline : null,
    stateError: errorFrom(stateResult),
    timelineError: errorFrom(timelineResult),
    archiveError: errorFrom(archiveResult),
    previewError: errorFrom(previewResult),
    evidenceGroups: projectIncidentEvidence({ archive, preview }),
    approvalFacts: projectApprovalFacts({ archive, preview, serverNow: authoritativeNow, approvalStatus: view.stageSubstate }),
    view,
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
  const [evidenceOpen, setEvidenceOpen] = React.useState(false);
  const [refreshToken, setRefreshToken] = React.useState(0);
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
  }, [api, refreshToken, selectedIncident, selectedIncidentId]);

  const view = command?.view || fromManagerLoop({ incidentId: selectedIncidentId });
  const stateError = command?.stateError?.message;
  const timelineError = command?.timelineError?.message;
  const archiveError = command?.archiveError?.message;
  const previewError = command?.previewError?.message;

  return (
    <section
      aria-label="事故指挥"
      style={{
        '--ops-incident-surface': 'var(--ops-surface, #ffffff)',
        '--ops-incident-border': 'var(--ops-surface-border, #94a3b8)',
        '--ops-incident-foreground': 'var(--ops-surface-foreground, #0f172a)',
        display: 'grid',
        gap: 12,
        padding: '14px 24px 0',
      }}
    >
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
        onOpenEvidence={() => setEvidenceOpen(true)}
      />
      <NextActionCard action={view.nextAction} />
      {!command && selectedIncidentId && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>正在读取权威事故状态…</p>
      )}
      {!selectedIncidentId && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>暂无可选事故，请等待告警或刷新列表。</p>
      )}
      <StageTimeline
        stages={view.stageTimeline}
        onOpenEvidence={() => setEvidenceOpen(true)}
      />
      {(view.stage === 'approved' || view.nextAction?.kind === 'approve') && (
        <ApprovalChecklist facts={command?.approvalFacts || {}} />
      )}
      {stateError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>阶段状态读取失败：{stateError}；保留可用读back。</p>
      )}
      {timelineError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>时间线读取失败：{timelineError}；保留可用读back。</p>
      )}
      {archiveError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>证据档案读取失败：{archiveError}；其余权威数据保持可用。</p>
      )}
      {previewError && (
        <p role="status" style={{ margin: 0, fontSize: 12 }}>修复预览读取失败：{previewError}；不推断审批资格。</p>
      )}
      {(stateError || timelineError || archiveError || previewError) && (
        <button
          type="button"
          onClick={() => setRefreshToken((token) => token + 1)}
          style={{
            justifySelf: 'start',
            border: '1px solid var(--ops-incident-border)',
            borderRadius: 4,
            background: 'transparent',
            color: 'var(--ops-incident-foreground)',
            font: 'inherit',
            fontSize: 12,
            padding: '4px 8px',
          }}
        >
          重试只读刷新
        </button>
      )}
      <EvidenceDrawer
        open={evidenceOpen}
        groups={command?.evidenceGroups || []}
        onClose={() => setEvidenceOpen(false)}
      />
    </section>
  );
}
