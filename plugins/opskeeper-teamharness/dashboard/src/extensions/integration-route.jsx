import * as React from 'react';
import { opskeeperApi } from './api.js';
import { buildIntegrationPreflight } from './integration.js';

const STATUS_STYLES = {
  pass: { label: '通过', color: '#16a34a' },
  warn: { label: '警告', color: '#d97706' },
  fail: { label: '阻塞', color: '#dc2626' },
};

function readStoredRoomId(storage = globalThis.localStorage) {
  try {
    return storage?.getItem('opskeeper-integration-room') || '';
  } catch {
    return '';
  }
}

function storeRoomId(roomId, storage = globalThis.localStorage) {
  try {
    if (roomId) storage?.setItem('opskeeper-integration-room', roomId);
    else storage?.removeItem('opskeeper-integration-room');
  } catch {
  }
}

export default function OpskeeperIntegrationRoute() {
  const [roomId, setRoomId] = React.useState(() => readStoredRoomId());
  const [checking, setChecking] = React.useState(false);
  const [report, setReport] = React.useState(null);

  const runPreflight = async () => {
    setChecking(true);
    try {
      const normalizedRoomId = roomId.trim();
      setRoomId(normalizedRoomId);
      storeRoomId(normalizedRoomId);
      const results = await Promise.allSettled([
        opskeeperApi.getDashboardSession(),
        opskeeperApi.getMatrixSync(),
        opskeeperApi.getAgentTeamsHealth(),
        opskeeperApi.health(),
        opskeeperApi.getPluginHealth('opskeeper-teamharness'),
        opskeeperApi.getDashboardPluginManifest('opskeeper-teamharness'),
      ]);
      const [session, matrixSync, agentteamsHealth, opskeeperHealth, pluginHealth, manifest] = results;
      setReport(buildIntegrationPreflight({
        session,
        matrixSync,
        agentteamsHealth,
        opskeeperHealth,
        pluginHealth,
        manifest,
      }, {
        targetRoomId: normalizedRoomId,
        expectedPluginVersion: '1.0.56',
      }));
    } finally {
      setChecking(false);
    }
  };

  const summary = report ? STATUS_STYLES[report.status] : null;
  return (
    <section style={{ padding: 24, display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{
        padding: 16,
        borderRadius: 8,
        border: '1px solid var(--border)',
        background: 'var(--card)',
        display: 'flex',
        flexDirection: 'column',
        gap: 12,
      }}>
        <div>
          <div style={{ fontSize: 15, fontWeight: 600 }}>房间 ⇄ OpsKeeper 集成自检</div>
          <div style={{ fontSize: 12, color: 'var(--muted-foreground)', marginTop: 4 }}>
            只读检查 Dashboard 会话、Matrix 房间、AgentTeams、OpsKeeper 与 TeamHarness 插件链路；不会发送房间消息或修改数据。
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
          <input
            value={roomId}
            onChange={(event) => setRoomId(event.target.value)}
            placeholder="目标房间 ID 或 alias，例如 #team:example.com"
            style={{
              flex: '1 1 320px',
              minWidth: 220,
              padding: '8px 10px',
              borderRadius: 6,
              border: '1px solid var(--border)',
              background: 'var(--background)',
              color: 'var(--foreground)',
              fontSize: 12,
            }}
          />
          <button
            type="button"
            onClick={runPreflight}
            disabled={checking}
            style={{
              padding: '8px 14px',
              borderRadius: 6,
              border: '1px solid var(--primary)',
              background: checking ? 'var(--muted)' : 'var(--primary)',
              color: 'var(--primary-foreground)',
              fontSize: 12,
              fontWeight: 600,
              cursor: checking ? 'wait' : 'pointer',
            }}
          >
            {checking ? '检测中…' : '一键检测'}
          </button>
        </div>
        {summary && (
          <div style={{ fontSize: 12, color: summary.color, fontWeight: 600 }}>
            {summary.label} · 已加入房间 {report.joinedRoomCount} 个 · {new Date(report.checkedAt).toLocaleString()}
          </div>
        )}
      </div>

      {report && (
        <div style={{
          borderRadius: 8,
          border: '1px solid var(--border)',
          background: 'var(--card)',
          overflow: 'hidden',
        }}>
          {report.checks.map((item, index) => {
            const tone = STATUS_STYLES[item.status];
            return (
              <div key={item.name} style={{
                display: 'grid',
                gridTemplateColumns: '96px minmax(140px, 220px) 1fr',
                gap: 12,
                padding: '11px 14px',
                borderTop: index === 0 ? 0 : '1px solid var(--border)',
                fontSize: 12,
                alignItems: 'start',
              }}>
                <span style={{ color: tone.color, fontWeight: 600 }}>{tone.label}</span>
                <span style={{ fontWeight: 600 }}>{item.name}</span>
                <span style={{ color: 'var(--muted-foreground)', wordBreak: 'break-word' }}>{item.detail}</span>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
