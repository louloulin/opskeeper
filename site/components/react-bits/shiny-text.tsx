/**
 * React Bits "ShinyText" — a sweeping light band across the text.
 * Pure CSS keyframes; respects prefers-reduced-motion.
 */
export default function ShinyText({
  children,
  className = '',
  disabled = false,
  speed = 4,
}: {
  children: React.ReactNode;
  className?: string;
  disabled?: boolean;
  speed?: number;
}) {
  return (
    <span
      className={`shiny-text ${disabled ? 'shiny-text-disabled' : ''} ${className}`}
      style={{ ['--shiny-duration' as string]: `${speed}s` }}
    >
      {children}
    </span>
  );
}
