// Discover 统一壳测试 —— 一个壳层聚合技能 / 插件市场 / 自愈结晶三张既有页面,
// tab 状态完全落在 `?tab=` 查询参数里,所以每个入口(sidebar、重定向、书签)
// 都只是一条普通链接。
//
// 三张内嵌页面各自都会在挂载时发请求;共享 msw server(`src/test/msw-server.ts`)
// 是空的,`setup.ts` 又是 onUnhandledRequest:'error',所以缺一个 handler 会当众
// 炸,而不是静默挂起——这里按每个用例实际挂载的内嵌页把 handler 配齐。
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { Discover } from './Discover';
import { server } from '@/test/msw-server';

// SkillsPage 读 `useAuth((s) => s.role)`(Skills.tsx:31);
// PluginMarketplace / Crystallized 读 `usePermissions()`。
// 两个 store 都 mock 掉,避免依赖真实 store 的持久化状态。
vi.mock('@/store/auth', () => ({
  useAuth: Object.assign(
    <T,>(selector: (s: { role: string }) => T): T => selector({ role: 'admin' }),
    { getState: () => ({ logout: () => {} }) },
  ),
  getToken: () => null,
  getRefreshToken: () => null,
}));
vi.mock('@/store/me', () => ({
  usePermissions: () => ({ isAdmin: false, canMutate: false, role: 'user' }),
}));

function renderAt(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/discover" element={<Discover />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('Discover', () => {
  beforeEach(() => {
    // 断言的 tab 名正则中英都能命中,但仍按仓库既有约定 pin 住 locale,
    // 免得别的用例改了全局 locale 后这里出现假阴性。
    localStorage.setItem('opskeeper-locale', 'zh-CN');
    server.use(
      // SkillsPage 挂载时拉技能 + MCP flow-tools。
      http.get('/api/v1/skills', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/flow-tools', () => HttpResponse.json({ items: [] })),
      // 未知 tab 的回落到 skills 后,admin 会命中 SkillsPage 的 install 子 tab
      // (深链语义),那里的 InstallChatBar 会拉模型目录。
      http.get('/api/v1/aiops/models', () => HttpResponse.json({ providers: [], default: null })),
      // PluginMarketplacePage 挂载时拉节点清单与发布列表。
      http.get('/api/v1/edges', () => HttpResponse.json({ items: [], total: 0 })),
      http.get('/api/v1/plugins/releases', () => HttpResponse.json({ items: [] })),
      // CrystallizedPage 挂载时拉账本。
      http.get('/api/v1/loops/crystallized', () =>
        HttpResponse.json({ items: [], policy: null, observing_since: null }),
      ),
    );
  });

  it('defaults to the skills tab', () => {
    renderAt('/discover');
    expect(screen.getByRole('tab', { name: /技能|Skills/ }).getAttribute('aria-selected')).toBe('true');
  });

  it('honours ?tab=plugins', () => {
    renderAt('/discover?tab=plugins');
    expect(screen.getByRole('tab', { name: /插件|Plugins/ }).getAttribute('aria-selected')).toBe('true');
  });

  it('honours ?tab=crystals', () => {
    renderAt('/discover?tab=crystals');
    expect(screen.getByRole('tab', { name: /自愈结晶|Crystals/ }).getAttribute('aria-selected')).toBe('true');
  });

  it('falls back to skills for an unknown tab but keeps install deep links working', () => {
    renderAt('/discover?tab=install');
    expect(screen.getByRole('tab', { name: /技能|Skills/ }).getAttribute('aria-selected')).toBe('true');
  });
});
