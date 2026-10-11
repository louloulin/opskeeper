## Context

这是一次**硬化**而非改造：目标是让既有 UI 在全站范围内「同角色同样式、可交互即可达」。零新功能、零后端、零新依赖、零信息架构变化。

审计（2026-10-11，只读）已在 `proposal.md` 的同时给出**真实证据**：卡片容器有 4 套竞争配方、分区标题 6+ 档、空态有 2 个本地影子实现、`window.confirm/prompt/alert` 散落约 12 处、`Monitor.tsx` 完全没有加载态。任务据此逐条对齐，不凭印象。

## 策略：三层收敛

```
┌─ 层 1 共享原语 (ui/*) ──────────────────────────────┐
│ 先在这里定案：Card 内边距档、Button 变体半径、      │
│ EmptyState 唯一化、Modal→surface-card、Loading 模式、│
│ StatusPill 单一化。风格经组件向下传导，改动面最广。 │
└───────────────────────┬─────────────────────────────┘
                        ▼
┌─ 层 2 逐域页面 ────────────────────────────────────┐
│ 每个域把「同角色不同样式」对齐到层 1 的定案，        │
│ 并把本地影子实现（空态/加载态/徽标）换成原语。      │
└───────────────────────┬─────────────────────────────┘
                        ▼
┌─ 层 3 横切 a11y ───────────────────────────────────┐
│ 原生弹窗清除、icon-only aria、可点元素键盘化、      │
│ tablist/抽屉语义。这些跨越所有域，单独一期收口。    │
└────────────────────────────────────────────────────┘
```

## 关键取舍：对齐 ≠ 全量 token 化

**不做**「把裸 `zinc-*` 全换成 token」。实测 `--border`(40,40,50) 比 `border-zinc-700`(63,63,70) 暗、`--text-muted`(zinc-400) 比 `text-zinc-300` 暗、`--card` 与 `bg-zinc-900` 也有差 —— 全量替换会**静默改掉全站外观**，且无法逐屏验证。

做法改为：**把「同角色却不同配方」的元素对齐到同一配方**。若两个竞争配方视觉差异明显，选择「视觉影响最小且更符合既有 token 语义」的一方（如卡片统一到 `surface-card`：它已由浅色 shim 保证不双阴影），并**在该期截图里逐屏佐证**。

## 回归防护

- 改任一页面**前先读它的 co-located 测试**；`AgentAvatar`/`DeliverableCard`/`ContextPanel`/`Discover`/`Tasks` 有类名或结构断言，只可追加不可替换。
- 每期结束跑 `cd web && pnpm test && pnpm typecheck && pnpm build`，并出**真机截图**（深/浅 × 中/英，用已登录的无头浏览器）。
- 交互项以自动化断言锁定：键盘事件（`fireEvent.keyDown`）、`aria-*` 属性、空/加载态分支渲染。
- 每期独立提交，commit message 写明「对齐了哪几处竞争配方」。

## 分期

| 期 | 内容 | 主要文件 |
|---|---|---|
| 0 | 共享原语收敛 | `components/ui/{Card,Button,EmptyState,Modal,index}` + 新增 LoadingState |
| 1 | 高流量域一致性 | Home / Dashboard / Agents / Discover(+3 内嵌页) / Alerts / Devices / Monitor |
| 2 | 运维域一致性 | Logs / Traces / Topology / Edges / AlertRules / IncidentDetail / NodeAgents |
| 3 | 日常域一致性 | Tasks / Pages / ReportCards+ReportDetail / Knowledge / KnowledgeRepos / Flows |
| 4 | 审批与管理一致性 | Approvals / admin/{Users,Audit} / settings/* |
| 5 | 横切 a11y | 原生弹窗清除、icon-only aria、可点元素键盘化、IncidentDetail tablist、抽屉 dialog |
| 6 | 全量验证与截图 | test/typecheck/build + 真机双主题双语 + Non-Goals 复核 |

每期产出：改动 + 该期测试 + 该期截图。期与期之间是增量，可随时停。

## 边界与风险

- **`Button` 变体半径不一致**（primary pill vs ghost `rounded-lg`）可能是**有意**的视觉层级：定案为「保留 pill primary + 次级统一 `rounded-rk-md`」，但先确认无测试锁定，再改。
- **`window.confirm` 替换**：仓库已有 `ui/Modal`，但部分调用点是 `alert`/`prompt`（`Monitor`、`Pages`），需换成「确认弹层 + 应用内输入弹层」两种形态 —— 属既有能力的重新接线，不是新功能。
- **深/浅与双语**：所有新增样式优先 token 类（自动双态）；每期截图覆盖四组。
