# Comet Design Handoff
- Change: modelscope-final-showcase-hosting
- Phase: design
- Mode: compact
- Context hash: 87d3f5562c898bc081057fb1360cdf292f524b9ad1f37388f2857a8c2e9edc4d

Generated-by: comet-handoff.sh

OpenSpec remains the canonical capability spec. This handoff is a deterministic, source-traceable context pack, not an agent-authored summary.

## openspec/changes/modelscope-final-showcase-hosting/proposal.md

- Source: openspec/changes/modelscope-final-showcase-hosting/proposal.md
- Lines: 1-41
- SHA256: 8b1e6b215ea1b5fea3a4d6e3bde4a42c3df6ca118283e237e2903959a8781b31

```md
# Why

GOAI 建议参赛团队将作品托管到魔搭社区，以形成路演期间可持续访问的线上展示阵地。OpsKeeper 目前已有公开 GitHub 仓库、官网、OpsKeeper 控制台、AgentTeams Dashboard 和 Element 演示入口，但这些资产尚未按魔搭的展示形态统一整理，也缺少提交、验收和公开切换的完整清单。

本变更以当前 `prepare-final-demo-main-flow` 工作区为唯一事实源。旧 `repair-preview-readback` 工作区仅作为 spec 迁移来源，不再独立推进 ModelScope 决策或发布判断。

同时，当前决赛工作区包含决赛增强与 TeamHarness `1.0.66` 相关提交，但公开 GitHub `main` 尚未确认同步该基线。若直接提交旧代码，会造成魔搭代码、官网说明和公网演示版本不一致。

# What Changes

- 建立魔搭托管资产包，覆盖三类可提交资产：
  - 代码仓库：以 GitHub 为权威源，同步或导入到魔搭代码托管。
  - 创空间 Demo：提供轻量公开展示应用，不复制特权运维环境。
  - 项目实践/展示内容：整理 OpsKeeper × AgentTeams 的架构、场景、证据和复用说明。
- 建立五个决赛入口路由契约：`https://opskeeper.yueming.xin/home` 是产品官网，`https://opskeeper.yueming.xin` 是 OpsKeeper 服务入口，`https://rooms.yueming.xin` 是房间对话入口，`https://teams.yueming.xin` 是插件展示与 Agent 行为证据链观测入口，`https://home.yueming.xin` 是路演案例全流程控制台；魔搭创空间以新窗口外链这些权威入口，不尝试内嵌或反向代理特权控制台。
- 不创建模型资产：OpsKeeper 没有可发布模型权重，避免让评审误解项目形态。
- 制作面向路演的创空间展示内容，包含项目定位、B/C 展示墙文案、架构图、演示截图、真实监控视频、E2E 证据摘要、GitHub/官网/三个在线演示入口、安装入口和安全边界。
- 建立公开安全素材检查，剔除或脱敏凭据、私网拓扑、原始事故数据和内部项目管理信息。
- 梳理源码基线：将决赛验证过的分支合并到 GitHub `main`，补充 release/topic/description 等仓库元数据，再同步魔搭。
- 在官网或演示入口中增加魔搭展示链接，保持 GitHub、官网、魔搭和公网演示的入口一致性。
- 在提交前执行五个入口 preflight：当前 `https://opskeeper.yueming.xin/home` 返回 404、`https://home.yueming.xin` 返回 502，二者必须在魔搭公开前修复；若临时无法修复，对应入口先从创空间隐藏，并用截图、视频和 GitHub/魔搭展示内容兜底。
- 完成提交前检查、私有保存、路演前公开切换、链接 readback 和回滚说明。

# Capabilities

## New Capabilities

- `modelscope-showcase-hosting`: Manage the public-safe ModelScope showcase package, submission metadata, evidence assets, visibility transitions, and link readback for OpsKeeper.

## Modified Capabilities

- None.

# Impact

- 新增魔搭托管资产和提交清单，预计位于 `deliverables/modelscope/`。
- 可能更新官网演示入口、README 生态入口和仓库发布元数据。
- 需要确认线上域名与反向代理的最终角色，尤其是 `opskeeper.yueming.xin` 根路径服务与 `/home` 官网路径共存，以及 `home.yueming.xin` 全流程控制台上游恢复。
- 需要整理本地复赛/决赛材料、截图和监控视频为公开安全素材。
- 涉及 GitHub 仓库元数据、release/tag、魔搭代码同步和 GOAI 提交表单的手工操作。
- 不修改 OpsKeeper Manager、AgentTeams Controller、AgentTeams Dashboard 或 TeamHarness 的运行时行为。
```

## openspec/changes/modelscope-final-showcase-hosting/design.md

- Source: openspec/changes/modelscope-final-showcase-hosting/design.md
- Lines: 1-89
- SHA256: 698bcd1a7436fedf874da528f46f5657cf68f0b93796153dc8b301852604d1de

[TRUNCATED]

```md
# Context

- 魔搭 GOAI 提交通道支持创空间 Demo、模型、代码和项目实践成果；作品可先保持私有，路演前后再公开。
- 本变更由当前 `prepare-final-demo-main-flow` 工作区统一推进；旧 `repair-preview-readback` 工作区只保留历史来源，不再承载 ModelScope 决策或发布状态。
- 当前线上路由需要先收敛：`https://opskeeper.yueming.xin` 返回 OpsKeeper 服务入口；`https://teams.yueming.xin` 与 `https://rooms.yueming.xin` 分别返回 Dashboard 和 Element；`https://opskeeper.yueming.xin/home` 当前返回 404；`https://home.yueming.xin` 当前返回 502。决赛与魔搭适配采用五个权威入口：`opskeeper.yueming.xin/home` 是官网，`opskeeper.yueming.xin` 是 OpsKeeper 服务，`rooms.yueming.xin` 是房间对话，`teams.yueming.xin` 是插件与行为证据链观测，`home.yueming.xin` 是路演案例全流程控制台。
- GitHub 仓库公开并使用 Apache-2.0，但缺少 topics 和 release；本地分支包含 TeamHarness `1.0.66` 决赛基线，尚未进入远端 `main`。
- 官网源码已有中文 Demo 页，部署到 `/home` 后应继续将真实交互环境作为路演入口。
- 现有复赛/决赛材料、E2E 报告和 PG pool 真实监控视频可作为展示证据，但需要公开安全筛选。

# Goals / Non-Goals

**Goals:**

- 在 2026-09-20 12:00 前完成 GOAI/魔搭提交与托管。
- 让评审可以从魔搭统一看到项目介绍、代码、演示入口、架构和证据。
- 保持 GitHub 为源码权威源，魔搭为展示与镜像渠道。
- 保持五个权威入口不变，避免赛前引入高风险域名迁移。
- 保证素材公开安全、表述可验证、版本信息一致。
- 提供私有保存、公开切换、验收和回滚操作记录。

**Non-Goals:**

- 不在魔搭复制或部署完整特权运维环境。
- 不暴露私网拓扑、运行凭据、真实敏感事故数据或后台管理能力。
- 不发布没有模型权重的“模型”资产。
- 不宣称未实现的 PolarDB HA 或活动会话完整复制能力。
- 不修改 AgentTeams 或 Dashboard 宿主代码。

# Decisions

## 1. 提交三类资产，跳过模型资产

代码仓库展示开源事实，创空间承载公开展示和访问入口，项目实践内容沉淀方法与证据。OpsKeeper 的核心交付是 Agent 运维工作台、插件和流程，不是模型权重，因此不创建模型仓库，避免项目形态误导。

## 2. GitHub 保持权威源，魔搭做镜像与展示

先将本地决赛基线合并到 GitHub `main`，打 release/tag 并校验版本 readback，再导入或同步魔搭代码库。后续变更仍以 GitHub 为准，魔搭展示页记录源码 commit、release tag 和发布日期。

## 3. 创空间使用轻量展示应用

创空间不运行完整 OpsKeeper 控制面，而是提供静态或轻量交互展示：定位、场景、架构、插件边界、修复预演、审批安全、事故档案、演示截图、视频和外链入口。真实交互继续使用官网、Dashboard 和 Element 三个公网环境。

魔搭与特权运行入口采用外链适配，不做 iframe 嵌入或域名反代。OpsKeeper 服务、全流程控制台、Dashboard 和 Rooms 可能包含登录态、审批、房间消息或运维入口，内嵌到第三方展示页会扩大 CSRF、点击劫持和凭据泄露风险。创空间提供“产品官网 / OpsKeeper 服务 / AgentTeams Rooms / AgentTeams Dashboard / 路演案例全流程控制台”五类新窗口入口，并在入口卡片中标明角色和路演建议路径。

OpsKeeper 服务和全流程控制台深链仅使用现有路由，例如 `/dashboard`、`/monitor`、`/alerts`、`/approvals` 和 `/alerts/incidents/:id`。未登录访问由现有登录重定向处理；创空间不保存、转发或自动填充登录凭据。

`https://opskeeper.yueming.xin/home` 在公开前必须返回官网首页 200，且不能影响 `https://opskeeper.yueming.xin` 根路径的 OpsKeeper 服务入口。推荐通过官网构建的 `basePath: /home` 或反向代理前缀重写实现，禁止把 404 页面当作官网入口。

`https://home.yueming.xin` 在公开前必须恢复为路演案例全流程控制台并返回可用页面。若无法修复，创空间先隐藏该入口，使用全流程录屏和关键截图兜底；禁止把当前 502 域名作为必需路演入口。

优先采用魔搭支持且依赖少的静态/Gradio 形态；应用只引用公开安全素材，不内置管理员 token、数据库 DSN 或私网地址。

## 4. 证据表述与当前能力一致

展示内容引用现有 E2E 报告和公网 readback，只声明：

- 独立 `preview-pg` 中执行候选修复。
- 重放固定负载并比较一致性、延迟、吞吐和写入影响。
- Candidate A 通过后仍需人工审批，Candidate B 验证失败被拒绝。
- 不宣称 PolarDB HA。
- 不宣称复制原实例活动会话。

## 5. 素材和链接集中治理

建立 `deliverables/modelscope/` 作为资产目录，维护：

- `manifest.json`：提交类型、URL、可见性、commit/tag、素材 hash。
- `README.md`：提交步骤和验收清单。
- 展示文案与项目实践内容。
- 截图、图示和压缩视频等公开安全素材。

创空间 URL 生成后回写官网 Demo 入口，但不改变五个在线权威入口。

## 6. 公开切换采用两阶段验收

第一阶段提交并保持私有，完成素材、链接、版本和移动端检查；第二阶段在路演前切公开，重新访问创空间、代码库、外链和媒体播放，记录时间戳与回滚步骤。

## 7. 控制台兜底顺序

若任一运行入口临时不可用，创空间依次降级：展示对应关键路径截图与操作说明；播放 PG pool 真实监控和闭环证据视频；引导评审查看项目实践、架构和代码；保留恢复后重新打开入口。不在魔搭运行时内重建 OpsKeeper 服务、全流程控制台、Dashboard 或 Rooms，也不把管理凭据写入展示应用。
```

Full source: openspec/changes/modelscope-final-showcase-hosting/design.md

## openspec/changes/modelscope-final-showcase-hosting/tasks.md

- Source: openspec/changes/modelscope-final-showcase-hosting/tasks.md
- Lines: 1-50
- SHA256: ce392321139e2b7011665b55f2c257af5521703eff42dbe055e02d6f3c28781f

```md
# ModelScope Final Showcase Hosting Tasks

## 1. 基线与提交策略

- [ ] 1.1 确认并记录 GitHub 权威分支、远端 `main`、本地决赛分支、TeamHarness 版本、release tag 和公网环境版本的一致性。
- [ ] 1.2 将已验证的决赛基线合并到 GitHub `main`，推送并建立可引用 release/tag。
- [ ] 1.3 补齐 GitHub description、topics、License 展示和 release 说明，确保仓库首屏可理解。
- [ ] 1.4 确认魔搭提交身份、组织/个人空间、作品名称、短描述、标签和可见性策略。
- [ ] 1.5 梳理并记录五个决赛入口路由契约：`opskeeper.yueming.xin/home` = 官网、`opskeeper.yueming.xin` = OpsKeeper 服务、`rooms.yueming.xin` = 房间对话、`teams.yueming.xin` = Dashboard、`home.yueming.xin` = 路演案例全流程控制台。
- [x] 1.6 修复 `opskeeper.yueming.xin/home` 404，采用 `/home` basePath 或反向代理前缀重写，并确认根路径服务不受影响。
- [x] 1.7 修复 `home.yueming.xin` 502，恢复路演案例全流程控制台；未修复前不公开该入口。
- [ ] 1.8 后续 ModelScope 决策、素材清单和提交状态只记录在当前工作区；旧工作区仅作历史参考，禁止并行修改。

## 2. 公开安全素材清理

- [ ] 2.1 盘点复赛/决赛 PPT、官网截图、E2E 报告、真实监控视频和架构图，筛选 6–10 张核心截图。
- [ ] 2.2 压缩并规范命名监控视频，校验时长、清晰度、体积和播放兼容性。
- [ ] 2.3 对所有公开素材执行安全检查：无 token、DSN、账号、私网拓扑、敏感事故数据和内部管理信息。
- [ ] 2.4 生成素材清单与 SHA256，记录每个素材的来源和授权状态。

## 3. 创空间 Demo

- [ ] 3.1 设计创空间信息架构：首屏定位、场景挑战、解决方案、架构、插件价值、安全审批、证据、入口和快速开始。
- [ ] 3.2 复用官网和决赛材料整理中文展示文案，保留英文项目名与关键术语。
- [ ] 3.3 实现轻量静态或 Gradio 展示应用，内嵌公开安全截图/图示/视频，并链接五个权威入口。
- [ ] 3.4 在创空间增加角色化入口卡片：产品官网、OpsKeeper 服务、AgentTeams Rooms、AgentTeams Dashboard、路演案例全流程控制台；全部使用新窗口外链，不 iframe 特权入口。
- [ ] 3.5 为运行入口准备截图/视频兜底路径，覆盖 `/dashboard`、`/monitor`、`/alerts`、`/approvals` 与事故闭环页。
- [ ] 3.6 本地验证移动端与桌面布局、图片加载、视频播放、外链打开和无敏感控制能力。
- [ ] 3.7 部署到魔搭创空间，先保持私有并记录访问 URL。

## 4. 代码托管与项目实践

- [ ] 4.1 将 GitHub 权威代码导入或同步到魔搭代码库，校验分支、commit、License 和 README 呈现。
- [ ] 4.2 检查 README 首屏是否解释项目定位、演示入口、快速开始、架构和安全边界。
- [ ] 4.3 整理“OpsKeeper × AgentTeams 实践”内容，覆盖插件扩展、角色分离、人工审批、修复预演、事故档案和复用方式。
- [ ] 4.4 按“项目实践/展示成果”类型提交内容，不创建模型资产。

## 5. 官网与入口联动

- [ ] 5.1 在官网适当位置增加魔搭展示入口和徽标，链接创空间 URL。
- [ ] 5.2 保持 `/zh/demo` 为真实演示入口，明确魔搭创空间是项目展示与备用入口。
- [ ] 5.3 更新官网 `/home` 部署并验证 HTTPS、移动端、五个入口链接可达性和根路径服务共存。

## 6. GOAI 提交与验收

- [ ] 6.1 通过 `https://modelscope.cn/active/GOAI` 完成作品提交并记录提交时间。
- [ ] 6.2 保存代码库、创空间和项目实践的最终 URL、可见性、commit/tag 和素材 hash。
- [ ] 6.3 私有状态下完成提交 readback：字段完整、素材可加载、代码可查看，官网、OpsKeeper 服务、Rooms、Dashboard、全流程控制台均无 404 或 502。
- [ ] 6.4 路演前切公开并复验创空间启动、代码访问、媒体播放、外链和移动端展示。
- [ ] 6.5 输出提交报告和回滚说明，包含公开切换时间、验收结果和异常处理步骤。
```

## openspec/changes/modelscope-final-showcase-hosting/specs/modelscope-showcase-hosting/spec.md

- Source: openspec/changes/modelscope-final-showcase-hosting/specs/modelscope-showcase-hosting/spec.md
- Lines: 1-70
- SHA256: fbd5a39a94d9efcb476089b68d413dc728b0ed77e2676661194a2ded3cd87d75

```md
## ADDED Requirements

### Requirement: Maintain a coordinated ModelScope submission package
The project SHALL maintain a ModelScope submission package that identifies the authoritative source revision, release tag, showcase assets, submission URLs, visibility state, and verification result.

#### Scenario: Submission identity is auditable
- **WHEN** a reviewer inspects the ModelScope package manifest
- **THEN** it identifies the code repository, Creative Space, and project-practice submissions
- **AND** records the GitHub commit, release tag, TeamHarness version, public demo URLs, and visibility state
- **AND** does not identify a model repository because OpsKeeper publishes no model weights

#### Scenario: Source and showcase diverge
- **WHEN** the public demo runs a newer validated baseline than the default GitHub branch
- **THEN** the package records the exact demo commit and pending main-merge status
- **AND** the main-merge task remains open until the authoritative repository reaches that baseline

### Requirement: Keep showcase assets public-safe
The ModelScope showcase SHALL include only assets that are cleared for public disclosure and SHALL exclude runtime credentials, private topology, administrative tokens, unmasked sensitive incident data, and internal project-management records.

#### Scenario: Assets are selected for publication
- **WHEN** screenshots, diagrams, documents, or videos are added to the showcase
- **THEN** the package records their source, purpose, and digest
- **AND** a review confirms they contain no credential, secret, private endpoint, or sensitive customer data

#### Scenario: Claims must match implementation
- **WHEN** the showcase describes repair preview or reliability capabilities
- **THEN** it states that candidates run in disposable `preview-pg` branches with controlled fixed-load replay
- **AND** it does not claim PolarDB HA or copying of the original instance's active sessions

### Requirement: Provide a lightweight reachable showcase
The project SHALL provide a Creative Space showcase that loads without privileged OpsKeeper runtime dependencies and routes visitors to the authoritative website, repository, and public interactive demos.

#### Scenario: Visitor opens the Creative Space
- **WHEN** the showcase loads in a browser
- **THEN** it presents the project positioning, scenario, architecture, safety boundary, evidence summary, and media assets
- **AND** provides links to GitHub, the official website, OpsKeeper, AgentTeams Dashboard, and AgentTeams Element
- **AND** does not request or accept an OpsKeeper administrative credential

#### Scenario: Visitor opens a privileged roadshow entry
- **WHEN** a visitor selects OpsKeeper, AgentTeams Rooms, AgentTeams Dashboard, or the full-flow console from the Creative Space
- **THEN** the selected entry opens in a new browser tab at its authoritative URL
- **AND** existing authentication and route protection remain in force
- **AND** the Creative Space neither embeds, proxies, nor stores credentials

#### Scenario: Public interactive demo is unavailable
- **WHEN** an external interactive demo cannot be reached
- **THEN** the visitor can still understand the end-to-end workflow from the showcase text, diagrams, screenshots, and recorded evidence

### Requirement: Verify visibility transitions and links
The submission process SHALL verify the package while private, then re-verify required public links and media before roadshow exposure.

#### Scenario: Private submission is checked
- **WHEN** the GOAI submission is saved before public exposure
- **THEN** the recorded fields, code revision, media, and submission links pass a private-state readback
- **AND** any failure is tracked before the package becomes public

#### Scenario: Package is switched public
- **WHEN** the package is made public before the roadshow
- **THEN** the Creative Space starts, code is viewable, media plays, and all external demo links return successfully
- **AND** the verification timestamp and rollback instruction are recorded

#### Scenario: Legacy home domain is unavailable
- **WHEN** `home.yueming.xin` returns an error or cannot host the full-flow roadshow console
- **THEN** the package either restores the console or omits the entry from the public showcase until it is available
- **AND** the ModelScope showcase never exposes the failing URL as a required roadshow entry

#### Scenario: Official website path is unavailable
- **WHEN** `https://opskeeper.yueming.xin/home` does not return the official website
- **THEN** the package either deploys the website under `/home` without breaking the OpsKeeper root service or omits the website entry until it is available
- **AND** a 404 response is never presented as the official website entry
```
