// Agents 页面测试 — 覆盖「点击助理卡片查看定义详情」链路，并验证
// 按 source 区分的编辑路径（user 直接编辑 / 内置预置 fork 成自定义）。
import { useEffect } from 'react';
import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import AgentsPage from './Agents';
import { server } from '@/test/msw-server';

vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));

const diskAgent = {
  name: 'specialist-sre',
  description: 'SRE 专家：黄金四信号 / SLO / 错误预算。',
  when_to_use: '当任务围绕系统是否健康时派给我。',
  system_prompt: '# SRE 专家\n你是 SRE 专家，优先看黄金四信号。',
  tools: ['query_promql', 'query_incidents'],
  permission_mode: 'read-only',
  source: 'disk',
};

const userAgent = {
  name: 'my_db_helper',
  description: '我的数据库助手。',
  system_prompt: '你是数据库专家。',
  tools: ['analyze_database_status'],
  source: 'user',
};

const listURL = '/api/v1/skills';

// MemoryRouter doesn't expose navigation on its own, so mount a tiny probe
// inside the same router tree to observe pathname changes. No new deps.
function LocationProbe({ onPath }: { onPath: (p: string) => void }) {
  const { pathname } = useLocation();
  useEffect(() => {
    onPath(pathname);
  }, [pathname, onPath]);
  return null;
}

function renderWithRouter(onPath: (p: string) => void) {
  return render(
    <MemoryRouter>
      <AgentsPage />
      <LocationProbe onPath={onPath} />
    </MemoryRouter>,
  );
}

describe('AgentsPage', () => {
  beforeEach(() => {
    localStorage.setItem('opskeeper-locale', 'zh-CN');
    server.use(
      http.get('/api/v1/agents', () =>
        HttpResponse.json({ items: [diskAgent, userAgent], total: 2 }),
      ),
      // AgentEditor 打开时拉工具列表
      http.get(listURL, () => HttpResponse.json({ items: [], total: 0 })),
    );
  });

  it('点击预置助理卡片打开只读详情，编辑入口是「复制为自定义助理」', async () => {
    render(
      <MemoryRouter>
        <AgentsPage />
      </MemoryRouter>,
    );
    // 卡片标题用短名；点击卡片主体打开详情
    await userEvent.click(await screen.findByText('SRE 专家'));

    const dialog = await screen.findByRole('dialog');
    // system prompt 原文完整展示
    expect(within(dialog).getByText(/你是 SRE 专家，优先看黄金四信号/)).toBeInTheDocument();
    // 工具清单
    expect(within(dialog).getByText('query_promql')).toBeInTheDocument();
    // disk source 不可直接编辑 → 提供 fork 入口，没有「编辑」按钮
    expect(within(dialog).getByRole('button', { name: /复制为自定义助理/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: '编辑' })).not.toBeInTheDocument();
  });

  it('点击自定义助理卡片，详情里有「编辑」入口', async () => {
    render(
      <MemoryRouter>
        <AgentsPage />
      </MemoryRouter>,
    );
    // user 助理无短名，name 同时出现在卡片标题和 mono 行；点标题
    await userEvent.click((await screen.findAllByText('my_db_helper'))[0]);

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/你是数据库专家/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: /编辑/ })).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: /复制为自定义助理/ })).not.toBeInTheDocument();
  });

  it('卡片展示 Agent 头像、工具 Chip 与「开始对话」按钮', async () => {
    renderWithRouter(vi.fn());

    // 档案墙卡片：头像 + 本地化名 + 描述 + 工具 Chip
    await screen.findByText('SRE 专家');
    expect(screen.getAllByTestId('agent-avatar').length).toBeGreaterThan(0);
    // 工具集以 Chip 呈现（diskAgent.tools = ['query_promql', 'query_incidents']）
    expect(screen.getByText('query_promql')).toBeInTheDocument();
    expect(screen.getByText('query_incidents')).toBeInTheDocument();
    // 「开始对话」CTA
    expect(screen.getAllByText('开始对话').length).toBeGreaterThan(0);
  });

  it('开始对话 creates a persona session and navigates to the chat', async () => {
    const onNavigateChange = vi.fn();
    server.use(
      http.post('/api/v1/chat/sessions', () =>
        HttpResponse.json({ id: 's-new', user_id: 1, title: 'x', agent_id: 'specialist-sre' }),
      ),
    );
    renderWithRouter(onNavigateChange);
    await userEvent.click((await screen.findAllByText('开始对话'))[0]);
    await waitFor(() => expect(onNavigateChange).toHaveBeenCalledWith('/chat/s-new'));
  });

  it('keeps the user on the page and shows an inline error when the session cannot be created', async () => {
    const onNavigateChange = vi.fn();
    server.use(
      http.post('/api/v1/chat/sessions', () =>
        HttpResponse.json({ message: '创建失败' }, { status: 500 }),
      ),
    );
    renderWithRouter(onNavigateChange);
    await userEvent.click((await screen.findAllByText('开始对话'))[0]);

    expect(await screen.findByText(/失败/)).toBeInTheDocument();
    expect(onNavigateChange).not.toHaveBeenCalledWith('/chat/s-new');
    // 失败时不弹详情：卡片点击未被按钮的 stopPropagation 之外的行为触发
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
