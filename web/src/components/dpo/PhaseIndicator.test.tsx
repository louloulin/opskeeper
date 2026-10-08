import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { PhaseIndicator } from './PhaseIndicator';

describe('PhaseIndicator motion', () => {
  it('gives the status chip a motion-safe 200ms colour transition', () => {
    render(<PhaseIndicator phase="recovered" status="success" />);
    const chip = screen.getByText('success').closest('span');
    expect(chip?.className).toContain('motion-safe:transition-colors');
    expect(chip?.className).toContain('motion-safe:duration-200');
  });

  it('gates the colour transition behind prefers-reduced-motion (no bare transition)', () => {
    render(<PhaseIndicator phase="recovered" status="success" />);
    const chip = screen.getByText('success').closest('span');
    // 断言是 motion-safe 变体，而非裸工具类 —— 减少动态效果下 Tailwind 不生成该声明。
    expect(chip?.className).not.toMatch(/(^|\s)transition-colors(\s|$)/);
  });
});
