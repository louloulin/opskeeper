export const OPSKEEPER_TABS = [
  { id: 'incident-command', label: '事故指挥', description: '七阶段事故闭环与协同读back' },
  { id: 'evidence-approval', label: '证据审批', description: '审批证据与执行边界' },
  { id: 'archive-replay', label: '复盘档案', description: '事故证据链、完整性与复盘反查' },
  { id: 'system-status', label: '系统状态', description: '健康、依赖、指标与最近事故' },
];

const OPSKEEPER_TAB_ALIASES = new Map([
  ['diagnostics', 'incident-command'],
  ['integration', 'incident-command'],
  ['plugins', 'incident-command'],
  ['archive', 'archive-replay'],
  ['runtime', 'system-status'],
]);

export function normalizeOpskeeperTab(value) {
  return OPSKEEPER_TAB_ALIASES.get(value) ?? (
    OPSKEEPER_TABS.some((item) => item.id === value) ? value : 'incident-command'
  );
}
