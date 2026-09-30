import * as React from 'react';

const FACT_LABELS = [
  ['incidentId', '事故标识'],
  ['candidateId', '候选修复标识'],
  ['executionId', '执行身份'],
  ['targetFingerprint', '目标指纹'],
  ['workloadFingerprint', '负载指纹'],
  ['impactScope', '影响范围'],
  ['parameters', '执行参数'],
  ['expiresAt', '有效期'],
  ['serverNow', '权威服务器时间'],
  ['rollbackPlan', '回滚计划'],
  ['previewEligibility', '预览资格'],
  ['verificationCriteria', '验证标准'],
  ['approvalStatus', '权威审批状态'],
  ['channel', '审批渠道'],
];

function displayValue(value) {
  if (value == null || value === '') return '缺失';
  if (typeof value === 'object') return JSON.stringify(value, null, 2);
  return String(value);
}

function approvalStatusLabel(value) {
  if (value === 'awaiting_human' || value === 'awaiting_approval') return '等待人工决策';
  if (value === 'approved') return '已有权威审批记录';
  if (value === 'rejected') return '已有权威拒绝记录';
  return value || '未知';
}

export default function ApprovalChecklist({ facts = {}, locale = 'zh-CN' }) {
  const [copyState, setCopyState] = React.useState('idle');
  const missingContext = [
    ...(!facts.incidentId ? ['缺少 incident 精确上下文'] : []),
    ...(!facts.candidateId ? ['缺少 candidate 精确上下文'] : []),
    ...(!facts.executionId ? ['缺少执行身份'] : []),
    ...(!facts.targetFingerprint ? ['缺少目标指纹'] : []),
    ...(!facts.workloadFingerprint ? ['缺少负载指纹'] : []),
    ...(!facts.rollbackPlan ? ['缺少回滚计划'] : []),
    ...(!facts.verificationCriteria ? ['缺少验证标准'] : []),
  ];

  const copyInstruction = async () => {
    if (!facts.instruction) return;
    try {
      if (typeof globalThis.navigator?.clipboard?.writeText !== 'function') throw new Error('clipboard unavailable');
      await globalThis.navigator.clipboard.writeText(facts.instruction);
      setCopyState('copied');
    } catch {
      setCopyState('unavailable');
    }
  };

  return (
    <section
      aria-label="精准人工审批清单"
      lang={locale}
      style={{
        '--ops-incident-checklist-border': 'var(--ops-surface-border, #94a3b8)',
        '--ops-incident-checklist-foreground': 'var(--ops-surface-foreground, #0f172a)',
        '--ops-incident-checklist-muted': 'var(--ops-muted-foreground, #475569)',
        border: '1px solid var(--ops-incident-checklist-border)',
        borderRadius: 6,
        padding: 10,
        display: 'grid',
        gap: 8,
        color: 'var(--ops-incident-checklist-foreground)',
      }}
    >
      <header style={{ display: 'grid', gap: 2 }}>
        <h3 style={{ margin: 0, fontSize: 13 }}>精准人工审批清单</h3>
        <p style={{ margin: 0, fontSize: 11, color: 'var(--ops-incident-checklist-muted)' }}>
          本清单仅呈现事实，不授予执行权限；Manager 是唯一验证与记录方。
        </p>
      </header>
      {missingContext.length > 0 && (
        <div role="alert" style={{
          border: '1px solid var(--ops-status-waiting, #b45309)',
          borderRadius: 4,
          padding: 6,
          color: 'var(--ops-status-waiting, #b45309)',
          fontSize: 11,
        }}>
          <strong>缺少审批上下文</strong>
          <ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>
            {missingContext.map((warning) => <li key={warning}>{warning}</li>)}
          </ul>
        </div>
      )}
      <dl style={{ margin: 0, display: 'grid', gap: 5, fontSize: 11 }}>
        {FACT_LABELS.map(([key, label]) => (
          <div key={key} style={{ display: 'grid', gridTemplateColumns: 'minmax(96px, 150px) 1fr', gap: 8 }}>
            <dt>{label}</dt>
            <dd style={{ margin: 0, overflowWrap: 'anywhere' }}>
              {key === 'approvalStatus' && facts.approvalStatus
                ? `${approvalStatusLabel(facts.approvalStatus)}（${facts.approvalStatus}）`
                : displayValue(facts[key])}
            </dd>
          </div>
        ))}
      </dl>
      <div style={{ display: 'grid', gap: 4 }}>
        <div style={{ display: 'grid', gridTemplateColumns: 'minmax(96px, 150px) 1fr', gap: 8, fontSize: 11 }}>
          <strong>精确指令</strong>
          <code style={{ overflowWrap: 'anywhere' }}>{facts.instruction || '无法生成精确审批指令'}</code>
        </div>
        <button
          type="button"
          onClick={copyInstruction}
          disabled={!facts.instruction}
          aria-live="polite"
          style={{
            justifySelf: 'start',
            border: '1px solid var(--ops-incident-checklist-border)',
            borderRadius: 4,
            background: 'transparent',
            color: 'var(--ops-incident-checklist-foreground)',
            font: 'inherit',
            fontSize: 11,
            padding: '4px 7px',
          }}
        >
          {copyState === 'copied' ? '已复制精确指令' : copyState === 'unavailable' ? '剪贴板不可用，请手动复制' : '复制精确审批指令'}
        </button>
      </div>
    </section>
  );
}
