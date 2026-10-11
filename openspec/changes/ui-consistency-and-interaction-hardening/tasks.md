## 1. 共享原语收敛（阶段 0）

- [ ] 1.1 `components/ui/Card.tsx`：定案并锁定内边距档（如常规 `p-4` / 大容器 `p-6`），使同角色卡片不再在 `p-6`/`p-5`/`p-4`/`p-3`/`p-0` 间漂移；确保 `surface-card` 为唯一卡片配方
- [ ] 1.2 `components/ui/Button.tsx`：定案变体半径规范（primary pill 保留；次级变体统一到 `rounded-rk-md`）与尺寸档；先确认无测试锁定类名再改
- [ ] 1.3 `components/ui/EmptyState.tsx`：确立为全站唯一空态原语；文档化其尺寸/文案档
- [ ] 1.4 `components/ui/Modal.tsx`：表面由 `border-zinc-800 bg-zinc-900` 改为 `.surface-card`，与全站卡片一致
- [ ] 1.5 新增统一加载态原语（如 `components/ui/LoadingState.tsx`）：单一加载模式，供全站替换 5 种手写变体
- [ ] 1.6 `components/StatusPill.tsx` / `components/StatusRow.tsx`：收敛为单一状态 pill 语义（去重形态）
- [ ] 1.7 阶段 0 自检：`pnpm test && pnpm typecheck && pnpm build` 全绿 + 首屏截图

## 2. 高流量域一致性（阶段 1）

- [ ] 2.1 `pages/Dashboard.tsx`：卡片→`surface-card`；分区标题收敛到统一档（现 4 处 `uppercase tracking-wider text-zinc-300`）；空态→`EmptyState`/`LoadingState`
- [ ] 2.2 `pages/Home.tsx`：分区标题（现 3 处 `text-xs uppercase tracking-wide text-zinc-500`）收敛；卡片一致
- [ ] 2.3 `pages/Agents.tsx`：卡片配方统一；icon-only 按钮补 `aria-label`；`<Card onClick={onView}>`（:318）键盘化
- [ ] 2.4 `pages/Discover.tsx` + `Skills.tsx`/`PluginMarketplace.tsx`/`Crystallized.tsx`：消 `Skills.tsx` 本地 `EmptyState` 影子；`Crystallized.tsx` 抽屉补 dialog 语义+Esc；标题档统一
- [ ] 2.5 `pages/Alerts.tsx`：消本地 `EmptyState` 影子；表头统一（现 `bg-zinc-950`）
- [ ] 2.6 `pages/DeviceShell.tsx`（及 Devices 相关）：卡片内边距统一（现 `p-6`）
- [ ] 2.7 `pages/Monitor.tsx`：**补加载态**（当前无任何加载指示）；清除 `window.confirm`(:445)/`window.alert`(:454)

## 3. 运维域一致性（阶段 2）

- [ ] 3.1 `pages/Logs.tsx`：徽标形态收敛；空/加载态统一
- [ ] 3.2 `pages/Traces.tsx`：卡片（现 `rounded-lg`，比其它低一档）、表头（现 `bg-zinc-900/60` 无 `font-medium`）、按钮尺寸档收敛
- [ ] 3.3 `pages/Topology.tsx`：popover 配方与遮罩 z-index 统一（现 `z-10` vs 他处 `z-40`）；清除 `window.confirm`(:404,530,950,1250)
- [ ] 3.4 `pages/Edges.tsx` / `EdgeDetail.tsx`：卡片配方（两文件各用一套）统一；徽标收敛
- [ ] 3.5 `pages/AlertRules.tsx`：表头/空态统一
- [ ] 3.6 `pages/IncidentDetail.tsx`：卡片统一；tablist 补 `aria-controls`/roving `tabIndex`/方向键（参考 `Discover.tsx`）；徽标收敛

## 4. 日常域一致性（阶段 3）

- [ ] 4.1 `pages/Tasks.tsx`：卡片/表头/空态统一；`IconBtn` 补 `aria-label`；清除 `window.confirm`(:99)
- [ ] 4.2 `pages/Pages.tsx`：卡片统一；清除 `window.confirm`(:127) 与 `window.prompt`(:158 复制链接→应用内输入弹层)
- [ ] 4.3 `components/ReportCards.tsx` / `pages/ReportDetail.tsx`：卡片统一；清除 `window.confirm`(:43)
- [ ] 4.4 `pages/Knowledge.tsx` / `KnowledgeRepos.tsx`：卡片配方统一；按钮尺寸档统一；清除 `window.confirm`(:494)
- [ ] 4.5 `pages/Flows.tsx` / `FlowEditor.tsx`：icon-only `aria-label`；空态统一；清除 `window.confirm`(:97)

## 5. 审批与管理一致性（阶段 4）

- [ ] 5.1 `pages/Approvals.tsx`：卡片/空态(`h-40`)/加载态(`h-40`)统一到原语；按钮尺寸档
- [ ] 5.2 `pages/settings/*`：卡片内边距档（现 `p-5`/`p-4`/`p-3`/`p-0` 漂移）统一；标题档统一
- [ ] 5.3 `pages/admin/*`（Users/Orgs/Audit）：卡片与标题统一；`AuditLog.tsx` 抽屉补 dialog 语义 + Esc + 关闭按钮 `aria-label`

## 6. 横切 a11y 收口（阶段 5）

- [ ] 6.1 原生弹窗**全清**：`Topology`/`KnowledgeRepos`/`Tasks`/`ReportDetail`/`Monitor`/`Flows`/`Pages` 的 `window.confirm|prompt|alert` 全部改应用内 `Modal`（确认型 + 输入型）
- [ ] 6.2 icon-only 按钮 `aria-label` 补齐：`Tasks`/`Agents`/`Flows`/`Mcp`/`admin/AuditLog`
- [ ] 6.3 可点非交互元素键盘化：`components/NLQueryHelper.tsx`(:155)、`pages/Skills.tsx`(:454 表格行)、残留的 `<Card onClick>`
- [ ] 6.4 `IncidentDetail.tsx` tablist 对齐 `Discover.tsx` 参考实现（`aria-controls`/roving/方向键）
- [ ] 6.5 抽屉（`Crystallized`/`admin/AuditLog`）补 `role="dialog"`/`aria-modal`/Esc/焦点管理

## 7. 全量验证与截图（阶段 6）

- [ ] 7.1 `cd web && pnpm test` 全绿（exit 0）、`pnpm typecheck`、`pnpm build` 均 exit 0
- [ ] 7.2 真机走查：用已登录的无头浏览器扫四个域的关键页，深/浅 × 中/英，逐期截图（原子捕获）
- [ ] 7.3 Non-Goals 复核：无新功能、无后端改动、无新依赖、无 IA 变化、未删除既有测试用例
