import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { DeliverableCard, matchDeliverable } from './DeliverableCard';

// getLocale() falls back to autoDetectLocale() when localStorage is empty,
// which resolves to en-US outside a CN timezone. The card button assertion is
// on the Chinese label 打开, so pin the locale explicitly instead of relying
// on detection — same precedent as SessionList.test.tsx.
localStorage.setItem('opskeeper-locale', 'zh-CN');

const HEX24 = 'a3f9c2d81b7e4056c9d0e1f2'; // 24 位十六进制,serve_page 产物真实形态
const UUID = '7b2c1a9e-3f4d-4c5b-8e9f-0a1b2c3d4e5f'; // 报表 UUID 真实形态

describe('matchDeliverable', () => {
  it('recognizes hosted pages and reports by real artifact id shapes', () => {
    expect(matchDeliverable(`/pages/${HEX24}`)).toEqual({ type: 'page', id: HEX24, href: `/pages/${HEX24}` });
    expect(matchDeliverable(`/reports/${UUID}`)).toEqual({ type: 'report', id: UUID, href: `/reports/${UUID}` });
  });

  it('rejects legacy numeric ids, /view subroutes, non-hex segments and foreign paths', () => {
    expect(matchDeliverable('/pages/12')).toBeNull(); // 既有 \d+ 误匹配形态:过短且非 hex
    expect(matchDeliverable('/reports/3')).toBeNull();
    expect(matchDeliverable(`/pages/${HEX24}/view`)).toBeNull(); // 聊天链接无此形态,移除 (\/view)? 分支
    expect(matchDeliverable('/pages/settings')).toBeNull(); // 非 hex 路径段
    expect(matchDeliverable('/settings')).toBeNull();
    expect(matchDeliverable('https://example.com')).toBeNull();
  });
});

describe('DeliverableCard', () => {
  it('opens the deliverable in a new tab', () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(<DeliverableCard info={{ type: 'page', id: HEX24, href: `/pages/${HEX24}` }} />);
    screen.getByText('打开').click();
    expect(open).toHaveBeenCalledWith(`/pages/${HEX24}`, '_blank');
    open.mockRestore();
  });
});