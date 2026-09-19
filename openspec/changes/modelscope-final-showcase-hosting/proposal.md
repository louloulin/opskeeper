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
