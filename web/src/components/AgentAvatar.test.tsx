import { fireEvent, render } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { AgentAvatar } from './AgentAvatar';

describe('AgentAvatar', () => {
  it('renders the mapped icon and tone for a known persona', () => {
    const { container } = render(<AgentAvatar agentId="incident-investigator" />);
    expect(container.querySelector('svg')).not.toBeNull();
    expect(container.firstElementChild).toHaveClass('bg-violet-500/10');
  });

  it('normalizes the sre-agent alias onto specialist-sre', () => {
    const { container } = render(<AgentAvatar agentId="sre-agent" />);
    expect(container.firstElementChild).toHaveClass('bg-cyan-500/10');
  });

  it('normalizes loop-controller onto default and falls back for unknown ids', () => {
    const { container: a } = render(<AgentAvatar agentId="loop-controller" />);
    expect(a.firstElementChild).toHaveClass('bg-violet-500/10');
    const { container: b } = render(<AgentAvatar agentId="mystery-agent" />);
    expect(b.firstElementChild).toHaveClass('bg-violet-500/10');
  });

  it('prefers the avatar image and falls back to the icon when it fails to load', () => {
    const { container } = render(<AgentAvatar agentId="default" avatar="http://x/y.png" />);
    const img = container.querySelector('img');
    expect(img).not.toBeNull();
    fireEvent.error(img as HTMLImageElement);
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('svg')).not.toBeNull();
  });

  it('scales 40px variants larger than 32px ones', () => {
    const { container: small } = render(<AgentAvatar agentId="default" />);
    const { container: large } = render(<AgentAvatar agentId="default" size={40} />);
    expect(small.firstElementChild).toHaveClass('h-8');
    expect(large.firstElementChild).toHaveClass('h-10');
  });
});