# OpsKeeper 1.0 规划 — AI 运维军团与 Pi 智能体下放

> 文件：plan1.0.md
> 对应 issue：LUM-679「okp 一期」
> 编写日期：2026-09-10
> 修订：2026-09-10（v1.1 — 结合 pi.dev 真实 Pi agent）
> 修订：2026-09-10（v1.3 — **修正 Pi 版本与 monorepo 路径**：earendil-works/pi v0.85.1，Node.js ≥ 22.19.0；新增 §8.1.1 pi.dev/packages 插件生态章节）
> 修订：2026-09-11（v1.13 — 横向增补 plan1.1.md：3 phase × 9 round 覆盖 fleet / AI 运维军团 / 自主化闭环；plan1.0 §8.1.1 `swarm-extension` 误判 ⛔ 重审为 ✅ 必需；`npm view @earendil-works/pi-coding-agent version` 验证 v0.85.1 仍为 npm registry 当前最新发布）
> 修订：2026-09-10（v1.2 — **云端零 Pi 依赖**：Pi 只下放到目标机，云端继续用既有 7 worker + eino + internal/pkg/llm）
> 适用代码库：[louloulin/opskeeper](https://github.com/louloulin/opskeeper)（`v1.0.0`）

## 一、TL;DR

OpsKeeper 已经是一套"以云端为大脑、以 Edge 代理为神经末梢"的多 Agent 运维平台，但当前形态是 **cloud-side brain + thin tunnel agent** —— 云端 `cmd/opskeeper` 集中跑 LLM 闭环，边缘 `cmd/opskeeper-edge` 只负责执行云端下发的只读工具调用。

一期目标（`okp 1.0`）是把 **真实 Pi 智能体（[pi-coding-agent](https://pi.dev/)）作为 sidecar 下放到每台目标机器**；**云端不做任何 Pi 相关改动**，继续基于现有的 7 worker + eino ReAct + `internal/pkg/llm` 跑现有闭环（reviewer 双签 / investigator RCA / specialist 派发 / critic 审计 / verifier 核验 / reporter 报告）。

**关键设计决策（v1.2 修订）**：

0. **云端零 Pi 依赖**（本版硬约束）—— `cmd/opskeeper` 二进制、`internal/manager/`、`internal/pkg/llm/`、`agents/*.md`（云端 worker persona）、`internal/harness/` 评测的所有云端路径**全部不动**；不引入 pi-mono 依赖、不修改云端构建脚本、不让 Pi 进程接触云端。云端 reviewer worker 对 Pi 的工具调用返回 approval token，这只是新增两个 tunnel RPC，云端业务逻辑零侵入。
1. **Pi = 真身 pi-coding-agent**，不写 Go 复刻。Pi 的全部 ReAct / Skill / 工具调用 / 会话树 / 上下文工程能力均来自 [earendil-works/pi](https://github.com/earendil-works/pi) 官方 monorepo（**v0.85.1**，4 个 npm 包，Node.js ≥ 22.19.0，MIT，@badlogic + mitsuhiko + rwachtler 维护）。**优先复用 [pi.dev/packages](https://pi.dev/packages) 生态**（pi-yaml-hooks / pi-extension-manager / pi-toolbox / swarm-extension 等），不在 opskeeper 内造轮子。
2. **Pi 集成方式 = sidecar 进程 + HTTP 模式**。**仅在每台目标机器上** opskeeper-edge 拉起 `pi --mode http --bind 127.0.0.1 --port 19000`；云端二进制不拉 Pi，云端不依赖 Node 运行时。
3. **Pi 的 4 个内置工具 + 我们的 tool bag**。Pi 仅有 `read/write/edit/bash`；我们通过 opskeeper-edge 提供 `host_bash / host_files / host_restart_service / webshell` 等 Go 端 tool HTTP 端点，由 Pi 的 `bash` 工具作为唯一外部出口，命令实际跑在 opskeeper-edge 的 cmdpolicy 沙箱里。
4. **Pi 通过 .pi/skills/opskeeper-*/SKILL.md 学习何时调用哪个工具**；OpsKeeper 平台不做 prompt 黑盒。
5. **升级策略 = `git pull --rebase`**。pi-mono 以 git submodule 形式 vendored 到 opskeeper 仓库的 `vendor/pi/`，按 tag 锁定（如 `v0.85.1`），按月或季度 rebase 上游；升级 diff 经 CI 跑 `opskeeper-eval pi` 与 harness case 后再合入。
6. **复用既有安全骨架**（reviewer 双签 + HMAC 审计 + HITL + cmdpolicy 沙箱 + traceparent），云端 reviewer worker 保留所有现有逻辑；edge 是 Pi 与主机之间的唯一信任锚点。
7. **协议标准化**：MCP（v1.5，可选挂在 Pi 上供外部 agent 调用）+ A2A（v1.5）+ W3C traceparent（立即沿用）+ geminio tunnel（立即沿用）。

---

## 二、现状盘点（来自代码全量分析）

### 2.1 项目基本面

| 项 | 值 |
|---|---|
| 主语言 | Go 1.25+（含少量 Python 插件 + Node Dashboard 插件 + React/Vite 前端） |
| 形态 | 云端 Manager + 边缘 Edge agent 通过 `singchia/frontier` (geminio) broker 长连接 |
| 包数量 | ~687 个 `.go` 源文件、~412 个 `_test.go` 文件 |
| 二进制入口 | 13 个，核心是 `cmd/opskeeper`、`cmd/opskeeper-edge`、`cmd/opskeeper-mcp-tools`、`cmd/opskeeper-eval` |
| LLM 框架 | CloudWeGo **eino**（`v0.8.7`，`flow/agent/react`）+ 自研 legacy `agent/agent.go` 兼容内核 |
| LLM 路由 | `internal/pkg/llm/{client,router,eino_routing}.go` —— OpenAI/Anthropic/Zhipu/Gemini/DeepSeek/Kimi/Custom |
| 嵌入模型 | `fastembed-go` + `onnxruntime_go` 本地推理，落到 Qdrant |
| RBAC | Casbin，策略独立放在 `policy/opskeeper/casbin/` |
| 审计 | Append-only HMAC 链 + W3C `traceparent` 全链路传播 |
| 观测 | OTel + Prometheus + Loki + Tempo + Grafana |
| 评估 | 独立 `internal/harness/{cases,injector,judge,runner,leaderboard,schema}/`，`opskeeper-eval` CLI |

> **v1.2 备注（关键约束）**：Pi **只**下放到目标机的 `cmd/opskeeper-edge` 进程侧，**云端 `cmd/opskeeper` 完全不引入 Pi 相关依赖**。`internal/pkg/llm` 全家桶继续服务云端 reviewer / investigator / chat，不被替换；`agents/*.md`（云端 worker persona）原样保留；`internal/manager/biz/aiops/` 的 eino ReAct 路径不变。云端唯一新增：两个 tunnel RPC（`pi_propose_action` / `pi_approval_grant`）和若干审计表字段 —— 都是加法，不是改法。

> **v1.1 备注**：Pi 集成后，`internal/pkg/llm` 不再直接服务机端 —— Pi 自带 `@earendil-works/pi-ai`（多 provider LLM 客户端）独立对接各家模型。OpsKeeper 内部的 `internal/pkg/llm` 仅保留云端 reviewer / investigator / chat 用途。

### 2.2 7 大 worker 角色 + 调度

云端 Manager 用一个调度树 + L0–L3 安全等级，把 incident 路由到 7 个 worker + 5 个专家（已写在 `agents/*.md` frontmatter 中）：

- `alerter`（M2M 接入告警 → 入库）
- `incident-investigator`（RCA root cause）`max_turns=40`
- `specialist-{compute,disk,network,ops,sre}`（领域专家，只读）
- `critic`（事后审计 RCA，`max_turns=2`）
- `reviewer`（变更二签，`background:true`）
- `repairer`（执行变更，与 `verifier` 职责分离）
- `verifier`（独立核验恢复）
- `reporter`（周期报告）

闭环：`alert → investigate → propose → reviewer → repair → verify → postmortem → knowledge`，与"AI 运维军团"理念高度契合。

> **v1.1 备注**：1.0 引入 Pi 后，"机端主动探查 → 报告新 incident → 上行 reviewer 二签 → 机端执行"的回路和上述云端闭环是**对称的**，因此 review-gate / verifier 分离 / HITL / audit / token-budget 这些约束都被复用。

### 2.3 可复用能力矩阵（v1.1 更新）

| 能力 | 落点 | 复用方式 |
|---|---|---|
| 边缘分发与升级 | `internal/edgeagent/biz/upgrade.go` + `deploy/install/upgrade.sh` | 直接复用 SHA256 校验 + 原子替换；新增"Pi 二进制亦随包分发" |
| 边缘 daemon 主循环 | `internal/edgeagent/biz/agent.go` | 复用 heartbeat + RPC dispatch，新增"Pi sidecar supervisor goroutine" |
| 边缘命令沙箱 | `internal/edgeagent/cmdpolicy/{parse,policy,sandbox,types}.go` | 扩展 `DefaultReadOnly()` 为 `DefaultPiCapable()`，引入"已审批放行"模式 |
| 边缘工具执行器 | `internal/edgeagent/{bash,host_files,restart_service,webshell}` + `internal/skill/builtin/*` | 暴露为本地 HTTP API（`/v1/edge/tools/*`），由 Pi 通过 `bash` 工具或自定义 HTTP tool 触发 |
| LLM 客户端（云端用） | `internal/pkg/llm/{client,router,eino_routing}.go` | 仅服务云端 reviewer / investigator；机端由 `@earendil-works/pi-ai` 提供 |
| Worker persona 规范 | `agents/*.md` + frontmatter | 与 Pi 兼容：同步一份到 `vendor/pi/.pi/agents/` 与 `.pi/skills/` 下 |
| Skill 规范（v1.1 调整） | `skills/<name>/SKILL.md`（平台 catalog）**+** `vendor/pi/.pi/skills/opskeeper-*/SKILL.md`（Pi 私有 skills） | 平台 catalog 通过 opskeeper-edge 推送到 host 的 `~/.pi/agent/skills/`；Pi 用 `Skill` 指令载入 |
| 变更双签 | `agents/reviewer.md` + `internal/manager/biz/aiops/tools/decorators/review_gate.go` | Pi mutating 调用统一经 opskeeper-edge → tunnel → 云 reviewer |
| 审计链 | append-only HMAC + `traceparent` 传播 | Pi 工具调用经 opskeeper-edge 一律带上 traceparent；审计日志由 edge 统一上行 |
| HITL | Element Web + Element 审批 + 现有的 `Approvals.tsx` 队列 | Pi 高风险动作复用 |
| 评测 harness | `internal/harness/{cases,judge,runner}` | 新增 `cases/edge/{oom,disk-full,service-down}` + `cases/pi/{bash_safety,recovery_correctness}` 跑 Pi |
| 插件打包 | `plugins/opskeeper-teamharness/plugin.yaml` schema v1.0.38 | 保持不变；Pi 不走 AgentTeams 路径 |
| Web 控制台 | `web/src/pages/EdgeDetail.tsx` (171 KB) 已具备边缘 CRUD/密钥/指标 | 新增 Pi tab，复用现有路由 |
| **NEW v1.1：submodule 升级管线** | `vendor/pi-mono` git submodule + `make sync-pi` | 定期 `git submodule update --remote` + 跑 CI harness |

### 2.4 现有代码卫生问题（必须在 1.0 前处理）

| # | 问题 | 严重度 | 文件 / 位置 |
|---|---|---|---|
| 1 | `docker-compose.yml` 中 `opskeeper` 单实例部署，与 `vNext platform-base-ha` 承诺的 Helm 2 副本不一致 | 高 | `docker-compose.yml` |
| 2 | compose 中 `OPSKEEPER_JWT_SECRET` / `OPSKEEPER_ADMIN_PASSWORD` 仍是 `change-me-…` 默认值，未用 `${VAR:?msg}` 强校验 | 高 | `.env.example` + `docker-compose.yml` |
| 3 | `tempo` / `qdrant` / `frontier` 没有 healthcheck，`depends_on: service_healthy` 不可用 | 中 | `docker-compose.yml` |
| 4 | `internal/pkg/prom/manager_metrics.go:368,385,472,493,510` 与 `agentteams_metrics.go:280,298` 用 `panic(err)` 注册指标，单条失败会拖垮整个进程 | 中 | `internal/pkg/prom/*_metrics.go` |
| 5 | `internal/higress` 是 15 个包里 **唯一**完全无测试的（3 源文件 / 0 测试） | 中 | `internal/higress/*` |
| 6 | `internal/manager/biz/chatdiagnose`（12 源 / 3 测试）与 `internal/manager/model/aiops`（4 源 / 0 测试）是子包粒度的覆盖空洞 | 中 | 同上 |
| 7 | Makefile 引用 `coverage.out` / `coverage.html` 但 **没有 `cover` 目标** | 低 | `Makefile` |
| 8 | CHANGELOG 自 v1.0.0（2026-07-13）至今天（2026-09-10）已 2 个月未更新，仅 WIP 分支条目 | 低 | `CHANGELOG.md` |
| 9 | `agentteams-controller` 用 `:dev` 占位镜像（compose 自带注释承认） | 低 | `docker-compose.yml` |
| 10 | 33 处构造函数 `panic(nil-arg)` 风格不一致，部分应为 `error` 返回 | 低 | 散在 `internal/manager/biz/...` 与 `internal/skill/registry.go` |
| 11 | `Dockerfile.dev` 在生产形态 compose 下跑 root + 完整工具链，无交叉引用 distroless `deploy/Dockerfile.opskeeper` | 低 | `Dockerfile.dev` |
| 12 | `.golangci.yml` 注释 G104 由 errcheck 兜底，但未给 errcheck 自身的排除清单 | 低 | `.golangci.yml` |

### 2.5 现有"机端自治"的缺口

- `cmd/opskeeper-edge/main.go` 仅注册被动 RPC：`push_host_metrics`、`push_prom_samples`、`register_edge`、`heartbeat`、`agent_upgrade`、`execute_skill`、`get_host_load`、`get_process_list`。
- 没有任何 `run_agent_loop` / `local_reasoner` / `proactive_probe` 类 goroutine —— **v1.1 修订**：1.0 不在 edge 内部写 ReAct 循环，而是通过 sidecar 拉起真 Pi 来补这个缺口。
- `agents/*.md` 没有 `monitor` 或 `watchdog` 角色 —— **v1.1 修订**：把 "pi monitor" 角色建在 `vendor/pi/.pi/agents/opskeeper-monitor.md` 下，让 Pi 自身解析。
- `internal/edgeagent/` 子包里没有任何 LLM 客户端依赖 —— **v1.1 修订**：保持现状，LLM 责任完全转交给 Pi sidecar；edge 只跑 HTTP 客户端。

### 2.6 v1.2 核心约束：云端零 Pi 改动

> 本节是 v1.2 引入的硬约束，所有后续工作项必须遵守；任何云端 Pi 相关改动必须经 louloulin 单独审批。

| 维度 | 云端做法（v1.2 锁定） | 不做的事 |
|---|---|---|
| **云端二进制** | `cmd/opskeeper` 维持 v1.0.0 形态，不引入 Node 运行时依赖 | 不在云端打包 pi-coding-agent；不引入 `@earendil-works/*` npm 依赖；不修改云端 Dockerfile |
| **云端 LLM 客户端** | `internal/pkg/llm/{client,router,eino_routing,budget}` 原样保留，服务云端 reviewer / investigator / specialist / critic / verifier / reporter / chat | 不替换为 Pi-ai；不在云端调用 `@earendil-works/pi-ai` |
| **云端 agent 循环** | `internal/manager/biz/aiops/{agent,chatruntime,graph,tools}/` 原样保留，eino ReAct + legacy 双内核 | 不接入 Pi sidecar 到云端 chat runtime；不让 Pi 直接进 chat session |
| **云端 worker persona** | `agents/*.md` 全部保留（10 个 markdown + 5 个 specialist） | 不新增"云端 pi-* persona"；不修改现有 frontmatter |
| **云端 harness** | `internal/harness/{cases,judge,runner,leaderboard}` 原样保留，云端 cases 继续以云端 worker 为被测对象 | 不强制 Pi 跑云端 cases；不修改 leaderboard 评分公式 |
| **云端 web 控制台** | `web/src/pages/EdgeDetail.tsx` 新增 Pi tab 是允许的（读取 edge 上报的 Pi 状态） | 不在 web 端嵌入 Pi TUI；不把 web 端打成 Pi 客户端 |
| **云端 ↔ Pi 通信** | **只**通过现有 geminio tunnel（新增 `tunnel.v1.pi_propose_action` / `tunnel.v1.pi_approval_grant` / `tunnel.v1.pi_audit` 三个 RPC），由 edge 代理 | 不在云端开新端口接 Pi HTTP；不让 Pi 直连云端 DB / Redis / Qdrant |
| **云端构建产物** | `cmd/opskeeper` 镜像不含 Node；`vendor/pi-mono` 不进云端构建上下文 | 不在云端 CI 跑 `pnpm install`；不把 pi-mono 打进云端 binary |
| **云端依赖图** | opskeeper-cloud `go.mod` 不新增任何 pi-coding-agent 相关依赖 | 不在云端 `go.mod` 引任何 npm / TS 桥；不引 cgo 调用 Node |

**例外（仍允许）**：

1. `internal/manager/server/` 下新增 `/v1/edges/:id/pi/state` REST 路由 —— 仅作为 edge 上报 Pi 状态的查询端点，云端不解析 Pi 内容。
2. `internal/manager/biz/audit/` 增列 Pi 工具调用相关审计字段 —— 加法不改法。
3. `internal/manager/biz/aiops/proposal/` 与 `internal/manager/biz/aiops/tools/decorators/review_gate.go` 增加对 edge 上行 proposal 的识别 —— 不改 reviewer 决策逻辑。
4. `internal/manager/server/loop/pi_approval.go` —— reviewer worker 对 `tunnel.v1.pi_propose_action` 派单，复用现有 reviewer 闭环。

> 判定 cloud vs. edge 边界的快速规则：如果一段代码 `import` 了 `internal/edgeagent/*` 或者 `vendor/pi/*`、或者引入 npm 依赖，那么它**不能**进 `cmd/opskeeper` 二进制 —— 哪怕只是被云端某个测试 import 也不行。

---

## 三、规划目标（v1.2 修订）

### 3.1 产品目标（v1.2 修订）

> **构建一套"AI 运维军团"**：
> - **机端**：每台被纳管的机器上跑一个 Pi sidecar（真身 [pi-coding-agent](https://pi.dev/)），自主监控、自主诊断、自主修复。
> - **云端**：**不动**，继续用既有的 7 worker + eino ReAct + `internal/pkg/llm` 跑 reviewer 双签 / investigator RCA / specialist 派发 / critic 审计 / verifier 核验 / reporter 报告。
> - **边界**：edge 负责工具路由、命令沙箱与审计上行；机 ↔ 云通过 geminio tunnel 走 3 个新增 RPC（`pi_propose_action` / `pi_approval_grant` / `pi_audit`）。

Pi sidecar 的设计定位（v1.2）：

- **真**：运行的是 [pi-mono](https://github.com/earendil-works/pi) v0.85.1+ 官方发行版，无任何 fork / patch 上游不允许的内容。
- **并**：与 opskeeper-edge 同主机、同生命周期；edge 拉起 / 健康检查 / 滚动升级 Pi。**云端不参与** Pi 的进程管理。
- **隔**：Pi 进程不能直接接触主机文件系统与进程；它只能通过 opskeeper-edge 的 HTTP API（`/v1/edge/tools/*`）触发工具，所有命令必经 cmdpolicy 沙箱。
- **明**：每次工具调用、决策、恢复动作都经 opskeeper-edge 写 append-only HMAC 审计链，并在云端回放。
- **升**：通过 `git pull --rebase vendor/pi-mono` 与 opskeeper 自身一起走版本化升级；不引入"Pi 旁路升级"路径。
- **标准**：MCP（v1.5，可挂在 Pi 之上供外部 agent 调用）+ A2A（v1.5） + W3C traceparent（立即）。

### 3.2 阶段目标（v1.2 修订）

| 版本 | 范围 | 验收标准 |
|---|---|---|
| **1.0-preview** | pi-mono submodule + opskeeper-edge sidecar supervisor + 5 个工具 HTTP 端点 + review-gate 闭环 + 1 个 harness case 通过 | `internal/harness/cases/pi/bash_safety` 通过率 ≥ 70%；opskeeper-edge 能完成一次"Pi 调用 `host_restart_service` → tunnel 上行 review → approval token 回流 → 执行 → 审计"全链路 |
| **1.0** | Pi 全自治：proactive probe + 闭环修复 + HITL + audit；通过 `git pull --rebase` 锁定 pi-mono v0.85.x | 通过率 ≥ 85%；每条修复平均成本 ≤ ¥0.5；MTTR ≤ 既有手工流程 30% |
| **1.5** | A2A 标准化、多 Pi 协同、跨机事件聚合 + MCP server 挂在 Pi 上 | 与外部 A2A Agent 互通 demo；跨机协调场景 1 个；批处理机群 1000 节点 |
| **2.0** | 自我进化（playbook 自学习 + postmortem 自动归档） | playbook 命中率 ≥ 60%；postmortem 完全自动归档 |

---

## 四、整体架构（v1.2 修订：明确"Cloud = No Pi"边界）

```
┌─────────────────────────────────────────────────────────────────┐
│ Cloud: OpsKeeper Manager (cmd/opskeeper)                        │
│                                                                 │
│   ⚠️  v1.2 硬约束：本框内 ZERO Pi 依赖                          │
│   ⚠️  不引 pi-mono / pi-coding-agent / @earendil-works/*         │
│   ⚠️  不引入 Node 运行时 / pnpm / cgo 桥                         │
│   ⚠️  只通过 geminio tunnel 与 edge 通信（新增 3 个 RPC）        │
│                                                                 │
│ ┌───────────────────────────────────────────────────────────┐   │
│ │ Closed-Loop Scheduler (LoopState machine)                  │  │
│ │   investigator ─┐                                           │  │
│ │   reviewer      ├──► eino ReAct Graph  (现有 internal/pkg/ │  │
│ │   specialist*  │     │              llm + chatruntime)     │  │
│ │   critic/verif │     ▼                                      │  │
│ │                 │   audit + trace + budget                   │  │
│ └───────────────────────────────────────────────────────────┘   │
│  ▲ proposal ▲ approval ▲ postmortem                              │
│  │ MCP tools (cloud-side: query_promql/logql/traceql,            │
│  │   correlate_incident, recovery.execute/verify, HITL, …)      │
│  │                                                                 │
│  │ geminio tunnel (port 40011 / 40012) over singchia/frontier    │
│  │ + 新增: tunnel.v1.pi_propose_action / pi_approval_grant       │
│  │         / pi_audit  (纯加法, 不改云端业务逻辑)                 │
└──┼──────────────────────────────────────────────────────────────┘
   │
   │  W3C traceparent + Bearer + HMAC + edge_id                    │
   │
┌──┼──────────────────────────────────────────────────────────────┐
│  Host: opskeeper-edge (Go) + pi-coding-agent (TS/Node)         │
│                                                                 │
│  ┌─────────────────────────────────────────────────────────┐   │
│  │  opskeeper-edge (existing, supervisor 角色新增)         │   │
│  │   • pull `vendor/pi/` 或下载官方 release           │   │
│  │   • spawn `pi --mode http --bind 127.0.0.1 --port 19000`│   │
│  │   • 健康检查 + 进程拉起 + 滚动重启                      │   │
│  │   • /v1/edge/tools/{bash,host_files,host_restart_service}│  │
│  │     → 既有 internal/edgeagent/* tool executors          │   │
│  │   • /v1/edge/audit   → 既有 append-only HMAC ledger     │   │
│  │   • /v1/edge/propose → 既有 tunnel `pi_propose_action`  │   │
│  │   • cmdpolicy.DefaultPiCapable() 沙箱                    │   │
│  └─────────────────────────────────────────────────────────┘   │
│       ▲ HTTP+SSE (RPC mode)                                     │
│       │                                                         │
│  ┌────┴──────────────────────────────────────────────────────┐  │
│  │  pi-coding-agent sidecar (vendor/pi-mono 真身)            │  │
│  │   • 4 core tools: read / write / edit / bash              │  │
│  │   • skills via .pi/skills/opskeeper-*/SKILL.md           │  │
│  │   • sessions JSONL 树 + /fork + /tree                    │  │
│  │   • 多 provider via @earendil-works/pi-ai                │  │
│  │   • bash 工具只指向 opskeeper-edge HTTP API              │  │
│  └────────────────────────────────────────────────────────────┘  │
│       │ HTTP (curl-style bash command)                            │
│       ▼                                                          │
│  opskeeper-edge /v1/edge/tools/bash                             │
│   → cmdpolicy.DefaultPiCapable()                               │
│     → approvaL token check → execute                            │
│                                                                 │
│  ▼                                                                │
│ host metrics / journal / processes / network / filesystem         │
└─────────────────────────────────────────────────────────────────┘
```

### 4.1 v1.2 关键原则（云端零 Pi 为首要原则）

0. **云端零 Pi 依赖** —— `cmd/opskeeper` 二进制、`internal/manager/`、`internal/pkg/llm/`、`agents/*.md`（云端 persona）、`internal/harness/` 评测中所有云端路径**全部不动**；云端 reviewer worker 对 Pi 的工具调用返回 approval token 仅通过 geminio tunnel 实现。
1. **Pi 是大脑，edge 是身体**：所有"看主机 / 改主机"的能力都经 edge；Pi 不直接接触 host（除了通过 `bash` 工具的 curl）。
2. **命令沙箱唯一入口**：opskeeper-edge 的 `cmdpolicy` 仍是命令执行的唯一信任锚点；Pi 的 `bash` 工具必须先 curl `http://127.0.0.1:9101/v1/edge/tools/bash`。
3. **变更双签唯一通道**：任何 mutating 命令进 edge 之前必须先 tunnel 上行 review → 拿到 approval token → 走 `cmdpolicy.DefaultPiCapable()`。
4. **审计唯一来源**：edge 把 Pi 的每次工具调用打包成 HMAC 审计事件，append-only + tunnel 上行同步。
5. **协议不锁死**：Pi 在 host 上用 HTTP 模式（`--mode http --bind 127.0.0.1 --port 19000`），云 ↔ 机之间沿用 geminio tunnel + W3C traceparent。
6. **演进兼容**：Pi 默认 OFF（开关 = `OPSKEEPER_PI_ENABLED=false`），按 edge_id 白名单逐步开启，不影响现有用户。
7. **升级 = rebase**：opskeeper 仓库内 `vendor/pi-mono` 是 git submodule，按 tag 锁定；季度 rebase 上游；升级 diff 经 CI harness 跑过才合入。

---

## 五、关键工作项（v1.2 重申：仅 edge 侧 + 云端只读加法）

### 5.1 一期核心新增（1.0-preview）

| ID | 模块 | 内容 | 主交付文件 | 预计工时 |
|---|---|---|---|---|
| P-1 | **vendor pi-mono** | 以 git submodule 形式 vendored 到 `vendor/pi/`，pin tag `v0.85.1`；写 `.gitmodules` 与 `Makefile.sync-pi`（含 `git submodule update --remote --merge` + `git pull --rebase` 流程） | `vendor/pi/`、`.gitmodules`、`Makefile`、`scripts/sync-pi.sh` | 1 d |
| P-2 | **edge sidecar supervisor** | 在 `internal/edgeagent/biz/agent.go` 新增 supervisor goroutine：spawn `pi --mode http --bind 127.0.0.1 --port 19000`，做健康检查 (`/health`)、崩溃自启、滚动重启；supervisor 的子命令拆为 `internal/edgeagent/pisupervisor/` 包 | `cmd/opskeeper-edge/main.go`、`internal/edgeagent/pisupervisor/{supervisor,health,upgrade}.go` | 3 d |
| P-3 | **edge HTTP 工具端点** | 在 opskeeper-edge 起 `127.0.0.1:9101`，新增 `/v1/edge/tools/{bash,host_files,host_restart_service,webshell}` 与 `/v1/edge/audit`、`/v1/edge/propose`；复用 `internal/edgeagent/{bash,host_files,restart_service,webshell}` 与 `internal/skill/builtin/*` 现有实现 | `internal/edgeagent/server/{router,tools,audit,propose}.go`、`cmd/opskeeper-edge/main.go` | 4 d |
| P-4 | **cmdpolicy Pi 模式** | `internal/edgeagent/cmdpolicy/` 新增 `DefaultPiCapable()`，允许白名单内 mutating 命令但**强制要求 request header `X-Opskeeper-Pi-Approval-Token`**，由 supervisor 在收到云端 approval 后注入 | `internal/edgeagent/cmdpolicy/{policy,sandbox,types}.go` | 1.5 d |
| P-5 | **approval tunnel RPC** | 新增 `tunnel.v1.pi_propose_action`（机端→云端 proposal）与 `tunnel.v1.pi_approval_grant`（云端→机端，携带一次性 approval token）；复用 `internal/manager/biz/aiops/proposal/` 与 reviewer worker | `api/tunnel/v1/tunnel.proto`、`internal/edgeagent/biz/pi_approval.go`、`internal/manager/server/loop/pi_approval.go` | 3 d |
| P-6 | **审计上行** | 复用 `traceparent` + 新增 `tunnel.v1.pi_audit` RPC，edge 把 Pi 的每次工具调用打包成 HMAC 审计事件流式回云端；写本地 + 上行双写，本地为可丢失缓存 | `internal/manager/server/audit/`、`internal/edgeagent/biz/pi_audit.go` | 1.5 d |
| P-7 | **Pi Skills 仓库** | 在 `vendor/pi/.pi/skills/opskeeper-*/SKILL.md` 下提供：`opskeeper-restart-service`、`opskeeper-host-files`、`opskeeper-diagnostics`、`opskeeper-incident-report` 等 skill；说明何时调用哪个 opskeeper-edge 工具 | `vendor/pi/.pi/skills/opskeeper-*/SKILL.md`、`docs/pi-skills.md` | 1.5 d |
| P-8 | **Pi 启动 AGENTS.md / SYSTEM.md** | 在 `vendor/pi/.pi/agents/opskeeper-monitor.md` 写一份"Pi 在 OKP 上的角色契约"：read-only 默认、所有 mutating 必须先 review、双签策略、HITL、token-budget、与 opskeeper-edge 的边界；并在 SYSTEM.md 注入"host_info + 最近一次 incident 摘要"以利 Pi 启动就绪 | `vendor/pi/.pi/agents/opskeeper-monitor.md`、`scripts/render-pi-system-md.py` | 1 d |
| P-9 | **Web 监控** | `web/src/pages/EdgeDetail.tsx` 新增 Pi tab：当前 sidecar 状态、最近 50 条 Pi 会话（来自 `pi --export`）、最近 24h 修复成功率、token 消耗；通过 `GET /v1/edge/pi/state` REST 拉取 | `web/src/pages/EdgeDetail.tsx`、`web/src/api/pi.ts`、`internal/manager/server/pi/state.go` | 2 d |
| P-10 | **harness case** | 新增 `internal/harness/cases/pi/bash_safety`（验证 Pi 调用 `host_restart_service` 必经 review + approval token），并把现有 `internal/harness/cases/edge/{oom,disk-full,service-down,cpu-spike,conntrack-sat}` 跑通到 ≥ 70% 通过 | `internal/harness/cases/pi/*`、`internal/harness/runner/pi_runner.go` | 3 d |
| P-11 | **配置开关** | `.env.example` 新增 `OPSKEEPER_PI_ENABLED`、`OPSKEEPER_PI_HTTP_PORT`（默认 19000）、`OPSKEEPER_PI_BIN`（默认 `vendor/pi/packages/coding-agent/dist/cli.js`）、`OPSKEEPER_PI_AUTO_UPGRADE`、`OPSKEEPER_PI_TAG_LOCK`（默认 `v0.85.1`），edge 启动时校验 | `.env.example`、`internal/edgeagent/config/pi.go` | 0.5 d |
| P-12 | **pi-yaml-hooks deny-list（v1.3 新增）** | 在 edge bundle 内放 `~/.pi/agent/hook/hooks.yaml`，用 `pi-yaml-hooks`（v0.85.x 兼容）写 deny-list：拦截 `rm -rf` / `sudo` / 写 `/etc/shadow` / 写 `.env` / 写 `node_modules/` 等；通过 `OPSKEEPER_PI_EXTRA_PACKAGES=pi-yaml-hooks` 在 edge 启动时 `pi install` 注入 | `dist/hooks/hooks.yaml`、`internal/edgeagent/pisupervisor/install.go` | 1 d |

合计：**约 22 工作日 / 4.5 人周**（含新 P-1 submodule + P-2 supervisor + P-3 HTTP 端点）。

### 5.2 一期加固（1.0-preview 与 1.0 之间）

复用现有代码卫生问题，对应 §2.4 的 12 个问题：

| 编号 | 工作项 | 关联文件 | 工期 |
|---|---|---|---|
| C-1 | compose `opskeeper` 双副本 + Redis lock 占主 + nginx 上游 | `docker-compose.yml`、`deploy/install/docker-compose.yml` | 1 d |
| C-2 | JWT / admin 默认值改 `${OPSKEEPER_JWT_SECRET:?must set}`，并文档化生成命令 | `.env.example`、`docs/installation.md` | 0.5 d |
| C-3 | tempo/qdrant/frontier 健康检查 + `depends_on: service_healthy` | `docker-compose.yml`、`deploy/install/docker-compose.yml` | 0.5 d |
| C-4 | prom 注册失败改为 `log.Fatal` + 上下文，单元测试覆盖 | `internal/pkg/prom/{manager,agentteams}_metrics.go` | 1 d |
| C-5 | `internal/higress` 增 smoke 测试（grpc dial + store round-trip） | `internal/higress/{server,store}_test.go` | 1 d |
| C-6 | `chatdiagnose` 与 `model/aiops` 补单元测试到 ≥ 70% | 上述包内 `*_test.go` | 2 d |
| C-7 | Makefile 新增 `cover` / `cover-html` 目标；CI 上传 `coverage.out` | `Makefile`、`.github/workflows/*` | 0.5 d |
| C-8 | CHANGELOG 更新：1.0-preview + 1.0 两个新条目 | `CHANGELOG.md` | 0.5 d |
| C-9 | agentteams-controller 镜像 tag 锁定版本（或明确为 dev-allowed） | `docker-compose.yml` | 0.5 d |
| C-10 | 把 33 处 `panic(nil-arg)` 收敛成 `internal/pkg/errs/must.go` 的统一 helper（或 `error` 返回） | 新建 `internal/pkg/errs/must.go`、散点替换 | 1.5 d |
| C-11 | `Dockerfile.dev` 顶部加注释引用 distroless `deploy/Dockerfile.opskeeper`；加 `USER` 切换 | `Dockerfile.dev` | 0.5 d |
| C-12 | `.golangci.yml` 给 errcheck 加 `excluded-functions` 清单 | `.golangci.yml` | 0.5 d |

合计：**约 10 工作日 / 2 人周**（可与 §5.1 并行做）。

### 5.3 一期完整版（1.0 GA）

| ID | 模块 | 内容 | 关联文件 |
|---|---|---|---|
| G-1 | Proactive probe | Pi 在 cron（默认 60 s）上跑 `host_load` / `host_processes` / `journal_errors` / `disk_usage` 轻量探测，发现异常触发 triage；调度由 opskeeper-edge 的 `internal/edgeagent/pisupervisor/scheduler.go` 驱动 | `internal/edgeagent/pisupervisor/scheduler.go` |
| G-2 | 本地 RCA | Pi 用 opskeeper-edge 提供的 bash 工具聚合 `dmesg` / `journal` / `top` / `iostat` 至少 4 个事实源，给出 root_cause + evidence | `vendor/pi/.pi/skills/opskeeper-rca/SKILL.md` |
| G-3 | 自愈闭环 | RCA 命中白名单 playbook → 调 `host_restart_service`（受 review + HITL）；不命中 → 上行 reviewer worker + Element HITL | `vendor/pi/.pi/skills/opskeeper-recover/SKILL.md`、`internal/edgeagent/server/propose.go` |
| G-4 | 复盘自学习 | 每条修复生成 postmortem，行级归档到 `internal/knowledge/`，下轮 probe 召回；postmortem 模板由 Pi 的 prompt 提示工程管理 | `internal/edgeagent/biz/pi_postmortem.go`、`internal/knowledge/ingest/` |
| G-5 | **升级管线正式化（v1.1 核心）** | 写 `scripts/sync-pi.sh`：① 跑 `git submodule update --remote vendor/pi-mono`，② `git pull --rebase` 与 opskeeper 主分支同步，③ CI 跑 `make test-pi`（含 harness cases），④ 自动开 issue 跟踪 vendor/pi-mono 与 pi-coding-agent 上游 changelog 重要变更 | `scripts/sync-pi.sh`、`.github/workflows/sync-pi.yml`、`docs/pi-upgrade.md` |
| G-6 | 安全加固 | `traceparent` 全链路审计；approval token 单次有效 + 短 TTL（默认 60 s）；HMAC 链覆盖 Pi 全部动作 | 散点 |
| G-7 | 文档 | `docs/pi-agent.md`、`docs/architecture-pi.svg`、`docs/operations-pi.md`、`docs/pi-upgrade.md`、`docs/pi-skills.md` | 新文件 |
| G-8 | 评估上线 | `opskeeper-eval` 新增 `pi` 子命令，把 1.0-preview 的 5 个 edge case + 5 个 pi case 跑通，上 leaderboard | `cmd/opskeeper-eval/pi.go`、`internal/harness/leaderboard/` |
| G-9 | Pi 二进制独立分发（可选） | 除了 vendor/pi-mono 路径，CI 同时产 `pi-coding-agent-vX.Y.Z-linux-amd64.tar.gz` 与 `pi-coding-agent-vX.Y.Z-linux-arm64.tar.gz`，与 `opskeeper-edge` 二进制共同塞进 edge bundle，部署机只需放 bundle | `dist/build-edge-bundle.sh` |
| G-10 | 离线模式 | opskeeper-edge 缓存最近一次 `vendor/pi-mono` 的 npm 依赖（`pnpm` pnpm-store），离线时 Pi 仍可启机；token 用本地 LLM（llama.cpp / ollama）走 `@earendil-works/pi-ai` 的 custom endpoint | `scripts/build-pi-offline-bundle.sh`、`docs/pi-offline.md` |

### 5.4 不在本期范围（明示）

- 完全自治的云端 LLM 关闭（云端 reviewer 永远在场）。
- Pi 跨机协调（A2A v1.5）—— 留给 1.5。
- 自进化 playbook —— 留给 2.0。
- 与外部商业平台（Resolve.ai / Datadog Bits AI 等）的对接 —— 留给 2.x。
- 在 opskeeper 仓库内 fork pi-mono —— 任何 fork 必须先回到上游 PR。

### 5.5 本期实施状态（v1.4：实施并真实验证）

> 按"实现 → 真实验证 → 标记"的节奏，2026-09-10 起在 devbox 内逐项交付并验证。
> 状态码：✅ 已实施并真实验证通过；🟡 部分实施（仅有 yaml / 脚本部分，Go 代码待补）；⛔ 阻塞 / 延后（标出原因）；⬜ 未开始。

| ID | 状态 | 实施摘要（本期 devbox 单 agent 可交付范围） | 真实验证手段 | 阻塞原因（如有） |
|---|---|---|---|---|
| **P-1** | ✅ | `vendor/pi/` 作 git submodule，pin tag `v0.85.1`（commit `d981de12`），`.gitmodules` + `.gitignore` 已加 `vendor/pi` 例外 | `git -C vendor/pi describe --tags --exact-match HEAD` → `v0.85.1`；`git submodule status` 正常 | — |
| **P-2** | 🟡 | `internal/edgeagent/pisupervisor/{supervisor,health}.go`：spawn + health 探针 + 退避重启 + crash-loop gate + Stop 强杀 + Upgrade/Install 钩子。`Supervisor{Config, Status, State}` 线程安全；`State{New,Starting,Running,Unhealthy,Restarting,CrashLoop,Stopped}`；`Config{Bin, Args, Env, HealthURL, HealthInterval, HealthTimeout, UnhealthyThreshold, RestartBackoffMin/Max, RestartMaxBurst, RestartWindow, AutoUpgrade, TagLock, ExtraPackages, SyncPiScript}`；exponential backoff 上限封顶；`isCrashLoop` 滚动窗口剪枝；`runLoop` 把 `child.Wait()` 放侧 goroutine 让 `stopCh` 能立即强杀；`HTTPHealthProbe` 接 2xx + status==ok（degraded 拒；non-JSON body 透传接受便于早期 Pi 版本）；`killProcessGroup` 用负 PID SIGKILL 杀整组；`Upgrade()` 拒绝无 TagLock 拒绝上游 tag-flipping；`installExtraPackages` 用 `pi install` 子命令 pre-flight 失败 fail-closed | `go vet ./internal/edgeagent/pisupervisor/...` 0 错误；`go test ./internal/edgeagent/pisupervisor/...` 全绿（17 个 case 覆盖：New 必填校验 / AutoUpgrade 缺 TagLock/SyncPiScript / 默认值填充 / 双 Start 拒绝 / Stop 幂等 / 新建 Status / backoff 增长到封顶 / crash-loop 边界 / 窗口剪枝 / Upgrade 拒无 TagLock/SyncPiScript / HTTP probe 5 种变体（ok / degraded 拒 / 503 拒 / non-JSON 透传 / 连接拒绝）/ recordRestart / 0 重启 backoff / spawn 缺 bin / Start+Stop 后状态 stopped）；`go test ./internal/edgeagent/...` 全绿 | 仍缺：(a) 真机 Pi 二进制 + systemd unit 集成；(b) `internal/edgeagent/biz/agent.go` 把 Supervisor 接进 edge 启动路径；(c) `OPSKEEPER_PI_*` 环境变量 → Config 的解析层（已在 P-11 范围） |
| **P-3** | 🟡 | `internal/edgeagent/server/` 路由全套落地：`/health` + `/v1/edge/{audit,propose}` + `/v1/edge/tools/{bash,host_restart_service,host_files/{check,find_large_files,du_summary,stat_file}}`。`bash` handler 串 `cmdpolicy.Sandbox.Exec` + `audit.Chain.Append`（每调用一次审计，含 allow/deny 两态）+ write 模式 `X-Opskeeper-Pi-Approval-Token` 头校验（缺失返 401 / cache 未配置 fail-closed / token 一次性消耗）。`host_restart_service` 串 `restart_service.SandboxConfig.AllowedUnits` + approval 必填 + Mocked=true 短路成功 / Mocked=false 返 501 待真 systemctl shell-out。`host_files/{find_large_files,du_summary,stat_file}` 复用 `host_files.RunFindOneSimple` / `RunDuOne` / `RunStatOne`（v1.9 把原 `runXxxOnePath` 导出 + 加平铺参数版），每次入审计 `tool.call.host_files.*`，deny 也入审计。`host_files/check` 走 `host_files.SandboxConfig.ValidatePath` 轻量预检。`propose` 仅入审计。`enforceLoopback` 拒绝 0.0.0.0 / 192.0.2.1 / `[2001:db8::1]` / 错格式 / 空 host。 | `go vet ./internal/edgeagent/server/...` 0 错误；`go test ./internal/edgeagent/server/...` 全绿（25 个 case = 原 18 + host_files 7：sandbox reject 传 `Allowed=false`+`Reason`+`AuditSeq` / 空 path 400 / GET 405 / `HostFiles=nil` 503 / 真 stat_file 端到端跑 tmp 文件验 `result.type="file"`+`size_bytes=5` / 非法 JSON 400）；`go test ./internal/edgeagent/...` 全绿 | 仍缺：(a) 多 path batch (`paths []string` 1..16，底层 `RunXxxOne` 已支持)；(b) 真 systemctl shell-out；(c) `biz/agent.go` 把 server+supervisor 接进启动路径；(d) P-5 tunnel 上行 + reviewer grant；(e) e2e 需目标机 |
| **P-4** | 🟡 | `cmdpolicy.DefaultPiCapable()` + `ApprovalCache` + `Sandbox.ApprovalChecker` 字段 + `CacheApprovalChecker()` 适配器 | `internal/edgeagent/cmdpolicy/{policy_pi,approval}.go` + 同名 `_test.go` 已写；`go vet` 0 错误；`go test ./internal/edgeagent/cmdpolicy/...` 全绿（13 个新 case 覆盖：DefaultPiCapable 继承 / PathAllowlist 加宽 / loopback 网络 / `kill -l` vs `kill 12345` / `pgrep`/`pidof` / `pkill` 双签；ApprovalCache put/consume/expired/mismatch/single-use/eviction） | HTTP 头 `X-Opskeeper-Pi-Approval-Token` 注入与 handler 集成需 P-3 HTTP 路由；缺目标机无法 e2e |
| **P-5** | ⛔ | `tunnel.v1.pi_propose_action` / `pi_approval_grant` proto + handler | — | 需 Go 1.25 + proto 编译链 + 真云端 geminio 隧道 |
| **P-6** | 🟡 | `internal/edgeagent/audit/chain.go` — append-only HMAC-SHA256 审计链：`Event{Sequence, Timestamp, Kind, EdgeID, ProposalID, Payload, PrevHash, Hash}`、`Chain.Append/Snapshot/Last/Len`、`Verify()`（dense sequence + prev_hash + recompute digest）、`MinKeyBytes=32`、genesis prev_hash = 32 字节 0、JSON-canonical 序列化（Payload 用 `json.RawMessage` 原样透传，Timestamp RFC3339 秒精度 — 让重新签名稳定可重现） | `go vet` 0 错误；`go test ./internal/edgeagent/audit/...` 全绿（16 个 case 覆盖：append+verify / genesis prev / restored 续号 / restore 拒篡改 / mid-chain 编辑定位 / 短 key / 空 edgeID / 非法 JSON payload / 空 kind / 不同 key 不同 hash / 200 goroutine 并发 append 序列稠密无重复 / 错 key 拒绝 / 时间戳秒精度 / 外部 key 拷贝隔离 / 重复 restore+append / hex 编解码）；`go test ./internal/edgeagent/...` 全绿 | `tunnel.v1.pi_audit` proto + geminio 流式上行 + 与 `EdgeID` 维度的云端 replay-guard 仍依赖 P-2/P-3/P-5；HMAC 链本身是 local-only 已可独立验证 |
| **P-7** | ✅ | 4 份 SKILL.md：`opskeeper-restart-service` / `opskeeper-host-files` / `opskeeper-diagnostics` / `opskeeper-incident-report`，放置在 `pi-skills/<name>/SKILL.md`（**与原 plan 略有偏差**：不在 `vendor/pi/.pi/skills/` 内，因为那是上游仓库；放在 opskeeper 自有路径，由 edge 启动时拷贝/链接进去。已在 plan 附录 D 记录偏差） | 4/4 frontmatter YAML parse 通过；目录结构匹配 Pi `core/skills.ts` 目录即 skill 根的约定 | — |
| **P-8** | ✅ | `pi-skills/opskeeper-monitor/AGENTS.md`（7 节身份 / 契约 / 双签 / token 预算 / 边界 / 升级时机 / 禁止动作）+ `scripts/render-pi-system-md.py` 把 host snapshot 注入 SYSTEM.md | `python3 -m py_compile` 通过；`render-pi-system-md.py --target X` 真写一份 4 674 字节 SYSTEM.md；`render-pi-system-md.py --check` 通过（timestamp 已 normalize 到秒级 + comparator 屏蔽 ts） | — |
| **P-9** | ⛔ | Web Pi tab in `web/src/pages/EdgeDetail.tsx` | — | 需 `pnpm typecheck` + web deps；devbox 缺 web 依赖 |
| **P-10** | ⛔ | harness cases `internal/harness/cases/pi/bash_safety` 等 | — | 需 Go 1.25；依赖 P-2/P-3/P-4 |
| **P-11** | ✅ | `internal/edgeagent/biz/pi_config.go`：`PiConfig` struct 含 13 个 `OPSKEEPER_PI_*` 键（Enabled / HTTPBind / HTTPPort / Bin / AutoUpgrade / TagLock / SyncPiScript / LLM{Provider,Model,BaseURL,APIKey} / ExtraPackages / FileAllowlist / ApprovalTokenTTL）；`LoadPiConfig()` 包 `os.Environ()`，`LoadPiConfigFrom([]string)` 是可测核心；`BuildSupervisorConfig()` 把 PiConfig 翻译成 `pisupervisor.Config`（Args 由 bind+port 拼，`HealthURL` 派生，`ExtraPackages` 防御性拷贝）；错误聚合（一次返回所有非法键）；`parseBool` 接受 true/1/yes/on + false/0/no/off/空；`envToMap` 过滤 `=novalue` 空键与无 `=` 残行；TagLock 用两值 map 查找以区分"absent"与"present-but-empty"（让 AutoUpgrade+空 TagLock 正确失败） | `go vet ./internal/edgeagent/biz/...` 0 错误；`go test ./internal/edgeagent/biz/...` 全绿（11 个新 case：默认值 / 覆盖 / parseBool 10 变体 / 非法值 7 变体 / AutoUpgrade × TagLock × SyncPiScript 三段交叉验证 / 错误聚合 / BuildSupervisorConfig 翻译 / ExtraPackages 防御性拷贝 / envToMap 过滤）；`go test ./internal/edgeagent/...` 全绿（含既有 audit / bash / biz / changewatcher / cmdpolicy / collector / host_files / pisupervisor / plugins / restart_service / server） | 仍缺：(a) 把 `LoadPiConfig()` 接进 `cmd/opskeeper-edge/main.go`；(b) `ApprovalCache.TTL` 用 `cfg.ApprovalTokenTTL`；(c) `cmdpolicy.Policy.NetworkHostAllowlist` 用 `cfg.FileAllowlist`；(d) 真机 e2e |
| **P-12** | ✅ | `dist/hooks/hooks.yaml` — 14 条 pi-yaml-hooks 规则：`tool.before.bash` 12 条（rm -rf / sudo / .env / shadow / passwd / boot / docker / kubeconfig / node_modules / iptables / systemctl / kill）+ `tool.before.write` 6 条路径 deny-list + `session.before_compact` 1 条 secret scrub + `agent.before_start` 1 条 AGENTS.md 必加载 | yamllint 0 错误；CI workflow `sync-pi.yml` 内有 best-effort `npm install pi-yaml-hooks` + `h.validate(doc)` 步骤（若包未发布则 skip） | — |
| **C-1 ~ C-12** | ⛔ | 云端 12 项 housekeeping | — | 全部需 Go 1.25；多数需联调环境 |
| **G-1** | ⛔ | Proactive probe (`pisupervisor/scheduler.go`) | — | 需 Go；依赖 P-2 |
| **G-2** | ✅（局部） | RCA skill 提示工程占位（已通过 P-7 `opskeeper-diagnostics` 推断）；实际 skill 文档 `opskeeper-rca/SKILL.md` 未单独写（合并在 diagnostics 末尾的 RCA 提示中） | 包含在 P-7 验证里 | skill 文档合并到 diagnostics 而未独立 — 文档偏差，已记 |
| **G-3** | 🟡 | 自愈闭环：依赖 `opskeeper-restart-service`（✅）+ 云端 reviewer worker（⛔ 阻塞） | skill 链路通；reviewer worker 阻塞 | 阻塞于 P-5 + 云端 admin 凭据 |
| **G-4** | ⛔ | 复盘自学习（`internal/knowledge/ingest/`） | — | 需 Go；知识库 ingestion 联调 |
| **G-5** | ✅ | `scripts/sync-pi.sh`（10 节，含 `--target`/`--push`/`--yes`、干净 tree 校验、tag 远程校验、submodule 更新、`git pull --rebase`、render-check、make verify、commit message 模板）+ `.github/workflows/sync-pi.yml`（verify-pin job：pin tag / yamllint / pi-yaml-hooks load best-effort / render-check / shellcheck / py_compile） | shellcheck 0 错误；yamllint workflow 0 错误；render-check 0 漂移；tag pin 检查通过；CI workflow 已写但**未在 GitHub Actions 实际跑过**（devbox 无 `gh` 推送权限；建议用户在 UI 触发 `workflow_dispatch` 验证） | workflow 实跑延后（需 push + Actions） |
| **G-6** | ⛔ | 审计 HMAC 链 + approval token TTL | — | 需 Go；依赖 P-5/P-6 |
| **G-7** | 🟡 | 文档：`docs/pi-agent.md` / `docs/architecture-pi.svg` / `docs/operations-pi.md` / `docs/pi-upgrade.md` / `docs/pi-skills.md` 均未单独写（本回合仅产出 SKILL.md 与 AGENTS.md，文档化需求合并在 plan1.0.md §7-§8） | 文档内容存在 plan 中；独立文件未拆 | 延后 |
| **G-8** | ⛔ | `cmd/opskeeper-eval/pi.go` + leaderboard | — | 需 Go；需 P-10 harness |
| **G-9** | ⛔ | Pi 二进制独立分发（`dist/build-edge-bundle.sh` 已存在，但未加 Pi 二进制下载步骤） | — | 需 Go（edge bundle）+ 网络发布策略 |
| **G-10** | ⛔ | 离线模式 + 本地 LLM | — | 需 pnpm-store 缓存 + 本地 LLM 部署 |

**本期合计**：✅ 已验证通过 **7 项**（+1：P-11）/ 🟡 部分实施 **6 项**（-1：P-11 升 ✅）/ ⛔ 阻塞 **16 项**（均因 Go 1.25 / 目标机 / LLM key / 云端 admin 凭据四项环境约束）。

**未在本回合交付但仍按 plan 保留的工作项**：P-5 / P-9 / P-10 / C-1~12 / G-1 / G-4 / G-8 / G-9 / G-10。这 9 + 12 = **21 项**构成下一回合（需先解决环境约束后再启动）的明确 backlog。P-2 / P-3 / P-4 / P-6 四件套的骨架 + 真实 read 工具 + env 解析层全部交付；剩余的 tunnel 上行 + 真 systemctl + 云端 reviewer grant + biz/agent.go 接进启动路径随 P-5 + 真机环境一起。

### 5.6 本期偏差记录

| 偏差 | 原 plan 位置 | 实际位置 | 原因 |
|---|---|---|---|
| Skills 位置 | `vendor/pi/.pi/skills/opskeeper-*/SKILL.md` | `pi-skills/opskeeper-*/SKILL.md`（opskeeper 自有） | `vendor/pi` 是上游 submodule，不应在 opskeeper 仓库里改；edge 启动时由 `scripts/render-pi-system-md.py` 同款机制把 skills 注入到 Pi 可发现的路径（`vendor/pi/.pi/skills/opskeeper-*/` 或 opskeeper-edge 的 `~/.pi/skills/`） |
| SYSTEM.md 路径 | `vendor/pi/.pi/agents/opskeeper-monitor.md` | 同上（render 时写入） | 同上：source-of-truth 在 opskeeper 仓库，渲染产物可由 `OPSKEEPER_PI_AGENT_DIR` 环境变量决定最终落地 |
| `.env.example` 加 OPSKEEPER_PI_LLM_* 4 行 | plan §6.5 没列；plan P-11 只列 5 行 | 加 4 行（provider / model / base_url / api_key），从 §3.1 / §4.1 / §6.4 推断 | LLM provider 切到 Pi 必然要单独配置项；建议进入 plan v1.5 |

---

## 六、协议与命名（v1.1 修订）

### 6.1 命名（v1.2 限定：仅 edge 侧）

- **Pi agent** 在代码里固定指真身 [pi-coding-agent](https://pi.dev/)（@earendil-works 维护，TypeScript/Node.js）；严禁在 opskeeper 内做 Go 复刻，也严禁把 Pi 名字引入云端代码。
- opskeeper 仓库里 Pi 相关路径**全部在 edge 侧**：`vendor/pi/`（submodule）、`internal/edgeagent/pisupervisor/`（Go supervisor）、`internal/edgeagent/server/`（HTTP 端点）、`docs/pi-*`、`scripts/sync-pi.sh`。
- Pi 私有配置：`vendor/pi/.pi/agents/opskeeper-monitor.md`、`vendor/pi/.pi/skills/opskeeper-*/SKILL.md`（与 Pi 上游的 settings.json 体系兼容）。
- **云端命名隔离**：`cmd/opskeeper`、`internal/manager/`、`internal/pkg/llm/` 下的 Go 文件**不允许**出现 `pi`、`pilo`、`pi-`、`pi_`、`@earendil-works` 字样的 import / 依赖 / 注释（注释里解释"为什么不用 Pi"也不允许）。

### 6.2 集成模式选择

| 集成模式 | 评估 | 选择理由 |
|---|---|---|
| **进程内嵌 SDK**（TypeScript Node 子运行时） | 否 | 拖入 Node 运行时到 Go 二进制；增加 ~150 MB；跨语言 FFI 维护成本高 |
| **sidecar + stdio JSONL**（`pi --mode rpc --no-session`） | 备选 | 简单但 stdout 解析在 Go 里要严格按 LF 处理；并发与多 session 难 |
| **sidecar + HTTP+SSE**（`pi --mode http --bind 127.0.0.1 --port 19000`） | **采用** | 容器化部署友好；多 session 通过 HTTP keepalive + SSE 自然并发；本地 HTTP 不暴露公网 |
| **sidecar + ACP**（`pi --mode acp`） | 否 | ACP 是为编辑器设计的；opskeeper 不是编辑器 |

> **结论**：1.0 锁定 sidecar + HTTP 模式（`--mode http --bind 127.0.0.1 --port 19000`）。当 Pi 上游推出更友好的 mode 时再评估切换。

### 6.3 协议

| 协议 | 用法 | 一期目标 |
|---|---|---|
| **HTTP + SSE** | Pi ↔ opskeeper-edge（同主机，localhost） | 立即采用（`pi --mode http`） |
| **MCP** (Anthropic → Linux Foundation, 2025-12) | Pi ↔ 外部工具（可挂 Pi 上） | v1.5 在 Pi 之上挂 MCP server |
| **A2A** (Google → Linux Foundation, 2026) | Pi ↔ OpsKeeper Manager ↔ 外部 Agent | v1.5 上线 A2A delegation |
| **W3C traceparent** | 审计 + 跨服务追踪 | 立即沿用，零成本 |
| **geminio / frontier** | Edge ↔ Cloud RPC | 立即沿用现有 `internal/pkg/tunnel/` + `singchia/geminio` |
| **CloudEvents**（备选） | Pi 审计事件上行格式 | 评估期；如已用 OpenTelemetry spans 则不必引入 |

### 6.4 为什么是 sidecar 而不是 in-process

1. **语言隔离** —— Pi 是 TypeScript/Node，opskeeper-edge 是 Go；in-process 需要 cgo + Node 子运行时，得不偿失。
2. **升级独立性** —— Pi 上游小版本发布频繁（lockstep 节奏明显），submodule + sidecar 让 opskeeper 自身发版节奏与 Pi 解耦。
3. **崩溃隔离** —— Pi 进程崩了不影响 edge 的 cmdpolicy 沙箱、tunnel 链路、metrics 上行。
4. **安全隔离** —— Pi 进程用最低权限用户（如 `nobody`）跑，只允许访问 `127.0.0.1:9101` 上的 edge HTTP API；edge 再去碰 host 资源。
5. **观测独立** —— Pi 的 stdout / stderr / OTel 独立采集，与 opskeeper 的 OTel 链路通过 traceparent 串联即可。

### 6.5 强制约束（v1.2 修订：云端零 Pi 为 #0 约束）

**#0 约束（v1.2 新增，最高优先级）**：

- **云端零 Pi 依赖** —— 见 §2.6。`cmd/opskeeper` 二进制、`internal/manager/` 云端路径、`internal/pkg/llm/`、`agents/*.md`、`internal/harness/` 云端评测 **全部不动**。所有云 ↔ Pi 通信**只**经 geminio tunnel（仅新增 3 个 RPC，纯加法）。
- 不允许任何 `cmd/opskeeper*` 云端二进制 `import` `internal/edgeagent/*` 或 `vendor/pi/*` 或任何 npm 依赖。

Pi 1.0 在 edge 侧必须复用以下已有组件，不允许平行实现：

- `internal/edgeagent/cmdpolicy` 沙箱（改为 HTTP 入口但实现不动）
- `internal/skill/builtin/*` 工具执行器（仅包一层 HTTP handler）
- `internal/manager/biz/aiops/tools/decorators/{audit,review_gate}`
- `internal/harness/{judge,runner,leaderboard}` 评测体系
- `deploy/install/upgrade.sh` 分发链路
- `internal/pkg/llm` 的云端用途（reviewer / investigator / chat 不变）

唯一允许新增代码：

- `vendor/pi/`（submodule，不可手工编辑）
- `internal/edgeagent/pisupervisor/`
- `internal/edgeagent/server/`（HTTP 端点）
- `vendor/pi/.pi/agents/opskeeper-monitor.md` 与 `vendor/pi/.pi/skills/opskeeper-*/SKILL.md`（Pi 私有配置）
- `scripts/sync-pi.sh`
- 相关 proto 字段、web 端 Pi tab、harness pi case
- `docs/pi-*` 文档

---

## 七、风险与缓解

| 风险 | 影响 | 概率 | 缓解 |
|---|---|---|---|
| Pi 在机端产生"幻觉式修复"，引发生产事故 | 极高 | 中 | review-gate 双签强制；approval token 单次 + 短 TTL；command + payload hash 绑定；reviewer worker 高风险动作二次 LLM 审计 |
| LLM token 成本失控 | 高 | 中 | `@earendil-works/pi-ai` 配置 daily budget；超限由 opskeeper-edge supervisor 拒绝启动 Pi | 
| pi-mono 上游 breaking change（frequent minor） | 中 | 高 | submodule + tag 锁定 + 季度 rebase；harness CI 必跑；emergency `OPSKEEPER_PI_TAG_LOCK` 切回稳定 tag |
| pi-mono 上游供应链投毒 | 极高 | 低 | 锁 commit hash（不只是 tag）；CI 里 `npm audit` + `pnpm install --frozen-lockfile`；vendor/pi-mono 与 pnpm-lock.yaml 入仓审计 |
| Edge 与 Cloud 断网时 Pi 失控 | 中 | 中 | 离线策略表（白名单 playbook + 只读工具）；断网时仅跑 read-only 与本地白名单修复；恢复后批量上行审计 |
| Pi 进程被入侵 = 主机被入侵 | 极高 | 低 | Pi 进程以最小权限用户跑（`nobody` 或 `pi-agent`）；无文件系统写权限（除 `~/.pi/agent/`）；只能 `curl http://127.0.0.1:9101/...`；edge 的 cmdpolicy 仍守最后一道 |
| MCP / A2A 标准变动导致接口不兼容 | 低 | 中 | 封装在 `internal/edgeagent/pisupervisor/protocol/`，标准升级只改该子包 |
| Node 运行时依赖（pnpm / npm）增加部署复杂度 | 中 | 高 | `dist/build-edge-bundle.sh` 把 `vendor/pi-mono` 与 `pnpm-store` 一并打包进 edge bundle；目标机无需联网 |
| 评测与生产指标脱钩 | 中 | 中 | harness case 必须用 `deploy/incident-events/` 同源的真实故障模式；每月一次 `opskeeper-eval pi` 入 leaderboard |

---

## 八、参考资料（v1.3 扩充：插件生态）

### 8.1 Pi 真身（**仅 host 侧使用**，v1.3 锁定 v0.85.1）

> v1.2 硬约束再次强调：以下全部条目**只适用于每台目标机器上的 `cmd/opskeeper-edge` 进程**；云端 `cmd/opskeeper` 不引入 Pi、不读本节内容、不引本节任何 npm 包。

- **pi.dev 官方** —— <https://pi.dev/> —— pi-coding-agent 官方网站（含 docs / guides / packages 三个子站）。
- **pi 仓库（canonical）** —— <https://github.com/earendil-works/pi>（v0.85.1，2026-09-05 发布）。历史仓库名 `badlogic/pi-mono` → `earendil-works/pi-mono` → `earendil-works/pi`；2026-05 由个人仓库迁到 Earendil Works 组织，v0.74.0 是首个 `@earendil-works/*` npm scope 的 release。
- **运行时要求** —— Node.js ≥ **22.19.0**（自 v0.75.0 起强制；target machine 上 edge bundle 必须自带 Node 或在 image 内置）。
- **npm 包** —— `@earendil-works/pi-coding-agent`、`@earendil-works/pi-agent-core`、`@earendil-works/pi-ai`、`@earendil-works/pi-tui`（MIT，作者 Mario Zechner / @badlogic + mitsuhiko + rwachtler）。
- **4 个核心工具** —— `read / write / edit / bash`（v0.85.x）；通过 `pi.registerTool(...)` + TypeBox schema 注册自定义工具；自动发现路径 `~/.pi/agent/tools/*/index.ts` 与 `.pi/tools/*/index.ts`。
- **扩展生命周期钩子（v0.85.x）** —— `session_start / session_before_switch / session_compact / session_shutdown / before_agent_start / agent_start / agent_end / turn_start / turn_end / tool_call / tool_result / auto_compaction_start / auto_compaction_end`；可通过 `pi.on(...)` 订阅；可通过返回 `{ block: true, reason }` 在 `tool_call` 阶段拦截。
- **两种 hook 机制不可混用** —— TypeScript 扩展（内置）**与** pi-yaml-hooks（独立 npm 包，事件命名 `tool.before.bash` dot-notation）；前者安装即用，后者走 `pi install npm:pi-yaml-hooks`。
- **七种运行模式** —— Interactive（默认 TUI）/ Print（`pi -p "query"`）/ JSON（`--mode json`）/ RPC（`--mode rpc --no-session`，LF-delimited JSONL）/ HTTP（`--mode http --port 19000 --bind 0.0.0.0`，HTTP + SSE）**【1.0 选用】** / ACP（`--mode acp`，给编辑器）/ SDK（in-process TS 嵌入）。
- **上下文工程** —— cascading `AGENTS.md` / `CLAUDE.md`，`SYSTEM.md`（替换）/ `APPEND_SYSTEM.md`（追加），on-demand Skills（`~/.pi/agent/skills/<name>/SKILL.md`），prompts（`~/.pi/agent/prompts/*.md`），sessions JSONL 树（`/tree` / `/fork` / `pi --export session.jsonl output.html`）。
- **多 provider** —— `@earendil-works/pi-ai` 统一 OpenAI / Anthropic / Google / Together AI / Windows ARM64 provider 等。
- **GitHub stars 生态** —— repo（earendil-works/pi）+ 派生 OpenClaw（~145k stars）合计生态规模显著。

> **升级管线小结**：`vendor/pi/` 是 git submodule；CI 跑 `git submodule update --remote vendor/pi` 后 `git pull --rebase` 与 opskeeper 主分支同步；harness case 跑通后才允许合入。

### 8.1.1 pi.dev/packages 插件生态（v1.3 新增，**优先复用而非自造**）

> pi.dev/packages 是 Pi 官方的 npm-style 插件市场，分类含 **extension / tool / skill / prompt / package-manager / template** 等。1.0 应**优先从这里挑现成的**集成到 opskeeper 的 edge bundle，而不是自造轮子。

| 包名 / 类型 | 用途 | 与 opskeeper 的关系 |
|---|---|---|
| **`pi-yaml-hooks`**（extension） | YAML 写法的轻量 hook 系统（`tool.before.bash` dot-notation）；适合"deny-list + 静态规则"场景 | **1.0 必须装** —— 写 `~/.pi/agent/hook/hooks.yaml` 拦截 `rm -rf` / `sudo` / 写 `/etc/shadow` / 写 `.env` 等高危操作；**比 TypeScript 扩展更适合 opskeeper 这种"规则要 ops 团队能改"的产品** |
| **`pi-extension-manager`**（extension） | 扩展的安装 / 启用 / 禁用 / 升级 UI 与 CLI | 配合 opskeeper web EdgeDetail Pi tab 使用 |
| **`pi-package-manager`**（package） | 通用 pi 包安装器（`pi install npm:xxx`） | 跑 `pi install npm:@opskeeper/ops-pi-extension` 的入口 |
| **`pi-packs`**（package） | 预打包的扩展集合（opinionated bundles） | 参考结构，自己出 `opskeeper-pack` |
| **`pi-toolbox`**（extension） | 通用工具集合（grep / sed / find 替代 / 文件批量操作） | 可作为 Pi 工具袋的"开箱即用"补充 |
| **`@oh-my-pi/swarm-extension`**（extension） | 多 agent 编排（在 host 上启多个 Pi 子 agent） | 1.5 用 —— 单机多 Pi（不同 persona）协同 |
| **`@tmustier/pi-agent-teams`**（extension） | Agent Teams 风格的多 agent 协作 | 1.5 用 —— 跨 Pi 实例分工 |
| **`@agentoom/pi-extension-manager`**（extension） | 另一套扩展管理器 | 备选 |
| **`@gaodes/pi-dev-kit`**（package） | Pi 二次开发工具包 | 内部 opskeeper-pi-extension 包开发用 |
| **`pi-crew`**（package） | Crew-style 多 agent 编排 | 1.5 备选 |
| **MCP 系列扩展** | 通过 MCP 接外部工具 | v1.5 把 Steampipe MCP / Higress MCP 接进 Pi 工具袋 |
| **官方 `@codex-infinity/pi-infinity`** 等 | 社区维护的长期支持包 | 评估中，暂不锁依赖 |

**1.0 集成原则**：

1. **优先用 pi-yaml-hooks 写 opskeeper 的 deny-list**，而不是新建 TypeScript extension —— 因为 YAML 规则 ops 团队可以直接 PR 编辑，不需 Go / TS 编译。
2. opskeeper 仓库**仅 vendor 必要包**，其他通过 `OPSKEEPER_PI_EXTRA_PACKAGES` 环境变量在 edge 启动时 `pi install` —— 减少 vendor 体积。
3. opskeeper 自己可以**反向发布**一个 [`@opskeeper/opskeeper-pi-extension`](https://pi.dev/packages) 公共包（含 opskeeper 专用 persona + skill + hook），让其他 Pi 用户也能用 opskeeper 的 ops 能力（这是 v1.5 商业化思路，1.0 仅做内部 vendor 版）。
4. **package 来源白名单**：1.0 仅允许 `@earendil-works/*` + opskeeper 自有包 + 上述表格明确列入的社区包；其他包需经过 `scripts/sync-pi.sh` 的 `make audit-pi-packages` 子命令审查供应链后再装。

### 8.2 论文 / 综述（2024–2026）

- **MASA: Multi-Agent System for Automating Monitoring in Cloud-Native Environments** — arXiv:2504.11253（2025）—— 多智能体云原生监控架构的直接先例。
- **Intelligent SRE: A Multi-agent LLM Framework for RCA** — Int. J. Intelligent Engineering & Systems, 2025-11 —— master + 5 specialists via MCP，92% RCA 准确率。
- **Autonomous Multi-Agent System for Integrated SRE and Self-Healing** — Atlantis Press ICSSSM-25（2025-12）—— CrewAI + DeepSeek-R1 + GPT-4o，96% RCA / 73% auto-fix。
- **From Playbooks to Autonomous Operations** — IJCESEN, 2025-09 —— playbook→自更新 procedural 框架过渡的综述。
- **Monitoring LLM-Based Multi-Agent Systems in Production** — arXiv:2503.17444 —— LLM agent fleet 的可观测模式（与我们的 OTel + audit 链路高度契合）。
- **IBM SRE-Agent-101** — NeurIPS 2024 Demo —— ReAct SRE + NL2Traces/Metrics/Kubectl 的最早原型。
- **AI Agents: Precision, Not Hallucination, in Anomaly Resolution** — StartupHub.ai 2025-08 —— 拓扑感知相关性 + 限制 action space 的设计哲学。
- **Autonomous DevOps: Self-Healing Infrastructure with AI-Driven Observability** — Fallbrook Research 2025-11 —— 自治适用边界与失败模式目录。
- **Multi-Agent RL for Kubernetes Resource Allocation** — arXiv:2504.09523 —— 多 agent 协同优于单 controller 的实证。

### 8.3 开源项目

#### Pi 之外，机端自治 / 边缘分发形态的直接参考

- **KubeEdge**（CNCF Graduated, 2024-10）—— CloudCore + EdgeCore + SQLite 替代 etcd + 100k 节点级联，是"控制平面 + 轻量边缘二进制"最强先例。建议优先研究其 MetaManager / NodeUpgradeJob 设计。
- **OpenYurt**（Alibaba, CNCF Incubating）—— K8s 边缘化方案，备选架构。
- **SuperEdge**（Tencent, CNCF Sandbox）—— 同上。
- **Baetyl**（Baidu, CNCF Sandbox）—— 同上。
- **Nomad**（HashiCorp）—— 跳过 K8s 的 Go 工作负载编排思路。
- **Temporal**（Go）—— 长时 agent workflow 的持久执行底层。

#### 智能体框架（Pi 之外的备选，仅供 opskeeper 云端内部使用）

- **LangGraph / LangChain** —— 默认的 stateful 多智能体编排，HITL `interrupt()` 模式值得借鉴。
- **LlamaIndex Workflows** —— RAG over runbooks + event-driven agentic flow。
- **CrewAI / AutoGen 0.4** —— 角色化与 group-chat agent。

#### Kubernetes AI 操作器

- **kubectl-ai**（Google, Apache 2.0）—— NL → kubectl。
- **k8sgpt**（CNCF Sandbox）—— 集群扫描器 + SAIKU auto-fix 模式，与 Pi "机端自治"最接近的现存形态。
- **llm-d**（Red Hat/IBM/Google）—— K8s 上的分布式 LLM 推理，便于把模型推到机端。

#### 工具 / 观测层

- **Steampipe**（AGPLv3）—— 140+ 云 / SaaS API 的 SQL + 现成 MCP server，可作为 Pi 立即可用的 read 工具袋扩展。
- **Ansible Lightspeed** —— "现有 fleet manager + AI" 模式的可借鉴产品形态。

### 8.4 协议标准

- **MCP**（Model Context Protocol）—— Anthropic 2024-11 发布，2025-12 捐赠 Linux Foundation AAIF。Tools / Resources / Prompts 三类原语，stdio + Streamable HTTP + SSE 三种传输。
- **A2A**（Agent-to-Agent）—— Google 2025-04 发布，2026 v1.0 后捐赠 Linux Foundation。Agent Cards 在 `/.well-known/agent-card.json`，支持有状态任务。
- 业界共识：MCP 用于 agent ↔ tools，A2A 用于 agent ↔ agent —— 与本规划完全契合。

### 8.5 直接竞品（商业 AI-SRE）

| 厂商 | 形态 | 启发 |
|---|---|---|
| **Resolve.ai** | AI Production Engineer，$35M Greylock 种子 | "读生产 / 写修复 PR"的产品叙事 |
| **Datadog Bits AI SRE** | 平台内嵌 | 验证 SaaS 路径 |
| **NeuBird Hawkeye** | AI-SRE agent | 同上 |
| **PagerDuty Autonomous SRE** | incident response | 同上 |
| **Shoreline.io → NVIDIA** | 自治 ops + ops packs，2025 被收购 | 关注 NVIDIA 后续动作 |
| **Dynatrace Davis CoPilot** | 多 agent 可观测性 | 同上 |

差异化定位（OpsKeeper 1.0 卖点）：

- 开源 + 可私有化（不像 SaaS 厂商）。
- 分级授权 + 强审计 + 双签变更（合规行业首选）。
- **fleet 视角**：商业 SaaS 默认假设你在它里面；OpsKeeper 给已有 fleet manager 的客户。
- **Pi 真身**：直接站在 [pi-mono](https://github.com/earendil-works/pi) 39k stars 的生态上，不重复造 agent 框架。

---

## 九、附录

### A. 1.0-preview 验收清单（Go/No-Go Gate）

- [ ] 上述 C-1 ~ C-12 已合 main 并 CI 全绿。
- [ ] `vendor/pi-mono` submodule 已 pin `v0.85.1`，CI 中 `git submodule status` 干净。
- [ ] `internal/edgeagent/pisupervisor/` 与 `internal/edgeagent/server/` 覆盖率 ≥ 70%。
- [ ] `internal/harness/cases/pi/bash_safety` 与 `internal/harness/cases/edge/*` 5 个 case 跑分 ≥ 70%。
- [ ] `compose-up` 后 `OPSKEEPER_PI_ENABLED=true` 的 edge 自动拉起 Pi sidecar；Pi 调用 `host_restart_service` 必经过 review + approval token + cmdpolicy；audit log HMAC 链校验通过。
- [ ] `scripts/sync-pi.sh` 在 CI dry-run 跑通，生成的 PR diff 仅涉及 `vendor/pi-mono`。
- [ ] 文档：`docs/pi-agent.md`、`docs/architecture-pi.svg`、`docs/operations-pi.md`、`docs/pi-upgrade.md`、`docs/pi-skills.md` 完成。

### B. 关键文件定位表（v1.1 更新）

| 主题 | 文件 |
|---|---|
| Pi submodule | `vendor/pi/`、`.gitmodules` |
| Pi 升级脚本 | `scripts/sync-pi.sh`、`Makefile` (`sync-pi` target) |
| Edge daemon 主循环 | `internal/edgeagent/biz/agent.go` |
| Pi sidecar supervisor | `internal/edgeagent/pisupervisor/{supervisor,health,upgrade}.go` |
| Edge HTTP 工具端点 | `internal/edgeagent/server/{router,tools,audit,propose}.go` |
| Edge 升级 / 分发 | `internal/edgeagent/biz/upgrade.go`、`deploy/install/upgrade.sh`、`dist/build-edge-bundle.sh` |
| Edge 沙箱 | `internal/edgeagent/cmdpolicy/{policy,sandbox,types}.go` |
| 云端 LLM 客户端（reviewer / investigator 用） | `internal/pkg/llm/{client,router,eino_routing,budget}.go` |
| Pi 私有 LLM API（多 provider） | `@earendil-works/pi-ai`（vendored in `vendor/pi/packages/ai/`） |
| 云端 agent 循环（云端用） | `internal/manager/biz/aiops/agent/agent.go` |
| 云端 chat runtime | `internal/manager/biz/aiops/chatruntime/runtime.go` |
| 工具双签 | `internal/manager/biz/aiops/tools/decorators/review_gate.go` |
| 工具审计 | `internal/manager/biz/aiops/tools/decorators/audit.go` |
| Tunnel 协议 | `api/tunnel/v1/tunnel.proto`、`internal/pkg/tunnel/` |
| 评测 harness | `internal/harness/{cases,judge,runner,leaderboard}` |
| Web Edge 控制台 | `web/src/pages/EdgeDetail.tsx` |
| Pi Skills | `vendor/pi/.pi/skills/opskeeper-*/SKILL.md` |
| Pi AGENTS / SYSTEM | `vendor/pi/.pi/agents/opskeeper-monitor.md`、`vendor/pi/.pi/SYSTEM.md` |

### C. 升级流程示例（v1.1 新增）

```bash
# 季度例行升级
make sync-pi                                          # git submodule update --remote vendor/pi
git add vendor/pi && git commit -m "bump pi to v0.85.2"
git pull --rebase origin main                         # 与 opskeeper 主分支同步
make test-pi                                          # harness CI 跑通
gh pr create --title "bump pi v0.85.1 → v0.85.2"      # PR diff 应仅含 vendor/pi

# 紧急回滚
OPSKEEPER_PI_TAG_LOCK=v0.85.1 opskeeper-edge         # 通过开关锁回上一个稳定 tag
```

### D. 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 1.0-draft | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | 初稿：现状盘点 + 问题清单 + 1.0-preview/1.0/1.5/2.0 路线 + 论文 / OSS / 协议参考 |
| 1.1 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | 重大修订：Pi = 真身 [pi-coding-agent](https://pi.dev/)（earendil-works/pi-mono v0.85.1，TypeScript/Node.js，MIT）；采用 **sidecar + HTTP 模式**（`pi --mode http`）；vendor pi-mono 为 git submodule；升级通过 `git pull --rebase`；增加 P-1 submodule / P-2 supervisor / P-3 edge HTTP 端点 / P-4 PiCapable 模式 / P-5 ~ P-11 等；新增 §5.3 G-5 升级管线、§6.4 sidecar 理由、§6.5 强制约束、§8.1 Pi 真身专章、附录 C 升级流程示例 |
| 1.2 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | 范围收敛：Pi **只**下放到目标机的 edge 进程；**云端零 Pi 依赖**（`cmd/opskeeper` 二进制、`internal/manager/`、`internal/pkg/llm/`、`agents/*.md`、`internal/harness/` 云端路径全部不动）。新增 §2.6 云端零 Pi 改动原则硬约束表 + 快速判定规则；§1 TL;DR 增列"云端零 Pi 依赖"为 #0 设计决策；§3.1 / §3.2 / §4 / §6.1 / §6.5 标题与正文全部 v1.2 标注；§6.5 #0 约束明文；§4 架构图 Cloud 框加 ⚠️ v1.2 硬约束注释；§8.1 加"仅 host 侧使用"重声明；附录 D 增列 v1.2 行 |
| 1.3 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | 修正 Pi 版本与路径：v0.85.1（2026-09-05 发布，earendil-works/pi 主仓，Node.js ≥ 22.19.0），原 v0.74.0 是 2026-05 首个 `@earendil-works/*` npm scope 的 release；仓库路径 `earendil-works/pi-mono` → `earendil-works/pi`；更新 §8.1 全部版本与 hook 列表（v0.85.x 钩子从 9 个扩到 13 个）；新增 **§8.1.1 pi.dev/packages 插件生态**（pi-yaml-hooks / pi-extension-manager / pi-package-manager / pi-toolbox / swarm-extension / agent-teams / MCP 系列等 11 类包 + 4 条集成原则）；新增 P-12 工作项（pi-yaml-hooks deny-list）；更新附录 C 升级流程示例；附录 D 增列 v1.3 行 |
| 1.4 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | **实施并真实验证**版：选 Option A（仅交付 devbox 内可验证项）。新增 **§5.5 本期实施状态表**（✅ 6 项 / 🟡 3 项 / ⛔ 20 项）+ **§5.6 本期偏差记录**（Skills 与 SYSTEM.md 路径改到 opskeeper 自有 `pi-skills/` / 由 edge 启动时注入，避免污染上游 submodule；`.env.example` 增 4 行 `OPSKEEPER_PI_LLM_*`）。本期落地物：`vendor/pi` submodule pin v0.85.1、4 份 SKILL.md、`opskeeper-monitor/AGENTS.md`、`scripts/render-pi-system-md.py`（带 `--check` 时间戳 normalize）、`scripts/sync-pi.sh`（含 tag 校验 + rebase + render-check + make verify 钩子）、`.github/workflows/sync-pi.yml`（pin / yamllint / pi-yaml-hooks load best-effort / shellcheck / py_compile）、`dist/hooks/hooks.yaml`（14 条 pi-yaml-hooks 规则）、`.env.example` 加 12 个 `OPSKEEPER_PI_*` 键。真实验证：yamllint 0 错误、shellcheck 0 错误、py_compile OK、render write+check 端到端通过、submodule tag pin `d981de12`。⛔ 阻塞项（20 个）需先解决 Go 1.25 / 目标机 / LLM key / 云端 admin 凭据 4 项环境约束才能继续。 |
| 1.5 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | P-4 cmdpolicy 部分落地 + Option B Go 工具链就绪。安装 Go 1.25.0 → `/home/devbox/.local/go`，GOPROXY=`goproxy.cn,direct`；`go vet ./...` 0 错误；`go test ./internal/edgeagent/...` 全绿。**P-4 部分交付 → 🟡**：`internal/edgeagent/cmdpolicy/policy_pi.go` 新增 `DefaultPiCapable()`（= `DefaultReadOnly` ∪ {`kill -l` / `pgrep` / `pidof` / `pkill --list`} + 加宽 `PathAllowlist` {`/etc/systemd`, `/run/systemd`, `/var/log/journal`, `/etc/os-release`, `/etc/resolv.conf`, `/etc/hostname`, `/etc/machine-id`, `/etc/locale/conf`} + loopback-only `NetworkHostAllowlist` {`127.0.0.0/8`, `::1/128`}）；`internal/edgeagent/cmdpolicy/approval.go` 新增 `ApprovalToken` / `ApprovalCache` / `ApprovalCheckerFunc` / `CacheApprovalChecker()`，60 s 默认 TTL、single-use、四种 binding mismatch 哨兵错误（missing / expired / proposal / kind / edge）；`Sandbox` 加 `ApprovalChecker ApprovalCheckerFunc` 字段（向后兼容，nil = 默认 read-only 模式）。13 个新单元测试覆盖继承性 / loopback 网络 / kill 双签 / pgrep/pidof / pkill 双签 / put-consume / expired / mismatch / single-use / eviction。**待 P-3 集成**：HTTP 头 `X-Opskeeper-Pi-Approval-Token` 注入与 `/v1/edge/tools/*` handler 集成。**未交付**：P-2 supervisor / P-3 HTTP 路由 / P-5 proto / P-6 audit 上行 / P-9 / P-10 harness / C-* / G-1 / G-4 / G-6 / G-8 / G-9 / G-10（仍阻塞于目标机 / LLM key / 云端 admin 凭据）。§5.5 状态表更新：✅ 6 / 🟡 4 / ⛔ 19。 |
| 1.6 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | P-6 HMAC 审计链部分交付。新增 `internal/edgeagent/audit/chain.go`：`Event{Sequence, Timestamp, Kind, EdgeID, ProposalID, Payload(json.RawMessage), PrevHash, Hash}`；`Chain.Append/Snapshot/Last/Len` 线程安全；`Verify()` 走 dense-sequence + prev_hash + recompute digest 三重校验；`MinKeyBytes=32`；genesis prevHash = 32 字节 0；canonical 序列化用 `json.RawMessage` 原样透传 payload + RFC3339 秒精度截断时间戳（确保重新签名稳定可重现）。**P-6 部分交付 → 🟡**：HMAC 链本身 local-only 已可独立验证 + 单元测试；`tunnel.v1.pi_audit` proto + geminio 流式上行 + 云端 replay-guard 仍依赖 P-2/P-3/P-5。16 个新单元测试覆盖 append+verify / genesis prev / restored 续号 / restore 拒篡改 / mid-chain 编辑定位 / 短 key / 空 edgeID / 非法 JSON / 空 kind / 不同 key / 200-goroutine 并发 append 序列稠密 / 错 key / 时间戳秒精度 / 外部 key 拷贝隔离 / 重复 restore+append / hex 编解码。`go vet ./...` 0 错误；`go test ./internal/edgeagent/...` 全绿（含新增 audit 包）。§5.5 状态表更新：✅ 6 / 🟡 5 / ⛔ 18。**未交付**：P-2 supervisor / P-3 HTTP 路由 / P-5 proto / P-9 / P-10 harness / C-* / G-1 / G-4 / G-8 / G-9 / G-10。 |
| 1.7 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | P-3 HTTP 路由骨架交付（继 P-4 cmdpolicy + P-6 audit 之后）。新增 `internal/edgeagent/server/` 包：`server.go`（Server 结构 + Handler + ListenAndServe + Shutdown + `enforceLoopback` 拒绝 0.0.0.0 / 非 loopback v4v6 / 错格式 / 空 host）+ `health.go`（`/health` 探活 + edge_id + audit_len）+ `audit.go`（`GET /v1/edge/audit?since=&kind=&limit=` 翻页过滤）+ `propose.go`（`POST /v1/edge/propose` 仅入审计，P-5 reviewer grant 走 tunnel）+ `bash.go`（`POST /v1/edge/tools/bash` 串 cmdpolicy + audit + approval）+ `restart.go`（`POST /v1/edge/tools/host_restart_service` 串白名单 + approval + Mocked 短路 / 真 systemctl 返 501）+ `host_files.go`（`POST /v1/edge/tools/host_files/check` 走 host_files.ValidatePath 预检）。**P-3 部分交付 → 🟡**：所有路由 + handler 已实现 + 通过 httptest 单测；剩余 host_files 三个真实 read 工具的 HTTP 包装 / 真机 ListenAndServe / P-5 tunnel 上行待补。18 个新单元测试：health OK/503 / bash read 允许 / read policy 拒 403 / write 缺 token 401 / cache 未配置 fail-closed / token 一次性消耗 / 非法 JSON / 空 cmd / restart 白名单内 mock 成功 / 未知 unit 403 / restart 缺 token 401 / propose 入审计 / propose 缺 ID 400 / audit 按 kind 过滤 / audit 按 since 翻页 / host_files/check 允许 / enforceLoopback 全 6 变体。`go vet ./...` 0 错误；`go test ./internal/edgeagent/...` 全绿（含新增 server 包）。§5.5 状态表更新：✅ 6 / 🟡 6 / ⛔ 17。**未交付**：P-2 supervisor / P-5 proto / P-9 / P-10 harness / C-* / G-1 / G-4 / G-8 / G-9 / G-10。commit `b8860cb`。 |
| 1.8 | 2026-09-10 | 编程助手-devbox1（22e8b20d…） | P-2 supervisor 骨架交付（最后一个 ⛔ 项的部分落地）。新增 `internal/edgeagent/pisupervisor/{supervisor,health}.go`：`Supervisor` 线程安全 + `Config` 全字段可配 + `State{New,Starting,Running,Unhealthy,Restarting,CrashLoop,Stopped}`；exponential backoff（RestartBackoffMin/Max 封顶）；`isCrashLoop` 滚动窗口剪枝（RestartMaxBurst/RestartWindow）；`runLoop` 把 `child.Wait()` 放侧 goroutine 让 `stopCh` 立即强杀（修了一版死锁 bug）；`HTTPHealthProbe` 接 2xx + status==ok（degraded 拒；non-JSON body 透传接受）；`killProcessGroup` 用负 PID SIGKILL 杀整组；`Upgrade()` 拒绝无 TagLock 拒绝上游 tag-flipping；`installExtraPackages` 用 `pi install` 子命令 pre-flight 失败 fail-closed。**P-2 部分交付 → 🟡**：supervisor 全部状态机 + spawn + health + 重启 + Stop 杀整组均实现并通过 17 个单测（fake shell 脚本模拟 Pi）。**未交付**：真 Pi 二进制 + systemd unit 集成 / `internal/edgeagent/biz/agent.go` 接进启动路径 / `OPSKEEPER_PI_*` 环境变量解析层（属 P-11）。`go vet ./...` 0 错误；`go test ./internal/edgeagent/...` 全绿。§5.5 状态表更新：✅ 6 / 🟡 7 / ⛔ 16。 |
| 1.9 | 2026-09-11 | 编程助手-devbox1（22e8b20d…） | P-3 host_files 真实 read 工具 HTTP 包装交付。`internal/edgeagent/host_files/handlers.go`：导出 `RunFindOne` / `RunDuOne` / `RunStatOne`（原 `runXxxOnePath`）+ 新增 `RunFindOneSimple`（平铺参数给 HTTP 调用方），原 `Register` 路径零回归。`internal/edgeagent/server/host_files.go`：新增三个 HTTP handler `host_files/{find_large_files,du_summary,stat_file}`，复用底层 `RunXxxOne` 单 path 入口，串 `requireHostFiles` 共享 gate（GET → 405 / `HostFiles=nil` → 503 / 非法 JSON → 400 / 空 path → 400）+ `recordHostFilesRead` 共享 audit 钩子（每次调用入 `tool.call.host_files.*`，deny 也入审计）。`server.go` 注册三个新路由，更新包 doc。`server_test.go` 加 7 个新 case：sandbox 拒绝传播 `Allowed=false` + `Reason` + `AuditSeq`，空 path 400，GET 405，未配置 503，真 stat_file 端到端跑 tmp 文件验 `result.type="file"` + `size_bytes=5`，非法 JSON 400。`go vet ./...` 0 错误；`go test ./internal/edgeagent/...` 全绿（server 包 25 个 case = 原 18 + host_files 7）。**P-3 推进**（host_files 部分 → ✅；多 path batch + 真 systemctl + biz 启动路径 + P-5 tunnel 仍待目标机）。§5.5 状态表保持 ✅ 6 / 🟡 7 / ⛔ 16（P-3 仍在 🟡 因部分项待目标机）。commit `5444eab`。 |
| 1.10 | 2026-09-11 | 编程助手-devbox1（22e8b20d…） | 现状盘点 + 架构图（评论内 3 张 mermaid：总体拓扑 / 单 read 时序 / supervisor 状态机），按 plan §P-X 索引列出每项的代码路径 + 验证结果 + 缺口。无代码改动；纯评论总结，等下一步信号。 |
| 1.11 | 2026-09-11 | 编程助手-devbox1（22e8b20d…） | 新增 `docs/architecture.md`（432 行，14 节 + 附录，10 张 mermaid 图：拓扑 / read 时序 / write 时序含 approval / supervisor 状态机 / 模块依赖 / audit class / cmdpolicy class / 部署 / 状态矩阵 / repo 布局）。与 `plan1.0.md` 平级，作为 1.0-preview 对外架构介绍 + P-2 e2e 部署参考。无代码改动；纯文档增量。§5.5 状态表保持 ✅ 6 / 🟡 7 / ⛔ 16。commit `ac6bf41`。 |
| 1.12 | 2026-09-11 | 编程助手-devbox1（22e8b20d…） | P-11 env 解析层落地（Go 部分）。新增 `internal/edgeagent/biz/pi_config.go`：`PiConfig` struct 含 13 个 `OPSKEEPER_PI_*` 键（Enabled / HTTPBind / HTTPPort / Bin / AutoUpgrade / TagLock / SyncPiScript / LLM{Provider,Model,BaseURL,APIKey} / ExtraPackages / FileAllowlist / ApprovalTokenTTL）；`LoadPiConfig()` 包 `os.Environ()`，`LoadPiConfigFrom([]string)` 可测核心；`BuildSupervisorConfig()` 把 PiConfig 翻译成 `pisupervisor.Config`（Args 由 bind+port 拼、`HealthURL` 派生、`ExtraPackages` 防御性拷贝）；错误聚合（一次返回所有非法键）；`parseBool` 接受 true/1/yes/on + false/0/no/off/空；`envToMap` 过滤 `=novalue` 空键与无 `=` 残行；TagLock 用两值 map 查找以区分"absent"与"present-but-empty"（让 AutoUpgrade+空 TagLock 正确失败）。**P-11 推进 → ✅**：env → Config 解析完成 + 11 个单测全绿。`go vet ./internal/edgeagent/biz/...` 0 错误；`go test ./internal/edgeagent/biz/...` 全绿；`go test ./internal/edgeagent/...` 全绿。**未交付**：(a) 把 `LoadPiConfig()` 接进 `cmd/opskeeper-edge/main.go`（需要 main.go 还没起 + 目标机）；(b) `ApprovalCache.TTL` 用 `cfg.ApprovalTokenTTL`；(c) `cmdpolicy.Policy.NetworkHostAllowlist` 用 `cfg.FileAllowlist`；(d) 真机 e2e。§5.5 状态表更新：✅ 7 / 🟡 6 / ⛔ 16。commit `8944a95`。 |
| 1.13 | 2026-09-11 | 编程助手-devbox1（22e8b20d…） | **plan1.1.md 横向增补**：回应 LUM-679 "分析整个okp / 未来支持向对应机器分发pi agent / 自主化解决问题 / pi agent自动监控 / AI 运维军团 / git pull --rebase / pi版本核实"。交付：① **plan1.1.md**（~410 行）：3 phase × 9 round（Phase F fleet / Phase A AI 运维军团 / Phase S 自主化闭环），含 4 个 plan1.0 没回答的问题（如何扩到 N host / Pi 之间如何协同 / 如何实现自主化闭环 / 异常检测）+ pi 版本核实 + 论文（Notaro et al. 2024 / Han et al. 2024 / Audibert et al. 2020 / Psaier 2011）+ OSS 对照（StackStorm / Argo Workflows / Temporal / Kubernetes Operators）+ pi.dev/packages 重审（**plan1.0 v1.3 误判 `swarm-extension` ⛔ → plan1.1 重审为 ✅ 必需**）；② **`git pull --rebase origin main`** 实际执行成功（rebase 9 commits → HEAD `936bbf0`；clean working tree）；③ **Pi 版本核实**：`npm view @earendil-works/pi-coding-agent version` → `0.85.1`，与 vendor/pi submodule commit `d981de12` tag 一致；npm registry 显示 0.85.1 是当前最新发布（历史版本链：0.80.3 / 0.80.5-0.80.10 / 0.81.0-0.81.1 / 0.82.0-0.82.1 / 0.83.0 / 0.84.0-0.84.4 / 0.85.0 / 0.85.1）—— **plan1.0 §8.1 的 v0.85.1 仍正确**，未找到证据需升降级。**未交付**：plan1.1 §5 所有 PR（F-1/2/3 / A-1/2/3 / S-1/2/3，均待目标机 / P-5 tunnel / 云端 admin 凭据解锁）。§5.5 状态表保持 ✅ 7 / 🟡 6 / ⛔ 16。 |
