## Why

OpsKeeper 的功能面已经相当完整——差异化护城河（双签审批 + 载荷哈希绑定 + 独立验证、结晶式无 LLM 自愈、七阶段自治闭环、事件即群聊、防幻觉报表契约)是通用人机平台(teamily:全文 `on-call`/`incident`/`observability` 关键词为 0)与 AiOps 同业(Rootly / incident.io / PagerDuty)都不具备的。

但**产品表面的完成度在拖后腿**：全站存在「同一角色元素、不同页面用不同圆角/阴影/间距/层级」的不一致，以及交互与可访问性缺口(键盘不可达、焦点不可见、缺空态/加载态、icon-only 按钮无 aria-label)。竞品在这些基础打磨上更成熟，用户对 OpsKeeper 的第一印象因此低于其真实能力。

本次只做**硬化**：把既有 UI 收敛一致、把既有交互补齐可达与可感知——**不新增任何功能**。

## What Changes

- **一致性(改样式，不改结构)**：把「同角色元素」在全站收敛到同一组 token——卡片容器、分区标题、列表行、徽标/chip、按钮、空态、表头、弹层、工具栏。只修**真不一致**(同角色却不同样式)，**不做**「把所有裸 `zinc` 全换成 token」的全量改色：实测 token 与裸 zinc 在深色下**并非等价**(`--border`/`--text-muted` 更暗)，全量替换会静默改变外观。
- **交互与可访问性(补齐，不加功能)**：① 所有可交互元素键盘可达且有可见焦点；② `div`/`span` 上的 `onClick` 补 `role`/`tabIndex`/`keydown`；③ 每个列表/面板有明确**空态**与**加载态**(禁用态按需)；④ icon-only 按钮补 `aria-label`；⑤ 清除 `window.confirm`/`window.prompt`(仓库已立规用应用内 Modal 原语)；⑥ tablist/menu/dialog 补 `aria-*` 关联。
- **分期交付**：按页面域分阶段(对话/Agent → 运维域 → 日常域 → Discover/审批/管理)，每期有真机截图(深/浅 × 中/英)佐证。

## Capabilities

### New Capabilities

- `interaction-accessibility`: 全站交互可达性与可访问性契约(键盘可达、焦点可见、空/加载/禁用态、aria 关联、禁用原生 confirm/prompt)。

### Modified Capabilities

- `design-system-tokens`: 新增「跨表面同角色一致性」要求(卡片/标题/行/徽标/按钮/空态/表头/弹层/工具栏在全站使用同一 token 角色)；不改变既有 token 值，不要求把裸 zinc 全量 token 化。

## Impact

- **代码**：`web/src/pages/*`(对话/Agent/Discover/运维/日常/审批/管理各域)与 `web/src/components/*`(共享组件)与 `web/src/components/ui/*`(原语)。
- **无后端改动、无新依赖、无信息架构变化、无新增功能**；不删除任何既有测试用例。
- **不触碰**竞品差距中属新功能的「记忆/主动性/发现 remix」表面叙事(已明确排除)。
- **验证**：每期 `pnpm test`/`typecheck`/`build` + 真机双主题双语截图；交互项以自动化断言(键盘事件、aria、空/加载态分支)锁定。
