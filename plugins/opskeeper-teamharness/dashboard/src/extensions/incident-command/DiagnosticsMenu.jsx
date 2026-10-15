import * as React from 'react';

const DIAGNOSTICS_VIEWS = [
  { id: 'diagnostics', label: '诊断报告' },
  { id: 'integration', label: '链路自检' },
  { id: 'plugins', label: '插件管理' },
];

export default function DiagnosticsMenu({ view = 'diagnostics', onSelect }) {
  return (
    <nav aria-label="诊断导航" style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
      {DIAGNOSTICS_VIEWS.map((item) => {
        const active = item.id === view;
        return (
          <button
            key={item.id}
            type="button"
            aria-current={active ? 'true' : undefined}
            onClick={() => onSelect?.(item.id)}
            style={{
              borderRadius: 6,
              border: `1px solid ${active ? 'var(--ops-status-active)' : 'var(--ops-surface-border)'}`,
              background: active ? 'var(--ops-surface)' : 'transparent',
              color: 'var(--ops-surface-foreground)',
              cursor: 'pointer',
              fontSize: 12,
              padding: '5px 9px',
            }}
          >
            {item.label}
          </button>
        );
      })}
    </nav>
  );
}
