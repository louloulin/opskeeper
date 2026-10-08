// 测试专用 IntersectionObserver 桩:jsdom 无 IO。手动触发回调驱动 useInViewOnce;
// test-local 手写,不新增运行时依赖(Non-Goal)。
import { act } from '@testing-library/react';
import { vi } from 'vitest';

type MockEntry = { isIntersecting: boolean; target: Element };

let instances: Array<{ callback: (entries: MockEntry[]) => void; observe: ReturnType<typeof vi.fn>; disconnect: ReturnType<typeof vi.fn> }> = [];

class MockIntersectionObserver {
  observe = vi.fn();
  unobserve = vi.fn();
  disconnect = vi.fn();
  callback: (entries: MockEntry[]) => void;
  constructor(cb: (entries: MockEntry[]) => void) {
    this.callback = cb;
    instances.push(this);
  }
}

export function stubIntersectionObserver() {
  instances = [];
  vi.stubGlobal('IntersectionObserver', MockIntersectionObserver);
}

// 触发第 index 个创建的观察器回调(实例按创建顺序入数组 = 卡片挂载顺序)。
export function triggerVisibleAt(index: number) {
  const io = instances[index];
  if (!io) throw new Error(`no IntersectionObserver instance at ${index}; created: ${instances.length}`);
  act(() => io.callback([{ isIntersecting: true, target: document.body }]));
}

export function unstubIntersectionObserver() {
  vi.unstubAllGlobals();
  instances = [];
}
