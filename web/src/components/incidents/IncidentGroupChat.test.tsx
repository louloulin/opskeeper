import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { HttpResponse, http } from 'msw';
import { describe, expect, it } from 'vitest';

import { server } from '@/test/msw-server';
import type { IncidentEvent } from '@/api/alerts';
import type { ChatMessage, ChatSession } from '@/api/chat';
import { IncidentGroupChat } from './IncidentGroupChat';

// ChatInput renders a <Link> in its "no model configured" dropdown branch;
// a <Link> outside a router throws, so every render goes through
// MemoryRouter (same convention as ChatInput.test.tsx).
function renderChat(incidentId = 1) {
  return render(
    <MemoryRouter>
      <IncidentGroupChat incidentId={incidentId} />
    </MemoryRouter>,
  );
}

// stub wires the four GETs the incident group chat issues on mount: the
// incident's alert events, the incident-related chat sessions, each
// session's message tail, and the loop timeline. Handlers are test-local —
// msw-server ships an empty server and each test registers its own.
function stub(init: {
  events?: IncidentEvent[];
  sessions?: ChatSession[];
  messages?: Record<string, ChatMessage[]>;
} = {}) {
  const events = init.events ?? [];
  const sessions = init.sessions ?? [];
  const messages = init.messages ?? {};
  server.use(
    http.get('/api/v1/alerts/incidents/1/events', () =>
      HttpResponse.json({ items: events, total: events.length }),
    ),
    http.get('/api/v1/chat/sessions', () =>
      HttpResponse.json({ items: sessions, total: sessions.length }),
    ),
    http.get('/api/v1/chat/sessions/:id/messages', ({ params }) =>
      HttpResponse.json({ items: messages[String(params.id)] ?? [], total: 0 }),
    ),
    http.get('/api/v1/loops/1/timeline', () => HttpResponse.json({ phases: [] })),
  );
}

describe('IncidentGroupChat', () => {
  it('renders system chips for events and message bubbles for chat', async () => {
    stub({
      events: [
        {
          id: 1,
          incident_id: 1,
          event_type: 'firing',
          title: '告警触发',
          actor_type: 'system',
          occurred_at: '2026-10-07T10:00:00Z',
          created_at: '2026-10-07T10:00:00Z',
        },
        {
          id: 2,
          incident_id: 1,
          event_type: 'repeat_suppressed',
          title: '进入调查',
          actor_type: 'system',
          occurred_at: '2026-10-07T10:00:10Z',
          created_at: '2026-10-07T10:00:10Z',
        },
      ],
      sessions: [{ id: 's1', user_id: 1, title: '诊断', agent_id: 'incident-investigator' }],
      messages: {
        s1: [
          {
            id: 'm1',
            role: 'assistant',
            content: '根因是磁盘写满',
            created_at: '2026-10-07T10:01:00Z',
          },
        ],
      },
    });

    renderChat();

    expect(await screen.findByText('告警触发')).toBeTruthy();
    expect(await screen.findByText('根因是磁盘写满')).toBeTruthy();
  });

  it('lists the distinct session personas plus you', async () => {
    stub({ sessions: [{ id: 's1', user_id: 1, title: 'a', agent_id: 'incident-investigator' }] });

    renderChat();

    // Wait for the feed to load before asserting the human is present: with
    // the pre-§4 member logic the empty pre-load feed rendered 「你」 as the
    // `['default']` fallback, so only the post-load frame discriminates.
    await screen.findByText('incident-investigator');
    // Synchronous on purpose — a findByText here would resolve against the
    // pre-load fallback frame and would not guard the fixed-human behavior.
    expect(screen.getByText('你')).toBeInTheDocument();
  });

  it('sends a follow-up, creating a related session when none exists', async () => {
    // Feed has no session at all → send() must create one before posting.
    stub();
    const sessionPosts: unknown[] = [];
    const messagePosts: { sessionId: string; content: unknown }[] = [];
    server.use(
      http.post('/api/v1/chat/sessions', async ({ request }) => {
        sessionPosts.push(await request.json());
        return HttpResponse.json({ id: 's-new', user_id: 1, title: '事件追问', agent_id: 'default' });
      }),
      http.post('/api/v1/chat/sessions/:id/messages', async ({ params, request }) => {
        const body = (await request.json()) as { content?: unknown };
        messagePosts.push({ sessionId: String(params.id), content: body.content });
        return HttpResponse.json({
          session_id: String(params.id),
          assistant_message: { id: 'a1', content: '收到', created_at: '2026-10-07T10:02:00Z' },
          tool_calls: [],
          usage: { prompt_tokens: 0, completion_tokens: 0, total_tokens: 0 },
          iterations: 1,
        });
      }),
    );

    renderChat();

    const textarea = await screen.findByRole('textbox', { name: /消息输入框|message input/i });
    fireEvent.change(textarea, { target: { value: '磁盘为什么满？' } });
    fireEvent.keyDown(textarea, { key: 'Enter' });

    await waitFor(() => expect(messagePosts).toHaveLength(1));
    expect(sessionPosts).toHaveLength(1);
    expect(messagePosts[0].sessionId).toBe('s-new');
    expect(messagePosts[0].content).toBe('磁盘为什么满？');
    await waitFor(() => expect(textarea).toHaveValue(''));
  });
});
