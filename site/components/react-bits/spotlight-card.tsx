'use client';

import { useRef, type ReactNode, type MouseEvent } from 'react';
import { cn } from '@/lib/utils';

/**
 * React Bits "SpotlightCard" — card with a cursor-following radial spotlight.
 * Pure CSS custom properties, zero JS animation cost.
 */
export default function SpotlightCard({
  children,
  className = '',
  spotlightColor = 'rgba(16, 163, 127, 0.14)',
  ...rest
}: {
  children: ReactNode;
  className?: string;
  spotlightColor?: string;
} & React.HTMLAttributes<HTMLDivElement>) {
  const ref = useRef<HTMLDivElement>(null);

  const onMove = (e: MouseEvent<HTMLDivElement>) => {
    const el = ref.current;
    if (!el) return;
    const rect = el.getBoundingClientRect();
    el.style.setProperty('--spot-x', `${e.clientX - rect.left}px`);
    el.style.setProperty('--spot-y', `${e.clientY - rect.top}px`);
    el.style.setProperty('--spot-color', spotlightColor);
  };

  return (
    <div
      ref={ref}
      onMouseMove={onMove}
      className={cn('spotlight-card group relative overflow-hidden', className)}
      {...rest}
    >
      {children}
    </div>
  );
}
