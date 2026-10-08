import { render } from '@testing-library/react';
import { describe, expect, it, vi, afterEach } from 'vitest';
import { HostedPageView, computeScale } from './HostedPageView';

const HTML = '<!doctype html><html><head><title>t</title></head><body><h1>hi</h1></body></html>';

class MockResizeObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('computeScale', () => {
  it('scales container width against the THUMB_W=1100 baseline', () => {
    expect(computeScale(1100)).toBe(1);
    expect(computeScale(550)).toBe(0.5);
  });
  it('falls back to a positive default scale for zero/negative widths', () => {
    expect(computeScale(0)).toBeGreaterThan(0);
    expect(computeScale(-10)).toBeGreaterThan(0);
  });
});

describe('HostedPageView thumbnail mode', () => {
  it('renders a sandboxed (empty value) srcdoc iframe scaled to the container', () => {
    vi.stubGlobal('ResizeObserver', MockResizeObserver);
    render(<HostedPageView html={HTML} />);
    const iframe = document.querySelector('iframe');
    expect(iframe).not.toBeNull();
    expect(iframe!.getAttribute('sandbox')).toBe('');
    expect(iframe!.getAttribute('srcdoc')).toContain('hi');
    expect(iframe!.style.transform).toContain('scale(');
  });
  it('keeps the default scale when ResizeObserver is unavailable', () => {
    render(<HostedPageView html={HTML} />);
    const iframe = document.querySelector('iframe')!;
    expect(iframe.style.transform).toBe('scale(0.3)');
  });
});

describe('HostedPageView full-size mode', () => {
  it('renders an unscaled sandboxed iframe with the given height', () => {
    render(<HostedPageView html={HTML} height="60vh" />);
    const iframe = document.querySelector('iframe')!;
    expect(iframe.getAttribute('sandbox')).toBe('');
    expect(iframe.style.transform).toBe('');
    expect(iframe.style.height).toBe('60vh');
  });

  // 无障碍名称回归防护：弹窗 iframe 的 title 曾经是动态的 preview.title，
  // 抽出共享组件时被写死成 "page preview"。全尺寸模式改为消费可选 title prop，
  // 缺省仍回落到 "page preview"，保证既有调用点与既有断言向后兼容。
  it('uses the caller-provided accessible title', () => {
    render(<HostedPageView html={HTML} title="季度报告" height="60vh" />);
    expect(document.querySelector('iframe')!.getAttribute('title')).toBe('季度报告');
  });
  it('falls back to "page preview" when no title is provided', () => {
    render(<HostedPageView html={HTML} height="60vh" />);
    expect(document.querySelector('iframe')!.getAttribute('title')).toBe('page preview');
  });
});

describe('HostedPageView thumbnail mode accessible name', () => {
  it('keeps the hardcoded "thumbnail" name even when a title is passed', () => {
    render(<HostedPageView html={HTML} title="季度报告" />);
    expect(document.querySelector('iframe')!.getAttribute('title')).toBe('thumbnail');
  });
});
