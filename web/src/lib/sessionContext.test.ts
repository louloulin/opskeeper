import { describe, expect, it } from 'vitest';
import { deriveSessionContext } from './sessionContext';
import type { ChatMessage } from '@/api/chat';

function userMsg(id: string, content: string): ChatMessage {
  return { id, role: 'user', content };
}

describe('deriveSessionContext · mentions', () => {
  it('从 user 消息解析结构化 mention 并按 type+id 去重,携带来源', () => {
    const messages = [
      userMsg('m1', '看看 @device:7(核心网关) 的情况'),
      userMsg('m2', '再看 @device:7(核心网关) 和 @incident:42(磁盘满)'),
    ];
    const { mentions } = deriveSessionContext(messages);
    expect(mentions).toHaveLength(2);
    expect(mentions[0]).toMatchObject({
      type: 'device', id: '7', label: '核心网关', messageId: 'm1', sourceIndex: 0,
    });
    expect(mentions[1]).toMatchObject({ type: 'incident', id: '42', messageId: 'm2', sourceIndex: 1 });
  });

  it('忽略 type 不在 device/incident/rule/file 之内的伪 token', () => {
    const messages = [userMsg('m1', '伪装 @user:1(张三) 与 @device:7(核心网关)')];
    const { mentions } = deriveSessionContext(messages);
    expect(mentions).toHaveLength(1);
    expect(mentions[0]).toMatchObject({ type: 'device', id: '7' });
  });

  it('纯文本消息、邮件地址不产生条目', () => {
    const messages = [userMsg('m1', 'hello world, mail me at ops@example.com please')];
    expect(deriveSessionContext(messages).mentions).toEqual([]);
  });

  it('非 user 角色不参与 mention 解析', () => {
    const messages: ChatMessage[] = [{ id: 'a1', role: 'assistant', content: '@device:7(核心网关)' }];
    expect(deriveSessionContext(messages).mentions).toEqual([]);
  });

  it('空会话返回空结构', () => {
    expect(deriveSessionContext([])).toEqual({ mentions: [], knowledgeRefs: [] });
  });
});
