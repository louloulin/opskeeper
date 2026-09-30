import * as React from 'react';
import OpskeeperRoute from './route.jsx';
import OpskeeperArchiveRoute from './archive-route.jsx';
import OpskeeperRuntimeRoute from './runtime-route.jsx';
import OpskeeperInstallView from './install-view.jsx';
import OpskeeperIntegrationRoute from './integration-route.jsx';
import IncidentCommandRoute from './incident-command/IncidentCommandRoute.jsx';
import DiagnosticsMenu from './incident-command/DiagnosticsMenu.jsx';
import { opskeeperCommandThemeStyle, opskeeperPluginThemeStyle } from './plugin-theme.js';
import { OPSKEEPER_TABS, normalizeOpskeeperTab } from './tabs.js';

function resolveSecondaryDiagnostics(value) {
  return ['diagnostics', 'integration', 'plugins'].includes(value)
    ? value
    : 'diagnostics';
}

export default function OpskeeperUnifiedRoute({ api, initialTab = 'incident-command' }) {
  const [tab, setTab] = React.useState(() => normalizeOpskeeperTab(initialTab));
  const [diagnosticsView, setDiagnosticsView] = React.useState(
    () => resolveSecondaryDiagnostics(initialTab),
  );

  return (
    <div style={{ ...opskeeperPluginThemeStyle, ...opskeeperCommandThemeStyle }}>
      <header style={{
        display: 'flex',
        alignItems: 'center',
        gap: 12,
        padding: '18px 24px 0',
      }}>
        <span style={{ fontSize: 24 }}>🛡️</span>
        <div>
          <h1 style={{ margin: 0, fontSize: 20 }}>OpsKeeper</h1>
          <p style={{ margin: '3px 0 0', fontSize: 12, color: 'var(--muted-foreground)' }}>
            AgentTeams 协同入口：事故指挥、证据审批、复盘与系统状态统一读back。
          </p>
        </div>
      </header>
      <nav style={{
        display: 'flex',
        gap: 6,
        padding: '14px 24px 18px',
        borderBottom: '1px solid var(--border)',
      }}>
        {OPSKEEPER_TABS.map((item) => {
          const active = item.id === tab;
          return (
            <button
              key={item.id}
              type="button"
              aria-current={active ? 'page' : undefined}
              title={item.description}
              onClick={() => setTab(item.id)}
              style={{
                padding: '6px 13px',
                borderRadius: 999,
                fontSize: 12,
                fontWeight: active ? 600 : 400,
                border: `1px solid ${active ? 'var(--primary)' : 'var(--border)'}`,
                background: active ? 'var(--primary)' : 'transparent',
                color: active ? 'var(--primary-foreground)' : 'var(--card-foreground)',
                boxShadow: active ? '0 0 0 2px var(--ops-focus-ring)' : 'none',
                cursor: 'pointer',
              }}
            >
              {item.label}
            </button>
          );
        })}
      </nav>
      {tab === 'incident-command' && (
        <section aria-label="事故指挥与诊断">
          <IncidentCommandRoute
            api={api}
            onOpenDiagnostics={setDiagnosticsView}
          />
          <section aria-label="事故诊断工具" style={{ padding: '14px 24px 24px', display: 'grid', gap: 10 }}>
            <DiagnosticsMenu view={diagnosticsView} onSelect={setDiagnosticsView} />
            {diagnosticsView === 'diagnostics' && <OpskeeperRoute api={api} />}
            {diagnosticsView === 'integration' && <OpskeeperIntegrationRoute api={api} />}
            {diagnosticsView === 'plugins' && <OpskeeperInstallView api={api} />}
          </section>
        </section>
      )}
      {tab === 'evidence-approval' && <OpskeeperRoute api={api} />}
      {tab === 'archive-replay' && <OpskeeperArchiveRoute api={api} />}
      {tab === 'system-status' && <OpskeeperRuntimeRoute api={api} />}
    </div>
  );
}
