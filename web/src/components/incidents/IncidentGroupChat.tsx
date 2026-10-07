import { useCallback, useEffect, useMemo, useState } from 'react';
import { listIncidentEvents, type IncidentEvent } from '@/api/alerts';
import { getMessages, listSessions, type ChatMessage, type ChatSession } from '@/api/chat';
import { fetchLoopTimeline } from '@/api/loops';
import { usePoll } from '@/lib/usePoll';
import { AgentAvatar } from '@/components/AgentAvatar';
import { PhaseIndicator } from '@/components/dpo';
import { tr } from '@/i18n/locale';

const EVENT_LIMIT = 200;
const MESSAGE_TAIL = 100;

type SessionMessages = { session: ChatSession; messages: ChatMessage[] };

// The incident group chat's raw feed: the alert event timeline, the
// incident's diagnosis sessions, and each session's message tail. Held as
// one object so a single load updates them together. Task 22 renders the
// progress strip and the members row from it; Task 23 merges
// `feed.events` + `feed.sessions` + `feed.messagesBySession` into the
// rendered message stream (see mergeIncidentStream).
type ChatFeed = {
  events: IncidentEvent[];
  sessions: ChatSession[];
  messagesBySession: SessionMessages[];
};

const EMPTY_FEED: ChatFeed = { events: [], sessions: [], messagesBySession: [] };

export function IncidentGroupChat({ incidentId }: { incidentId: number }) {
  const [feed, setFeed] = useState<ChatFeed>(EMPTY_FEED);
  const [phases, setPhases] = useState<Awaited<ReturnType<typeof fetchLoopTimeline>>['phases']>([]);

  const load = useCallback(async () => {
    try {
      const [ev, ss] = await Promise.all([
        listIncidentEvents(incidentId, EVENT_LIMIT),
        listSessions({ related_incident_id: incidentId }),
      ]);
      const messagesBySession = await Promise.all(
        ss.items.map(async (s) => ({
          session: s,
          messages: (await getMessages(s.id)).items.slice(-MESSAGE_TAIL),
        })),
      );
      setFeed({ events: ev.items, sessions: ss.items, messagesBySession });
    } catch {
      // A transient poll failure must not blank the loaded feed, and
      // usePoll calls this with `void` — an uncaught rejection here would
      // surface as an unhandled promise rejection. The page's own error
      // banner covers load failures for the incident itself.
    }
  }, [incidentId]);

  useEffect(() => {
    void load();
  }, [load]);
  usePoll(load, 5_000);

  useEffect(() => {
    void fetchLoopTimeline(incidentId)
      .then((r) => setPhases(r.phases))
      .catch(() => {});
  }, [incidentId]);

  const members = useMemo(() => {
    const ids = Array.from(
      new Set(feed.sessions.map((s) => s.agent_id).filter(Boolean)),
    ) as string[];
    return ids.length ? ids : ['default'];
  }, [feed.sessions]);

  return (
    <div className="flex flex-col gap-3">
      <div className="surface-card flex items-center gap-1 overflow-x-auto rounded-2xl px-3 py-2">
        {phases.map((p) => (
          <PhaseIndicator key={p.phase} phase={p.phase} status={p.status} className="shrink-0" />
        ))}
      </div>

      <div className="flex items-center gap-2 px-1">
        <span className="text-xs text-zinc-500">{tr('成员', 'Members')}</span>
        {members.map((id) => (
          <span key={id} className="flex items-center gap-1.5 text-xs text-zinc-400">
            <AgentAvatar agentId={id} size={32} />
            {id === 'default' ? tr('你', 'You') : id}
          </span>
        ))}
      </div>

      {/* 消息流与输入框在 Task 23 接入 */}
    </div>
  );
}
