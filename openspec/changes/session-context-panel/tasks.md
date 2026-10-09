## 1. 前置核查

- [x] 1.1 核查 mention token 写入格式(`ChatInput.tsx:229`)与 recomputeMentionContext(`:163`):确认为 `@{type}:{id}({label}) ` 文本 token,历史消息 API 不返回结构化 mentions,面板只能从 content 反推
- [x] 1.2 盘点消息内联 `tool_calls[]` 结构(`web/src/api/chat.ts:6-20`)与工具类别映射(`web/src/lib/toolSkill.ts` toolGroupKey:knowledge 类含 query_knowledge/web_search/*source*)；设计发现 DF1:chatruntime 无结晶类工具,结晶为 incident 闭环事后 hook,不出现在消息流——结晶节移出本期

## 2. 推导纯函数

- [x] 2.1 实现 `deriveSessionContext(messages) → { mentions[], knowledgeRefs[] }`:mention token 解析按 type+id 去重(type 仅接受 device/incident/rule/file);知识引用按 toolGroupKey==='knowledge' 类别映射识别(不逐个工具名硬编码)
- [x] 2.2 每条上下文条目携带来源消息标识,支持溯源
- [x] 2.3 纯函数单测:提及去重、非法 type 忽略、知识引用提取、纯文本不误报、空会话返回空结构

## 3. 面板 UI

- [x] 3.1 实现面板组件:两节呈现(提及对象/知识引用),引导空态
- [x] 3.2 宽屏对话页右列布局;窄屏可折叠侧板;默认折叠,展开态记忆(轻量本地持久化)
- [x] 3.3 点击运维对象跳详情页(既有路由);每条目可定位来源消息
- [x] 3.4 面板只读,不提供编辑/删除入口

## 4. 测试

- [x] 4.1 扩展测试:覆盖 spec 全部场景(提及/知识/空态/不误报/溯源/只读)
- [x] 4.2 保活既有测试:`ChatThread`、`MessageBubble`、`ChatInput` 既有断言不得破(消息流渲染不动);不得删除任何既有测试用例

## 5. 验证与走查

- [x] 5.1 `cd web && pnpm test` 全绿(exit 0),`pnpm typecheck` exit 0,`pnpm build` exit 0
- [ ] 5.2 双主题 + 中英双语走查:面板配色、空态文案、条目排版正确
- [ ] 5.3 宽窄屏走查:右列与折叠侧板两种形态切换正确,默认折叠且展开态记忆生效
- [x] 5.4 复核 Non-Goals 全部未破:零新增数据接口(`git diff` 无 `core/`、`cmd/` 改动)、消息流渲染未变、无编辑/删除入口

> 5.2 / 5.3 未勾:需要登录后在浏览器中人工核对双主题/双语与宽窄屏形态,本会话凭据录入被安全策略拦截(不得以任何工具绕过),故未执行。证据侧的等价覆盖:组件测试已断言展开/折叠、提及跳转、知识分节、空态、只读与持久化;主题/语言为纯 CSS 类与 i18n 文案,由既有 ui 原语与 tr() 承载。待人工走查补验后勾选。