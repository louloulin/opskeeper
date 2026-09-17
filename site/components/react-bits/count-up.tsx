'use client';

import { useEffect, useRef } from 'react';
import { useInView, animate } from 'motion/react';

/**
 * React Bits "CountUp" — animates a number from 0 to `to` when scrolled into view.
 * Supports suffix/prefix (%, +, ×) and fixed decimal count.
 */
export default function CountUp({
  to,
  suffix = '',
  prefix = '',
  decimals = 0,
  duration = 1.6,
  className = '',
}: {
  to: number;
  suffix?: string;
  prefix?: string;
  decimals?: number;
  duration?: number;
  className?: string;
}) {
  const ref = useRef<HTMLSpanElement>(null);
  const inView = useInView(ref, { once: true, margin: '-40px' });

  useEffect(() => {
    const el = ref.current;
    if (!el || !inView) return;

    const controls = animate(0, to, {
      duration,
      ease: [0.16, 1, 0.3, 1],
      onUpdate(v) {
        el.textContent = `${prefix}${v.toFixed(decimals)}${suffix}`;
      },
    });
    return () => controls.stop();
  }, [inView, to, decimals, duration, prefix, suffix]);

  return (
    <span ref={ref} className={className}>
      {prefix}
      {to.toFixed(decimals)}
      {suffix}
    </span>
  );
}
