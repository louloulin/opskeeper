import type { CommandPhase, StageStatus } from '@opskeeper/incident-command/types';
import type { DemoLocale } from '@/lib/demo-locale';
import { cn } from '@/lib/utils';

type Stage = {
  stage: CommandPhase;
  status: StageStatus;
  ownerLabel?: string;
};

const phaseCopy = {
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

export function CommandStageTimeline({
  stages,
  locale,
}: {
  stages: Stage[];
  locale: DemoLocale;
}) {
  const zh = locale === 'zh';

  function ownerLabel(label: string | undefined) {
    if (!label) return zh ? '未指定' : 'Not assigned';
    if (!zh) return label;
    const localized: Record<string, string> = {
      Manager: 'Manager',
      Alertmanager: '告警管理器',
      Human: '人工负责人',
      Investigator: '调查 Worker',
      Reviewer: '预审 Worker',
      Repairer: '修复 Worker',
      Verifier: '独立验证器',
      'Scenario runner': '场景运行器',
    };
    return localized[label] ?? label;
  }

  return (
    <section
      aria-labelledby="command-stage-title"
      className="rounded-2xl border border-white/10 bg-white/[0.03] p-5"
    >
      <h2 id="command-stage-title" className="text-xl font-semibold text-white">
        {zh ? '指挥阶段' : 'Command stages'}
      </h2>
      <ol className="mt-4 grid gap-3 md:grid-cols-2 xl:grid-cols-4" aria-label={zh ? '事故指挥阶段时间线' : 'Incident command stage timeline'}>
        {stages.map((stage) => {
          const isCurrent = stage.status === 'running' || stage.status === 'blocked';
          return (
            <li
              key={stage.stage}
              aria-current={isCurrent ? 'step' : undefined}
              className={cn(
                'rounded-xl border p-4',
                stage.status === 'completed'
                  ? 'border-accent-500/25 bg-accent-500/[0.05]'
                  : stage.status === 'blocked'
                    ? 'border-amber-400/40 bg-amber-400/[0.07]'
                    : stage.status === 'running'
                      ? 'border-cyan-400/40 bg-cyan-400/[0.07]'
                      : stage.status === 'failed'
                        ? 'border-rose-500/40 bg-rose-500/[0.07]'
                        : 'border-white/10 bg-white/[0.02]',
              )}
            >
              <div className="flex items-start justify-between gap-3">
                <p className="text-sm font-semibold text-white">{phaseCopy[stage.stage][locale]}</p>
              </div>
              <p className="mt-2 text-xs text-ink-300">{statusCopy[stage.status][locale]}</p>
              <p className="mt-1 text-xs text-ink-400">
                {zh ? '负责人：' : 'Owner: '}
                {ownerLabel(stage.ownerLabel)}
              </p>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
