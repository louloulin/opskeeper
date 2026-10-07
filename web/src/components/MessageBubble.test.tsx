import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { MessageBubble, type ConfigDraftResult } from './MessageBubble';
import type { ChatMessage } from '@/api/chat';
import { getApproval, approveApproval } from '@/api/approvals';
import { useApprovalBadge } from '@/store/approvalBadge';

vi.mock('@/api/approvals', () => ({
  getApproval: vi.fn(),
  approveApproval: vi.fn(),
  rejectApproval: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const supportedKinds = [
  'metric_threshold',
  'metric_raw',
  'metric_anomaly',
  'metric_forecast',
  'metric_burn_rate',
  'log_match',
  'log_volume',
  'trace_latency',
  'trace_error_rate',
];

function draftFor(kind: string): ConfigDraftResult {
  return {
    kind: 'config_draft',
    domain: 'alert_rule',
    action: 'create',
    summary: `Create ${kind} rule`,
    payload: {
      action: 'create',
      rule: {
        rule_key: `${kind}_natural_language`,
        kind,
        name: `${kind} from natural language`,
        severity: 'warning',
        spec: specFor(kind),
      },
    },
    preview: {
      fire_count: 2,
      samples: [{ summary: `${kind} sample` }],
    },
    warnings: [`${kind} preview warning`],
    scope: {
      type: 'host',
      label: '主机级',
      reason: '命中后会关联到具体设备。',
      change_hint: '如果要改成全局汇总，可以回复“改成全局”。',
    },
    confirmation_prompt:
      '当前告警范围：主机级。命中后会关联到具体设备。如果要改成全局汇总，可以回复“改成全局”。确认无误后可点击确认应用或回复“ok”。',
    rollback: 'Disable or edit the rule from Alerts.',
    apply_tool: 'apply_config_change',
    draft_hash: `sha256:${kind}`,
  };
}

function specFor(kind: string): Record<string, unknown> {
  switch (kind) {
    case 'metric_raw':
      return {
        expr: '(100 * max(redis_memory_used_bytes) / clamp_min(max(redis_memory_max_bytes), 1)) > 80',
      };
    case 'metric_anomaly':
      return { metric: 'cpu_pct', method: 'zscore', baseline_window: '1h', deviation: 3 };
    case 'metric_forecast':
      return { metric: 'disk_avail_bytes', predict_seconds: 21600, operator: '<=', threshold: 0 };
    case 'metric_burn_rate':
      return {
        sli: 'sum(rate(http_requests_total{code!~"5.."}[$window])) / sum(rate(http_requests_total[$window]))',
        slo: 99.9,
        burns: [{ window: '1h', multiplier: 14.4 }],
      };
    case 'log_match':
      return { stream_selector: '{opskeeper_source=~"journald:.+"}', line_filter: '(?i)error|panic' };
    case 'log_volume':
      return { stream_selector: '{opskeeper_source=~".+"}', ratio_op: '>=', ratio_threshold: 2 };
    case 'trace_latency':
      return { service: 'checkout', quantile: 'p95', threshold_ms: 500 };
    case 'trace_error_rate':
      return { service: 'checkout', operator: '>=', threshold_pct: 1 };
    default:
      return {};
  }
}

function toolCardMessage(draft: ConfigDraftResult): ChatMessage {
  return {
    id: `tool-card-${draft.summary}`,
    role: 'tool',
    kind: 'tool_card',
    tool_call: {
      id: `call-${draft.summary}`,
      name: 'draft_config_change',
      status: 'success',
      result: draft,
    },
  };
}

describe('MessageBubble config draft card', () => {
  beforeEach(() => {
    localStorage.setItem('opskeeper-locale', 'zh-CN');
  });

  it('compacts persisted config confirmation user payloads', () => {
    const longConfirmation = [
      '确认应用这个配置草案。',
      'domain: alert_rule',
      'action: create',
      'draft_hash: sha256:test',
      'apply_tool: apply_config_change',
      '请调用 apply_config_change，传 confirmed=true、domain=alert_rule、action=create、上方 draft_hash 和下方原始 payload，创建这条告警规则；不要改写 payload。',
      'payload:',
      '```json',
      JSON.stringify({
        action: 'create',
        rule: {
          rule_key: 'system_disk_pressure_v2',
          kind: 'metric_raw',
        },
      }, null, 2),
      '```',
    ].join('\n');

    render(<MessageBubble message={{ id: 'user-confirmation', role: 'user', content: longConfirmation }} />);

    expect(screen.getByText('确认创建这条告警规则')).toBeInTheDocument();
    expect(screen.queryByText(/draft_hash/)).not.toBeInTheDocument();
    expect(screen.queryByText(/system_disk_pressure_v2/)).not.toBeInTheDocument();
  });

  it('keeps ordinary user messages unchanged', () => {
    render(<MessageBubble message={{ id: 'user-normal', role: 'user', content: '创建一个 CPU 告警' }} />);

    expect(screen.getByText('创建一个 CPU 告警')).toBeInTheDocument();
  });

  it.each(supportedKinds)('renders and confirms %s drafts', async (kind) => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const draft = draftFor(kind);

    render(<MessageBubble message={toolCardMessage(draft)} onConfirmConfigDraft={onConfirm} />);

    expect(screen.getByText(`Create ${kind} rule`)).toBeInTheDocument();
    expect(screen.getByText('范围：主机级')).toBeInTheDocument();
    expect(screen.getByText(/当前告警范围：主机级/)).toBeInTheDocument();
    expect(screen.getByText(
      `action: create · rule_key: ${kind}_natural_language · kind: ${kind} · name: ${kind} from natural language · severity: warning`,
    )).toBeInTheDocument();
    expect(screen.getByText('Preview fire_count=2')).toBeInTheDocument();
    expect(screen.getByText(`${kind} preview warning`)).toBeInTheDocument();
    expect(screen.getByText('Disable or edit the rule from Alerts.')).toBeInTheDocument();

    await act(async () => {
      await user.click(screen.getByRole('button', { name: /确认应用|Apply/ }));
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onConfirm).toHaveBeenCalledWith(draft);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /已确认|Confirmed/ })).toBeDisabled();
    });
  });

  it('cancels without calling confirm', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();

    render(<MessageBubble message={toolCardMessage(draftFor('metric_raw'))} onConfirmConfigDraft={onConfirm} />);
    await act(async () => {
      await user.click(screen.getByRole('button', { name: /取消|Cancel/ }));
    });

    expect(onConfirm).not.toHaveBeenCalled();
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /已取消|Cancelled/ })).toBeDisabled();
    });
  });

  it('allows retry when confirm fails', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn().mockResolvedValue(false);

    render(<MessageBubble message={toolCardMessage(draftFor('metric_raw'))} onConfirmConfigDraft={onConfirm} />);
    await act(async () => {
      await user.click(screen.getByRole('button', { name: /确认应用|Apply/ }));
    });

    expect(onConfirm).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /确认应用|Apply/ })).toBeEnabled();
    });
    expect(screen.queryByRole('button', { name: /已确认|Confirmed/ })).not.toBeInTheDocument();
  });

  it('does not render a config draft card for unsupported config domains', () => {
    const draft = {
      ...draftFor('metric_raw'),
      domain: 'notification_channel',
      summary: 'Create notification channel',
    } as ConfigDraftResult;

    render(<MessageBubble message={toolCardMessage(draft)} onConfirmConfigDraft={vi.fn()} />);

    expect(screen.queryByText('Create notification channel')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /确认应用|Apply/ })).not.toBeInTheDocument();
  });

  it('renders a persisted tool result string as a draft card', () => {
    const draft = draftFor('metric_raw');
    const message: ChatMessage = {
      id: 'persisted-tool-result',
      role: 'tool',
      tool_name: 'draft_config_change',
      content: JSON.stringify(draft),
    };

    render(<MessageBubble message={message} onConfirmConfigDraft={vi.fn()} />);

    expect(screen.getByText('Create metric_raw rule')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /确认应用|Apply/ })).toBeInTheDocument();
  });
});

function approvalMessage(approvalID: string): ChatMessage {
  return {
    id: `approval-${approvalID}`,
    role: 'tool',
    kind: 'tool_card',
    tool_call: {
      id: `call-${approvalID}`,
      name: 'cloud_bash',
      status: 'success',
      result: { status: 'pending_approval', approval_id: approvalID, kind: 'cloud_bash' },
      arguments: { command: 'echo opskeeper-dualsign-OK' },
    },
  };
}

describe('MessageBubble agent bubble form', () => {
  beforeEach(() => {
    localStorage.setItem('opskeeper-locale', 'zh-CN');
  });

  const assistantMessage: ChatMessage = {
    id: 'assistant-persona',
    role: 'assistant',
    content: '磁盘使用率已达 92%，建议清理日志。',
    created_at: '2026-10-07T09:41:00Z',
    pending: false,
  };

  it('renders the agent avatar and persona head row when agentId is set', () => {
    render(<MessageBubble message={assistantMessage} agentId="specialist-disk" />);

    expect(screen.getByTestId('agent-avatar')).toBeInTheDocument();
    expect(screen.getByText('磁盘专家')).toBeInTheDocument();
    expect(screen.getByText('09:41')).toBeInTheDocument();
    expect(screen.getByText(/磁盘使用率已达/)).toBeInTheDocument();
  });

  it('keeps the bubble-agent semantic class and drops the head row without agentId', () => {
    const { container } = render(<MessageBubble message={assistantMessage} />);

    expect(container.querySelector('.bubble-agent')).not.toBeNull();
    expect(screen.queryByTestId('agent-avatar')).not.toBeInTheDocument();
    expect(screen.queryByText('磁盘专家')).not.toBeInTheDocument();
    expect(screen.getByText(/磁盘使用率已达/)).toBeInTheDocument();
  });

  it('renders the user bubble with the bubble-user semantic class', () => {
    const { container } = render(
      <MessageBubble message={{ id: 'user-bubble-user', role: 'user', content: '看一下磁盘' }} />,
    );

    expect(container.querySelector('.bubble-user')).not.toBeNull();
  });
});

describe('MessageBubble inline approval card', () => {
  beforeEach(() => {
    localStorage.setItem('opskeeper-locale', 'zh-CN');
    vi.mocked(getApproval).mockResolvedValue({
      id: 'ap-dualsign',
      kind: 'cloud_bash',
      title: 'echo opskeeper-dualsign-OK',
      summary: '',
      payload: JSON.stringify({ command: 'echo opskeeper-dualsign-OK' }),
      source: 'chat',
      status: 'pending',
      proposed_by: 1,
      created_at: new Date().toISOString(),
    });
  });

  it('keeps the card honest on a first dual-sign signature: waiting, never 已执行', async () => {
    // Destructive commands need two signatures from two users; the first
    // approve returns HTTP 202 with status still pending and the command
    // NOT run. The card must say so instead of claiming 已执行.
    vi.mocked(approveApproval).mockResolvedValue({
      id: 'ap-dualsign',
      kind: 'cloud_bash',
      title: 'echo opskeeper-dualsign-OK',
      summary: '',
      payload: '{}',
      source: 'chat',
      status: 'pending',
      signers: JSON.stringify([{ user_id: 1, role: 'admin', at: new Date().toISOString() }]),
      proposed_by: 1,
      created_at: new Date().toISOString(),
    });

    render(<MessageBubble message={approvalMessage('ap-dualsign')} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: /批准并执行/ });
    await user.click(screen.getByRole('button', { name: /批准并执行/ }));

    await waitFor(() => expect(screen.getByText(/已记录你的签名/)).toBeInTheDocument());
    expect(screen.getByText(/第二位批准人/)).toBeInTheDocument();
    expect(screen.queryByText('已执行')).not.toBeInTheDocument();
  });

  it('shows 已执行 with the result once the row actually executed', async () => {
    vi.mocked(approveApproval).mockResolvedValue({
      id: 'ap-dualsign',
      kind: 'cloud_bash',
      title: 'echo opskeeper-dualsign-OK',
      summary: '',
      payload: '{}',
      source: 'chat',
      status: 'executed',
      result: JSON.stringify({ stdout: 'opskeeper-dualsign-OK\n', exit_code: 0 }),
      proposed_by: 1,
      created_at: new Date().toISOString(),
    });

    render(<MessageBubble message={approvalMessage('ap-dualsign')} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: /批准并执行/ });
    await user.click(screen.getByRole('button', { name: /批准并执行/ }));

    await waitFor(() => expect(screen.getByText('已执行')).toBeInTheDocument());
    expect(screen.getAllByText(/opskeeper-dualsign-OK/).length).toBeGreaterThan(0);
  });

  it('refreshes the global pending-approval badge after an approve', async () => {
    // 3.4.3: after an approve the sidebar 审批 red dot must reconcile with the
    // now-decided row. The store's refresh() bails early without a token, so
    // there is nothing observable in the DOM — assert the call with a spy on
    // the very same state object the card reaches through getState().
    vi.mocked(approveApproval).mockResolvedValue({
      id: 'ap-dualsign',
      kind: 'cloud_bash',
      title: 'echo opskeeper-dualsign-OK',
      summary: '',
      payload: '{}',
      source: 'chat',
      status: 'pending',
      signers: JSON.stringify([{ user_id: 1, role: 'admin', at: new Date().toISOString() }]),
      proposed_by: 1,
      created_at: new Date().toISOString(),
    });

    const spy = vi.spyOn(useApprovalBadge.getState(), 'refresh').mockResolvedValue();

    try {
      render(<MessageBubble message={approvalMessage('ap-dualsign')} />);
      const user = userEvent.setup();
      await screen.findByRole('button', { name: /批准并执行/ });
      await user.click(screen.getByRole('button', { name: /批准并执行/ }));

      await waitFor(() => expect(spy).toHaveBeenCalled());
    } finally {
      spy.mockRestore();
    }
  });

  it('surfaces the blast radius / risk class / target on the approval card', async () => {
    // 3.4.3: the operator is authorising a change; the radius, risk class and
    // target are what say how much of the estate it touches. The backend row
    // already carries them (model.go), so the card renders them on mount.
    vi.mocked(getApproval).mockResolvedValue({
      id: 'ap-dualsign',
      kind: 'cloud_bash',
      title: 'echo opskeeper-dualsign-OK',
      summary: '',
      payload: JSON.stringify({ command: 'echo opskeeper-dualsign-OK' }),
      source: 'chat',
      status: 'pending',
      proposed_by: 1,
      created_at: new Date().toISOString(),
      blast_radius: 'node-12 nginx 5s',
      risk_class: 'destructive',
      target: 'node-12',
    });

    render(<MessageBubble message={approvalMessage('ap-dualsign')} />);

    expect(await screen.findByText(/影响面/)).toBeInTheDocument();
    expect(screen.getByText(/风险等级/)).toBeInTheDocument();
    // The blast-radius chip's text is "影响面: node-12 nginx 5s", so a broad
    // /node-12/ match stayed green even with the target span deleted. The
    // exact-match text node "node-12" is produced only by the target span
    // (meta.target), so this genuinely constrains the target rendering.
    expect(screen.getByText('node-12')).toBeInTheDocument();
  });
});

describe('MessageBubble deliverable link rendering', () => {
  beforeEach(() => {
    localStorage.setItem('opskeeper-locale', 'zh-CN');
  });

  it('renders a hosted-page markdown link as a DeliverableCard, not an anchor', () => {
    const { container } = render(
      <MessageBubble
        message={{
          id: 'assistant-deliverable',
          role: 'assistant',
          content: '报告已生成：[查看托管页](/pages/12)',
          pending: false,
        }}
      />,
    );

    // Card affordances render...
    expect(screen.getByText('托管页 #12')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '打开' })).toBeInTheDocument();
    // ...and the underlying markdown anchor is replaced (no <a href="/pages/12">).
    expect(container.querySelector('a[href="/pages/12"]')).toBeNull();
  });

  it('keeps a non-deliverable markdown link as a plain anchor with no target attr', () => {
    const { container } = render(
      <MessageBubble
        message={{
          id: 'assistant-plain-link',
          role: 'assistant',
          content: '请先到[设置页](/settings)调整阈值。',
          pending: false,
        }}
      />,
    );

    const link = container.querySelector('a[href="/settings"]');
    expect(link).not.toBeNull();
    // Pre-refactor fallback is verbatim: same markup, children preserved,
    // and no target attribute was ever added.
    expect(link?.textContent).toBe('设置页');
    expect(link?.hasAttribute('target')).toBe(false);
    // No deliverable card is produced for a plain link.
    expect(screen.queryByRole('button', { name: '打开' })).not.toBeInTheDocument();
  });
});
