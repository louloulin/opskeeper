import type { IncidentCommandView } from '@opskeeper/incident-command/types';
import type { DemoLocale } from '@/lib/demo-locale';
import { IncidentNextAction } from '@/components/demo/incident-next-action';
import { cn } from '@/lib/utils';

const expiryBoundaryMs = 60_000;

function formatDuration(ms: number, locale: DemoLocale) {
  const totalSeconds = Math.max(0, Math.round(ms / 1000));
  if (totalSeconds < 60) {
    return locale === 'zh' ? `${totalSeconds} 秒` : `${totalSeconds} s`;
  }
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (minutes < 60) {
    return locale === 'zh'
      ? `${minutes} 分 ${seconds} 秒`
      : `${minutes}m ${seconds}s`;
  }
  const hours = Math.floor(minutes / 60);
  return locale === 'zh'
    ? `${hours} 时 ${minutes % 60} 分`
    : `${hours}h ${minutes % 60}m`;
}

function elapsedMs(view: IncidentCommandView) {
  return view.elapsedMs;
}

function observedAt(value?: string) {
  if (!value) return undefined;
  const timestamp = Date.parse(value);
  if (Number.isNaN(timestamp)) return { label: 'unknown', valid: false };
  const isoTimestamp = new Date(timestamp).toISOString();
  return { label: isoTimestamp, isoTimestamp, valid: true };
}

function ownerLabel(view: IncidentCommandView, locale: DemoLocale) {
  const owner = view.owner;
  if (!owner) return locale === 'zh' ? '未知' : 'Unknown';
  if (locale === 'en') return owner.label;
  if (owner.label === 'Alertmanager') return '告警管理器';
  if (owner.label === 'Human') return '人工负责人';
  if (owner.label === 'Investigator') return '调查 Worker';
  if (owner.label === 'Reviewer') return '预审 Worker';
  if (owner.label === 'Repairer') return '修复 Worker';
  if (owner.label === 'Verifier') return '独立验证器';
  if (owner.label === 'Scenario runner') return '场景运行器';
  if (owner.kind === 'manager') return 'Manager';
  if (owner.kind === 'worker') return owner.role ?? 'Worker';
  if (owner.kind === 'verifier') return '独立验证器';
  if (owner.kind === 'human') return '人工负责人';
  return '系统';
}

const impactCopy = {
  unknown: { zh: '影响未知', en: 'Impact unknown' },
  normal: { zh: '业务正常', en: 'Business normal' },
  degraded: { zh: '业务降级', en: 'Business degraded' },
  severe: { zh: '业务严重受损', en: 'Business severely impacted' },
} as const;

const freshnessCopy = {
  fresh: { zh: '观测新鲜', en: 'Fresh observation' },
  stale: { zh: '观测过期', en: 'Stale observation' },
  unknown: { zh: '观测未知', en: 'Unknown observation' },
} as const;

const stageCopy = {
  detected: { zh: '检测', en: 'Detected' },
  correlated: { zh: '关联', en: 'Correlated' },
  investigated: { zh: '诊断', en: 'Investigated' },
  critiqued: { zh: '评审', en: 'Critiqued' },
  approved: { zh: '审批', en: 'Approval' },
  recovered: { zh: '恢复', en: 'Recovery' },
  postmortem: { zh: '复盘', en: 'Postmortem' },
} as const;

const statusCopy = {
  pending: { zh: '待进入', en: 'Pending' },
  running: { zh: '进行中', en: 'Running' },
  blocked: { zh: '已阻塞', en: 'Blocked' },
  completed: { zh: '已完成', en: 'Completed' },
  failed: { zh: '失败', en: 'Failed' },
  unknown: { zh: '未知', en: 'Unknown' },
} as const;

export function IncidentCommandBar({
  view,
  locale,
}: {
  view: IncidentCommandView;
  locale: DemoLocale;
}) {
  const zh = locale === 'zh';
  const scopes = view.businessImpact.affectedScopes;
  const healthyCount = scopes.filter((scope) => scope.state === 'healthy').length;
  const degradedCount = scopes.filter((scope) => scope.state === 'degraded').length;
  const unknownCount = scopes.filter((scope) => scope.state === 'unknown').length;
  const latency = view.businessImpact.indicators.find((indicator) => indicator.name === 'average_latency_ms');
  const elapsed = elapsedMs(view);
  const impactLevel = view.businessImpact.level;
  const stage = view.stage ? stageCopy[view.stage][locale] : zh ? '未知阶段' : 'Unknown stage';
  const stageStatus = statusCopy[view.stageStatus][locale];
  const freshness = freshnessCopy[view.freshness][locale];
  const observedTimestamp = observedAt(view.observedAt);

  const facts = [
    { label: zh ? '当前阶段' : 'Current stage', value: `${stage} · ${stageStatus}` },
    { label: zh ? '当前负责人' : 'Current owner', value: ownerLabel(view, locale) },
    { label: zh ? '观测经过' : 'Elapsed', value: elapsed === undefined ? (zh ? '未知' : 'Unknown') : formatDuration(elapsed, locale) },
    { label: zh ? '观测新鲜度' : 'Freshness', value: freshness },
    { label: zh ? '过期边界' : 'Expiry boundary', value: formatDuration(expiryBoundaryMs, locale) },
  ];

  const stateCards = [
    { state: 'healthy' as const, label: zh ? '健康卡片' : 'Healthy cards', count: healthyCount },
    { state: 'degraded' as const, label: zh ? '降级卡片' : 'Degraded cards', count: degradedCount },
    { state: 'unknown' as const, label: zh ? '未知卡片' : 'Unknown cards', count: unknownCount },
  ];

  return (
    <section
      aria-labelledby="incident-command-title"
      className="rounded-2xl border border-accent-500/40 bg-ink-900/80 p-5 shadow-glow sm:p-6"
    >
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <p className="font-mono text-xs uppercase tracking-wider text-accent-300">
            {zh ? '事故指挥' : 'Incident command'}
          </p>
          <h2
            id="incident-command-title"
            className="mt-2 text-2xl font-semibold tracking-tight text-white"
          >
            {zh ? `事故 ${view.incidentId || '未知'}` : `Incident ${view.incidentId || 'unknown'}`}
          </h2>
          {view.scenario && (
            <p className="mt-2 break-all font-mono text-xs text-ink-400">{view.scenario}</p>
          )}
        </div>
        <p
          className={cn(
            'rounded-full border px-3 py-1 text-xs font-semibold',
            impactLevel === 'severe'
              ? 'border-rose-500/50 bg-rose-500/15 text-rose-200'
              : impactLevel === 'degraded'
                ? 'border-amber-400/50 bg-amber-400/15 text-amber-200'
                : impactLevel === 'normal'
                  ? 'border-accent-500/50 bg-accent-500/15 text-accent-200'
                  : 'border-white/20 bg-white/[0.04] text-ink-300',
          )}
        >
          {impactCopy[impactLevel][locale]}
        </p>
      </div>

      <dl className="mt-5 grid gap-3 text-sm sm:grid-cols-2 xl:grid-cols-5">
        {facts.map((fact) => (
          <div key={fact.label} className="rounded-xl border border-white/10 bg-white/[0.03] p-4">
            <dt className="text-xs text-ink-400">{fact.label}</dt>
            <dd className="mt-2 font-medium text-white tabular-nums">{fact.value}</dd>
          </div>
        ))}
      </dl>

      <div className="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {stateCards.map((card) => (
          <div
            key={card.state}
            className={cn(
              'rounded-xl border p-4',
              card.state === 'healthy'
                ? 'border-accent-500/25 bg-accent-500/[0.07]'
                : card.state === 'degraded'
                  ? 'border-amber-400/30 bg-amber-400/[0.07]'
                  : 'border-white/20 bg-white/[0.02]',
            )}
          >
            <p className="text-xs text-ink-400">{card.label}</p>
            <p className="mt-2 text-xl font-semibold text-white tabular-nums">{card.count}/3</p>
            <p className="mt-1 text-xs text-ink-400">
              {card.state === 'healthy'
                ? zh ? '状态文本：健康' : 'Status: healthy'
                : card.state === 'degraded'
                  ? zh ? '状态文本：降级' : 'Status: degraded'
                  : zh ? '状态文本：未知' : 'Status: unknown'}
            </p>
          </div>
        ))}
        <div className="rounded-xl border border-white/10 bg-white/[0.03] p-4">
          <p className="text-xs text-ink-400">{zh ? '平均延迟' : 'Average latency'}</p>
          <p className={cn(
            'mt-2 text-xl font-semibold tabular-nums',
            latency?.state === 'degraded' ? 'text-amber-200' : latency?.state === 'healthy' ? 'text-accent-200' : 'text-white',
          )}>
            {latency?.value ?? (zh ? '待测' : 'Pending')}
          </p>
          <p className="mt-1 text-xs text-ink-400">
            {latency?.state
              ? `${zh ? '状态' : 'Status'}: ${latency.state === 'degraded' ? (zh ? '降级' : 'degraded') : latency.state === 'healthy' ? (zh ? '健康' : 'healthy') : zh ? '未知' : 'unknown'}`
              : zh ? '状态：未知' : 'Status: unknown'}
          </p>
        </div>
      </div>

      {observedTimestamp && (
        <p className="mt-4 text-xs text-ink-400">
          {zh ? '观测时间：' : 'Observed at: '}
          {observedTimestamp.valid
            ? (
                <time className="tabular-nums" dateTime={observedTimestamp.isoTimestamp}>
                  {observedTimestamp.label}
                </time>
              )
            : (zh ? '未知' : 'Unknown')}
        </p>
      )}

      <div className="mt-5">
        <IncidentNextAction action={view.nextAction} locale={locale} />
      </div>
    </section>
  );
}
