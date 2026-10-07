import type { HTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

// Card — unified card surface used across pages (DocCard / AgentCard /
// IncidentCard / etc.). Visual rules per HLD-style guide:
//   rounded-2xl + shadow-card + weak semantic border (border-border-soft)
//   default p-4, compact p-3.5
//   hover (when interactive) hover:border-zinc-700 hover:bg-zinc-900/60
// The container no longer hardcodes bg-zinc-*/border-zinc-*: bg-card-soft
// and border-border-soft are the dual-theme semantic tokens (the tailwind
// color keys are literally named `card-soft` / `border-soft`, hence the
// doubled prefix on the border one), so the light-mode zinc remap in
// index.css is no longer load-bearing here.
type CardProps = HTMLAttributes<HTMLDivElement> & {
  /** When true, the card is clickable / hover affordances kick in. */
  interactive?: boolean;
  /** Tighter padding for dense rows (e.g. AgentSessionCard). */
  compact?: boolean;
  as?: 'div' | 'section' | 'article';
};

export function Card({
  className,
  interactive,
  compact,
  as: Tag = 'section',
  ...rest
}: CardProps) {
  return (
    <Tag
      className={cn(
        'rounded-2xl border border-border-soft bg-card-soft shadow-card',
        compact ? 'p-3.5' : 'p-4',
        interactive && 'transition-colors hover:border-border hover:bg-card',
        className,
      )}
      {...rest}
    />
  );
}
