## 1. 交付物卡

- [x] 1.1 `web/src/components/DeliverableCard.tsx`：类型徽标改 `Chip` 原语；容器/缩略图/预览圆角统一 `rounded-rk-*`、边框统一 `border-border`；加载动画补 `motion-safe:`
- [x] 1.2 追加断言锁定 Chip 徽标与圆角 token（不删既有，尤其 `h-40` 占位断言）

## 2. AgentAvatar

- [x] 2.1 `web/src/components/AgentAvatar.tsx`：补同色低透明度细环（`ring-1 ring-inset`），消除邻接 card 的扁平感；不放 `aria-label`（发言人姓名已是可见文本，避免重复播报）
- [x] 2.2 追加断言（不得改动既有锁定的 `bg-violet-500/10`、`h-8/h-10`、`rounded-rk-sm/rk-md`，不得让 className 含 `rounded-s-`）

## 3. ContextPanel

- [ ] 3.1 `web/src/components/ContextPanel.tsx`：裸 `rounded`/`rounded-md` → `rounded-rk-sm`；行项与知识引用项补 hover；分区标题层级统一
- [ ] 3.2 追加断言（不得破坏「分区内零 button」只读断言与既有 href/`data-message-id` 断言）

## 4. Discover

- [ ] 4.1 `web/src/pages/Discover.tsx`：标签条与内容面板视觉衔接；补 `focus-visible`；`aria-controls`/`role=tabpanel` 关联；左右方向键切换
- [ ] 4.2 追加断言（不得破坏既有 `?tab=` 路由与 `aria-selected` 断言）

## 5. 日报预设与简报卡

- [ ] 5.1 `web/src/pages/Tasks.tsx`：日报预设项加视觉可辨识（强调色图标/轻分隔/圆角），文案与 onClick 与 `dailyBriefSeed` 载荷不变
- [ ] 5.2 `web/src/components/ReportCards.tsx`、`ReportContent.tsx`：容器迁移到 `surface-card rounded-rk-*`；硬编码状态色改语义 token
- [ ] 5.3 追加断言锁定简报卡 chrome 与预设入口可辨识

## 6. 全量验证与走查

- [ ] 6.1 `cd web && pnpm test` 全绿（exit 0）、`pnpm typecheck`、`pnpm build` 均 exit 0
- [ ] 6.2 浏览器双主题 × 中英双语四组走查五处表面（本地后端 :8090 / 前端 :5174）
- [ ] 6.3 Non-Goals 复核：无新功能、无后端改动、无新依赖、无 IA 变化、未删除既有测试用例
