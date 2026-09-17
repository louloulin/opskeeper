export const OPSKEEPER_TABS = [
  { id: 'diagnostics', label: '诊断', description: '7 阶段 RCA 与事故闭环' },
  { id: 'integration', label: '链路自检', description: '房间、AgentTeams、OpsKeeper 与 TeamHarness 只读自检' },
  { id: 'archive', label: '档案', description: '事故证据链、完整性与复盘反查' },
  { id: 'runtime', label: 'Runtime', description: '健康、依赖、指标与最近事故' },
  { id: 'plugins', label: '插件', description: '安装包管理与加载状态' },
];

export function normalizeOpskeeperTab(value) {
  const tab = OPSKEEPER_TABS.find((item) => item.id === value);
  return tab ? tab.id : 'diagnostics';
}
