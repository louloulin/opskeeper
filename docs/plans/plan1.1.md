# OpsKeeper 1.1 规划 — Pi Agent Fleet / AI 运维军团（plan1.1）

> 文件：plan1.1.md · 日期：2026-09-11（2026-09-12 同步 plan1.0 v1.16） · 适用代码库：`louloulin/opskeeper`
> 上游：`plan1.0.md`（**v1.16**） + `docs/architecture.md`（**v1.13**）
> 状态：**plan1.0 的横向增补，不取代 plan1.0 任何 P-/G-/E- 行**；聚焦「**plan1.0 路线之外、生产级 AI 运维军团还差什么**」

## 0. 与 plan1.0 的关系

- **plan1.0.md**（v1.12）：单 host 单 Pi sidecar 的端到端骨架（vendor/pi + supervisor + HTTP 工具 API + cmdpolicy + audit + skills + AGENTS.md + 升级管线）
- **plan1.1.md（本文件）**：横向增补，回答 4 个 plan1.0 没回答的问题
  1. **如何把单 host 单 Pi 扩到 N host N Pi**（fleet 分发与协调）
  2. **Pi 之间如何协同**（AI 运维军团 = 多 agent swarm 还是分层调度？）
  3. **如何实现"自主化解决问题"**（闭环 RCA → 修复 → 验证的端到端自动路径，不靠云端 reviewer 每步 HITL）
  4. **如何在大规模 host 上自动监控 + 异常检测**（anomaly detection + alert dedup + 智能降级）

plan1.1 不重写 plan1.0；它是「plan1.0 + 实施前 audit + pi.dev/OSS 资源盘点 + 自主化路线图」四件套。

## 1. 当前基线（plan1.0 v1.12 状态，自动可复现）

```text
来源：plan1.0.md §5.5 + 附录 D（截至 v1.14）
生成命令：git -C opskeeper log --oneline | head -10
```

| 度量 | 值 |
|---|---|
| plan1.0 已交付项 | ✅ 9（P-1 / **P-2R Pi RPC 通道** / P-7 / P-8 / P-11 / P-12 / **P-13 edge 接线** / G-2 部分 / G-5） |
| 部分交付项 | 🟡 5（P-3 HTTP 工具层 / P-4 cmdpolicy / P-6 audit chain / G-3 自愈闭环 / 其余基线部分项） |
| 阻塞项 | ⛔ 16（P-5 tunnel proto / P-9 web / P-10 harness / G-1 probe / G-4 knowledge / G-6 audit 上行 / G-8 eval / G-9 bundle / G-10 offline 等） |
| Pi 真身版本 | `@earendil-works/pi-coding-agent` **v0.85.1**（`npm view ... dist-tags` 2026-09-12 复核 `latest = 0.85.1`，已 pin 在 vendor/pi submodule commit `d981de12`） |
| Pi 集成模式 | **`pi --mode rpc`（stdio JSONL）** —— plan1.0 v1.16 实测更正：`--mode http` 不存在，Pi 不监听端口。传输层实现在 `internal/edgeagent/pirpc`，探活为 `get_state` 往返 |
| 关键约束 | **云端零 Pi 依赖**（plan1.0 v1.2 硬约束）—— `cmd/opskeeper` 不引入 `@earendil-works/*`，云端 `internal/pkg/llm` 原样保留 |
| 已实现 Pi sidecar 能力 | spawn / **RPC `get_state` 探活** / 退避重启 / crash-loop gate / edge 启动接线 / 5 个工具 HTTP 端点 / HMAC 本地审计 / 14 条 pi-yaml-hooks deny-list |
| 单 host 部署形态 | opskeeper-edge (Go 1.25) + Pi sidecar (TypeScript/Node 22.19，stdio JSONL 子进程) + geminio tunnel 上行 |

> ⚠️ **重要事实**：以上状态同步自 plan1.0 **v1.16**。每次结构性变更后**必须重新审** plan1.0 §5.5 状态表。
>
> v1.16 也影响 plan1.1 的两处前提：**A-1 引入 `swarm-extension`** 必须先按上游
> `docs/packages.md` 的安全警告读源码（"包以完整系统权限运行，extension 执行任意代码，
> skill 能指示模型执行任意命令"），并用带版本的 `npm:pkg@x.y.z` spec 以便被 pin；
> **A-2 按角色派发 skill** 现在有原生落点 —— `--skill <dir>` 与 `--tools` /
> `--exclude-tools` 已在 `pirpc.LaunchOptions` 里，按 host role 生成不同 argv
> 即可，不需要新造分发机制。

## 2. 现状问题清单（plan1.0 路线之外的差距）

按"哪个最阻塞用户"排序，不是按难度。

### 2.1 问题 A — 单 host 单 Pi ≠ Fleet

**现状**：plan1.0 假设每台目标机器独立跑 1 个 Pi + 1 个 opskeeper-edge。云端 Manager 通过 tunnel 收集 metrics + 上行 audit，但**没有任何 fleet 视角**：没有 fleet topology view、没有跨 host RCA 关联、没有 cluster-wide incident 聚合。

**证据**：
- `internal/manager/biz/aiops/` 主要服务"单 incident"路径；`TeamHarness`（v1.0.35）刚加 runtime readback，仍是单机视角
- opskeeper-edge 的 `internal/edgeagent/collector/` 已采集 host_load/processes/disk/journal，但 collector 上行后只到单 host 维度

**对自主化的影响**：单 host 看到 `host_load=99%` 触发 RCA，但**为什么**这台机器高负载（cluster-wide scheduler 问题？neighbor OOM cascade？）—— 缺 fleet 视角

### 2.2 问题 B — Pi 之间无协同 = 不是"军团"

**现状**：plan1.0 v1.2 明确"云端零 Pi 依赖"—— Pi 只在 host side，云端不下 Pi。这意味着每台 host 的 Pi 是**孤岛**：A 机器的 Pi 完成 RCA + 自愈时，不知道 B 机器也遇到同样 incident、不知道 C 机器刚修了同类问题、不知道 D 机器的 postmortem 写了 RCA root cause。

**证据**：
- `vendor/pi/.pi/skills/opskeeper-*/SKILL.md` 是 per-host 视角（"this host's journal / this host's processes"）
- `pi.dev/packages` 上有 `swarm-extension` / `agent-teams` 包，但 plan1.0 v1.3 §8.1.1 标注"WorkBuddy 不需要；放弃"（那是 OpenBuddy 上下文，**opskeeper 应该重新评估**）
- v1.16 补充：真实 Pi 的多 agent 能力都在 host 内（一个 Pi 进程 + swarm extension 起子 agent），**跨 host 协同上游不提供** —— 跨机的"军团"仍必须由 opskeeper 云端编排（F-3 / A-3 的分组与召回），Pi 侧只贡献 per-host 事实。这一点让 §6 的"云端零 Pi 依赖"约束天然成立

**对自主化的影响**：自主化 = "同 incident 在 N host 上同时解决 + 经验共享"，不是"每 host 独立 RCA"

### 2.3 问题 C — 自主化闭环不闭环

**现状**：plan1.0 G-3 自愈闭环 ⛔ 阻塞于 P-5（tunnel.proto）+ 云端 reviewer worker 双签。即便 P-5 通了，"自主化"还需要：
1. Pi 在 host side 自主判断"这个问题该不该走 review"（白名单内 vs 白名单外）
2. 白名单内的修复**真的不需要云端 HITL**（避免 reviewer 成为新瓶颈）
3. 白名单外的修复**自动 compose proposal + 上行 + 等 grant**（已有 spec，缺实现）

**证据**：
- `pi-skills/opskeeper-restart-service/SKILL.md` 已有"P-5 上行 review → approval token 回流 → 执行 → 审计"链路（plan1.0 §一.161）
- 但"白名单内跳过 review"的规则没显式建模

### 2.4 问题 D — 监控缺"主动异常检测"

**现状**：plan1.0 G-1 Proactive probe ⛔；现有 `internal/edgeagent/collector/` 是**定时拉**（scrape + composite + hwfingerprint），不是异常检测。

**证据**：
- `collector/scrape.go` 周期拉 `/proc/loadavg` / `iostat` / `journal_errors`，频率固定（默认 30 s）
- 没有 baseline + 偏差检测、没有跨 metric 关联、没有 ML-based anomaly score

**对自主化的影响**：异常检测 = RCA 之前的"是不是 incident"判断；没有它，Pi 的 RCA 是"被催着跑"不是"主动发现问题"

## 3. Pi 真身版本核实（回应"pi 版本不对"）

| 来源 | 版本声明 |
|---|---|
| plan1.0.md v1.3 §8.1 | **v0.85.1**（earendil-works/pi-mono → earendil-works/pi 主仓，Node.js ≥ 22.19.0） |
| vendor/pi submodule | commit `d981de12` tag `v0.85.1`（`git submodule status`） |
| `npm view @earendil-works/pi-coding-agent version` (2026-09-11) | **`0.85.1`** ← npm registry 验证 |
| `npm view @earendil-works/pi-coding-agent versions --json` | 历史版本：0.80.3 / 0.80.5 / 0.80.6 / 0.80.7 / 0.80.8 / 0.80.9 / 0.80.10 / 0.81.0 / 0.81.1 / 0.82.0 / 0.82.1 / 0.83.0 / 0.84.0 / 0.84.1 / 0.84.2 / 0.84.3 / 0.84.4 / 0.85.0 / 0.85.1 |
| **结论** | **plan1.0 v1.3 的 v0.85.1 是当前最新发布版本**；用户的"pi 版本不对"——devbox 上没有任何信号表明需升级或降级 |

**诚实结论**：未找到证据需修改 plan1.0 §8.1 的版本号。如用户有特定来源（私有 registry / fork / nightly）请提供。

## 4. 相关论文与开源项目盘点

### 4.1 学术论文（Agent Fleet / AIOps / Self-healing）

| 主题 | 关键参考 | 对 plan1.1 的启发 |
|---|---|---|
| AIOps survey | Notaro et al. 2024 "A Survey of AIOps in the Era of Large Language Models" | 现有 G-1/G-2/G-3 是 AIOps 经典闭环；plan1.1 引入 LLMaA (LLM-as-Agent) 视角 |
| Self-healing systems | Psaier & Dustdar 2011 "A survey on self-healing systems" | 4 阶段：detect → diagnose → repair → verify —— plan1.1 G-1/G-2/G-3/G-4 对应 |
| Multi-agent systems | Han et al. 2024 "Multi-Agent Collaboration Mechanisms: A Survey of LLMs" | "AI 运维军团" = LLM 多 agent 协同；plan1.1 §5.2 借鉴 Role-play / Debate / Voting 模式 |
| Anomaly detection | Audibert et al. 2020 "USAD: UnSupervised Anomaly Detection" | plan1.1 §5.1 G-1 异常检测借鉴 USAD / Anomaly Transformer |
| Cluster scheduling | Schwarzkopf et al. 2013 "Omega: flexible, scalable schedulers for large compute clusters" | plan1.1 §5.2 fleet 协调借鉴 Omega 的"shared-state schedulers"模式 |
| Federated learning | Kairouz et al. 2021 "Advances and Open Problems in Federated Learning" | plan1.1 §5.4 知识共享借鉴（postmortem 不出 host，schema 同步） |

### 4.2 开源项目（同类系统对照）

| 项目 | 类别 | 借鉴点 | 风险 |
|---|---|---|---|
| **StackStorm** (Apache, Python) | Event-driven automation | action runner + workflow + rule engine | 启动器纯 Python 太重；学其 rule-based 触发器模式 |
| **Ansible** (Red Hat) | Configuration management / agentless | host 批量操作 + idempotent + YAML playbook | 不引入；plan1.1 用 opskeeper 自有 edge + Pi bash 调用 |
| **Puppet** | Configuration management | manifest + catalog apply + report | agent-server 双向连接 vs 我们的 tunnel 单向；不学 |
| **SaltStack** | Remote execution | ZeroMQ + salt-master/minion + grains | 与我们 tunnel 形态不同；学其"state.apply"判定 |
| **Argo Workflows** | K8s-native workflow engine | DAG 编排 + retry policy + artifact | plan1.1 §5.3 incident playbook DAG 借鉴 |
| **Temporal** | Durable execution | workflow + activity + signal + query | plan1.1 §5.3 长跑 RCA 借鉴；不引入二进制 |
| **Kubernetes Operators** | Control-loop pattern | reconcile loop + spec/status + finalizer | plan1.1 §5.4 fleet controller 借鉴 |
| **Apache Airflow** | DAG scheduling | scheduler + DAG + sensor | 杀鸡用牛刀；学其 sensor 模式 |
| **OpenObserve** | Observability (Rust) | 轻量 OTel/LOGS/METRICS | 不替换；plan1.1 §5.1 用 OTel 已建 |

### 4.3 pi.dev/packages 评估（plan1.0 v1.3 §8.1.1 的 opskeeper 重审）

| 包 | 用途 | opskeeper 是否需要 | 评估 |
|---|---|---|---|
| **pi-yaml-hooks** | 拦截 bash/write/agent 事件 | ✅ **必需** | plan1.0 P-12 已 ✅（14 条 deny-list）；可继续扩 |
| **pi-extension-manager** | 插件市场 / 启用 / 禁用 | 🟡 | opskeeper 不需要"市场"；但需要"extension enable/disable"做 fleet rollout |
| **pi-package-manager** | 第三方 package 安装 | 🟡 | 与 P-12 `ExtraPackages` 重叠；选一个 |
| **pi-toolbox** | 通用工具（文件/shell/grep）| ⛔ | Pi 已含 read/write/edit/bash；与 opskeeper 自有 `internal/edgeagent/server` 重叠 |
| **swarm-extension** | 多 agent 协同 | ✅ **关键**（plan1.0 v1.3 误标 ⛔）| plan1.1 §5.2 AI 运维军团核心：需重新评估引入 |
| **agent-teams** | 团队 agent | 🟡 | plan1.1 §5.2 fleet role-based dispatch 借鉴 |
| **MCP 系列** | Model Context Protocol 集成 | 🟡 | plan1.0 未审；plan1.1 §5.4 fleet knowledge broker 候选 |
| **pi-core-capabilities** | 官方核心能力集 | 🟡 | opskeeper 现有 `internal/pkg/llm` 已覆盖；不引入 |
| **pi-runtime-extension** | 自定义 runtime | ⛔ | opskeeper 自有 edge 是同等能力 |
| **pi-debug-tools** | debug / trace | 🟡 | Electron dev tools 类；edge 已有 traceparent；不引入 |
| **pi-yaml-hooks + swarm-extension 组合** | 跨 host 协调 + 拦截 | ✅ **强烈推荐** | 一次性解决 plan1.1 §5.2 军团协同 + §5.1 异常拦截 |

**plan1.0 v1.3 §8.1.1 vs plan1.1 评估的关键差异**：

| 包 | plan1.0 v1.3 评估 | plan1.1 重审 | 理由 |
|---|---|---|---|
| swarm-extension | ⛔ "WorkBuddy 不需要；放弃"（OpenBuddy 上下文） | ✅ **必需** | 那是 OpenBuddy 上下文下做的评估，opskeeper 完全不同 |
| agent-teams | ⛔ 同上 | 🟡 评估 | opskeeper 的"team"是 fleet 内角色分工，不是 user-team |
| pi-extension-manager | ⛔ | 🟡 | opskeeper 不需要"市场"，但 enable/disable 用于 fleet rollout |

## 5. plan1.1 路线图（3 phase × 9 round）

**不取代 plan1.0 的 P-/G- 行；是横向增补**。每轮一个 PR，符合 plan1.0 v1.12 附录 D commit 模板（"本期合计：✅ X / 🟡 Y / ⛔ Z"）。

### 5.1 Phase F — Fleet 维度（3 轮，可与 plan1.0 G-1 并行）

- **F-1**：✅ **已实现并验证** — fleet host read model + 云端 fleet view API（`GET /v1/fleet/hosts?status=&role=&since=&limit=&offset=`）；复用现有 `Device` / `Edge` / `edge_devices`，不引入新依赖；新增 `internal/manager/biz/fleet/` 与 `internal/manager/server/fleet/`，并接入 `cmd/opskeeper/main.go`。认证、筛选、分页、host-edge join、secret 不泄漏均有测试。
- **F-2**：✅ **已实现并验证** — 跨 host incident 聚合（cluster-wide "wave"）read model。`internal/manager/biz/fleet/cluster.go` 把 `alert_incidents` 的 per-host 行折叠成跨 host incident：分组键 = (anomaly class 由 rule key 归一化得出) + (signal dimension，剥离 host 身份标签) + (滑动 first-firing 窗口，默认 10 min / 上限 24 h)，并要求至少 `min_hosts`（默认 2 / 上限 100）台不同主机。窗口外的复发成为独立 wave，不会被并入陈旧分组。纯确定性、无 LLM 依赖：同样的 incident 行永远产出同样的分组与顺序。新增 `GET /v1/fleet/cluster-incidents?status=&severity=&since=&window=&min_hosts=&limit=&offset=`（未接线时 501）。分类器同时覆盖三类真实 rule key：内置 seed 规则（`cpu_high` / `disk_full_warning` / `scrape_down` …，前缀表）、harness/custom `<domain>/<case>`（`host/cpu-spike` / `k8s/pod-oom`，归一化后前缀表）、Alertmanager 告警名（`HostHighCpuLoad` 这类无分隔符 key，子串关键词阶段）。biz 层 13 个 case + HTTP 层 6 个契约 case 全绿；`go vet -mod=mod` 0 错误。
- **F-3**：跨 host RCA —— 当 F-2 把 N 个 host 聚成 1 个 incident，云端 LLM 用 eino ReAct（云端 reviewer worker）+ 抓各 host 的 `dmesg/journal/top/iostat` 出 cluster-wide root cause（仍待后续轮次；F-2 已提供其分组输入）

### 5.2 Phase A — AI 运维军团（3 轮，依赖 P-5 tunnel）

- **A-1**：引入 `swarm-extension`（plan1.0 §8.1.1 误判 ⛔ 重审 ✅）；评估其与 opskeeper 现有 cordis-like EventBus 兼容性；产出 ADR `docs/adr/0001-swarm-extension-integration.md`
- **A-2**：fleet role-based dispatch —— 按 host 角色（db / cache / web / batch / etcd）派给不同 Pi skill，避免"通用 Pi 在 db 主机上跑 web skill"
- **A-3**：postmortem 共享 + 召回 —— 每 host 的 postmortem 写本地 + 上行 hash + schema（pi-skills/opskeeper-knowledge/）；下一个 host 的 Pi 在 RCA 时召回相似 postmortem（用 fastembed-go 算相似度，已部署）

### 5.3 Phase S — 自主化闭环（3 轮，依赖 P-5 tunnel + F-2）

- **S-1**：白名单内修复免 review —— 把 plan1.0 §3.4 现有 "playbook 白名单" 显式建模为 `opskeeper/playbooks/{restart,reload,cleanup}.yaml`；Pi 在 `opskeeper-restart-service` skill 里加"白名单 unit 直接执行 + 仅审计"分支
- **S-2**：白名单外修复自动 compose proposal —— 现有 `internal/edgeagent/server/propose.go` 已实现 propose 入审计；新增 `internal/edgeagent/server/auto_propose.go` 自动组装 proposal payload（受 Pi skill 触发）
- **S-3**：端到端自动闭环验证 —— harness case `internal/harness/cases/pi/auto_self_heal` 跑通：mock incident → Pi RCA → 白名单内直接修 → audit → verify；目标通过率 ≥ 80%

## 6. 与 plan1.0 §六.5「强制约束」的关系

plan1.0 §六.5 列了 7 条强制约束（云端零 Pi 依赖、HTTP loopback、approval token 单次、reviewer 双签、HITL、cmdpolicy 沙箱、traceparent）。plan1.1 全程遵守：

| plan1.1 维度 | plan1.0 §六.5 约束如何保留 |
|---|---|
| F-1 fleet view | 云端 `internal/manager/` 新增 API；不引入 Pi 依赖 |
| F-2 incident dedup | 云端纯确定性 read model（无 LLM、无 Pi 依赖）；Pi 只上行 raw anomaly 数据 |
| F-3 cross-host RCA | 云端 reviewer + investigator worker；Pi 只提供 per-host 事实源 |
| A-1 swarm | Pi sidecar 加 swarm 包；云端不变 |
| A-2 fleet role | edge `internal/edgeagent/biz/pi_config.go` 加 role 字段；云端不变 |
| A-3 postmortem share | schema 上行（hash + 摘要），明文不上行；云端 `internal/manager/` 做聚合 |
| S-1 白名单内免 review | 受 cmdpolicy.Sandbox 现有白名单保护；approval cache TTL 不变 |
| S-2 auto_propose | 仍走 `/v1/edge/propose` 入口；P-5 reviewer grant 链路不变 |
| S-3 harness | 不引入新 framework；用现有 `internal/harness/{cases,judge,runner}` |

## 7. 成功标准（plan1.1 自身）

- ✅ **Phase F-1** 在 devbox 内交付并通过 targeted 单测 / vet（fleet read model + `/v1/fleet/hosts`）。
- ✅ **Phase F-2** 在 devbox 内交付并通过 targeted 单测 / vet（cluster-wide incident 聚合 read model + `/v1/fleet/cluster-incidents`，20 个 case）；F-3 仍待后续轮次。
- ✅ **前置依赖已解锁（plan1.0 v1.16）**：A-2 与 S-* 都要求"能真的驱动目标机上的 Pi"。在 v1.16 之前这条通道是按不存在的 HTTP 模式设计的，因此 A/S 两阶段实际上建立在错误前提上；现在 `pirpc` + `RPCAttach` + edge 接线已对真实 Pi 验证，A-2 / S-1 / S-2 可以按真实 argv 与真实命令集（`prompt` / `bash` / `abort` / `get_last_assistant_text`）落地。
- ⛔ Phase A A-1 ADR 仍未交付，需先完成 swarm-extension spike（且按上游安全警告先读源码）。
- ✅ 每轮 commit 符合 plan1.0 附录 D commit 模板
- ⛔ Phase S 全部待 P-5 tunnel proto + 真机 e2e

## 8. 失败条件 / 已知风险

1. **swarm-extension 与 opskeeper 现有架构兼容性**：plan1.0 v1.3 误判 swarm-extension 不需要；plan1.1 重审前应先做 spike（A-1 ADR 第一阶段）
2. **cross-host RCA 性能**：5 个 fact source × 10 host = 50 个并发 fetch，云端 reviewer worker 需要并发控制（F-2 本身为单次 `alert_incidents` 扫描，上限 2000 行，无并发放大）
3. **postmortem 召回语义**：fastembed-go 算相似度的阈值 / schema 版本兼容性 / postmortem 长度上限需 spec
4. **白名单误用风险**：S-1 显式白名单可能漏判；需配套 review 抽检（云端 reviewer 周期性 audit 白名单执行）
5. **fleet rollout 风险**：新 Pi version 上线时 N host 同时升级可能雪崩；需分批（10% → 50% → 100%）canary 策略（plan1.0 §五.5 G-5 升级管线已含 tag 校验 + rebase，缺分批）

## 9. 文档维护

- plan1.1 不重写 plan1.0；后续每轮 PR 后只在 plan1.0 §附录 D 加一行 + 在 plan1.1 §5 状态表里 update
- §3 "Pi 版本核实" 是 plan1.1 一次性审计；后续每次 pi-coding-agent 上游发布时重审一次

---

**附录 A — 与 plan1.0 的章节对应**

| plan1.1 § | 对应 plan1.0 章节 | 关系 |
|---|---|---|
| §0 | plan1.0 §0 | 关系说明 |
| §1 | plan1.0 §5.5 | 数据来源 |
| §2 | plan1.0 §三 (4 个 goal) | 横向增补（fleet/army/autonomous gap） |
| §3 | plan1.0 §8.1 | 横向 audit（Pi 版本核实） |
| §4 | plan1.0 §7 + §8.1.1 | 横向盘点（论文 / OSS / pi.dev/packages） |
| §5 | plan1.0 §五 | 路线图（9 轮增量） |
| §6 | plan1.0 §六.5 | 强制约束保留 |
| §7 | plan1.0 §附录 D | 成功标准增补 |
| §8 | plan1.0 §八 | 失败条件 |
| §9 | plan1.0 §九 | 文档维护规则 |
