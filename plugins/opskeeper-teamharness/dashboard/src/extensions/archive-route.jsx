import * as React from 'react';
import { opskeeperApi } from './api.js';
import {
  normalizeArchiveIncidentList,
  normalizeArchiveResponse,
  normalizeIncidentSummary,
} from './archive.js';

const PREVIEW_URL = 'https://opskeeper.yueming.xin/preview/';

export default function OpskeeperArchiveRoute({ api }) {
  const [incidents, setIncidents] = React.useState([]);
  const [incidentId, setIncidentId] = React.useState('');
  const [archive, setArchive] = React.useState(null);
  const [incidentSummary, setIncidentSummary] = React.useState(null);
  const [loadingIncidents, setLoadingIncidents] = React.useState(true);
  const [loadingArchive, setLoadingArchive] = React.useState(false);
  const [incidentError, setIncidentError] = React.useState('');
  const [archiveError, setArchiveError] = React.useState('');

  const loadIncidents = React.useCallback(async () => {
    setLoadingIncidents(true);
    try {
      // 演示/决赛场景需要看到最近触发的事故（含尚未走完 RCA 闭环的），
      // 所以从 /v1/incidents 拉取全量；闭环档案由 /incidents/<id>/archive 二次拉取。
      const items = normalizeArchiveIncidentList(await opskeeperApi.listIncidents({ limit: 100 }));
      setIncidents(items);
      setIncidentError('');
      setIncidentId((current) => current || items[0]?.id || '');
    } catch (error) {
      setIncidents([]);
      setIncidentError(error?.message || '事故列表读取失败');
    } finally {
      setLoadingIncidents(false);
    }
  }, []);

  const loadArchive = React.useCallback(async (selectedIncidentId) => {
    const targetIncidentId = String(selectedIncidentId ?? incidentId ?? '').trim();
    if (!targetIncidentId) {
      setArchiveError('请输入或选择事故 ID');
      setIncidentSummary(null);
      return;
    }
    setLoadingArchive(true);
    setIncidentSummary(null);
    try {
      setArchive(normalizeArchiveResponse(await opskeeperApi.getIncidentArchive(targetIncidentId)));
      setArchiveError('');
    } catch (error) {
      setArchive(null);
      if (error?.status === 404) {
        // 闭环档案还没生成（告警刚触发或 RCA 未走到闭环），回退到 incident 详情
        // 让页面至少能展示 alert/rule/label，避免 demo 时一片空白。
        try {
          const summary = normalizeIncidentSummary(
            await opskeeperApi.getIncident(targetIncidentId),
          );
          setIncidentSummary(summary);
          setArchiveError('该事故尚未生成闭环档案；以下为告警触发时刻的事实快照，RCA 闭环后会写入档案。');
        } catch (innerError) {
          setArchiveError(innerError?.message || error?.message || '事故档案读取失败');
        }
      } else {
        setArchiveError(error?.message || '事故档案读取失败');
      }
    } finally {
      setLoadingArchive(false);
    }
  }, [incidentId]);

  React.useEffect(() => {
    loadIncidents();
  }, [loadIncidents]);

  const requiredEventTypes = archive?.required_event_types || [];
  const missingEventTypes = new Set(archive?.missing_event_types || []);
  const timeline = React.useMemo(
    () => [...(archive?.timeline || [])].sort((left, right) => new Date(left.occurred_at) - new Date(right.occurred_at)),
    [archive],
  );

  return (
    <div style={{ padding: 24, color: 'var(--card-foreground)' }}>
      <header style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 18 }}>
        <div>
          <h2 style={{ margin: 0, fontSize: 18 }}>OpsKeeper 事故档案</h2>
          <p style={{ margin: '4px 0 0', fontSize: 12, color: 'var(--muted-foreground)' }}>
            Manager 保留权威证据与权限；插件仅做只读回看，不复制控制面事实源。
          </p>
        </div>
        <button
          type="button"
          onClick={() => { loadIncidents(); if (incidentId) loadArchive(incidentId); }}
          disabled={loadingIncidents || loadingArchive}
          style={buttonStyle(loadingIncidents || loadingArchive)}
        >
          {loadingIncidents || loadingArchive ? '刷新中…' : '刷新'}
        </button>
      </header>

      <Panel>
        <form
          onSubmit={(event) => { event.preventDefault(); loadArchive(incidentId); }}
          style={{ display: 'flex', flexWrap: 'wrap', gap: 8, alignItems: 'center' }}
        >
          <select
            value={incidents.some((incident) => incident.id === incidentId) ? incidentId : ''}
            onChange={(event) => setIncidentId(event.target.value)}
            disabled={loadingIncidents || incidents.length === 0}
            style={inputStyle()}
          >
            <option value="">{loadingIncidents ? '加载事故中…' : incidents.length ? '选择事故（闭环优先）' : '暂无可回看事故'}</option>
            {incidents.map((incident) => (
              <option key={incident.id} value={incident.id}>
                {`${incident.summary} · ${incident.eventCount}事件 · ${incident.status}${incident.evidenceComplete ? ' · 档案完整' : ''}`}
              </option>
            ))}
          </select>
          <input
            value={incidentId}
            onChange={(event) => setIncidentId(event.target.value)}
            placeholder="事故 ID"
            style={{ ...inputStyle, minWidth: 260 }}
          />
          <button type="submit" disabled={loadingArchive} style={buttonStyle(loadingArchive)}>
            {loadingArchive ? '查询中…' : '查询档案'}
          </button>
        </form>
        {incidentError && <ErrorState text={incidentError} />}
        {archiveError && <ErrorState text={archiveError} />}
      </Panel>

      {archive && (
        <>
          <section style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(160px, 1fr))', gap: 12, marginBottom: 12 }}>
            <SummaryCard label="证据完整性" value={archive.evidence_complete ? '完整' : '缺失'} hint={`${archive.event_count || 0} 条事件`} color={archive.evidence_complete ? '#16a34a' : '#dc2626'} />
            <SummaryCard label="恢复确认" value={archive.recovery_observed ? '已观测' : '未观测'} color={archive.recovery_observed ? '#16a34a' : '#f59e0b'} />
            <SummaryCard label="定位耗时" value={formatSeconds(archive.localization_seconds)} hint="告警 → 根因" />
            <SummaryCard label="恢复耗时" value={formatSeconds(archive.recovery_seconds)} hint="执行 → 恢复" />
          </section>

          <section style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 2fr) minmax(300px, 1fr)', gap: 12 }}>
            <Panel title="反向证据链">
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 12 }}>
                {requiredEventTypes.map((eventType) => (
                  <span key={eventType} style={stageChipStyle(missingEventTypes.has(eventType))}>
                    {eventType}
                  </span>
                ))}
              </div>
              {timeline.map((event) => (
                <div key={event.id || `${event.event_type}-${event.occurred_at}`} style={eventRowStyle()}>
                  <div>
                    <div style={{ fontSize: 12, fontWeight: 600 }}>{event.event_type}</div>
                    <div style={{ marginTop: 3, fontSize: 11, color: 'var(--muted-foreground)' }}>
                      {event.phase || '未记录阶段'} · {event.actor_type || 'unknown'} / {event.actor || 'unknown'} · {event.status || 'unknown'}
                    </div>
                  </div>
                  <div style={{ textAlign: 'right', fontSize: 11, color: 'var(--muted-foreground)', minWidth: 190 }}>
                    <div>{formatTime(event.occurred_at)}</div>
                    {event.evidence_ref && <div style={{ marginTop: 3 }}>{event.evidence_ref}</div>}
                    {event.trace_id && <div style={{ marginTop: 3 }}>trace: {event.trace_id}</div>}
                  </div>
                </div>
              ))}
              {timeline.length === 0 && <EmptyState text="暂无事件" />}
            </Panel>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              <Panel title="闭环状态">
                <MetricRow label="事故已关闭" value={archive.closed ? '是' : '否'} />
                <MetricRow label="Trace" value={archive.trace_ids?.length || 0} />
                <MetricRow label="缺失事件" value={archive.missing_event_types?.length || 0} />
                <MetricRow label="最近事件" value={formatTime(archive.last_event_at)} />
              </Panel>

              <Panel title="同类历史事故">
                {archive.similar_incidents?.map((incident) => (
                  <button key={incident.incident_id} type="button" onClick={() => { setIncidentId(incident.incident_id); loadArchive(incident.incident_id); }} style={linkRowStyle()}>
                    <span>{incident.incident_id}</span>
                    <span style={{ color: incident.closed ? '#16a34a' : 'var(--muted)' }}>{incident.closed ? '已关闭' : '未关闭'}</span>
                  </button>
                ))}
                {archive.similar_incidents?.length === 0 && <EmptyState text="暂无可反查历史事故" />}
              </Panel>

              <Panel title="复盘与修复预演">
                {archive.postmortem_refs?.map((reference) => (
                  <div key={reference.id || reference.incident_id} style={{ padding: '7px 0', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
                    <div>{reference.root_cause || '未记录根因'}</div>
                    <div style={{ marginTop: 3, fontSize: 11, color: 'var(--muted-foreground)' }}>
                      {reference.confirmed_by || 'unknown'} · {formatTime(reference.confirmed_at)}
                    </div>
                  </div>
                ))}
                {archive.postmortem_refs?.length === 0 && <EmptyState text="暂无复盘引用" />}
                <a href={PREVIEW_URL} target="_blank" rel="noreferrer" style={{ display: 'inline-block', marginTop: 10, fontSize: 12 }}>
                  打开 preview-pg 修复对比 →
                </a>
                <div style={{ marginTop: 5, fontSize: 11, color: 'var(--muted-foreground)' }}>
                  对比数据留在独立预演环境，不写入本事故权威档案。
                </div>
              </Panel>
            </div>
          </section>
        </>
      )}

      {!archive && !incidentSummary && !archiveError && !loadingArchive && (
        <Panel>
          <EmptyState text="选择或输入事故 ID 后查询证据档案" />
        </Panel>
      )}

      {!archive && incidentSummary && (
        <>
          <section style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(160px, 1fr))', gap: 12, marginBottom: 12 }}>
            <SummaryCard label="告警状态" value={incidentSummary.status} color={incidentSummary.status === 'resolved' ? '#16a34a' : '#f59e0b'} />
            <SummaryCard label="事件数量" value={String(incidentSummary.eventCount)} hint="已落库" />
            <SummaryCard label="规则" value={incidentSummary.ruleKey || '—'} hint={incidentSummary.ruleName} />
            <SummaryCard label="触发时间" value={formatTime(incidentSummary.firedAt)} hint={formatTime(incidentSummary.resolvedAt) !== '未采集' ? `恢复：${formatTime(incidentSummary.resolvedAt)}` : '尚未恢复'} />
          </section>

          <section style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 2fr) minmax(280px, 1fr)', gap: 12 }}>
            <Panel title="告警事实快照">
              <div style={{ fontSize: 13, fontWeight: 600, marginBottom: 8 }}>{incidentSummary.summary || '未提供 summary'}</div>
              <MetricRow label="严重级别" value={incidentSummary.severity || '—'} />
              <MetricRow label="Dedupe Key" value={incidentSummary.dedupeKey || '—'} />
              <MetricRow label="Target Type" value={incidentSummary.targetType || '—'} />
              <MetricRow label="指标值" value={incidentSummary.value ?? '—'} />
              {Object.keys(incidentSummary.labels || {}).length > 0 && (
                <div style={{ marginTop: 10 }}>
                  <div style={{ fontSize: 11, color: 'var(--muted-foreground)', marginBottom: 4 }}>Labels</div>
                  <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                    {Object.entries(incidentSummary.labels).map(([key, value]) => (
                      <span key={key} style={stageChipStyle(false)}>{key}={String(value)}</span>
                    ))}
                  </div>
                </div>
              )}
            </Panel>

            <Panel title="后续动作">
              <div style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>
                闭环档案（包含 alert/根因/修复/恢复 7 阶段事件）由 Manager 在 RCA 闭环后写入；当前展示的是告警事实快照，不复制控制面数据。
              </div>
              <a href={PREVIEW_URL} target="_blank" rel="noreferrer" style={{ display: 'inline-block', marginTop: 10, fontSize: 12 }}>
                打开 preview-pg 修复对比 →
              </a>
            </Panel>
          </section>
        </>
      )}
    </div>
  );
}

function Panel({ title, children }) {
  return (
    <div style={{ padding: 14, marginBottom: 12, borderRadius: 8, border: '1px solid var(--border)', background: 'var(--card)' }}>
      {title && <div style={{ fontSize: 12, color: 'var(--muted-foreground)', marginBottom: 8 }}>{title}</div>}
      {children}
    </div>
  );
}

function SummaryCard({ label, value, hint, color }) {
  return (
    <div style={{ padding: 14, borderRadius: 8, border: '1px solid var(--border)', background: 'var(--card)' }}>
      <div style={{ fontSize: 11, color: 'var(--muted-foreground)' }}>{label}</div>
      <div style={{ marginTop: 6, fontSize: 19, fontWeight: 600, color: color || 'inherit' }}>{value}</div>
      {hint && <div style={{ marginTop: 4, fontSize: 11, color: 'var(--muted-foreground)' }}>{hint}</div>}
    </div>
  );
}

function MetricRow({ label, value }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '7px 0', borderBottom: '1px solid var(--border)', fontSize: 12 }}>
      <span>{label}</span>
      <span style={{ marginLeft: 'auto', fontWeight: 600 }}>{value}</span>
    </div>
  );
}

function ErrorState({ text }) {
  return <div style={{ marginTop: 10, padding: 9, borderRadius: 6, background: 'rgba(220,38,38,.12)', color: '#dc2626', fontSize: 12 }}>{text}</div>;
}

function EmptyState({ text }) {
  return <div style={{ padding: 16, textAlign: 'center', fontSize: 12, color: 'var(--muted-foreground)' }}>{text}</div>;
}

function inputStyle() {
  return {
    padding: '6px 9px', borderRadius: 6, fontSize: 12, minWidth: 220,
    border: '1px solid var(--border)', background: 'var(--background)', color: 'inherit',
  };
}

function buttonStyle(disabled) {
  return {
    padding: '6px 12px', borderRadius: 6, fontSize: 12,
    border: '1px solid var(--border)', background: 'var(--card)', color: 'inherit',
    cursor: disabled ? 'wait' : 'pointer',
  };
}

function stageChipStyle(missing) {
  return {
    padding: '3px 8px', borderRadius: 999, fontSize: 11,
    border: `1px solid ${missing ? '#dc2626' : 'var(--border)'}`,
    color: missing ? '#dc2626' : 'inherit',
    background: missing ? 'rgba(220,38,38,.1)' : 'transparent',
  };
}

function eventRowStyle() {
  return {
    display: 'flex', justifyContent: 'space-between', gap: 12,
    padding: '9px 0', borderBottom: '1px solid var(--border)',
  };
}

function linkRowStyle() {
  return {
    display: 'flex', justifyContent: 'space-between', width: '100%', textAlign: 'left',
    padding: '7px 0', border: 0, borderBottom: '1px solid var(--border)',
    background: 'transparent', color: 'inherit', fontSize: 12, cursor: 'pointer',
  };
}

function formatTime(value) {
  if (!value) return '未采集';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function formatSeconds(value) {
  if (value == null) return '—';
  if (value < 60) return `${value.toFixed(0)}s`;
  if (value < 3600) return `${(value / 60).toFixed(1)}m`;
  return `${(value / 3600).toFixed(1)}h`;
}
