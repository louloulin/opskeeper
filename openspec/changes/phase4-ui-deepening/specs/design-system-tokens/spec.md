## ADDED Requirements

### Requirement: 交付物卡与简报卡共用交付物 chrome

交付物卡（DeliverableCard）类型徽标 SHALL 复用 `Chip` 原语，不得手写重复的 pill 标记；卡内所有容器、缩略图与展开预览 MUST 使用 `rounded-rk-*` 与 `border-soft` token，不得混用裸 `rounded`；加载态动画 MUST 受 `motion-safe` 约束。日报简报卡（ReportCards/ReportContent）SHALL 复用同一套容器 chrome（`surface-card` + `rounded-rk-*` + `shadow-card`），其状态色 MUST 取自语义 token（ok/warn/danger/info）而非硬编码颜色类。

#### Scenario: 类型徽标使用 Chip 原语
- **WHEN** 渲染交付物卡的类型徽标（如 report / page）
- **THEN** 徽标由 `Chip` 原语渲染，圆角为 `rounded-full`，浅底色调与全站 Chip 一致

#### Scenario: 卡内圆角 token 化
- **WHEN** 检查交付物卡的容器、缩略图与展开预览
- **THEN** 全部使用 `rounded-rk-*`，深/浅主题下无半径或描边不一致

#### Scenario: 简报卡与交付物卡同款
- **WHEN** 打开一份日报简报
- **THEN** 其容器与 D1 交付物卡使用相同的 `surface-card` 圆角与阴影，统计格状态色取自语义 token

### Requirement: AgentAvatar 视觉层次与可访问性

AgentAvatar SHALL 在与 `surface-card` 邻接时呈现与角色色一致的细环/描边以消除扁平感；当头像代表一条消息的发言人时 SHALL 提供可访问名称标签，纯装饰语境 SHALL 保持 `aria-hidden`。角色色 tone MUST 与 `Chip` 语义色同源，但可见色调 MUST 保持与本规范既有 AgentAvatar 定义一致。

#### Scenario: 发言人头像带可访问标签
- **WHEN** AgentAvatar 代表一条消息的发言人渲染
- **THEN** 该头像带可访问的角色名称标签，屏幕阅读器可读出

#### Scenario: 邻接卡片不扁平
- **WHEN** AgentAvatar 与 `surface-card` 容器相邻呈现
- **THEN** 头像带与角色色一致的细环，视觉层次与卡片协调，深/浅主题均可见

### Requirement: 会话上下文面板视觉一致性与只读性

ContextPanel SHALL 使用 `rounded-rk-*` 统一圆角，不得使用裸 `rounded`/`rounded-md`；列表项 SHALL 提供 hover 反馈，知识引用项 SHALL 可跳转并具 `focus-visible` 样式；分区标题的字号/字重 MUST 与控制台其余面板一致。面板 MUST 保持只读——任何分区内 MUST NOT 引入 button 元素。

#### Scenario: 面板圆角统一
- **WHEN** 展开会话上下文面板
- **THEN** 容器、行项与折叠按钮均为 `rounded-rk-*`，无裸 `rounded` 残留

#### Scenario: 只读不破
- **WHEN** 检查上下文面板的提及与知识分区
- **THEN** 分区内无任何 button 元素（只读展示）

### Requirement: Discover 标签条视觉衔接与键盘可达

Discover 标签条 SHALL 与内容面板视觉衔接，消除标签条与内嵌页面头部形成的双重 chrome；活动标签 MUST 具明确填充对比。标签切换 SHALL 支持左右方向键，MUST 提供 `focus-visible` 焦点样式，且标签与面板 SHALL 通过 `role="tablist"`/`aria-controls`/`role="tabpanel"` 关联。既有 `?tab=` 路由行为 MUST 保持不变。

#### Scenario: 方向键切换标签
- **WHEN** 焦点在 Discover 标签条上并按左右方向键
- **THEN** 焦点与选中标签随之移动，对应面板切换，`aria-selected` 同步更新

#### Scenario: 焦点可见
- **WHEN** 键盘 Tab 聚焦到某个标签
- **THEN** 该标签呈现可见的 `focus-visible` 焦点环

### Requirement: 日报预设入口视觉可辨识

「新建任务」下拉中的「每日值班简报」一键预设项 SHALL 与其相邻的普通新建项在视觉上可辨识（强调色图标 / 轻分隔 / 圆角之一或组合）；其文案、点击行为与提交载荷 MUST 保持不变——不得新增表单字段、端点或 persona。

#### Scenario: 预设入口可辨识
- **WHEN** 打开「新建任务」下拉
- **THEN** 日报预设项与普通「定时任务」项在视觉上可区分，点击后仍预填 daily / 09:00 / 飞书渠道 / 三节模板

### Requirement: ui 原语去裸 zinc

`web/src/components/ui/` 下的原语（Button 的 ghost/danger/subtle 等）SHALL 使用 token 类而非裸 `zinc-*`，以保证双主题一致；原语的 props 接口 MUST 保持向后兼容，既有调用点无需改动即可获得新形态。

#### Scenario: ghost 按钮 token 化
- **WHEN** 渲染 Button variant=ghost
- **THEN** 其边框与底色取自 token，在深/浅主题下均正确，props 调用点无需改动
