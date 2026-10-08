// DeliverableCard — the only place a chat link turns into a card.
// Deliverable hrefs exist purely as markdown text inside message.content, so
// ReactMarkdown's `components.a` in AssistantBubble is the single interception
// point (D4). 换卡点(MessageBubble.tsx:142-143)不改。
import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { BarChart3, FileText, Loader2 } from 'lucide-react';
import { tr } from '@/i18n/locale';
import { fetchPageHTML } from '@/api/pages';
import { getReport, type ReportDetail, type ReportKind } from '@/api/reports';
import { shouldRenderThumb, useInViewOnce } from './deliverableLazy';
import { HostedPageView } from './HostedPageView';
import { Button } from './ui/Button';

const PAGE_RE = /^\/pages\/([0-9a-f]{16,64})$/;
const REPORT_RE = /^\/reports\/([0-9a-f-]{16,64})$/;

export type DeliverableInfo = { type: 'page' | 'report'; id: string; href: string };

export function matchDeliverable(href: string): DeliverableInfo | null {
  const page = PAGE_RE.exec(href);
  if (page) return { type: 'page', id: page[1], href };
  const report = REPORT_RE.exec(href);
  if (report) return { type: 'report', id: report[1], href };
  return null;
}

// extractHtmlTitle — 托管页标题的唯一零后端改动来源(design §3)。
// 解析不到 → 空串(留空,不编造)。
export function extractHtmlTitle(html: string): string {
  const m = /<title[^>]*>([\s\S]*?)<\/title>/i.exec(html);
  return m ? m[1].trim() : '';
}

type LoadState = 'idle' | 'loading' | 'ready' | 'failed';

// --- 单条消息交付物序号(缩略上限用,design §5;Task 7 由 MessageBubble 接线)---
// 卡片首次渲染时 claim 一个序号;没有 Provider(单测直渲染)时序号恒为 0。
const SequenceCtx = createContext<(() => number) | null>(null);

export function DeliverableSequence({ children }: { children: ReactNode }) {
  const counter = useRef(0);
  const claim = useCallback(() => counter.current++, []);
  return <SequenceCtx.Provider value={claim}>{children}</SequenceCtx.Provider>;
}

export function DeliverableCard({ info }: { info: DeliverableInfo }) {
  const claim = useContext(SequenceCtx);
  const indexRef = useRef(-1);
  if (claim && indexRef.current < 0) indexRef.current = claim();
  const index = claim ? indexRef.current : 0;
  const thumbAllowed = shouldRenderThumb(index);

  const cardRef = useRef<HTMLSpanElement>(null);
  const visible = useInViewOnce(cardRef);
  const [state, setState] = useState<LoadState>('idle');
  const [pageTitle, setPageTitle] = useState('');
  const [html, setHtml] = useState<string | null>(null);
  const [report, setReport] = useState<ReportDetail | null>(null);

  // 卸载后不再 setState。用 ref 而不是 effect 局部 `let alive`:下面取数 effect 的
  // 依赖含 state,setState('loading') 会让它重跑并 cleanup 掉局部 alive,响应回来后
  // 成功与失败两条分支都会被静默丢弃(卡片永远停在 loading)。
  // 挂载 effect 开头重置为 true,StrictMode 双调用下也不会残留 false。
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  // 三态状态机:visible 之前不发请求(idle);失败不重试,降级占位 + 保留出口。
  useEffect(() => {
    if (!visible || state !== 'idle' || !thumbAllowed) return;
    setState('loading');
    if (info.type === 'page') {
      fetchPageHTML(info.id)
        .then((h) => {
          if (!aliveRef.current) return;
          setHtml(h);
          setPageTitle(extractHtmlTitle(h));
          setState('ready');
        })
        .catch(() => aliveRef.current && setState('failed'));
    } else {
      getReport(info.id)
        .then((r) => {
          if (!aliveRef.current) return;
          setReport(r);
          setState('ready');
        })
        .catch(() => aliveRef.current && setState('failed'));
    }
  }, [visible, thumbAllowed, state, info.type, info.id]);

  const Icon = info.type === 'page' ? FileText : BarChart3;
  const typeLabel = info.type === 'page' ? tr('托管页', 'Hosted page') : tr('报表', 'Report');
  // 标题诚实来源:托管页 = HTML <title>;报表 = fetchReportDetail title;缺失留空。
  const title = info.type === 'page' ? pageTitle : report?.title ?? '';

  return (
    <span ref={cardRef} data-testid="deliverable-card" className="surface-card my-1.5 block rounded-rk-md">
      <span
        data-testid="deliverable-card-header"
        className="flex items-center gap-3 px-3 py-2.5"
      >
        <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-rk-sm bg-accent-50 text-accent-700">
          <Icon className="h-4 w-4" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-300">{typeLabel}</span>
            {title ? <span className="block truncate text-xs font-medium text-zinc-200">{title}</span> : null}
          </span>
          <span className="block truncate text-[11px] text-zinc-500">{info.href}</span>
        </span>
        <Button variant="ghost" onClick={() => window.open(info.href, '_blank')}>
          {tr('新窗口打开', 'Open in new tab')}
        </Button>
      </span>
      {thumbAllowed && (
        <span data-testid="deliverable-thumb" className="mx-3 mb-2.5 block h-40 overflow-hidden rounded-rk-sm border border-zinc-800">
          {state === 'idle' && <span className="block h-full w-full bg-zinc-900/40" />}
          {state === 'loading' && (
            <span className="flex h-full w-full items-center justify-center bg-zinc-900/40 text-zinc-500">
              <Loader2 className="h-4 w-4 animate-spin" />
            </span>
          )}
          {state === 'failed' && <TypePlaceholder type={info.type} failed />}
          {state === 'ready' && info.type === 'page' && html != null && <HostedPageView html={html} />}
          {state === 'ready' && info.type === 'report' && report != null && <ReportThumb report={report} />}
        </span>
      )}
    </span>
  );
}

// ReportThumb — 报表类型化占位缩略:类型 + 生成时间,无 img/无 iframe(spec:报表缩略为类型占位)。
// pending/generating → 生成中占位,不当作 failed(design §7);后端 failed → 降级占位。
function ReportThumb({ report }: { report: ReportDetail }) {
  if (report.status === 'pending' || report.status === 'generating') {
    return <TypePlaceholder type="report" line1={tr('报告生成中…', 'Report is generating…')} line2="" />;
  }
  if (report.status === 'failed') return <TypePlaceholder type="report" failed />;
  return <TypePlaceholder type="report" line1={reportKindLabel(report.kind)} line2={report.generated_at ?? ''} />;
}

function reportKindLabel(kind: ReportKind): string {
  switch (kind) {
    case 'daily': return tr('日报', 'Daily report');
    case 'weekly': return tr('周报', 'Weekly report');
    case 'monthly': return tr('月报', 'Monthly report');
    default: return tr('自定义报告', 'Custom report');
  }
}

// TypePlaceholder — 类型化占位(缩略失败降级 / 报表占位共用,design §1.2)。
function TypePlaceholder({ type, failed, line1, line2 }: { type: 'page' | 'report'; failed?: boolean; line1?: string; line2?: string }) {
  const Icon = type === 'page' ? FileText : BarChart3;
  return (
    <span className="flex h-full w-full flex-col items-center justify-center gap-1 bg-zinc-900/60 text-zinc-500">
      <Icon className="h-6 w-6" aria-hidden="true" />
      <span className="text-[11px]">{line1 ?? (failed ? tr('加载失败', 'Failed to load') : '')}</span>
      {line2 ? <span className="text-[10px] text-zinc-600">{line2}</span> : null}
    </span>
  );
}
