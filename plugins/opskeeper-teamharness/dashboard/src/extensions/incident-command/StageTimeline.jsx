import * as React from 'react';
import { COMMAND_PHASE_LABELS } from '@opskeeper/incident-command';

const STATUS_LABELS = {
  completed: '已完成',
  running: '进行中',
  blocked: '已阻塞',
  failed: '已失败',
  unknown: '未知',
};

const SUBSTATE_LABELS = {
  awaiting_human: '等待人工',
  approved: '已审批',
  executing: '执行中',
  verifying: '验证中',
};

function formatDuration(durationMs) {
  if (!Number.isFinite(durationMs) || durationMs < 0) return '未知';
  if (durationMs < 1000) return `${Math.round(durationMs)}毫秒`;
  const seconds = Math.round(durationMs / 1000);
  if (seconds < 100) return `${seconds}秒`;
  const minutes = Math.floor(seconds / 60);
  const remainingSeconds = seconds % 60;
  if (minutes < 60) return remainingSeconds ? `${minutes}分${remainingSeconds}秒` : `${minutes}分钟`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes ? `${hours}小时${remainingMinutes}分` : `${hours}小时`;
}

function stageSubstate(stage) {
  if (stage.stage === 'approved') {
    if (stage.status === 'blocked') return 'awaiting_human';
    if (stage.status === 'completed') return 'approved';
  }
  if (stage.stage === 'recovered') {
    if (stage.status === 'running') return 'verifying';
  }
  return undefined;
}

function sourceReference(stage) {
  return [
    ...(Array.isArray(stage.evidenceRefs) ? stage.evidenceRefs : []),
    stage.sourceEventId ? `事件 ${stage.sourceEventId}` : '',
    stage.sourceTaskId ? `任务 ${stage.sourceTaskId}` : '',
  ].filter(Boolean).join('，') || '来源缺失';
}

export default function StageTimeline({ stages = [], locale = 'zh-CN', onOpenEvidence }) {
  const activeIndex = Math.max(-1, ...(stages || []).map((stage, index) => (
    stage && stage.status !== 'unknown' ? index : -1
  )));

  return (
    <section
      className="opskeeper-incident-stage-timeline"
      aria-label="七阶段事故时间线"
      lang={locale}
      style={{
        '--ops-incident-surface': 'var(--ops-surface, #ffffff)',
        '--ops-incident-border': 'var(--ops-surface-border, #94a3b8)',
        '--ops-incident-foreground': 'var(--ops-surface-foreground, #0f172a)',
        '--ops-incident-muted': 'var(--ops-muted-foreground, #475569)',
        display: 'grid',
        gap: 8,
      }}
    >
      <style>{`
        .opskeeper-incident-stage-grid {
          display: grid;
          grid-template-columns: repeat(7, minmax(132px, 1fr));
          gap: 6px;
        }
        .opskeeper-incident-stage-item {
          display: grid;
          min-width: 0;
        }
        .opskeeper-incident-stage-row {
          display: grid;
          gap: 4px;
          min-width: 0;
          padding: 8px;
          border: 1px solid var(--ops-incident-border);
          border-radius: 6px;
          background: var(--ops-incident-surface);
          color: var(--ops-incident-foreground);
        }
        .opskeeper-incident-stage-item[data-status="completed"] .opskeeper-incident-stage-row { border-left: 3px solid var(--ops-status-success, #15803d); }
        .opskeeper-incident-stage-item[data-status="running"] .opskeeper-incident-stage-row { border-left: 3px solid var(--ops-status-active, #0369a1); }
        .opskeeper-incident-stage-item[data-status="blocked"] .opskeeper-incident-stage-row { border-left: 3px solid var(--ops-status-waiting, #b45309); }
        .opskeeper-incident-stage-item[data-status="failed"] .opskeeper-incident-stage-row { border-left: 3px solid var(--ops-status-failure, #b91c1c); }
        @media (max-width: 960px) {
          .opskeeper-incident-stage-grid { grid-template-columns: 1fr; }
        }
      `}</style>
      <h3 style={{ margin: 0, fontSize: 13 }}>七阶段事故时间线</h3>
      <ol className="opskeeper-incident-stage-grid" style={{ listStyle: 'none', margin: 0, padding: 0 }}>
        {(stages || []).map((stage, index) => {
          const substate = stageSubstate(stage);
          const future = stage?.status === 'unknown' && index > activeIndex;
          const status = future ? 'future' : (stage?.status || 'unknown');
          const statusLabel = future ? '未开始' : (STATUS_LABELS[status] || '未知');
          const owner = stage?.ownerLabel || stage?.workerRole || '未知';
          return (
            <li
              key={stage?.stage || index}
              className="opskeeper-incident-stage-item"
              data-stage={stage?.stage}
              data-status={status}
              aria-current={index === activeIndex && activeIndex >= 0 ? 'step' : undefined}
            >
              <article className="opskeeper-incident-stage-row" owner={owner}>
                <h4 style={{ margin: 0, fontSize: 12 }}>
                  {COMMAND_PHASE_LABELS[stage?.stage] || stage?.stage || '未知阶段'}
                </h4>
                <p style={{
                  margin: 0,
                  fontSize: 11,
                  fontWeight: 700,
                  color: status === 'completed'
                    ? 'var(--ops-status-success, #15803d)'
                    : status === 'blocked'
                      ? 'var(--ops-status-waiting, #b45309)'
                      : status === 'failed'
                        ? 'var(--ops-status-failure, #b91c1c)'
                        : 'var(--ops-incident-muted)',
                }}>
                  {statusLabel}{substate ? ` · ${SUBSTATE_LABELS[substate]}` : ''}
                </p>
                <dl style={{ margin: 0, fontSize: 11, color: 'var(--ops-incident-muted)', display: 'grid', gap: 2 }}>
                  <div style={{ display: 'grid', gridTemplateColumns: '42px 1fr', gap: 4 }}>
                    <dt>负责人</dt>
                    <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{owner}</dd>
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: '42px 1fr', gap: 4 }}>
                    <dt>耗时</dt>
                    <dd style={{ margin: 0 }}>{formatDuration(stage?.durationMs)}</dd>
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: '42px 1fr', gap: 4 }}>
                    <dt>结果</dt>
                    <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{stage?.outcome || (stage?.blockingReason || '暂无摘要')}</dd>
                  </div>
                  <div style={{ display: 'grid', gridTemplateColumns: '42px 1fr', gap: 4 }}>
                    <dt>来源</dt>
                    <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>{sourceReference(stage)}</dd>
                  </div>
                </dl>
                <button
                  type="button"
                  onClick={() => onOpenEvidence?.(stage)}
                  style={{
                    justifySelf: 'start',
                    border: '1px solid var(--ops-incident-border)',
                    borderRadius: 4,
                    background: 'transparent',
                    color: 'var(--ops-incident-foreground)',
                    font: 'inherit',
                    fontSize: 11,
                    padding: '3px 6px',
                  }}
                >
                  查看证据
                </button>
              </article>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
