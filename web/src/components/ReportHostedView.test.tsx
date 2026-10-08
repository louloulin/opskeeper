import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { ReportHostedView } from './ReportHostedView';
import type { ReportContent } from '@/api/reports';

// 最小合法 ContentJSON(镜像 biz/report/content.go 字段)。
const content = {
  version: '1',
  hero: [],
  narrative: { headline: '集群平稳' },
  resource: { available: false, cpu_avg: 0, cpu_peak: 0, mem_avg: 0, mem_peak: 0, disk_avg: 0, disk_peak: 0 },
  fleet: { total: 3, online: 2 },
  actions_summary: { mutating_total: 0, mutating_approved: 0, safe_total: 1 },
  assets: { new_agents: 0, new_skills: 0, new_repos: 0 },
  usage: { sessions: 1, prompt_tokens: 10, completion_tokens: 5 },
} satisfies ReportContent;

describe('ReportHostedView', () => {
  it('wraps ReportContentView in a 480px scroll bound by default', () => {
    const { container } = render(<ReportHostedView content={content} />);
    const scroller = container.firstElementChild as HTMLElement;
    expect(scroller.style.maxHeight).toBe('480px');
    expect(scroller.className).toContain('overflow-y-auto');
    expect(screen.getByText('集群平稳')).toBeInTheDocument();
  });
  it('renders unbounded for the standalone report page', () => {
    const { container } = render(<ReportHostedView content={content} maxHeight="none" />);
    expect((container.firstElementChild as HTMLElement).style.maxHeight).toBe('');
    expect(screen.getByText('集群平稳')).toBeInTheDocument();
  });
  it('honours an explicit maxHeight override', () => {
    const { container } = render(<ReportHostedView content={content} maxHeight={240} />);
    expect((container.firstElementChild as HTMLElement).style.maxHeight).toBe('240px');
  });
});