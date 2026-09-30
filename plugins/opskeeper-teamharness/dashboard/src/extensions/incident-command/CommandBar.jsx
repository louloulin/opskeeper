import * as React from 'react';

const FRESHNESS_COPY = {
  fresh: '数据新鲜',
  stale: '数据已过期',
  unknown: '数据新鲜度未知',
};

const IMPACT_COPY = {
  unknown: '影响未知',
  normal: '业务影响正常',
  degraded: '业务影响受损',
  severe: '业务影响严重',
};

const STAGE_STATUS_COPY = {
  pending: '未开始',
  running: '执行中',
  blocked: '已阻塞',
  completed: '已完成',
  failed: '已失败',
  unknown: '状态未知',
};

function timestamp(value, locale) {
  if (!value) return '未知时间';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '无效时间';
  return new Intl.DateTimeFormat(locale || 'zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    timeZone: 'UTC',
  }).format(date);
}

function duration(value) {
  if (!Number.isFinite(value) || value < 0) return '未知时长';
  const totalSeconds = Math.floor(value / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, '0')}`;
}

function expiry(value) {
  const date = value ? new Date(value) : null;
  return date && !Number.isNaN(date.getTime())
    ? new Date(date.getTime() + 60_000).toISOString()
    : null;
}

function TimeValue({ value, locale }) {
  const isValid = value && !Number.isNaN(new Date(value).getTime());
  return (
    <time
      dateTime={isValid ? value : undefined}
      style={{ fontVariantNumeric: 'tabular-nums' }}
    >
      {timestamp(value, locale)}
    </time>
  );
}

export default function CommandBar({ view, locale = 'zh-CN', onOpenEvidence }) {
  const stageLabel = view?.stage ? view.stage : 'unknown';
  const owner = view?.owner?.label || '负责人未知';
  const observed = view?.observedAt;
  const serverNow = view?.serverNow;
  const freshnessExpiresAt = expiry(observed);

  return (
    <section
      aria-label="事故指挥摘要"
      style={{
        border: '1px solid var(--ops-surface-border)',
        background: 'var(--ops-surface)',
        color: 'var(--ops-surface-foreground)',
        borderRadius: 8,
        padding: 14,
        display: 'grid',
        gap: 12,
      }}
    >
      <header style={{ display: 'flex', flexWrap: 'wrap', gap: 10, justifyContent: 'space-between' }}>
        <div>
          <h2 style={{ margin: 0, fontSize: 15 }}>事故 {view?.incidentId || '未知'}</h2>
          <p style={{ margin: '3px 0 0', fontSize: 12 }}>
            {IMPACT_COPY[view?.businessImpact?.level] || IMPACT_COPY.unknown}
            {view?.businessImpact?.affectedScopes?.length ? ` · ${view.businessImpact.affectedScopes.map((scope) => scope.name).join('、')}` : ''}
          </p>
        </div>
        <button
          type="button"
          onClick={onOpenEvidence}
          style={{
            border: '1px solid var(--ops-surface-border)',
            background: 'transparent',
            color: 'var(--ops-surface-foreground)',
            borderRadius: 6,
            padding: '6px 10px',
            cursor: 'pointer',
          }}
        >
          查看证据与诊断
        </button>
      </header>
      <dl style={{
        margin: 0,
        display: 'grid',
        gridTemplateColumns: 'repeat(auto-fit, minmax(132px, 1fr))',
        gap: 10,
        fontSize: 12,
      }}>
        <div>
          <dt style={{ fontWeight: 600 }}>当前阶段</dt>
          <dd style={{ margin: '3px 0 0' }}>
            {view?.stage ? `阶段 ${stageLabel}` : '当前阶段未知'} · {STAGE_STATUS_COPY[view?.stageStatus] || STAGE_STATUS_COPY.unknown}
          </dd>
        </div>
        <div>
          <dt style={{ fontWeight: 600 }}>负责人</dt>
          <dd style={{ margin: '3px 0 0' }}>{owner}</dd>
        </div>
        <div>
          <dt style={{ fontWeight: 600 }}>已持续</dt>
          <dd style={{ margin: '3px 0 0', fontVariantNumeric: 'tabular-nums' }}>{duration(view?.elapsedMs)}</dd>
        </div>
        <div>
          <dt style={{ fontWeight: 600 }}>数据状态</dt>
          <dd
            style={{
              margin: '3px 0 0',
              color: view?.freshness === 'stale'
                ? 'var(--ops-status-active)'
                : `var(--ops-status-${view?.freshness || 'unknown'})`,
            }}
          >
            {FRESHNESS_COPY[view?.freshness] || FRESHNESS_COPY.unknown}
          </dd>
        </div>
        <div>
          <dt style={{ fontWeight: 600 }}>权威时间边界</dt>
          <dd style={{ margin: '3px 0 0', fontVariantNumeric: 'tabular-nums' }}>
            <TimeValue value={observed} locale={locale} />
            {' 至 '}
            <TimeValue value={serverNow} locale={locale} />
          </dd>
        </div>
        <div>
          <dt style={{ fontWeight: 600 }}>新鲜度有效期</dt>
          <dd style={{ margin: '3px 0 0', fontVariantNumeric: 'tabular-nums' }}>
            <TimeValue value={freshnessExpiresAt} locale={locale} />
          </dd>
        </div>
      </dl>
    </section>
  );
}
