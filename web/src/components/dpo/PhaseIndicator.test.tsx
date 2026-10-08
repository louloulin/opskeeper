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
});
