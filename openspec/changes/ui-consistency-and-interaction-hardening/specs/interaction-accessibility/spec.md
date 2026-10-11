# interaction-accessibility Specification

## Purpose

全站交互可达性与可访问性契约：键盘可达、焦点可见、空/加载/禁用态、可访问名、原生弹窗禁用、复杂控件 aria 关联。适用于所有用户界面表面。

## Requirements

### Requirement: 可交互元素键盘可达

所有可点击元素 MUST 通过可聚焦元素承载交互：优先用原生 `<button>` / `<a>`；确实需要用容器 `onClick` 时，MUST 同时提供 `role`、`tabIndex={0}` 与 `onKeyDown`（Enter/Space 激活），且 MUST NOT 出现「鼠标可点、键盘不可达」的元素。参考实现：`Tasks.tsx` 表格行；参考反例：`Agents.tsx` 用 `<Card onClick>`（`Card` 渲染 `<section>`，不可聚焦）。

#### Scenario: 卡片式可点入口键盘可达
- **WHEN** 用键盘 Tab 遍历一个以卡片承载的可点入口（如 Agents 档案卡）
- **THEN** 该入口可获得焦点，按 Enter 触发与鼠标点击相同的动作

#### Scenario: 表格行可点
- **WHEN** 列表行 `onClick` 表示「打开详情」
- **THEN** 该行可聚焦、可 Enter 激活，且 `role`/`tabIndex`/`onKeyDown` 齐备

### Requirement: 焦点可见与弹层焦点管理

所有可聚焦元素 MUST 在键盘聚焦时呈现可见焦点（沿用全局 `*:focus-visible` token 规则）。模态与抽屉 MUST 具备 `role="dialog"`、`aria-modal`、`aria-label`/`aria-labelledby`，MUST 支持 Esc 关闭，关闭按钮 MUST 有可访问名。参考实现：`Modal.tsx`、`ChatDrawer.tsx`；反例：`Crystallized.tsx`、`settings/AuditLog.tsx` 的 `fixed inset-0` 抽屉。

#### Scenario: 抽屉可 Esc 关闭且有可访问名
- **WHEN** 打开任一抽屉/模态
- **THEN** 其带 `role="dialog"` 与可访问名，按 Esc 关闭，关闭按钮具 `aria-label`

### Requirement: 空态与加载态必需

每个列表/面板 MUST 明确区分三种状态：**加载中**、**空**、**有数据**，不得出现「无数据时页面空白且无提示」或「请求进行中无任何指示」。加载指示 MUST 使用统一模式；`Monitor.tsx` 当前的「无任何加载指示」属缺陷。

#### Scenario: 数据为空
- **WHEN** 列表请求成功但结果为空
- **THEN** 呈现明确的空态文案（而非空白）

#### Scenario: 加载中
- **WHEN** 面板正在取数
- **THEN** 呈现统一加载指示，且与空态视觉可区分

### Requirement: 图标按钮具可访问名

仅含图标的交互控件 MUST 提供 `aria-label`（`title` 不能替代）。参考实现：`Sidebar.tsx`（`aria-label` 16 处）；反例：`Tasks.tsx` 的 `IconBtn`、`Agents.tsx`、`Flows.tsx`、`Mcp.tsx`、`settings/AuditLog.tsx`。

#### Scenario: 图标按钮可被读出
- **WHEN** 屏幕阅读器聚焦一个纯图标按钮
- **THEN** 读出其 `aria-label` 描述的动作

### Requirement: 原生弹窗禁用

代码 MUST NOT 使用 `window.confirm` / `window.prompt` / `window.alert` 承载确认、输入或提示；MUST 改用应用内 Modal 原语（`ui/Modal.tsx`）。现状违规点：`Topology.tsx`、`KnowledgeRepos.tsx`、`Tasks.tsx`、`ReportDetail.tsx`、`Monitor.tsx`、`Flows.tsx`、`Pages.tsx`（含 `window.prompt` 复制链接）。

#### Scenario: 危险操作确认
- **WHEN** 触发删除/重跑等危险操作
- **THEN** 呈现应用内确认弹层（非浏览器原生 confirm），键盘可操作、可 Esc 取消

### Requirement: 复杂控件 aria 关联

`tablist` MUST 提供 `aria-controls`、roving `tabIndex` 与左右方向键切换；菜单 MUST 有 `role="menu"`/`menuitem` 语义或等价键盘支持。参考实现：`Discover.tsx`；反例：`IncidentDetail.tsx` 的 tablist（有 `role` 但无 `aria-controls`/方向键）。

#### Scenario: 标签页方向键切换
- **WHEN** 焦点在 `tablist` 上按左右方向键
- **THEN** 选中标签移动、面板切换、`aria-selected` 同步
