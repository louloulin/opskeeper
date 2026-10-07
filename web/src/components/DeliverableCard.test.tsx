import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { DeliverableCard, matchDeliverable } from './DeliverableCard';

// getLocale() falls back to autoDetectLocale() when localStorage is empty,
// which resolves to en-US outside a CN timezone. The card button assertion is
// on the Chinese label 打开, so pin the locale explicitly instead of relying
// on detection — same precedent as SessionList.test.tsx.
localStorage.setItem('opskeeper-locale', 'zh-CN');

describe('matchDeliverable', () => {
  it('recognizes hosted pages and reports', () => {
    expect(matchDeliverable('/pages/12')).toEqual({ type: 'page', id: 12, href: '/pages/12' });
    expect(matchDeliverable('/pages/12/view')).toEqual({ type: 'page', id: 12, href: '/pages/12/view' });
    expect(matchDeliverable('/reports/3')).toEqual({ type: 'report', id: 3, href: '/reports/3' });
  });

  it('rejects anything else', () => {
    expect(matchDeliverable('/settings')).toBeNull();
    expect(matchDeliverable('https://example.com')).toBeNull();
  });
});

describe('DeliverableCard', () => {
  it('opens the deliverable in a new tab', () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null);
    render(<DeliverableCard info={{ type: 'page', id: 12, href: '/pages/12' }} />);
    screen.getByText('打开').click();
    expect(open).toHaveBeenCalledWith('/pages/12', '_blank');
    open.mockRestore();
  });
});
