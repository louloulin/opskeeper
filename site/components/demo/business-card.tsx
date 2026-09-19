import type { BusinessSection, BusinessSnapshot } from '@/lib/demo-types';
import { formatBeijingClock } from '@/lib/time-format';
import { cn } from '@/lib/utils';

type BusinessCardProps = {
  section: BusinessSection;
  snapshot?: BusinessSnapshot;
  errorCode?: string;
  loading: boolean;
};

const sectionCopy: Record<BusinessSection, { title: string; label: string }> = {
  orders: { title: '订单查询', label: '已支付订单' },
  inventory: { title: '库存查询', label: '最近仓库' },
  audit: { title: '审计查询', label: '审计事件' },
};

function formatSnapshot(section: BusinessSection, snapshot: BusinessSnapshot) {
  if (section === 'orders') {
    return {
      value: `${snapshot.value} 笔`,
      detail: `金额合计 ${(Number(snapshot.detail || '0') / 100).toFixed(2)} 元`,
    };
  }
  if (section === 'inventory') {
    return { value: snapshot.value, detail: snapshot.detail };
  }
  return {
    value: `${snapshot.value} 条`,
    detail: snapshot.detail ? `最新事件 ${snapshot.detail}` : '暂无最新事件',
  };
}

export function BusinessCard({
  section,
  snapshot,
  errorCode,
  loading,
}: BusinessCardProps) {
  const degraded = Boolean(errorCode);
  const timedOut = errorCode === 'query_timeout' || errorCode === 'pool_exhausted';
  const status = timedOut
    ? '查询超时'
    : degraded
      ? '降级'
      : snapshot
        ? '正常'
        : '查询中';
  const formatted = snapshot ? formatSnapshot(section, snapshot) : undefined;

  return (
    <article className="flex h-full flex-col rounded-xl border border-white/10 bg-white/[0.03] p-5">
      <div className="flex items-start justify-between gap-3">
        <div>
          <p className="font-mono text-xs uppercase tracking-wider text-ink-400">
            {sectionCopy[section].label}
          </p>
          <h3 className="mt-2 text-lg font-semibold text-white">
            {sectionCopy[section].title}
          </h3>
        </div>
        <span
          className={cn(
            'inline-flex shrink-0 items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium',
            !degraded && snapshot && 'border-accent-500/30 bg-accent-500/10 text-accent-200',
            timedOut && 'border-amber-400/30 bg-amber-400/10 text-amber-200',
            degraded && !timedOut && 'border-rose-500/30 bg-rose-500/10 text-rose-200',
            !snapshot && !degraded && 'border-white/10 bg-white/5 text-ink-300',
          )}
          role="status"
          aria-live="polite"
        >
          <span
            className={cn(
              'h-1.5 w-1.5 rounded-full',
              !degraded && snapshot && 'bg-accent-400',
              timedOut && 'bg-amber-300',
              degraded && !timedOut && 'bg-rose-400',
              !snapshot && !degraded && 'bg-ink-400',
            )}
          />
          {status}
        </span>
      </div>

      <div className="mt-5 min-h-[74px] flex-1" aria-busy={loading}>
        {formatted ? (
          <>
            <div className="text-2xl font-semibold text-white">
              {formatted.value}
            </div>
            <p className="mt-2 text-sm text-ink-300">
              {degraded ? '上次成功：' : ''}
              {formatted.detail}
            </p>
          </>
        ) : (
          <p className="text-sm leading-relaxed text-ink-400">
            {errorCode === 'demo_scenario_not_started'
              ? '等待演示场景初始化后开始真实查询。'
              : '暂无成功查询结果；本页不展示模拟成功数据。'}
          </p>
        )}
      </div>

      <footer className="mt-4 flex items-center justify-between gap-3 border-t border-white/5 pt-3 text-xs text-ink-400">
        <span className="tabular-nums">
          {snapshot ? `延迟 ${snapshot.latency_ms} ms` : '延迟待测'}
        </span>
        <time className="tabular-nums" dateTime={snapshot?.generated_at}>
          {snapshot ? formatBeijingClock(snapshot.generated_at) : '--:--:-- 北京时间'}
        </time>
      </footer>
    </article>
  );
}
