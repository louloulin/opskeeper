// DeliverableCard — the only place a chat link turns into a card.
// Deliverable hrefs exist purely as markdown text inside
// message.content, so ReactMarkdown's `components.a` in AssistantBubble
// is the single interception point (D4).
import { BarChart3, FileText } from 'lucide-react';
import { tr } from '@/i18n/locale';
import { Button } from './ui/Button';

const PAGE_RE = /^\/pages\/(\d+)(\/view)?$/;
const REPORT_RE = /^\/reports\/(\d+)$/;

export type DeliverableInfo = { type: 'page' | 'report'; id: number; href: string };

export function matchDeliverable(href: string): DeliverableInfo | null {
  const page = PAGE_RE.exec(href);
  if (page) return { type: 'page', id: Number(page[1]), href };
  const report = REPORT_RE.exec(href);
  if (report) return { type: 'report', id: Number(report[1]), href };
  return null;
}

export function DeliverableCard({ info }: { info: DeliverableInfo }) {
  const Icon = info.type === 'page' ? FileText : BarChart3;
  const label = info.type === 'page' ? tr('托管页', 'Hosted page') : tr('报表', 'Report');
  return (
    <span className="surface-card my-1.5 flex items-center gap-3 rounded-rk-md px-3 py-2.5">
      <span className="flex h-8 w-8 items-center justify-center rounded-rk-sm bg-accent-50 text-accent-700">
        <Icon className="h-4 w-4" aria-hidden="true" />
      </span>
      <span className="min-w-0 flex-1">
        <span className="block truncate text-xs font-medium text-zinc-200">
          {label} #{info.id}
        </span>
        <span className="block truncate text-[11px] text-zinc-500">{info.href}</span>
      </span>
      <Button variant="ghost" onClick={() => window.open(info.href, '_blank')}>
        {tr('打开', 'Open')}
      </Button>
    </span>
  );
}
