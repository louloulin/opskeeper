'use client';

import { motion } from 'motion/react';

/**
 * React Bits "BlurText" — words slide in with a blur→sharp transition,
 * staggered per word. Respects prefers-reduced-motion.
 */
export default function BlurText({
  text,
  className = '',
  delay = 60,
}: {
  text: string;
  className?: string;
  delay?: number;
}) {
  const words = text.split(' ');

  return (
    <span className={className} aria-label={text}>
      {words.map((word, i) => (
        <motion.span
          key={`${word}-${i}`}
          className="inline-block whitespace-nowrap will-change-transform"
          initial={{ opacity: 0, filter: 'blur(8px)', y: 8 }}
          animate={{ opacity: 1, filter: 'blur(0px)', y: 0 }}
          transition={{
            duration: 0.5,
            delay: (i * delay) / 1000,
            ease: [0.16, 1, 0.3, 1],
          }}
        >
          {word}
          {i < words.length - 1 ? ' ' : ''}
        </motion.span>
      ))}
    </span>
  );
}
