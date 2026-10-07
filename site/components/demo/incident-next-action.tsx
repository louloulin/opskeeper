import type { IncidentCommandView } from '@opskeeper/incident-command/types';
import type { DemoLocale } from '@/lib/demo-locale';

type NextAction = NonNullable<IncidentCommandView['nextAction']>;

const actionCopy = {
  wait: { zh: '等待权威流程推进', en: 'Wait for the authoritative loop' },
  'inspect-evidence': { zh: '检查权威状态与证据', en: 'Inspect authoritative state and evidence' },
  approve: { zh: '提供精准人工审批', en: 'Provide precise human approval' },
  reject: { zh: '出具精准人工拒绝', en: 'Issue a precise human rejection' },
  verify: { zh: '确认独立验证结果', en: 'Confirm independent verification' },
  archive: { zh: '归档并回放事故', en: 'Archive and replay the incident' },
  retry: { zh: '重试或检查失败原因', en: 'Retry or inspect the failure' },
} as const;

const detailCopy = {
  wait: { zh: '系统保留当前控制边界，不并行发起人工动作。', en: 'The system keeps the control boundary and starts no parallel human action.' },
  'inspect-evidence': { zh: '先补齐权威阶段或观测数据，不用推测填充状态。', en: 'Restore authoritative phase or observation data; do not infer missing state.' },
  approve: { zh: '审批必须绑定 incident、candidate、目标与有效期。', en: 'Approval must bind incident, candidate, target, and expiry.' },
  reject: { zh: '拒绝必须保留预演证据与明确理由。', en: 'Rejection must retain preview evidence and an explicit reason.' },
  verify: { zh: '使用独立验证结果确认业务恢复。', en: 'Use independent verification results to confirm recovery.' },
  archive: { zh: '关闭前核对业务指标与完整证据链。', en: 'Check business metrics and the complete evidence chain before closure.' },
  retry: { zh: '仅在前一次失败原因明确后重试。', en: 'Retry only after the previous failure is understood.' },
} as const;

export function IncidentNextAction({
  action,
  locale,
}: {
  action: NextAction | undefined;
  locale: DemoLocale;
}) {
  if (!action) return null;
  const zh = locale === 'zh';
  const label = actionCopy[action.kind][locale];
  const detail = zh ? action.detail ?? detailCopy[action.kind].zh : detailCopy[action.kind].en;

  return (
    <article
      aria-labelledby="incident-next-action-title"
      className="rounded-xl border border-amber-400/40 bg-amber-400/[0.07] p-5"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 id="incident-next-action-title" className="text-lg font-semibold text-white">
          {zh ? '唯一优先下一步' : 'Single prioritized next action'}
        </h3>
        <p className="rounded-full border border-amber-400/30 bg-amber-400/10 px-3 py-1 text-xs font-semibold text-amber-200 tabular-nums">
          {zh ? `优先级 ${action.priority}` : `Priority ${action.priority}`}
        </p>
      </div>
      <p className="mt-3 text-base font-medium text-white">{label}</p>
      <p className="mt-2 text-sm leading-relaxed text-ink-300">{detail}</p>
      {action.disabledReason && (
        <p className="mt-2 text-xs text-amber-200">{action.disabledReason}</p>
      )}
    </article>
  );
}
