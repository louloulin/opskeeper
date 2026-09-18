import * as React from 'react';
import { opskeeperApi } from './api.js';

// Plugin source/status color codes. Aligned with agentteams-plugin-installer
// (`installed/enabled/disabled/system/dashboard`) so the OpsKeeper
// 插件管理 tab and the upstream plugin-installer surface read identically.
const STATUS_META = {
  installed: { tone: 'zinc',   label: '已安装' },
  enabled:   { tone: 'success', label: '已启用' },
  disabled:  { tone: 'warning', label: '已停用' },
  system:    { tone: 'info',    label: '内置' },
  dashboard: { tone: 'info',    label: '仅面板' },
  error:     { tone: 'danger',  label: '异常' },
};

const TONE_COLORS = {
  success: { fg: '#10b981', bg: 'rgba(16,185,129,0.12)', border: 'rgba(16,185,129,0.4)' },
  warning: { fg: '#d97706', bg: 'rgba(217,119,6,0.12)',  border: 'rgba(217,119,6,0.4)' },
  info:    { fg: '#0891b2', bg: 'rgba(8,145,178,0.12)',   border: 'rgba(8,145,178,0.4)' },
  zinc:    { fg: '#475569', bg: 'rgba(71,85,105,0.12)',  border: 'rgba(71,85,105,0.4)' },
  danger:  { fg: '#dc2626', bg: 'rgba(220,38,38,0.12)',  border: 'rgba(220,38,38,0.4)' },
};

function StatusBadge({ status }) {
  const raw = String(status || '').trim().toLowerCase();
  const meta = STATUS_META[raw] || { tone: 'zinc', label: status || 'unknown' };
  const colors = TONE_COLORS[meta.tone] || TONE_COLORS.zinc;
  return (
    <span
      title={`状态：${meta.label}`}
      style={{
        marginLeft: 'auto',
        padding: '1px 8px',
        borderRadius: 999,
        fontSize: 10,
        fontWeight: 600,
        letterSpacing: 0.2,
        color: colors.fg,
        background: colors.bg,
        border: `1px solid ${colors.border}`,
        textTransform: 'lowercase',
      }}
    >
      {meta.label}
    </span>
  );
}

// OpskeeperInstallView — Dashboard surface that lists currently-installed
// opskeeper plugins and lets the operator upload a new plugin package.
//
// Wire:
//   GET  /api/v1/plugins        — list (via public Plugin Manager proxy)
//   POST /api/v1/plugins/install — upload package (multipart/form-data)
//
// The Dashboard proxy in turn calls Manager → worker
// /api/opskeeper-teamharness/install-plugin → qwenpaw plugin install. The
// whole chain can take 30s+, so we surface progress + a busy state.
export default function OpskeeperInstallView({ api }) {
  const [plugins, setPlugins] = React.useState([]);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState(null);
  const [selectedFile, setSelectedFile] = React.useState(null);
  const [installing, setInstalling] = React.useState(false);
  const [installProgress, setInstallProgress] = React.useState(0);
  const [installResult, setInstallResult] = React.useState(null);

  const refresh = React.useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await opskeeperApi.listPlugins();
      setPlugins(data.plugins || data.data || []);
    } catch (e) {
      setError(e.message);
      setPlugins([]);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    refresh();
  }, [refresh]);

  function onPickFile(ev) {
    const f = ev.target.files && ev.target.files[0];
    setSelectedFile(f || null);
    setInstallResult(null);
  }

  async function onInstall() {
    if (!selectedFile || installing) return;
    setInstalling(true);
    setInstallProgress(0);
    setInstallResult(null);
    try {
      const result = await opskeeperApi.installPlugin(selectedFile, {
        onProgress: ({ loaded, total }) => {
          const pct = total ? Math.min(100, Math.round((loaded / total) * 100)) : 0;
          setInstallProgress(pct);
        },
      });
      setInstallResult({ ok: true, data: result });
      api?.dashboard?.toast?.('插件安装成功', 'success');
      setSelectedFile(null);
      await refresh();
    } catch (e) {
      setInstallResult({ ok: false, error: e.message, body: e.body });
      api?.dashboard?.toast?.(`安装失败：${e.message}`, 'error');
    } finally {
      setInstalling(false);
    }
  }

  return (
    <div style={{ padding: 24, display: 'flex', flexDirection: 'column', gap: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <h2 style={{ margin: 0, fontSize: 18 }}>opskeeper 插件管理</h2>
        <button
          onClick={refresh}
          disabled={loading}
          style={{
            padding: '4px 10px', fontSize: 12, borderRadius: 4,
            border: '1px solid var(--border)', background: 'var(--card)',
            color: 'var(--card-foreground)', cursor: loading ? 'wait' : 'pointer',
          }}
        >
          {loading ? '…' : '刷新'}
        </button>
      </div>

      {/* Installed plugin list */}
      <div style={{
        padding: 14, borderRadius: 8, border: '1px solid var(--border)',
        background: 'var(--card)',
      }}>
        <div style={{ fontSize: 12, color: 'var(--muted-foreground)', marginBottom: 8 }}>
          已安装插件
        </div>
        {error && (
          <div style={{
            padding: 10, fontSize: 12, borderRadius: 6,
            background: 'rgba(220,38,38,0.1)', color: '#ef4444',
            border: '1px solid #ef4444', marginBottom: 8,
          }}>
            加载失败：{error}
          </div>
        )}
        {!loading && plugins.length === 0 && (
          <div style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>
            暂无已安装插件
          </div>
        )}
        {plugins.map((p) => (
          <div
            key={p.id}
            style={{
              padding: 10, borderRadius: 6, marginBottom: 6,
              border: '1px solid var(--border)', background: 'var(--background)',
              display: 'flex', flexDirection: 'column', gap: 4,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <strong style={{ fontSize: 13 }}>{p.id}</strong>
              <span style={{
                fontSize: 10, padding: '1px 6px', borderRadius: 3,
                background: 'var(--muted)', color: 'var(--muted-foreground)',
              }}>
                v{p.version}
              </span>
              <StatusBadge status={p.status} />
            </div>
            <div style={{ fontSize: 11, color: 'var(--foreground)', opacity: 0.76, lineHeight: 1.5 }}>
              {p.description?.split('\n')[0].slice(0, 200)}
            </div>
            <div style={{ display: 'flex', gap: 12, fontSize: 11, color: 'var(--foreground)', opacity: 0.72 }}>
              <span>skills: {p.skill_count ?? '—'}</span>
              <span>tools: {p.tool_count ?? '—'}</span>
              <span>prompts: {p.prompt_count ?? '—'}</span>
              <span>adapters: {(p.adapter_ids || []).join(', ') || '—'}</span>
            </div>
          </div>
        ))}
      </div>

      {/* Upload + install */}
      <div style={{
        padding: 14, borderRadius: 8, border: '1px solid var(--border)',
        background: 'var(--card)',
      }}>
        <div style={{ fontSize: 12, color: 'var(--muted-foreground)', marginBottom: 8 }}>
          上传并安装新插件
        </div>
        <input
          type="file"
          accept=".tar.gz,.zip"
          onChange={onPickFile}
          disabled={installing}
          style={{
            display: 'block', width: '100%', padding: 8,
            fontSize: 12, borderRadius: 4,
            border: '1px solid var(--border)', background: 'var(--background)',
            color: 'var(--card-foreground)',
            marginBottom: 8,
          }}
        />
        {selectedFile && (
          <div style={{ fontSize: 11, color: 'var(--muted-foreground)', marginBottom: 8 }}>
            已选：<code>{selectedFile.name}</code>
            {' '}({(selectedFile.size / 1024).toFixed(1)} KiB)
          </div>
        )}
        <button
          onClick={onInstall}
          disabled={!selectedFile || installing}
          style={{
            padding: '6px 14px', fontSize: 12, borderRadius: 4,
            border: 'none',
            background: !selectedFile || installing ? 'var(--muted)' : 'var(--primary)',
            color: 'var(--primary-foreground)',
            cursor: !selectedFile || installing ? 'not-allowed' : 'pointer',
          }}
        >
          {installing ? `上传中… ${installProgress}%` : '上传并安装'}
        </button>

        {installing && (
          <div style={{
            marginTop: 10, height: 6, borderRadius: 3,
            background: 'var(--muted)', overflow: 'hidden',
          }}>
            <div style={{
              width: `${installProgress}%`, height: '100%',
              background: 'var(--primary)',
              transition: 'width 0.2s ease',
            }} />
          </div>
        )}

        {installResult?.ok && (
          <div style={{
            marginTop: 10, padding: 10, borderRadius: 6, fontSize: 12,
            background: 'rgba(16,185,129,0.1)', color: '#10b981',
            border: '1px solid #10b981',
          }}>
            ✅ 安装成功 — v{installResult.data?.version ?? '?'}
            {installResult.data?.skills && `, ${installResult.data.skills} skills`}
            {installResult.data?.tools && `, ${installResult.data.tools} tools`}
          </div>
        )}

        {installResult && !installResult.ok && (
          <div style={{
            marginTop: 10, padding: 10, borderRadius: 6, fontSize: 12,
            background: 'rgba(220,38,38,0.1)', color: '#ef4444',
            border: '1px solid #ef4444',
          }}>
            ❌ 安装失败：{installResult.error}
            {installResult.body?.detail && (
              <pre style={{
                marginTop: 6, padding: 8, fontSize: 10,
                background: '#0a0a0a', color: '#eee', borderRadius: 4,
                overflow: 'auto', maxHeight: 200,
              }}>
                {typeof installResult.body.detail === 'string'
                  ? installResult.body.detail
                  : JSON.stringify(installResult.body.detail, null, 2)}
              </pre>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
