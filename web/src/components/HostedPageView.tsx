// HostedPageView — 托管页只读渲染器(共享,design §1.1/§2)。
// 从 Pages.tsx PageThumb 抽出:props 只收页面内容,不含取数。
// 缩略模式(不传 height):THUMB_W=1100 基准等比缩放 + ResizeObserver 跟随容器宽。
// 全尺寸模式(传 height):不缩放,iframe 占满容器宽与给定高。
// iframe 强制 sandbox=""(全属性收紧,spec:托管页 iframe 沙箱收紧)。
import { useEffect, useRef, useState } from 'react';

const THUMB_W = 1100;
const THUMB_H = 900;
export const DEFAULT_THUMB_SCALE = 0.3;

// computeScale — 容器宽 → 缩放系数(纯函数,可单测)。
export function computeScale(containerWidth: number): number {
  if (containerWidth <= 0) return DEFAULT_THUMB_SCALE;
  return containerWidth / THUMB_W;
}

export function HostedPageView({ html, height }: { html: string; height?: number | string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [scale, setScale] = useState(DEFAULT_THUMB_SCALE);

  useEffect(() => {
    if (height != null) return; // 全尺寸模式不缩放
    const el = ref.current;
    if (!el) return;
    if (typeof ResizeObserver === 'undefined') return; // 测试环境:保持默认缩放
    const ro = new ResizeObserver(() => {
      if (el.clientWidth > 0) setScale(computeScale(el.clientWidth));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [height]);

  if (height != null) {
    return (
      <iframe
        title="page preview"
        srcDoc={html}
        sandbox=""
        className="w-full rounded-md border border-zinc-800 bg-white"
        style={{ height }}
      />
    );
  }

  return (
    <div ref={ref} className="relative h-40 w-full overflow-hidden bg-white">
      <iframe
        title="thumbnail"
        srcDoc={html}
        sandbox=""
        tabIndex={-1}
        scrolling="no"
        className="pointer-events-none absolute left-0 top-0 origin-top-left border-0"
        style={{ width: THUMB_W, height: THUMB_H, transform: `scale(${scale})` }}
      />
    </div>
  );
}
