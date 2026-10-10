## Context

本 change 是纯打磨（tweak），不改后端、不加依赖、不加功能。所有改动都收敛到**既有 token**：`surface-card`、`rounded-rk-*`（4/8/12/16/24/32/40px 七级阶梯）、`shadow-card`/`shadow-pop`、`Chip` 原语、语义色 `ok/warn/danger/info` 与品牌紫阶 `accent-50/100/600/700`。原则：**能用 token 就不写裸 `zinc-*`；能复用原语就不手写重复标记。**

双主题靠 `html.light` 选择器上的 `zinc` 重映射块兜底。因此新增样式**优先使用 token 类**（`rgb(var(--x))` 驱动，自动双态），避免引入新的裸 `zinc` 类——后者需要同步扩写重映射块，容易漏。本次不改动重映射块的既有语义，只在其确需覆盖时做最小追加。

## 实现说明

### 1. 交付物卡（`components/DeliverableCard.tsx`）
- 类型徽标（现为手写 `rounded bg-zinc-800 px-1.5 py-0.5 text-[10px] text-zinc-300`）改用 `Chip` 的 neutral 语调，消除重复 pill 标记。
- 容器、缩略图、展开预览的 `rounded`/`rounded-rk-*` 混用统一为 `rounded-rk-sm`/`rounded-rk-md`；缩略图与预览的 1px 边框统一到 `border-soft`。
- 加载态 `animate-spin` 外层补 `motion-safe:`。

### 2. AgentAvatar（`components/AgentAvatar.tsx`）
- 保留 `TONE_CLASS` 的 `-500/10` 浅底 + `-300` 图标语汇，但抽取为与 `Chip` 语气同源的映射（同色系）；不改可见色调，只统一来源。
- 与 `surface-card` 邻接时不加环会显扁：新增 `ring-1 ring-inset` 搭配同色低透明度，深浅主题均可见。
- 代表发言人时补 `aria-label={角色名}`；纯装饰语境保持 `aria-hidden`。**注意**：既有测试锁定了 `bg-violet-500/10`、`h-8/h-10`、`rounded-rk-sm/rk-md`，且断言 className 不得含 `rounded-s-`——新增类只能追加，不能替换这些。

### 3. ContextPanel（`components/ContextPanel.tsx`）
- 裸 `rounded`/`rounded-md` → `rounded-rk-sm`；折叠按钮、行项统一。
- 行项补 `hover:bg-zinc-900`（已部分存在）与知识引用项的可点击跳转（保留 `href`，补 hover 与 focus-visible）。
- 分区标题（现 `text-[11px] uppercase tracking-wide`）与行字号层级统一到控制台惯例。
- 窄屏：`w-72`/`w-11` 增加一个更窄断点，避免挤压聊天区。
- **硬约束**：面板只读——既有测试断言「分区内零 button」，任何新增都不得引入 button。

### 4. Discover（`pages/Discover.tsx`）
- 标签条（`rounded-rk-md bg-zinc-900/60 p-1` + 内层 `rounded-full`）与嵌入式页面的自带 `PageHeader` 形成双重 chrome：收紧内嵌页头部与标签条的视觉衔接（间距/分隔），不改路由。
- 补 `focus-visible:ring` 焦点样式；标签容器补 `role="tablist"` 语义，标签补 `aria-controls`，面板补 `id` + `role="tabpanel"`。
- 左右方向键在标签间移动焦点并切换（标准 tablist 行为）。

### 5. 日报预设入口（`pages/Tasks.tsx:154-176`）
- 该按钮现与上下两个普通项类名完全相同。给它加可辨识度：强调色图标已有（`text-sky-400/80`），补一项轻分隔（`border-t border-zinc-800/60` 或 `mt`）与 `rounded-rk-sm`，或给标题加 accent 色。**不改文案、不改 onClick、不改 `dailyBriefSeed` 载荷。**

### 6. 简报卡（`components/ReportCards.tsx`、`components/ReportContent.tsx`）
- 容器从 `rounded-xl border border-zinc-800/60 bg-zinc-900/40` 迁移到 `surface-card rounded-rk-lg`。
- 统计格 `bg-zinc-900/40` → `surface-card` 变体；硬编码状态色（`bg-red-400`/`bg-amber-400`/`text-indigo-300`）→ `danger`/`warn`/`info` 语义 token。
- 与交付物卡（D1）同款 chrome，使简报卡不再像「另一个系统」。
- 这两个文件当前**无** co-located 测试，改动风险低；新增断言以锁定 chrome。

### 7. ui 原语（`components/ui/Button.tsx`）
- ghost（`border-zinc-700 bg-zinc-900`）/danger/subtle 收敛到 token：`border-border bg-card` 等；`text-*` 用 `--text` 系。保持 props 接口不变（32 处调用点无需改动）。

## 测试策略

- **不改既有断言、不删既有用例**；新行为以追加断言覆盖。
- 每个表面改完后运行其 co-located 测试；全量 `cd web && pnpm test` 必须全绿。
- 关键回归风险：AgentAvatar（类名被精确锁定）、ContextPanel（只读零按钮断言）、DeliverableCard（`h-40` 占位尺寸断言）、Discover（路由与 `aria-selected`）。改这四个文件前先读对应测试。

## 验证

- `cd web && pnpm test && pnpm typecheck && pnpm build` 全绿。
- 浏览器（本地已启动：后端 :8090、前端 :5174）双主题 × 中英双语四组走查五处表面。
- Non-Goals 复核：无新功能、无后端改动、无新依赖、无 IA 变化、未删既有测试。
