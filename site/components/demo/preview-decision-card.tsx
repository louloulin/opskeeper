import type { PreviewDecisionSummary } from '@/lib/demo-types';
import { cn } from '@/lib/utils';

export function PreviewDecisionCard({ decision }: { decision: PreviewDecisionSummary | null }) {
  const comparable = Boolean(
    decision &&
      decision.replay_profile_id &&
      !decision.boundary_text.toUpperCase().includes('NOT COMPARABLE'),
  );

  return (
    <section className="rounded-xl border border-white/10 bg-white/[0.03] p-5" aria-labelledby="preview-decision-title">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 id="preview-decision-title" className="text-lg font-semibold text-white">
            修复预演决策
          </h3>
          <p className="mt-1 text-sm text-ink-300">
            只展示进入人工审批前的紧凑证据；完整对比表在事故关闭后由档案页回看。
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
            {comparable ? '可对比' : '不可对比'}
          </span>
        )}
      </div>

      <p className="mt-4 rounded-lg border border-white/10 bg-white/[0.04] p-3 text-sm leading-relaxed text-ink-200">
        受控负载边界：{decision?.boundary_text || 'preview-pg 只重放固定负载，不复制原实例活动会话。'}
      </p>

      <dl className="mt-4 grid gap-3 text-sm sm:grid-cols-2">
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-4">
          <dt className="text-xs text-ink-400">Candidate A</dt>
          <dd className="mt-2 font-mono text-white">{decision?.candidate_a || '未获得可用候选'}</dd>
          <p className={cn('mt-2 text-xs', decision?.eligible_for_hitl ? 'text-accent-200' : 'text-ink-400')}>
            {decision?.eligible_for_hitl ? 'PASS · 仅获得人工审批资格' : '未获得人工审批资格'}
          </p>
        </div>
        <div className="rounded-lg border border-white/10 bg-white/[0.02] p-4">
          <dt className="text-xs text-ink-400">Candidate B</dt>
          <dd className="mt-2 font-mono text-white">{decision?.candidate_b || '未返回'}</dd>
          <p className="mt-2 text-xs text-rose-200">预演拒绝 · 不进入人工审批</p>
        </div>
      </dl>

      {decision?.replay_profile_id && (
        <p className="mt-3 break-all font-mono text-xs text-ink-400">
          replay_profile_id: {decision.replay_profile_id}
        </p>
      )}
    </section>
  );
}
