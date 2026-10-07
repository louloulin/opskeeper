import * as React from 'react';

const KIND_COPY = {
  wait: '等待',
  'inspect-evidence': '检查',
  approve: '审批',
  reject: '拒绝',
  verify: '验证',
  archive: '归档',
  retry: '重试',
};

export default function NextActionCard({ action, locale = 'zh-CN' }) {
  if (!action) {
    return (
      <section aria-label="下一步动作" style={{ border: '1px solid var(--ops-surface-border)' }}>
        <p style={{ margin: 12 }}>下一步动作未知</p>
      </section>
    );
  }

  return (
    <section
      aria-label="下一步动作"
      lang={locale}
      style={{
        border: '1px solid var(--ops-surface-border)',
        borderLeft: '3px solid var(--ops-status-active)',
        background: 'var(--ops-surface)',
        color: 'var(--ops-surface-foreground)',
        borderRadius: 8,
        padding: 12,
      }}
    >
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 10 }}>
        <h3 style={{ margin: 0, fontSize: 13 }}>{action.label || '下一步动作未知'}</h3>
        <output
          style={{
            fontSize: 12,
            fontWeight: 600,
            fontVariantNumeric: 'tabular-nums',
          }}
        >
          P{Number.isFinite(Number(action.priority)) ? action.priority : '??'}
        </output>
      </div>
      <p style={{ margin: '6px 0 0', fontSize: 12 }}>
        动作类型：{KIND_COPY[action.kind] || action.kind || '未知'}
      </p>
      {action.detail && <p style={{ margin: '5px 0 0', fontSize: 12 }}>{action.detail}</p>}
      {action.disabledReason && (
        <p style={{ margin: '5px 0 0', fontSize: 12 }}>
          当前不可执行：{action.disabledReason}
        </p>
      )}
    </section>
  );
}
