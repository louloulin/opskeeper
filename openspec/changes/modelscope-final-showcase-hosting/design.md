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

# Risks / Trade-offs

- **公网演示依赖阿里云环境**：创空间作为展示与备用入口，不保证完整交互能力；需要视频和截图兜底。
- **入口域名当前不一致**：`opskeeper.yueming.xin/home` 返回 404，`home.yueming.xin` 返回 502，必须在提交前修复或隐藏；域名迁移不做赛前激进切换。
- **魔搭运行环境限制不确定**：优先使用低依赖静态展示，避免把展示可用性绑定到容器内特权能力。
- **素材体积和格式限制**：需要对视频压缩和分辨率做实际上传验证。
- **认证与提交流程手工依赖**：需要用户在魔搭页面完成账号授权/提交，本变更保存可复现清单与 readback。
- **源码基线延迟**：如果 `main` 合并被阻塞，应在提交说明中明确演示代码 commit 与 release candidate 状态，避免使用过期 `main` 造成版本混淆。
