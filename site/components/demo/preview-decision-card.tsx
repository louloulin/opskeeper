import type { PreviewDecision, PreviewMetrics } from '@/lib/demo-types';
import { cn } from '@/lib/utils';

const metricRows: Array<{
  key: keyof PreviewMetrics;
  label: string;
  format: (value: PreviewMetrics[keyof PreviewMetrics]) => string;
}> = [
  { key: 'latency_p95_ms', label: 'P95 延迟', format: (value) => `${value} ms` },
  { key: 'throughput_rps', label: '吞吐', format: (value) => `${value} req/s` },
  { key: 'write_impact_pct', label: '写入影响', format: (value) => `${value}%` },
  { key: 'storage_delta_mb', label: '存储变化', format: (value) => `${value} MB` },
];

function metricsFor(decision: PreviewDecision, id: 'baseline' | 'A' | 'B') {
  if (id === 'baseline') return decision.baseline;
  return decision.candidates.find((candidate) => candidate.id === id)?.metrics;
}

function decisionFor(decision: PreviewDecision, id: 'A' | 'B') {
  return decision.candidates.find((candidate) => candidate.id === id)?.decision;
}

export function PreviewDecisionCard({ decision }: { decision: PreviewDecision | null }) {
  const replayProfiles = decision
    ? [decision.replay_profile_id, decision.baseline.replay_profile_id, ...decision.candidates.map((item) => item.metrics.replay_profile_id)]
    : [];
  const comparable = decision && new Set(replayProfiles).size <= 1;

  return (
    <section className="rounded-xl border border-white/10 bg-white/[0.03] p-5" aria-labelledby="preview-decision-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 id="preview-decision-title" className="text-lg font-semibold text-white">
            修复预演决策
          </h3>
          <p className="mt-1 text-sm text-ink-300">
            只展示进入 HITL 前的紧凑证据；完整对比表在事故关闭后由 Archive 回看。
          </p>
        </div>
        {decision && (
          <span
            className={cn(
              'rounded-full border px-3 py-1 font-mono text-xs',
              comparable
                ? 'border-accent-500/30 bg-accent-500/10 text-accent-200'
                : 'border-amber-400/30 bg-amber-400/10 text-amber-200',
            )}
          >
            {comparable ? 'COMPARABLE' : 'NOT COMPARABLE'}
          </span>
        )}
      </div>

      <p className="mt-4 rounded-lg border border-white/10 bg-white/[0.04] p-3 text-sm leading-relaxed text-ink-200">
        受控负载边界：{decision?.boundary ?? 'preview-pg 只重放固定负载，不复制原实例活动会话。'}
      </p>

      {decision ? (
        <>
          <div className="mt-4 flex flex-wrap items-center gap-3 text-xs text-ink-400">
            <span className="font-mono">replay_profile_id: {decision.replay_profile_id}</span>
          </div>
          <div className="mt-4 overflow-x-auto">
            <table className="w-full min-w-[640px] border-collapse text-sm">
              <thead>
                <tr className="border-b border-white/10 text-left text-xs uppercase tracking-wide text-ink-400">
                  <th scope="col" className="py-2 pr-4">指标</th>
                  <th scope="col" className="py-2 pr-4">Baseline</th>
                  <th scope="col" className="py-2 pr-4">Candidate A</th>
                  <th scope="col" className="py-2">Candidate B</th>
                </tr>
              </thead>
              <tbody className="tabular-nums">
                {metricRows.map((row) => (
                  <tr key={row.key} className="border-b border-white/5">
                    <th scope="row" className="py-2.5 pr-4 text-left font-normal text-ink-300">
                      {row.label}
                    </th>
                    {[metricsFor(decision, 'baseline'), metricsFor(decision, 'A'), metricsFor(decision, 'B')].map((metrics, index) => (
                      <td key={index} className="py-2.5 pr-4 text-white">
                        {metrics ? row.format(metrics[row.key]) : '未返回'}
                      </td>
                    ))}
                  </tr>
                ))}
                <tr>
                  <th scope="row" className="py-2.5 pr-4 text-left font-normal text-ink-300">决策</th>
                  <td className="py-2.5 pr-4 text-ink-300">基准</td>
                  {(['A', 'B'] as const).map((id) => {
                    const decisionValue = decisionFor(decision, id);
                    return (
                    <td key={id} className="py-2.5 pr-4">
                      <span className={cn(
                        'rounded-full border px-2 py-0.5 text-xs font-semibold',
                        decisionValue === 'PASS'
                          ? 'border-accent-500/30 bg-accent-500/10 text-accent-200'
                          : 'border-rose-500/30 bg-rose-500/10 text-rose-200',
                      )}>
                        {decisionValue ?? '未返回'}
                      </span>
                    </td>
                    );
                  })}
                </tr>
              </tbody>
            </table>
          </div>
        </>
      ) : (
        <p className="mt-4 rounded-lg border border-dashed border-white/10 p-4 text-sm text-ink-400">
          等待 Manager 返回预演证据。此处不会用静态数据替代真实结果。
        </p>
      )}
    </section>
  );
}
