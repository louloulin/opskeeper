import { StrictMode } from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { http, HttpResponse, delay } from 'msw';
import { DeliverableCard, DeliverableSequence, extractHtmlTitle, matchDeliverable } from './DeliverableCard';
import { server } from '@/test/msw-server';
import { stubIntersectionObserver, triggerVisibleAt, unstubIntersectionObserver } from '@/test/mockIO';

// getLocale() 在 localStorage 为空时走自动探测,这里固定 zh-CN(既有约定)。
localStorage.setItem('opskeeper-locale', 'zh-CN');

const HEX24 = 'a3f9c2d81b7e4056c9d0e1f2';
const UUID = '7b2c1a9e-3f4d-4c5b-8e9f-0a1b2c3d4e5f';
const PAGE_HTML = '<!doctype html><html><head><title>巡检日报页</title></head><body><h1>ok</h1></body></html>';
const PAGE_HTML_NO_TITLE = '<!doctype html><html><head></head><body></body></html>';

const pageInfo = { type: 'page' as const, id: HEX24, href: `/pages/${HEX24}` };
const reportInfo = { type: 'report' as const, id: UUID, href: `/reports/${UUID}` };

// 最小 ReportDetail 就绪响应。
const REPORT_READY = {
  id: UUID, title: '10月8日日报', kind: 'daily', status: 'ready', summary: '',
  period_start: '', period_end: '', generated_at: '2026-10-08T09:00:00Z',
  created_at: '2026-10-08T09:00:00Z', content_md: '', timezone: 'Asia/Shanghai',
};
const REPORT_GENERATING = { ...REPORT_READY, status: 'generating', generated_at: undefined, title: '' };

let pageHits = 0;
beforeEach(() => {
  pageHits = 0;
  server.use(
    http.get('/api/pages/:id', () => { pageHits++; return HttpResponse.text(PAGE_HTML); }),
    http.get('/api/v1/reports/:id', () => HttpResponse.json(REPORT_READY)),
  );
});
afterEach(() => {
  unstubIntersectionObserver();
  server.resetHandlers();
});

describe('matchDeliverable', () => {
  it('recognizes hosted pages and reports by real artifact id shapes', () => {
    expect(matchDeliverable(`/pages/${HEX24}`)).toEqual({ type: 'page', id: HEX24, href: `/pages/${HEX24}` });
    expect(matchDeliverable(`/reports/${UUID}`)).toEqual({ type: 'report', id: UUID, href: `/reports/${UUID}` });
  });
  it('rejects legacy numeric ids, /view subroutes, non-hex segments and foreign paths', () => {
    expect(matchDeliverable('/pages/12')).toBeNull();
    expect(matchDeliverable('/reports/3')).toBeNull();
    expect(matchDeliverable(`/pages/${HEX24}/view`)).toBeNull();
    expect(matchDeliverable('/pages/settings')).toBeNull();
    expect(matchDeliverable('/settings')).toBeNull();
    expect(matchDeliverable('https://example.com')).toBeNull();
  });
});

describe('extractHtmlTitle', () => {
  it('parses the page <title>', () => {
    expect(extractHtmlTitle('<!doctype html><title>巡检日报页</title><body></body>')).toBe('巡检日报页');
  });
  it('trims whitespace around the title', () => {
    expect(extractHtmlTitle('<title>  spaced  </title>')).toBe('spaced');
  });
  it('returns empty string when there is no title (metadata 留空)', () => {
    expect(extractHtmlTitle('<!doctype html><body></body>')).toBe('');
  });
});

describe('DeliverableCard thumbnails', () => {
  it('opens the deliverable in a new tab (exit preserved on every state)', () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(<DeliverableCard info={pageInfo} />);
    screen.getByRole('button', { name: '新窗口打开' }).click();
    expect(open).toHaveBeenCalledWith(`/pages/${HEX24}`, '_blank');
    open.mockRestore();
  });

  it('does not fetch before entering the viewport', () => {
    render(<DeliverableCard info={pageInfo} />);
    expect(pageHits).toBe(0);
    expect(document.querySelector('iframe')).toBeNull();
  });

  it('renders the real sandboxed thumbnail after visibility', async () => {
    stubIntersectionObserver();
    render(<DeliverableCard info={pageInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull());
    const iframe = document.querySelector('iframe')!;
    expect(iframe.getAttribute('sandbox')).toBe('');
    expect(iframe.getAttribute('srcdoc')).toContain('ok');
    expect(screen.getByText('巡检日报页')).toBeInTheDocument(); // <title> 诚实标题
    expect(pageHits).toBe(1);
  });

  it('shows a placeholder-sized loading pane while fetching', async () => {
    server.use(http.get('/api/pages/:id', async () => { await delay(50); return HttpResponse.text(PAGE_HTML); }));
    stubIntersectionObserver();
    render(<DeliverableCard info={pageInfo} />);
    triggerVisibleAt(0);
    const pane = screen.getByTestId('deliverable-thumb');
    expect(pane).toBeInTheDocument();
    expect(pane.className).toContain('h-40'); // idle/loading 期保持占位尺寸
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull());
  });

  it('renders a typed report placeholder with kind + generated time, never an img/iframe', async () => {
    stubIntersectionObserver();
    render(<DeliverableCard info={reportInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(screen.getByText('日报')).toBeInTheDocument());
    expect(screen.getByText('2026-10-08T09:00:00Z')).toBeInTheDocument();
    expect(document.querySelector('iframe')).toBeNull();
    expect(document.querySelector('img')).toBeNull();
  });

  it('shows the generating placeholder for pending/generating reports (not failed)', async () => {
    server.use(http.get('/api/v1/reports/:id', () => HttpResponse.json(REPORT_GENERATING)));
    stubIntersectionObserver();
    render(<DeliverableCard info={reportInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(screen.getByText('报告生成中…')).toBeInTheDocument());
    expect(screen.queryByText('加载失败')).not.toBeInTheDocument();
  });

  it('degrades to a typed placeholder on page fetch failure and keeps the exit', async () => {
    server.use(http.get('/api/pages/:id', () => HttpResponse.text('', { status: 404 })));
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    stubIntersectionObserver();
    render(<DeliverableCard info={pageInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(screen.getByText('加载失败')).toBeInTheDocument());
    expect(document.querySelector('iframe')).toBeNull();
    screen.getByRole('button', { name: '新窗口打开' }).click();
    expect(open).toHaveBeenCalledWith(`/pages/${HEX24}`, '_blank');
    open.mockRestore();
  });

  it('degrades to a typed placeholder on report fetch failure and keeps the exit', async () => {
    server.use(http.get('/api/v1/reports/:id', () => HttpResponse.json({}, { status: 500 })));
    stubIntersectionObserver();
    render(<DeliverableCard info={reportInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(screen.getByText('加载失败')).toBeInTheDocument());
    expect(screen.getByRole('button', { name: '新窗口打开' })).toBeInTheDocument();
  });

  it('leaves the title blank when the HTML has no <title> (不编造)', async () => {
    server.use(http.get('/api/pages/:id', () => HttpResponse.text(PAGE_HTML_NO_TITLE)));
    stubIntersectionObserver();
    render(<DeliverableCard info={pageInfo} />);
    triggerVisibleAt(0);
    await waitFor(() => expect(document.querySelector('iframe')).not.toBeNull());
    expect(screen.getByText('托管页')).toBeInTheDocument(); // 类型 chip 仍在
    // 断言整条头部的确切文本而不是「某个标题不存在」:只断言 queryByText('巡检日报页')
    // 会放过任何别的编造标题。头部此刻必须是 类型 chip + href + 出口按钮,chip 与
    // href 之间没有标题节点 —— 头部文本一变就失败,这就是「不编造」的约束本身。
    expect(screen.getByTestId('deliverable-card-header').textContent).toBe(
      `托管页${pageInfo.href}新窗口打开`,
    );
  });
});

describe('DeliverableSequence thumbnail cap', () => {
  const infos = [
    'a3f9c2d81b7e4056c9d0e1f2',
    'b4e0d3f92c8a5167d0f2a3b4',
    'c5f1e40a83d9b6278e103b4c5',
    'd602f51b94eac7389f214c5d6',
    'e713a62cab5fd849a0325d6e7',
  ].map((id) => ({ type: 'page' as const, id, href: `/pages/${id}` }));

  // 回归防护:StrictMode 会把组件渲染两次。若序号用「首次渲染自增计数」领取,
  // 第二次渲染时卡片的 useRef 被重建回初值,每张卡会再领一次,3 张卡的序号变成
  // [1,3,5],THUMB_CAP=3 只剩 1 个窗格 —— 实际表现是每条消息只有第一张卡有缩略。
  // 必须包 StrictMode 测,否则普通单次渲染下序号式 claim 看起来完全正常。
  it('caps at 3 panes for 5 cards under StrictMode (double render must not burn slots)', () => {
    render(
      <StrictMode>
        <DeliverableSequence>
          {infos.map((info) => (
            <DeliverableCard key={info.href} info={info} />
          ))}
        </DeliverableSequence>
      </StrictMode>,
    );
    expect(screen.getAllByTestId('deliverable-thumb')).toHaveLength(3);
  });

  it('gives a card with no provider the same index-0 thumbnail as before', () => {
    render(<DeliverableCard info={infos[0]} />);
    expect(screen.getAllByTestId('deliverable-thumb')).toHaveLength(1);
  });
});
