// sessionContext — 从「已渲染的消息流」反推本会话的运维上下文
// (被 @提及的平台对象 + 知识库引用)。纯函数:零网络、零 UI 依赖、零 store 依赖。
//
// 数据来源即渲染中的 messages[]:mention token 存在于 user 消息文本内
// ("@{type}:{id}({label})",由 ChatInput 写入),工具调用存在于
// assistant 的 tool_calls[] 或流式合成的 tool_card 行。面板是「屏幕上
// 已有内容」的只读视图,不发起任何请求。
import type { ChatMessage, MentionType } from '@/api/chat';

export type MentionRef = {
  type: MentionType;
  id: string;
  label: string;
  /** 来源消息在 messages[] 中的 0 基下标(用于「定位来源消息」)。 */
  sourceIndex: number;
  /** 来源消息 id。 */
  messageId: string;
};

export type KnowledgeRef = {
  name: string;
  status: 'pending' | 'success' | 'error' | 'timeout';
  durationMs?: number;
  sourceIndex: number;
  messageId: string;
  toolCallId?: string;
};

export type SessionContext = {
  mentions: MentionRef[];
  knowledgeRefs: KnowledgeRef[];
};

const MENTION_TYPES: readonly MentionType[] = ['device', 'incident', 'rule', 'file'];

// MENTION_RE 匹配结构化 mention token。组1=type,组2=id,组3=label。
// 要求 '@' 之前是行首或空白,避免把 user@host 这类邮件地址当作提及;
// type 组仅收 [a-z]+,非法的 'user'/'foo' 交由白名单过滤。
const MENTION_RE = /(?:^|\s)@([a-z]+):([^\s()]+)\(([^)]*)\)/g;

export function deriveSessionContext(messages: ChatMessage[]): SessionContext {
  return {
    mentions: deriveMentions(messages),
    knowledgeRefs: deriveKnowledgeRefs(messages),
  };
}

function deriveMentions(messages: ChatMessage[]): MentionRef[] {
  const out: MentionRef[] = [];
  const seen = new Set<string>();
  messages.forEach((m, index) => {
    if (m.role !== 'user' || !m.content) return;
    // matchAll 使用正则的内部克隆,不依赖共享 lastIndex,可安全复用 MENTION_RE。
    for (const match of m.content.matchAll(MENTION_RE)) {
      const type = match[1] as MentionType;
      if (!MENTION_TYPES.includes(type)) continue; // 非法/伪造 type 一律忽略,防误报
      const id = match[2];
      const key = `${type}:${id}`;
      if (seen.has(key)) continue; // 按 type+id 去重
      seen.add(key);
      out.push({ type, id, label: match[3], sourceIndex: index, messageId: m.id });
    }
  });
  return out;
}

// deriveKnowledgeRefs 在 Task 2 实现;此处先返回空数组以满足 mentions 单测。
function deriveKnowledgeRefs(_messages: ChatMessage[]): KnowledgeRef[] {
  return [];
}
