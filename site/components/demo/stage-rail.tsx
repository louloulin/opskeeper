import { SCENARIO_STAGES, type WorkflowStage } from '@/lib/demo-types';
import { cn } from '@/lib/utils';

const stageLabels: Record<WorkflowStage, string> = {
  starting: '场景启动',
  awaiting_alert: '等待告警',
  alert_correlated: '告警关联',
  diagnosis_dispatched: '诊断派发',
  preview_ready: '预演就绪',
  awaiting_approval: '等待审批',
  repair_dispatched: '修复派发',
  verifying: '独立验证',
  recovered: '恢复确认',
  closed: '档案关闭',
  start_failed: '启动失败',
};

export function StageRail({ status }: { status?: WorkflowStage }) {
  const currentIndex = status ? SCENARIO_STAGES.indexOf(status as (typeof SCENARIO_STAGES)[number]) : -1;
  const failed = status === 'start_failed';

  return (
    <div className="rounded-xl border border-white/10 bg-white/[0.03] p-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-lg font-semibold text-white">闭环阶段</h3>
          <p className="mt-1 text-sm text-ink-300">
            Manager 权威状态驱动；预演通过仅获得审批资格，不代替人工批准。
          </p>
        </div>
        <span className="rounded-full border border-white/10 bg-white/5 px-3 py-1 font-mono text-xs text-ink-200">
          {status ?? 'not_started'}
        </span>
      </div>

      <ol className="mt-5 grid gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {SCENARIO_STAGES.map((stage, index) => {
          const current = index === currentIndex && !failed;
          const completed = !failed && currentIndex >= 0 && index < currentIndex;
          return (
            <li
              key={stage}
              aria-current={current ? 'step' : undefined}
              className={cn(
                'rounded-lg border px-3 py-2.5',
                current && 'border-accent-500/50 bg-accent-500/10',
                completed && 'border-white/10 bg-white/[0.05]',
                !current && !completed && 'border-white/5 bg-transparent',
              )}
            >
              <p className={cn(
                'text-sm font-medium',
                current ? 'text-accent-100' : completed ? 'text-white' : 'text-ink-400',
              )}>
                {stageLabels[stage]}
              </p>
              <p className="mt-1 font-mono text-[11px] text-ink-500">{stage}</p>
            </li>
          );
        })}
        <li
          className={cn(
            'rounded-lg border px-3 py-2.5',
            failed ? 'border-rose-500/50 bg-rose-500/10' : 'border-white/5',
          )}
        >
          <p className={cn(
            'text-sm font-medium',
            failed ? 'text-rose-100' : 'text-ink-400',
          )}>
            {stageLabels.start_failed}
          </p>
          <p className="mt-1 font-mono text-[11px] text-ink-500">start_failed</p>
        </li>
      </ol>
    </div>
  );
}
