import { useCallback, useEffect, useState } from 'react';
import { ShieldCheck, RefreshCw, Check, X, ChevronDown, ChevronRight } from 'lucide-react';
import { listApprovals, approveApproval, rejectApproval, type Approval } from '@/api/approvals';
import { ApiError } from '@/api/client';
import { useI18n } from '@/i18n/locale';
import { Chip, PageHeader } from '@/components/ui';
import { parseSigners, dualSignState, signerWording } from '@/lib/approvalSigners';

// Approvals inbox (HLD-017 propose-confirm). Dangerous actions proposed by
// the agent (or a flow approval node) wait here; an admin approves (→ runs)
// or rejects. Default view = pending.

const STATUSES = ['pending', 'approved', 'executed', 'rejected', 'failed'] as const;

export default function ApprovalsPage() {
  const { tr } = useI18n();
  const [items, setItems] = useState<Approval[]>([]);
  const [status, setStatus] = useState<string>('pending');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await listApprovals(status);
      setItems(r.items ?? []);
      setErr(null);
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [status]);

  useEffect(() => {
    void load();
  }, [load]);

  const onApprove = async (a: Approval) => {
    if (!window.confirm(tr(`确认批准并执行：${a.title}？`, `Approve and execute: ${a.title}?`))) return;
    setBusy(a.id);
    try {
      await approveApproval(a.id);
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy('');
    }
  };

  const onReject = async (a: Approval) => {
    const reason = window.prompt(tr('拒绝原因（可选）', 'Reject reason (optional)')) ?? '';
    setBusy(a.id);
    try {
      await rejectApproval(a.id, reason);
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : (e as Error).message);
    } finally {
      setBusy('');
    }
  };

  return (
    <main className="anim-fade flex flex-1 flex-col overflow-hidden">
      <PageHeader
        title={tr('待确认', 'Approvals')}
        subtitle={tr('Agent / 工作流提交的危险操作，需人工批准后才执行', 'Dangerous actions proposed by agents / flows — execute only after a human approves')}
        actions={
          <button
            type="button"
            onClick={() => void load()}
            className="inline-flex items-center gap-1.5 rounded-md border border-zinc-700 px-2.5 py-1.5 text-[12px] text-zinc-300 hover:bg-zinc-800"
          >
            <RefreshCw size={13} />
            {tr('刷新', 'Refresh')}
          </button>
        }
        extra={
          <div className="-mb-2 flex items-center gap-1">
            {STATUSES.map((s) => (
              <button
                key={s}
                type="button"
                onClick={() => setStatus(s)}
                className={`rounded-md px-2.5 py-1 text-[12px] transition-colors ${
                  status === s ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'
                }`}
              >
                {statusLabel(s, tr)}
              </button>
            ))}
          </div>
        }
      />

      <div className="flex-1 overflow-auto px-6 py-4">
        {err && <div className="mb-3 rounded-md border border-red-900/50 bg-red-950/30 px-3 py-2 text-[12px] text-red-400">{err}</div>}
        {loading ? (
          <div className="flex h-40 items-center justify-center text-sm text-zinc-500">{tr('加载中…', 'Loading…')}</div>
        ) : items.length === 0 ? (
          <div className="flex h-40 flex-col items-center justify-center gap-2 text-zinc-600">
            <ShieldCheck size={28} className="text-zinc-700" />
            <div className="text-[13px]">{status === 'pending' ? tr('没有待确认的操作', 'No actions awaiting approval') : tr('暂无记录', 'Nothing here')}</div>
          </div>
        ) : (
          <div className="space-y-2">
            {items.map((a) => (
              <div key={a.id} className="surface-card rounded-2xl p-3">
                <div className="flex items-start gap-3">
                  <button
                    type="button"
                    onClick={() => setExpanded((e) => ({ ...e, [a.id]: !e[a.id] }))}
                    className="mt-0.5 text-zinc-500 hover:text-zinc-300"
                  >
                    {expanded[a.id] ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
                  </button>
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="font-medium text-zinc-100">{a.title}</span>
                      <StatusChip status={a.status} tr={tr} />
                      <span className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-[10px] text-zinc-500">{a.kind}</span>
                    </div>
                    {a.summary && <div className="mt-1 whitespace-pre-wrap text-[12px] text-zinc-400">{a.summary}</div>}
                    {(a.blast_radius || a.risk_class || a.target) && (
                      <div className="mt-1 flex flex-wrap items-center gap-1.5">
                        {a.blast_radius && <Chip tone="warning" dense>{tr('影响面', 'Blast radius')}: {a.blast_radius}</Chip>}
                        {a.risk_class && <Chip tone={a.risk_class === 'destructive' ? 'danger' : 'default'} dense>{tr('风险等级', 'Risk')}: {a.risk_class}</Chip>}
                        {a.target && <span className="font-mono text-[11px] text-zinc-400">{a.target}</span>}
                      </div>
                    )}
                    <div className="mt-1 text-[11px] text-zinc-600">
                      {tr('来源', 'source')}: {a.source}
                      {a.session_id ? ` · ${a.session_id.slice(0, 8)}` : ''} · {new Date(a.created_at).toLocaleString()}
                    </div>
                    <SignerProgress approval={a} />
                    {expanded[a.id] && (
                      <div className="mt-2 space-y-1">
                        <div className="text-[11px] text-zinc-500">{tr('操作内容', 'Action payload')}</div>
                        <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[10px] text-zinc-400">{prettify(a.payload)}</pre>
                        {a.result && (
                          <>
                            <div className="text-[11px] text-zinc-500">{tr('执行结果', 'Result')}</div>
                            <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950 p-2 text-[10px] text-zinc-400">{prettify(a.result)}</pre>
                          </>
                        )}
                        {a.reason && <div className="text-[11px] text-amber-400/80">{tr('原因', 'reason')}: {a.reason}</div>}
                      </div>
                    )}
                  </div>
                  {a.status === 'pending' && (
                    <div className="flex shrink-0 items-center gap-1.5">
                      <button
                        type="button"
                        onClick={() => void onApprove(a)}
                        disabled={busy === a.id}
                        className="inline-flex items-center gap-1 rounded-md border border-emerald-700 bg-emerald-950/30 px-2 py-1 text-[12px] text-emerald-300 hover:bg-emerald-900/40 disabled:opacity-40"
                      >
                        <Check size={13} />
                        {tr('批准', 'Approve')}
                      </button>
                      <button
                        type="button"
                        onClick={() => void onReject(a)}
                        disabled={busy === a.id}
                        className="inline-flex items-center gap-1 rounded-md border border-zinc-700 px-2 py-1 text-[12px] text-zinc-400 hover:border-red-800 hover:text-red-400 disabled:opacity-40"
                      >
                        <X size={13} />
                        {tr('拒绝', 'Reject')}
                      </button>
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </main>
  );
}

function statusLabel(s: string, tr: (zh: string, en: string) => string): string {
  switch (s) {
    case 'pending':
      return tr('待确认', 'Pending');
    case 'approved':
      return tr('已批准', 'Approved');
    case 'executed':
      return tr('已执行', 'Executed');
    case 'rejected':
      return tr('已拒绝', 'Rejected');
    case 'failed':
      return tr('失败', 'Failed');
    default:
      return s;
  }
}

// console-reskin delta spec：状态标签用 rounded-full 胶囊 + 呼吸点。
// 呼吸点复用 tailwind.config.ts 里既有的 pulse-dot keyframes（纯 opacity 明暗，
// 不是缩放/位移），并用内置 motion-safe 变体卡在 prefers-reduced-motion 之外——
// 开了「减少动态效果」的用户看到的是一个静止的圆点，而不是被动画反复闪。
// 圆点颜色与所在状态的语义色同系，不另引入色板。
function StatusChip({ status, tr }: { status: string; tr: (zh: string, en: string) => string }) {
  const cls =
    status === 'pending'
      ? 'bg-amber-900/40 text-amber-300'
      : status === 'executed' || status === 'approved'
        ? 'bg-emerald-900/40 text-emerald-300'
        : status === 'failed'
          ? 'bg-red-900/40 text-red-300'
          : 'bg-zinc-800 text-zinc-400';
  const dot =
    status === 'pending'
      ? 'bg-amber-400'
      : status === 'executed' || status === 'approved'
        ? 'bg-emerald-400'
        : status === 'failed'
          ? 'bg-red-400'
          : 'bg-zinc-500';
  return (
    <span className={`inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium ${cls}`}>
      <span className={`h-1 w-1 shrink-0 rounded-full motion-safe:animate-pulse-dot ${dot}`} aria-hidden="true" />
      {statusLabel(status, tr)}
    </span>
  );
}

function prettify(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// SignerProgress 呈现一行审批的双签进度,取自共享基元 approvalSigners。
// decided(非 pending) 不渲染;unknown 作中性提示,不禁用任何操作按钮。
function SignerProgress({ approval }: { approval: Approval }) {
  const { tr } = useI18n();
  const state = dualSignState(approval);
  if (state === 'decided') return null;
  const { signers } = parseSigners(approval.signers);
  const w = signerWording(state, signers.length);
  return (
    <div className="mt-1.5 space-y-0.5 text-[11px]">
      <div className={state === 'partial' ? 'text-amber-400/90' : 'text-zinc-500'}>
        {tr(w.zh, w.en)}
      </div>
      {state === 'partial' && (
        <ul className="flex flex-wrap gap-x-3 gap-y-0.5 text-zinc-500">
          {signers.map((s, i) => (
            <li key={`${s.user_id}-${i}`} className="font-mono">
              {s.role ?? String(s.user_id)}
              {s.at ? ` · ${new Date(s.at).toLocaleString()}` : ''}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
