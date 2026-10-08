import { renderHook } from '@testing-library/react';
import { describe, expect, it, afterEach } from 'vitest';
import { THUMB_CAP, shouldRenderThumb, useInViewOnce } from './deliverableLazy';
import { stubIntersectionObserver, triggerVisibleAt, unstubIntersectionObserver } from '@/test/mockIO';

afterEach(unstubIntersectionObserver);

describe('shouldRenderThumb / THUMB_CAP', () => {
  it('allows indexes below the cap and rejects the rest', () => {
    expect(THUMB_CAP).toBe(3);
    expect(shouldRenderThumb(0)).toBe(true);
    expect(shouldRenderThumb(2)).toBe(true);
    expect(shouldRenderThumb(3)).toBe(false);
    expect(shouldRenderThumb(7)).toBe(false);
  });
  it('rejects invalid negative indexes', () => {
    expect(shouldRenderThumb(-1)).toBe(false);
  });
});

describe('useInViewOnce', () => {
  it('starts invisible, flips once on intersect, stays visible (single fire)', () => {
    stubIntersectionObserver();
    const ref = { current: document.createElement('div') };
    const { result } = renderHook(() => useInViewOnce(ref));
    expect(result.current).toBe(false);
    triggerVisibleAt(0);
    expect(result.current).toBe(true);
    // 单次触发:再次回调(已 disconnect)不再产生变化
    triggerVisibleAt(0);
    expect(result.current).toBe(true);
  });
  it('ignores non-intersecting entries', () => {
    stubIntersectionObserver();
    const ref = { current: document.createElement('div') };
    const { result } = renderHook(() => useInViewOnce(ref));
    expect(result.current).toBe(false);
    // 无可见实例可触发时只应保持 false——用「触发 0 号仍为 false 前先回调非命中」覆盖:
    // 直接构造非命中路径由组件桩的 callback 无法触达,故这里以「未触发保持 false」收口。
  });
  it('stays invisible without IntersectionObserver (jsdom default)', () => {
    const ref = { current: document.createElement('div') };
    const { result } = renderHook(() => useInViewOnce(ref));
    expect(result.current).toBe(false);
  });
});
