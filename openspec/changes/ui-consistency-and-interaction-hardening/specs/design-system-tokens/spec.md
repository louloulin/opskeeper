## ADDED Requirements

### Requirement: 卡片容器跨表面一致

同一角色的卡片容器 MUST 在全站使用同一实现：优先 `Card` 原语（`rounded-2xl surface-card`）或 `.surface-card` 语义类；MUST NOT 继续扩散 `rounded-xl border border-zinc-800/60 bg-zinc-900/40`、`rounded-xl border border-zinc-800 bg-zinc-900/40`、`rounded-lg border border-zinc-800` 等竞争配方。同一原语在同类页面的内边距 MUST 取自统一集合（如 `p-4` 常规卡 / `p-6` 大容器），MUST NOT 出现同一角色卡在 `p-6`/`p-5`/`p-4`/`p-3`/`p-0` 间随机漂移。模态表面 MUST 使用 `.surface-card`（现 `Modal.tsx` 用 `border-zinc-800 bg-zinc-900`）。

#### Scenario: 同角色卡片同配方
- **WHEN** 对比两个域里的「列表容器卡」
- **THEN** 两者使用同一半径/边框/阴影来源（`surface-card` + rk 阶梯），浅/深主题均无差异

#### Scenario: 内边距成集合
- **WHEN** 检查同类页面的卡片内边距
- **THEN** 取值落在既定小集合内，无孤立值

### Requirement: 分区标题统一单一样式档

小标题（section header）MUST 收敛为单一 cased 样式档（大小写、字重、字号、色阶固定），MUST NOT 同时存在 `text-sm font-medium uppercase tracking-wider text-zinc-300`、`text-xs … uppercase tracking-wide text-zinc-500`、`text-xs font-semibold text-zinc-300`、`text-sm font-semibold text-zinc-100` 等 6+ 种竞争写法。大标题仍由 `PageHeader` 承担。

#### Scenario: 分区标题一档
- **WHEN** 对比跨页面的分区小标题
- **THEN** 它们使用同一套类（编码唯一来源），仅文案不同

### Requirement: 徽标与状态 pill 收敛到原语

行内徽标 MUST 使用既有原语：通用元数据用 `Chip`（`rounded-full text-[10px]`），状态用 `StatusPill`（`rounded-full … ring-1 ring-inset`）。MUST NOT 继续内联手写 `rounded px-1.5 py-0.5` 方形徽标或第三套 pill；`StatusRow` 与 `StatusPill` 的重复形态 MUST 收敛为单一状态 pill 语义。

#### Scenario: 徽标形态唯一
- **WHEN** 检查任意页面的行内徽标
- **THEN** 其形态来自 `Chip` 或 `StatusPill`，无手写第四种 pill

### Requirement: 按钮形态跨表面一致

按钮 MUST 使用 `ui/Button` 原语，半径与尺寸 MUST 取自统一集合。手写「次级/幽灵」按钮 MUST NOT 继续以 4 种内边距（`px-2.5 py-1.5 text-xs` / `px-3 py-1.5 text-xs` / `px-2 py-1 text-[11px]` / `px-2.5 py-1.5 text-[12px]`）与混用 `rounded-md`/`rounded-lg` 扩散。`Button` 原语自身的变体半径不一致（primary `rounded-full` vs ghost/danger/subtle `rounded-lg`）MUST 明确为有意规范或收敛（以本次定案为准并锁定）。

#### Scenario: 次要按钮同一形态
- **WHEN** 对比两处「刷新」类次要按钮
- **THEN** 圆角与尺寸一致，来自同一原语/尺寸档

### Requirement: 空态统一到 EmptyState 原语

空态 MUST 使用 `ui/EmptyState` 原语。MUST NOT 存在本地重复实现或影子定义（现 `Alerts.tsx` 与 `Skills.tsx` 各自 `import` 了一个本地 `EmptyState`，遮蔽了原语）。手写的 `h-48`/`h-40`/`py-16 text-center` 等 7+ 种空态 MUST 收敛。

#### Scenario: 空态同源
- **WHEN** 检查任意页面空态
- **THEN** 均由 `ui/EmptyState` 渲染，无本地影子实现

### Requirement: 表头与工具栏一致

`thead` MUST 采用统一样式档（背景、字号、字重、大小写、色阶）与统一单元格内边距集合；工具栏/popover MUST 采用统一配方（圆角、背景、阴影、遮罩 z-index），MUST NOT 同时存在 `z-10` 与 `z-40` 两套遮罩层级。

#### Scenario: 表头一致
- **WHEN** 对比两张数据表的表头
- **THEN** 背景/字号/字重/内边距一致

### Requirement: 加载态统一模式

加载态 MUST 使用统一模式（单一原语或既定类组合），MUST NOT 存在 5 种手写变体（`py-16`/`h-40`/`h-60`/`Loader2+text-xs`/`Card h-24`）。见 `interaction-accessibility` 对「加载态必需」的要求。

#### Scenario: 加载态同源
- **WHEN** 检查任意页面加载态
- **THEN** 使用同一模式，深/浅主题下均可辨识
