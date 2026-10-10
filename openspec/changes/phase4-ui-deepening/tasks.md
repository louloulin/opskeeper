## 1. 共用原语与 token 收敛

- [ ] 1.1 `web/src/components/ui/Button.tsx`：ghost/danger/subtle 的裸 `zinc-*` 收敛到 token（`border-border`/`bg-card`/`text-*`）；保持 props 接口不变
- [ ] 1.2 复核改动未引入新的裸 `zinc` 类（新样式优先用 token，避免扩写 light 重映射块）；若确需，做最小追加且不改块语义

## 2. 交付物卡

- [ ] 2.1 `web/src/components/DeliverableCard.tsx`：类型徽标改 `Chip` 原语；容器/缩略图/预览圆角统一 `rounded-rk-*`、边框统一 `border-soft`；加载动画补 `motion-safe:`
- [ ] 2.2 追加断言锁定 Chip 徽标与 `rounded-rk-*`（不删既有，尤其 `h-40` 占位断言）

## 3. AgentAvatar

- [ ] 3.1 `web/src/components/AgentAvatar.tsx`：与 `surface-card` 邻接补同色细环；代表发言人时补 `aria-label`，装饰语境保持 `aria-hidden`；tone 与 Chip 语义同源（可见色调不变）
- [ ] 3.2 追加断言（不得改动既有锁定的 `bg-violet-500/10`、`h-8/h-10`、`rounded-rk-sm/rk-md`，不得让 className 含 `rounded-s-`）

## 4. ContextPanel

- [ ] 4.1 `web/src/components/ContextPanel.tsx`：裸 `rounded`/`rounded-md` → `rounded-rk-sm`；行项与知识引用项补 hover/`focus-visible`（保留 href 跳转）；分区标题层级统一；窄屏宽度断点
- [ ] 4.2 追加断言（不得破坏「分区内零 button」只读断言与既有 href/`data-message-id` 断言）

## 5. Discover

- [ ] 5.1 `web/src/pages/Discover.tsx`：标签条与内嵌页面头部视觉衔接（去双重 chrome）；补 `focus-visible`；`role=tablist`/`aria-controls`/`role=tabpanel`；左右方向键切换
- [ ] 5.2 追加断言（不得破坏既有 `?tab=` 路由与 `aria-selected` 断言）

## 6. 日报预设与简报卡

- [ ] 6.1 `web/src/pages/Tasks.tsx`：日报预设项加视觉可辨识（强调色图标/轻分隔/圆角），文案与 onClick 与 `dailyBriefSeed` 载荷不变
- [ ] 6.2 `web/src/components/ReportCards.tsx`、`ReportContent.tsx`：容器迁移到 `surface-card rounded-rk-*`；硬编码状态色改语义 token
- [ ] 6.3 追加断言锁定简报卡 chrome 与预设入口可辨识

## 7. 全量验证与走查

- [ ] 7.1 `cd web && pnpm test` 全绿（exit 0）、`pnpm typecheck`、`pnpm build` 均 exit 0
- [ ] 7.2 浏览器双主题 × 中英双语四组走查五处表面（本地后端 :8090 / 前端 :5174）
- [ ] 7.3 Non-Goals 复核：无新功能、无后端改动、无新依赖、无 IA 变化、未删除既有测试用例
