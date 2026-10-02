# OpsKeeper 2.0 整体架构分析

> 目标：把 OpsKeeper 从「一个大 Go 单体 + 编译死的运维 Agent」改造为
> 「以 PiG 为插件内核的多模块 AI 运维平台」。
>
> 本文回答三个问题：**整体架构长什么样**、**为什么这样切**、**现在到哪一步了**。
> 模块边界的规范性说明见 `docs/module-architecture.md`；本文聚焦全景与决策。

---

## 一、一句话架构

> **控制面管「谁可以做什么」，节点面管「在高权限现场能不能做」，
> 插件面管「具体怎么做」——三者用一份 wire 契约连起来，
> 而 PiG 的全部不稳定面被压缩进唯一一个模块。**

---

## 二、四层视图总图

```
┌───────────────────────────────────────────────────────────────────────────┐
│  ① 接入层  Web 控制台（React，不重写，仅加 2 个页面）                        │
│     会话 · 流式输出 · 审批弹窗 · 节点 Agent 页 · 插件市场页                 │
└───────────────────────────────┬───────────────────────────────────────────┘
                                │ HTTP / SSE（帧契约不变，前端零改动）
┌───────────────────────────────▼───────────────────────────────────────────┐
│  ② 控制面  manager                                                       │
│  ┌────────────┬─────────────┬────────────┬────────────┬────────────────┐  │
│  │  iam       │ approval    │ audit      │ topology   │ rag / 知识库    │  │
│  │ 身份租户   │ HITL 审批   │ HMAC 链    │ 拓扑       │ BM25+Qdrant    │  │
│  └────────────┴─────────────┴────────────┴────────────┴────────────────┘  │
│  ┌──────────────────────────────────────────────────────────────────────┐│
│  │ NodeFleet：每个 edge 持一个 AgentProcess 句柄（prompt/steer/abort/…）  ││
│  └──────────────────────────────────────────────────────────────────────┘│
│  ┌──────────────────────────────────────────────────────────────────────┐│
│  │ 导入器 pluginimport：.claude-plugin / openclaw / skills.sh → PiG 包   ││
│  │ 审核流水线：manifest 校验 → 签名 → 灰度（fetch_package/apply_package）││
│  └──────────────────────────────────────────────────────────────────────┘│
└───────────────────────────────┬───────────────────────────────────────────┘
                                │ tunnel（agent.prompt / agent.decide / agent.event …）
┌───────────────────────────────▼───────────────────────────────────────────┐
│  ③ 节点面  edge（每个被管主机一个进程）                                    │
│  ┌──────────────────────────────────────────────────────────────────────┐│
│  │ ① Supervisor：遥测采集（metrics/logs/traces）—— 长驻采集器，非 Agent  ││
│  └──────────────────────────────────────────────────────────────────────┘│
│  ┌───────────────┐  ┌────────────────────┐  ┌─────────────────────────┐  │
│  │ PigSupervisor │  │ policygate 策略闸门 │  │ gatesocket 准入 socket  │  │
│  │ 崩溃重启/退避  │  │ 白名单+审批+HITL    │  │ 信使：插件唯一的裁决入口  │  │
│  └───────┬───────┘  └─────────┬──────────┘  └────────────▲────────────┘  │
│          │ stdio JSONL        │ unix socket             │ 询问放行       │
│  ┌───────▼───────────────────▼──────────┐  ┌──────────────┴─────────────┐  │
│  │ pig --mode rpc（节点 AI Agent 进程）   │  │ toolbroker 执行闸门        │  │
│  │  ① 信使 extension（隔离子进程）        │  │  · 复检同一份白名单        │  │
│  │  ② 工具集 extension（零实现，纯路由）  │─►│  · 本地 skill / 反向调用    │  │
│  │  ③ 7 persona Skills                   │  └──────────────┬─────────────┘  │
│  └──────────────────────────────────────┘                 │ tunnel         │
└────────────────────────────────────────────────────────────┼────────────────┘
└───────────────────────────────────────────────────────────────────────────┘
┌───────────────────────────────────────────────────────────────────────────┐
│  ④ 插件面  PiG Package（8 类资源：extensions/skills/agents/prompts/        │
│     themes/mcp/hooks/agent-environments）+ pig-ops.yaml 治理元数据         │
└───────────────────────────────────────────────────────────────────────────┘
```

---

## 三、模块依赖图（编译期强约束）

```
                    ┌──────────────┐
                    │     core     │  领域模型 / 端口接口 / wire DTO
                    │  零基础设施   │  只依赖 stdlib
                    └──────┬───────┘
              ┌────────────┼────────────┬────────────┐
              │            │            │            │
        ┌─────▼────┐ ┌─────▼────┐ ┌─────▼────┐ ┌────▼─────┐
        │   pig    │ │ manager  │ │   edge   │ │ harness  │
        │ PiG 适配 │ │  控制面   │ │  节点面   │ │  评测    │
        └─────┬────┘ └─────┬────┘ └─────┬────┘ └────┬─────┘
              │            │            │            │
              └────────────┴─────┬──────┴────────────┘
                                 │
                    ┌────────────▼───────────┐
                    │         sdk            │  第三方插件唯一依赖面
                    │  清单 schema + 准入校验   │  → 只依赖 core
                    └────────────────────────┘

        ┌──────────────────────────────────────────────┐
        │  floor  两个平面共用的基础设施（第 7 个模块）    │
        │  config · log · pluginmanifest · prom ·       │
        │  tunnel · skill（宿主 skill registry）        │
        │  → 只依赖 core 与 sdk，不认任何 BC             │
        └──────────────────────────────────────────────┘
             manager ──► floor ◄── edge      （横向共用，不是上下级）

        唯一允许 import github.com/MichaelKinsy/PiG 的地方：core/pig
```

已拆出的模块（`go.mod` 实存）：`core`、`pig`、`edge`、`floor`、`manager`、
`harness`、`sdk`。七个模块都在了，`internal/` 已经不存在——控制面的最后一层
（`iam` + `manager/{biz,data,model,server,service}`）在决策 63 里搬进了
`core/manager`。根模块退化成装配层：`cmd/`、`scripts/`、`tests/`，以及 web
控制台那几个 Go 工具包。

**为什么 `pig` 必须是单点？** PiG 是 0.x 预稳定版本，API 与依赖图都会动
（当前还会拖进 AWS SDK、Google API client、十余个 provider 库）。若插件能
直接 import PiG，则上游每次升级都会打断全部插件，生态在第一次接触上游churn
时就死掉。所以规则被固化为：**PiG 全仓库只允许出现在 `core/pig`**，
`go run ./scripts/modulecheck .` 在 CI 强制（当前结果：边界全部成立）。

---

## 四、三条设计主线（架构的全部争议点都在这里）

### 4.1 事件翻译发生在「节点」，不是「控制面」

```
pig RPC 原始事件
      │
      ▼  core/pig/pigwire.Translator（在 pig 进程旁）
wire.StreamEvent（assistant_delta / tool_start / approval_pending …）
      │
      ▼  edge 侧 AgentBridge（core/wire 是共享契约）
AgentEventFrame ──tunnel──► manager ──SSE──► 控制台
```

**为什么必须在节点翻译**：控制台依赖的 SSE 帧是**已冻结的对外契约**（前端 52K 行
React 不重写）。如果让每个节点各自上报 PiG 原始事件，那么任何 PiG 事件结构变动
都会变成一次前端发版。把翻译下沉到 `pigwire` 后，PiG 的形状变化被限制在
`core/pig` 内部，SSE 契约对上游抖动免疫。

### 4.2 审批裁决权在宿主，插件永远拿不到放行权

```
插件（第三方代码）
   │  ① PiG extension 收到 tool_call 事件
   │     只能提问：{"block": true, "reason": "..."} ← 它没有别的动词
   ▼
gatesocket（unix socket，0700 目录 / 0600 socket）
   │  ② 宿主按 ActorResolver 从 session 反查真实角色
   │     （绝不信任请求里自带的 actor 字段）
   ▼
policygate
   │  ③ 查清单声明的工具等级 + 角色天花板 + 风险半径
   │     · 清单低报（实际更危险）→ 直接拒绝，视为安装期撒谎
   │     · 决策必须带 digest 匹配，摘要不符则拒收且不消耗请求
   ▼
   ├─ 只读且低风险 ──► 放行
   └─ 写/破坏性 ──► 挂起 ──► approval_pending ──► 管理面 HITL
                                            │
   管理面回 decision（tunnel agent.decide）──┘
   ④ 放行或拒绝，审计在宿主写 HMAC 链
```

**插件的权限是「只能请求，不能放行」**。三条宿主强制底线：
凭据按 `required_scopes` 注入（拿不到未声明的 key）、工具白名单由宿主维护
（未声明的工具每轮都被拒）、审计链插件无写权限（只能声明 `audit.emits`）。

### 4.3 宿主执行，插件路由：agent 进程里没有一行运维代码

```
模型调用 host_dmesg
      │
      ▼  ① PiG extension（工具集，18 个声明，零实现）
   跨 unix socket 发 {tool, arguments}
      │
      ▼  ② toolbroker（宿主代码，agent 够不到）
   复检同一份白名单 → skill.Get → 执行
                       └─ 未命中 → tunnel agent.tool 反向调用控制面
```

**为什么插件不自己实现工具**：`core/floor/skill/builtin` 里 13 个 `host_*`
实现早已存在，带权限等级、溢出处理和测试。在高权限 agent 进程里重写一遍，
是**把已解决的问题变成新的风险**。而另外 5 个工具（`get_topology`、
`query_alert_rules` 等）读的是控制面的图和规则表——agent 子进程根本没有
通往它们的合法路径，自己造一条就是**第二条未经审计的控制面入口**。

**为什么 broker 要再查一次白名单**：闸门已经放行过了，但闸门是**经由
agent 进程内的 extension** 触达的——一个换掉信使的插件就能让那次检查
消失。broker 是宿主代码，只按名字可达，查的是同一份 registry。**一个工具
必须能扛过一次 agent 有能力压制的检查。**

**清单低报在这里被抓住**：broker 拿得到本地 skill 的**真实**权限等级，
于是 `underdeclared` 规则在真实执行器面前生效——一个把
`host_restart_service` 声明成 `read` 的包，会在模型调用它的**那一刻**、
由宿主、在手握执行器的情况下被拒，而不是靠人读 YAML 复核。

### 4.4 Agent 独立进程，崩溃不拖垮节点

```
edge 主进程
  ├── 遥测 Supervisor      崩溃影响面：节点自身健康
  ├── policygate           崩溃影响面：所有工具调用被拒（fail-closed，安全）
  └── pigsupervisor
        └── pig --mode rpc  崩溃影响面：仅 AI 对话
             · 指数退避重启
             · CrashWindow 内超过 MaxCrashAttempts → 停止重启、报 Degraded、
               但进程保持存活并继续应答 health（运维能看到，而不是静默消失）
             · 手工 Restart 不重置崩溃预算（防止「点一下就好」的假象）
```

**为什么不让 Agent 在 edge 进程内跑**：pig 及其插件是高权限、高频变动代码。
独立进程让插件崩溃、内存泄漏、`bash` 误执行都止步于子进程；
pig 与插件还能独立于 opskeeper 发版热更（复用现有 `fetch_package`/`apply_package` 通道）。

---

## 五、插件契约：为什么「插件即 PiG Package」

不新造格式。PiG 是 Pi 的 Go 移植，**Pi 的 TypeScript 扩展原样运行**，
所以插件生态直接复用 Pi 全生态。`.claude-plugin/plugin.json` 与
`openclaw.plugin.json` 在 2.0 中降级为**导入器**（存量生态平滑接入），
由 `pluginimport` 转换为 PiG package，转换后用 `pluginmanifest.Load` 验证
「转换结果确实可加载」——不引入第二个可能与运行时分歧的解析器。

```
opskeeper-sre-readonly/                 # 插件根
├── extensions/opskeeper-gate/          # 准入信使（Go，独立 go.mod，崩溃隔离）
├── skills/opskeeper-{investigator,alerter,repairer,
│            verifier,reviewer,critic,postmortem}/   # 7 个 Worker persona
├── skills/diagnose-readonly/           # 只读诊断 SOP
└── pig-ops.yaml                        # 治理元数据
```

`pig-ops.yaml` 只补 Pi 清单**没有语义**的治理字段，Pi 兼容性保持：

| 字段 | 作用 |
|---|---|
| `targets: [edge\|manager]` | 部署位置 |
| `safety_level: L0–L3` | 决定审批强度 |
| `capabilities: [read,write,destructive]` | 声明上限（工具等级不得超过它，否则加载即报错） |
| `tools: [{name, class}]` | **工具清单 = 宿主白名单 = 评审面**；未列出的工具每轮被拒 |
| `required_scopes` | 凭据按此注入，插件拿不到未声明的 key |
| `audit: {emits, mutates}` | 仅声明，宿主强制执行 |
| `approval: {required, blastRadius}` | 插件只能请求，不能放行 |
| `install: {strategy, minEdgeVersion}` | 滚动 or 固定 |

`spec.tools` 是本次改造新增的关键字段：它把「插件能做什么」从隐式
（代码里注册了什么就是什么）变成显式清单，使「能力声明与实际工具集一致」
成为可自动校验的验收项。

---

## 六、当前实现进度

基线：`go build ./...`、`go vet ./...` 通过。测试**必须按模块分别跑**——
`go test ./...` 在仓库根只覆盖根模块，`core/harness` 拆出去之后黄金事故
语料就不再被根测试触达（决策 37 记录了这个静默漏洞与它的补法）：

### 进度百分比（对照计划 §四 A–E 的验收闸门）

百分比是**估计，不是测量**：判据是每一阶段计划里写下的验收闸门（§五），
不是代码行数。它适合回答"还差多少"，不适合回答"值多少"——A 阶段 95% 与
E 阶段 85% 里剩下的东西，不是一个量级的工作。

| 阶段 | 权重 | 完成度 | 判据与剩余 |
|---|---|---|---|
| A 模块化地基 | 20% | **95%** | 13 个模块落地、`internal/` 清空、`modulecheck` + `go-arch-lint` 两个闸门可执行且非空转。剩下 5% 是两条已记账的**债务**（底座的包级 setter 反向边、arch-lint 债务清单缺守卫），不是缺失的功能 |
| B PiG 适配层 | 20% | **92%** | `pigmodel` / `pigagent` / `pigrpc` / `pigwire` 四件套齐、eino 与 go-openai 清零、`llm.Client` 换实现并接线、内核接缝（决策 32/33）打开。剩下：PiG 仍是 `replace` 到本地 checkout，发布要换成固定 tag；契约测试只锁住实际用到的 API 面 |
| C 节点 Agent | 20% | **90%** | `pig --mode rpc` 运维 profile + supervisor + `policygate` + 7 个 `agent.*` 隧道方法 + `NodeFleet` + 只读 piglet，三个剧本在新拓扑下通过。剩下：MCP 运行时（PiG 的 `mcp` 只是声明）、连接池上限与风暴抑制的规模验证 |
| D 插件生态 | 25% | **90%** | B1/B2/B3 全部闭环（18 + 12 + 53 + 5 个工具）、审核流水线（签名 → 清单 → 准入 → 灰度 → 回滚）、运输通道 6 条路由、`sdk` 三个发布物。剩下：能力声明从"家族"升级到"逐方法"、更多插件迁移、容器格式导入器的覆盖面 |
| E 生态治理 | 15% | **85%** | 兼容矩阵（edge 轴 × PiG 轴）、金融 / SaaS 两个 profile 模板、插件 × golden case 覆盖报告。剩下：插件市场前端页面、版本矩阵可视化、发布流程自动化 |

加权合计 ≈ **90.7%**（20×0.95 + 20×0.92 + 20×0.90 + 25×0.90 + 15×0.85）。

**这个数最容易被误读的地方**：C 与 D 的 90% 里，"能跑通"和"能上生产"之间
差的是规模验证与运维面（插件市场 UI、连接规模、发布自动化），不是核心链路。
核心链路——告警 → 诊断 → 审批 → 修复 → 验收——是通的，也是本仓库测试盯得
最紧的一段。

| 模块 | 结果 |
|---|---|
| 根模块 `go test ./... -count=1` | **18 包 ok / 0 failed**——`internal/` 已清空（决策 63），根模块只剩 `cmd/`、`scripts/`、`tests/` 与 web 的 Go 工具包，全部是装配层与测试 |
| `core` | 全部 ok（2 包） |
| `core/pig` | 全部 ok（5 包） |
| `core/edge` | 全部 ok（26 包）——`internal/edgeagent` 的 62 个文件整体迁入（决策 61），原有的 pigsupervisor/policygate/gatesocket/toolbroker/agentprofile 与它同模块 |
| `core/floor` | 全部 ok（9 包，6 个有测试）——`spill_helper` 的 3 个用例在这里被修好（见决策 60） |
| `core/harness` | 全部 ok（13 包） |
| `sdk` | 全部 ok（1 包） |
| `core/manager` | **全部 ok（222 包，`-race` 亦 ok）**——控制面基础设施（决策 62）之后，`biz`/`data`/`model`/`server`/`service` 与 `iam` 也在这一轮迁入（决策 63）。1100 个 Go 文件，其中 413 个测试文件 |
| 5 个 extension 模块 | 各 1 包，全部 ok |

13 个目录（根模块 + 7 个已拆模块 + 5 个 extension）串行跑完：**302 包 ok / 0 failed**。四次搬迁（决策 60/61/62/63）前后总数一次没变，说明搬的是位置，不是测试；变的只是包落在哪个模块里——根模块 113 → 18，`core/manager` 50 → 222。`core/floor/skill/builtin` 的三个
`TestTruncateOrSpill_*` 曾长期在 `/var/tmp` 存在但不可写的机器上失败——降级判据写在
`MkdirAll` 而不是写入上，所以那条降级路径从未生效；决策 60 顺手修好了它（测试一直是
对的，代码不是）。
`cmd/opskeeper-eval` 的两个闸门现在都是绿的：`plugin-coverage` **20/20**
（包 → case 的家族级覆盖），`vocabulary` `--fail-on-gap` 在真语料上通过、在
一个合成的不可满足语料上必须失败（新增的正反两面，见决策 59）。
`go run ./scripts/modulecheck .` → 边界全部成立（模块规则见决策 37，BC 规则见决策 38）。
`make module-test` 一次跑完全部模块。
`make module-race`（`core` / `core/pig` / `core/edge` / `core/floor` / `core/harness` 各自
`go test ./... -count=1 -race`）→ 全部 ok，`pigsupervisor` 的重启循环这条
并发热点没有数据竞争。sdk 无并发测试。

| 阶段 | 内容 | 状态 |
|---|---|---|
| **A 模块化地基** | `go.work` + 7 个模块 `go.mod`（`core` / `pig` / `edge` / **`floor`**（决策 60）/ **`manager`**（决策 62+63）/ `harness` / `sdk`）+ 5 个 extension 模块；`core`（domain/ports/wire）；`sdk` 清单准入 | ✅ **已完成**——`internal/` 已清空（决策 63），模块依赖方向由 `modulecheck`（可执行）+ `.go-arch-lint.yml`（文档）双重钉住；遗留两条债务（底座包级 setter、arch-lint 债务清单无守卫）见「当前真实缺口」 |
| **B PiG 适配层** | `pigmodel`（settings→`*ai.Model`）、`pigagent`（含 `buildPrompt` 历史回放，见决策 25）、`pigrpc`（`pig --mode rpc` 客户端）、`pigwire`（SSE 帧翻译）；**`go-openai` 已整包移除**（`core/manager/pkg/llm` 自持 HTTP wire，见决策 22）；**工具治理已抽成与内核无关的装饰器**（见决策 24）；**PiG 支撑的 `llm.Client` 已落地并接入装配层**（`core/manager/pkg/llm/pigclient.go` + `pigsettings.go` + `pigregistry.go`，`OPSKEEPER_LLM_BACKEND=pig` 切换，见决策 26）；**内核侧宿主绑定已落地**（`core/manager/biz/aiops/agentkernel/`：`ToolBag` + `Persister`（同时是 `ToolCallRecorder`）；`core/manager/biz/aiops/chatruntime/kernelsink.go`：`ports.EventSink` → 控制面事件，含准入/结算两帧的 join，见决策 27/28；审计/预算/审批/依赖装配四件套落在 `agentkernel`，见决策 30；历史回放改为一计划两渲染，见决策 31；**换内核接缝已开**：`Runtime.Handle` 第 5d 步分流 + `kernelpath.go` 驱动 `ports.Agent`，见决策 32） | ⚠️ 部分——模型接口与**编排接缝**都已就位，**装配层已接线**（`OPSKEEPER_AGENT_KERNEL=pig`，见决策 33）；**eino 已彻底移除**：`go.mod`/`go.sum` 中 `cloudwego/eino` 与 `eino-contrib/jsonschema` 双双消失，`chatruntime` 只剩内核一条路（见决策 34） | ✅ 已落地 |
| **C 节点 Agent** | `pigsupervisor`（崩溃重启/退避/Degraded）、`policygate`（白名单+审批+digest）、`gatesocket`（unix socket 准入）、准入信使 extension、tunnel 7 个 `agent.*` 方法 + `agent.decide`、控制面 `NodeFleet` + `Service.Decide` + HTTP 决策端点、per-session 角色表、**profile piglet**（`tools: []` 摘除 PiG 8 个内置工具含 `bash`，真实二进制 A/B 验证 0/8 active，见决策 48）、**内置具名 piglet**（`plugins/pig-ops/opskeeper-sre-readonly/pig-opskeeper-ops.yaml`：18 只读工具 + 8 skill + 信使，见决策 56） | ✅ 已落地——节点侧生成 profile 与内置具名 piglet 并存（决策 56） |
| **D 插件生态** | L1 只读 profile（18 工具 + 7 persona + 信使）、`pluginimport` 导入器（`/v1/marketplace/import` 入口，见决策 55）、**B1 只读工具集**（工具集 extension + `toolbroker` + `agent.tool` 反向调用 + 双向漂移测试）、**B2 可观测工具集**（12 只读工具，schema 由控制面 registry 生成，全量 upcall）、**B2 中间件工具集**（`opskeeper-sre-middleware`：53 个只读工具，由 `core/manager/middleware/toolset` 从适配器活注册生成，`plugin-coverage` 因此从 2/20 到 **20/20**，见决策 59）、**B3 修复包**（L2/5 工具/`approval.required`/`pod` 半径/pin 安装 + 审批回执 + 写操作全部走控制面）、**审核流水线**（ed25519 树签名 + 信任库 + 签名→清单→准入三段审核 + 灰度波次闸门 + 节点侧 `admitPackages` 接线）、**发布运输通道**（`plugin.install` / `plugin.remove` / `plugin.list` + 节点 `pluginStore` + 控制面 `ReleaseManager` + 6 条 `/v1/plugins/releases` 路由）、**控制面适配器真实化**（pg/redis/k8s/mq/host 五条，见「闭环修复派发链路」）、**git 适配器真实化**（8 工具全实现，只读，见决策 45）、**`sdk` 发布面**（清单类型 + 注册 API + 版本协商，见决策 46） | ✅ B1/B2/B3/审核流水线/运输通道全部完成；`git` 适配器 8/8 工具真实；`sdk` 三个发布物齐全；**四个只读包**（readonly / observability / middleware / 修复包的只读半边）在 `plugins/pig-ops` 下齐备 |
| **E 生态治理** | 兼容矩阵（edge 轴 + **PiG 轴**）、跨云 profile 模板（金融/SaaS）、插件能力 × golden case 覆盖报告 | ✅ 已落地 |

### 闭环修复派发链路

approved phase 拿到一个 `RemediationOption` 之后，把它派发到工具上的链路是
本仓库**唯一**一条"AI 说要修 → 真的动手"的路径，因此它的每一步都被显式钉住：

```
RootCauseJSON.remediation_options[]           （investigator 写进合同）
        │  RootCauseRemediationLoader
        ▼
RemediationOption{Action, Target, Risk}       （选择：SelectRemediation）
        │  RegistryInvoker.Invoke
        ▼
LookupTool(Action)  ──未注册──► 失败并说明"这个名字没有实现"，不降级、不跳过
        │  resolveArgs
        │    ├ EvidenceArgResolver：EvidenceChain ──► pid / role
        │    │    （候选多于一个 ──► 拒绝并列出候选，不猜）
        │    ├ target / approved_by / reason
        ▼
RequiredArgs 缺值 ──► ErrUnresolvableArguments，报出缺哪个参数
        │  CallTool
        ▼
ToolReplay{Args, Result}                      （复盘里记的是"实际发了什么"）
```

六个中间件适配器各自独立按环境变量装配，装到**同一个 registry** 上——
一个动作可不可派发，取决于这个部署连了哪些中间件，而不是取决于二进制是为谁
编译的。没配 DSN 的适配器只是不出现，配了但连不上的会被记录并从装配里跳过
（不为一个修复便利把控制面拖下水）。

| 环境变量 | 适配器 | 命名空间 |
|---|---|---|
| `OPSKEEPER_LOOP_PG_DSN` | postgres（22 工具） | `pg.` |
| `OPSKEEPER_LOOP_REDIS_DSN` | redis（17 工具） | `redis.` |
| `OPSKEEPER_LOOP_K8S_DSN` | k8s（19 工具，自建 REST 客户端） | `k8s.` |
| `OPSKEEPER_LOOP_MQ_DSN` | mq（6 工具，RabbitMQ management API / Kafka offset reset） | `mq.` |
| `OPSKEEPER_LOOP_HOST_DSN` | host（7 工具，`local://` 或 `ssh://`） | `host.` |
| `OPSKEEPER_LOOP_GIT_DSN` | git（8 工具，只读；本地路径或 remote URL） | `git.` |

参数缺口在闸门里被**再分成两类**：`resolvable`（有提取器，值在证据里）与
`no evidence records this value`（值根本没被采集过）。两者都是拒绝，但需要的
工作完全不同——给后者写解析器，是给一个从没被观测到的 Pod 名写查找表。
**决策 49 已把这一类清零**：Pod 名与队列名不是"没被观测到"，而是 firing 告警
的 `labels_json` 里一直写着、只是没有进入证据链。9/9 动作现为 `resolvable`。

装配代码 `cmd/opskeeper/loop_adapters.go`，测试 `loop_adapters_test.go`
（无 DSN 空注册表 / 坏 DSN 被跳过不影响其它适配器 / `local://` 真的注册了
`host.garbage_collect` 与 `host.restart_service` / 环境变量名是部署契约）。
`git` 是这条规则的例外：它没有对应的闭环动作（`git.*` 不在
`investigatorreal.RemediationActions` 里），所以接不接它**不改变任何闭环数字**；
接它的理由是"拿证据去仓库核对"这类根因查询需要它，而一个只在别的进程里
碰巧注册了的工具等于不存在。

### 验收闸门（计划 §五）现状

| 阶段 | 闸门 | 状态 |
|---|---|---|
| A | 7 模块 `go build` + 全量 `go test -count=1` 绿；arch-lint 拦住逆向依赖 | ✅ **13 个模块目录 `go build` 全过、全量 `go test` 302 包 ok / 0 failed**；`internal/` 已清空（决策 63）；逆向依赖由 `modulecheck`（可执行，含"只允许测试的跨模块边"一条）与 `go-arch-lint`（0 warnings）双重拦住（决策 37/38/60/62/63） |
| B | **SSE 帧 golden 逐帧一致** | ✅ 两条路径各有一份 golden，且互相逐字节相等（见下） |
| B | **eino / go-openai 依赖清零** | ✅ `rg eino go.mod` 无命中；`core/manager/biz/aiops/graph/`（14 文件）与 `core/manager/pkg/llm/eino_*.go`、`budget_callback.go` 全部删除；`chatruntime` 测试全量迁到 `scriptedKernel`；`OPSKEEPER_AGENT_KERNEL=pig` 与退役拼写 `graph` 解析到同一内核（见决策 34） |
| B | **7 provider 冒烟** | ✅ `pigmodel/smoke_test.go`：7 个 provider 各起一个 httptest SSE 源，真实走 `Provider.Stream` |
| B | `go test -race` 无泄漏 | ✅ `core/pig/...`（198）、`service/plugin` + `core/edge/biz`（60）、`cmd/opskeeper-edge`（118）、`core/manager/pkg/llm`（107）全部 `-race` 通过 |
| B | **`llm.Client` 换实现（PiG 支撑）** | ✅ `pigclient_test.go` 用 PiG 真实 provider 栈跑 httptest：选择/注册表/转写/请求体/流式/回传全链路，含 tool-call 往返与预算「先扣后发」；`router.go` 的子客户端工厂让**每个** provider 都走 PiG（见决策 26） |
| C | 端到端剧本 `alert_storm` / `rca_loop` / `recovery_verify` 在新拓扑下通过 | ✅ `core/manager/biz/nodefleet/e2e/` 6 个用例接在真 `NodeFleet` + 真 `AgentBridge` + 真 `policygate` + 真 `pigwire` 上跑（见决策 36） |
| D | **插件安装→审核→灰度→回滚全链路** | ✅ `cmd/opskeeper-edge/pluginchain_test.go`：真包+真签名+真节点审核+真 `service/plugin.Manager`，只换掉 socket |
| D | 越权调用被宿主闸门 Block；审计链完整 | ✅ 闸门已有回归；**审计链已落地**：`audit_logs` 增 `seq`/`prev_hash`/`hash`，`ChainStamper` 做 HMAC-SHA256 规范化摘要，`audit_chain_head` 单行 CAS 在同一事务内推进链头与插入，`VerifyChain` 全链走查 + `ErrChainBroken` 定位首个断点，保留策略只能裁前缀（见决策 35） |
| E | 插件安装→审核→灰度→回滚全链路；版本矩阵兼容性检查 | ✅ 同 D 行；版本矩阵已双向锁 |

### 已落地的关键决策（不可回退）

1. `core/pig` 是唯一 PiG 导入者；节点 supervisor 经 `ports.AgentProcess` 触达 RPC 客户端。
2. 事件翻译在节点侧完成，SSE 帧契约不变。
3. 崩溃循环保护：`CrashWindow` 内超预算则停止重启、报 `Degraded`、保持存活应答 health；手工 Restart 不重置预算。
4. 节点拒绝 ≠ 传输失败：`RemoteError.Retryable() == false`。
5. `ApprovalFrame.Decision` 是封闭的 `grant`/`deny` 对，原因走 `Note`。
6. 清单低报（实际等级高于声明）→ 直接拒绝，视为安装期撒谎；等级只许高不许低地提升。
7. gate 决策必须 digest 匹配；摘要不符则拒收**且不消耗请求**（修正后的决策仍可送达）。
8. `FrameSink` 签名带 `sessionID`——否则控制面会丢帧。
9. **宿主执行、插件路由**：agent 进程内零运维实现。`host_*` 复用
   `core/floor/skill/builtin` 已有实现；`get_topology` / `query_alert_rules`
   等控制面工具经 `agent.tool` 反向调用，不在节点上另造路径。
10. **闸门与 broker 各查一次白名单**，读同一份 registry。闸门经由 agent
    进程内的 extension 触达，可被替换压制；broker 是宿主代码，按名字可达。
11. **broker 用本地 skill 的真实等级复检**，因此 `underdeclared` 在真实
    执行器面前生效——清单低报在模型调用那一刻被宿主当场拒绝。
12. **发送失败可重试，读取失败不重试**：请求没发出去重发是安全的；结果未知
    时重发是对写工具的二次执行，因此报「结果未知」而非重试。
13. **一次审批 = 一次执行**：`Admit` 仅在人工放行时铸造一次性回执，broker
    除白名单外还必须核验回执，回执按 `session + 工具名 + 参数摘要` 消费。
    摘要只覆盖工具与参数，所以 session 必须进 key——两个会话可以产生同一摘要。
    这堵住的是「用快递扩展替换信使」这一条真实路径。
14. **按工具等级路由，而非按是否注册**：`host_restart_service` 在节点 skill
    注册表里**确实存在**（否则目录画不出、宿主也分类不出），但它的 `Execute`
    是刻意锁死的——审批在管理器 BaseTool 的 reviewer 后面。broker 若按
    「注册了就在本地跑」解析，会找到一个被锁死的执行器。因此 `Invoke` 的本地
    分支收窄到 read：变更类一律上 `agent.tool` 走控制面。未知等级上送而非本地
    执行——上送只是多一个往返且有答案，本地执行则无人过问。
15. **审核顺序即安全属性**：`签名 → 清单 → 准入`。每一步都要读东西，顺序变了
    就是在读不同的东西。先准入再验签，未签名包已被「自证」通过；先读清单再
    验签，一个谎报能力的包已经被当作事实解析。因此先认证字节、再看字节里的
    任何内容。两条测试专门锁这个顺序，改动顺序即失败。
16. **签名覆盖整棵树，不只是清单**：`pig-ops.sig` 是对包目录的哈希签名
    （每个文件的路径、可执行位与内容）。清单与它治理的代码在事后无法被拆开；
    事后加一个文件与改一个文件一样会被发现。签名的宿主 key 配置在节点上，
    绝不随信封下发——携带自己公钥的信封等于自己验自己。
17. **不随仓库下发发布私钥**：那会让每个节点都信任持有仓库的人。签名发生在
    运维方自己的发布流水线里。因此「未配置信任库的节点跑未签名包」是一个
    过渡默认值，且它每次启动都会大声告警——开关就是信任库本身的存在：
    配了它，所有包必须验签，且没有任何配置项能把它关回去。**已配置但读不出来
    的信任库是硬错误**：会因为一个拼写错误而 fail open 的安全控制不是控制。
18. **灰度波次由「人人有回音」而非「时间到了」推进**：`Advance` 在当前波
    还有节点未应答时拒绝前进。失败的节点算已应答——否则一台因无关原因离线的
   机器会卡住整个集群——并由 `Failed()` 让运维看到它为何提前推进。金丝雀由
   包名+版本哈希选出：同一版本重试命中同一批节点（而不是随机换一批），不同
   版本命中不同节点（否则每次都在重测刚被证明的机器）。

19. **节点的「当前版本」是一个显式选择，不是「最大的那个」**：节点升级时
    故意保留旧版本的目录（回滚要有东西可回），因此升级后磁盘上同时存在两个
    版本。发布规则是**每个包名只发布一个**——否则 agent 会同时加载退役版本的
    工具，`plugin.list` 会报告一个不可能是真的节点。默认选最大版本（安装之后
    正确），但 `Restore` 会把选择改成被恢复的那个版本：否则「最大版本」会在
    恢复的下一秒把坏版本再装回去。默认与回滚对这件事的答案本来就应该不同。
20. **回滚是 `Restore`，不是 `Install`**：`plugin.restore` 是一个独立方法，
    请求体里**没有 URL、没有摘要、没有签名**。理由是管理器在决定回滚时手上
    只有「正在被摘掉的那个版本」的 spec，它**没有**旧版本的坐标。用 install
    去回滚意味着管理器必须永久记住每个版本的位置，且回滚依赖发布服务器还在
    提供可能已被回收的产物。`Restore` 只操作节点上已有的字节，并要求它们
    已经过同样的审核——恢复不是审核豁免。节点上找不到该版本即**拒绝**，
    绝不下载：会下载的恢复就是改名的安装。
21. ~~**审计链目前是行式的**~~ ✅ 已落地（决策 35）：`audit_logs` 现在带
    `seq`/`prev_hash`/`hash`，写入走 HMAC 链，`VerifyChain` 真会走查。
22. **LLM SDK 已从请求路径上移除，只剩 eino 的 agent 编排**：`core/manager/pkg/llm`
    原先依赖 `github.com/sashabaranov/go-openai`，该库在 `CreateChatCompletion`
    内会**在发请求前**做模型校验（`MaxTokens` 在推理模型上被拒、schema 校验、
    已知推理模型带采样参数被本地拒）。本地拒绝对调用点与 provider 400 无法区分，
    而「推理模型重试」这条自愈路径正是靠**解读一次真实拒绝**来学习的——一旦
    本地提前拒掉，那次重试就被消耗在没有到达 provider 的请求上，模型永远学不到，
    之后每一次调用都会以同样方式失败。因此 `wire.go` 自持 POST 路径，客户端
    看到的每个错误都来自真实响应或真实传输失败。`wireHTTPError.Error()` 刻意
    复刻原 SDK 的 `error, status code: N, status: S, message: M, body: B` 文本，
    因为 `isSamplingParamError` 按文本匹配 400——换掉格式等于静默关掉重试。
23. **eino 的移除面是「模型接口」，不是「调用点」**：`llm.Client` 是单方法接口
    （`Chat(ctx, ChatReq) (*ChatResp, error)`），所以换实现的成本集中在
    `cmd/opskeeper/main.go` 构造 `map[string]einomodel.ChatModel` 的那一处，
    以及 `graph.BuildReActGraph` / `chatruntime.Runtime` 这两处需要重新表达在
    `pigagent.Kernel` 上的编排。**根模块可以 import `core/pig/*`，`core/pig`
    不能 import 根模块**——因此一个 PiG 支撑的 `llm.Client` 必须落在根模块
    （如 `core/manager/pkg/llm/pigclient.go`），落在 `core/pig` 就是反向依赖。
24. **工具治理是装饰器，不是内核的一环**：`identical-call memo`、`per-tool 上限`、
    `draft_config_change 的 metric-catalog 前置校验` 原先长在 eino 的
    `graph/tool_adapter.go` 里（`einoToolAdapter.memo`）。这三条规则不是 eino 的
    业务，而是「一个工具包在一次 run 内如何表现」的陈述，必须**在任何内核下
    同真**——把它们留在适配器里，等于内核一换就静默丢掉，而它们防的正是内核
    切换最容易放大的那种失控（ReAct 循环反复重跑同一条昂贵 PromQL 直到迭代预算
    耗尽）。现在它们是 `tools/decorators/governance.go` 的 `Governance`，跑在
    `basetool.BaseTool` 上；`chatruntime` 在**所有过滤之后**为每个 run 建一个
    `NewGovernance()` 包住最终工具包（worker 各建一个）。两个必须记住的点：
    一是 memo 属于 run，**不能**提到进程级——boot 期建的包是全请求复用的，
    把 run 的治理挂到包上会让一个会话读到的结果从缓存里发给下一个会话；
    二是同名工具**只许被包一次**，`chatruntime` 包过之后图里再包一次会让执行
    计数翻倍，工具会在自己一半的额度上被切断。两条各有一个测试钉住。
25. **对话历史在请求上，不在内核里**：`ports.AgentRequest.History` 由宿主装配，
    内核只消费。理由是**转写是策略不是管道**：宿主已经应用了历史窗口上限、清掉了
    被取代的工具批、删掉了 viewer 不该看的内容；一个自己去读会话的内核会回答
    另一个问题。`pigagent.buildPrompt` 是这条契约的唯一翻译点，它的取舍是
    **宁丢不拒**：历史是运维改不动的持久化数据，一条本构建表达不出来的记录
    （新版本加的角色、丢了 id 的工具结果、参数不是合法 JSON 的助手轮）如果让
    整个 turn 硬失败，这个会话就**永久不可用且无法恢复**；丢掉它只损失一点
    上下文。三个变异测试分别钉住「历史被忽略」「坏参数被放行」「新消息被排到
    历史之前」。

26. **`OPSKEEPER_LLM_BACKEND=pig` 是一台可回退的开关，不是静默替换**：装配层现在
    同时持有两条 LLM 实现——自持 HTTP wire（默认）与 PiG provider 栈。切换点选在
    `llm.MultiClient` 的**子客户端工厂**上（`SetSubClientFactory`），因为 router 本来
    就只认识 `Client` 接口，换工厂就换掉了**每一个** provider，而不是只换 fallback
    那一条。三个刻意的设计：
    - **设置源适配器放在根模块**（`core/manager/pkg/llm/pigsettings.go`）。`core/pig`
    不能 import 控制面，所以桥必须在中间；它不缓存，因为 `pigmodel.Registry`
    本来就每次重读设置——那正是「改 key 下一次请求生效」的机制，再加一层 TTL 会让
    有效陈旧度变成两个间隔的乘积，没人推得清。
    - **默认 provider 会跳过未配置者**。一个指向未配置 provider 的 default 会让每次
    未指定模型的调用都失败成「default x is not configured」——读起来像 OpsKeeper 的
    bug，实际是缺 key；所以不可用的 default 退到第一个已配置 provider，与 SPA 选择器
    同一套 tie-break。
    - **切换工厂会作废已解析的目录并重建静态子客户端**。只作废动态缓存会让 fallback
    那条路（`Provider==""` 且无 default）继续走旧后端——典型的「有些请求换了、有些没换」。
    两条各有一个测试钉住。`pigclient_test.go` 用 PiG 真实 provider 栈而非桩：能坏的
    是**转写**，而桩会接受任何形状，包括真实 provider 会拒的那种。

27. **`tool_start` 与 `tool_end` 是两帧，join 发生在消费者侧**：wire 契约（golden
    逐字节锁定）里 `tool_end` **刻意不带** `started_at` / `args_json`——生产者只在准入
    那一刻知道开始时间，结算帧重复它就是同一事实写两遍。控制面的 `kernelSink`
    （`internal/manager/biz/aiops/chatruntime/kernelsink.go`）因此按
    `session → tool_call_id` 记住准入帧，在结算帧到达时**只填空缺**：生产者自己带了值
    就以它为准，覆盖等于用记忆时间替换测量时间。记录在结算时删除，会话 `done` 时整组
    删除——不删就是「每个没有结算帧的调用一条」的泄漏，而控制面进程服务所有会话。
28. **并行工具调用暴露的两处真实数据竞争**：PiG 并行执行同一轮的工具调用，于是
    `pigagent.Mapper` 的 `seq` / `iteration` / `usage` 与 `runState.blocked` 都是无保护
    的共享状态。前者的后果是**重复帧序号**（控制面据此排序），后者是 Go 运行时的
    `fatal error: concurrent map writes`——不是 `-race` 警告，是进程直接死。两者现在都由
    `sync.Mutex` 保护；`ports.EventSink` 的文档明确写了 `Emit` 可以被并发调用（内核
    并行跑同一轮的工具），所以 sink 实现必须自己保证串行——`kernelSink` 在锁内发帧，
    因为两个并发写会把两帧 SSE 交织成一条坏记录。`zzrace_test.go` 的并行探针是回归。

29. **审批绑定走请求上的 digest，空 digest 的「批准」不是批准**：`ports.ApprovalRequest`
    早就声明了 `Digest` 字段（「a decision that arrives without one is refused」），但
    `runState` 从来没有把它填上——算法是本包的（工具名 + NUL + 精确参数字节），宿主推不
    出来。结果只有两种可能：宿主自己猜（猜错则每一次批准都像「另一个调用」）或者留空
    （绑定关掉）。现在 kernel 在请求上带上 digest，判定改成 **`decision.Digest != digest`
    即拒**（空值也不例外），并且**先判显式拒绝、再校验绑定**——否则运维点下「拒绝」会被
    回一句「digest 不匹配」，读起来像控制台的 bug 而不是他自己的答复。两个测试分别钉住
    「不带 digest 的批准被拒」与「请求确实带着 digest」。
30. **控制面内核绑定的四件套落在 `agentkernel`**：内核要的四样宿主服务以前只存在于
    eino 的 `callbacks` 里，现在有了与内核无关的实现——
    - `Host.Provider()`：每轮解析 `ports.AgentDeps`。工具包按轮解析（角色/画像/写闸门
      实时决定），缺工具源时**在装配期报错**而不是发一个空包：空包看起来像「模型自己
      决定不用工具」，一轮会「成功」返回一个没人解释过的答案。
    - `AuditLedger`：`ports.AuditSink` 落在 `bizaudit` 行式账本上。`blocked` 映射为
      `denied` 并把原词留在 `error_code`（否则「被策略拒绝」和「工具崩了」在表里长得
      一样）；未知 outcome 映射为 `failure` 而**不是** success。`Verify` 明确返回
      `ErrNoAuditChain`——nil 的意思是「链完好」，行式账本返回 nil 等于用「不存在」
      给运维开一张「没被篡改」的证明。
    - `Budget`：把 `llm.BudgetChecker` 适配成 `ports.BudgetChecker`。**它比旧路径弱**：
      旧接口收一个 prompt 估算值，内核的端口只问是否放行，所以这里传 0——达到上限后
      才停，而不是在跨过上限的那一次调用之前停。这一点写在注释里而不是藏起来：谎称
      「两条路径查得一样」才是真正的问题。
    - `InboxGate`：`ports.ApprovalGate` 落在现有审批收件箱上（propose → await）。
      四种情况一律**拒绝而不是修补**：无 digest 的请求不入队、过期请求不入队、收件箱
      不返回行 id、判决指名的不是这一行。批准时把 digest 回显——行是由这个请求创建的，
      回显是在陈述本闸门已经知道的事实。
    **一个必须写下来的隐患**：内核闸门在写工具**运行之前**放行，而写工具内部还有自己的
    `ProposeAndAwait`（HLD-021 的过渡形态）。两条路同时在线 = 一次调用两次人工审批。
    所以内核路径在写工具的内部提案退役之前，必须只暴露不需要二次审批的工具包；这不是
    可以「先跑起来再说」的细节，是一次多余的审批本身就会被当成控制台故障。
31. **历史回放只做一次决定，两种渲染**：`planHistory` 出计划，`buildEinoHistory` 与
    `buildKernelHistory` 只是格式化。规则（去掉尾部 user 行、把工具结果上提到请求之后、
    丢弃孤儿 tool 行、过期工具预算文案改写）决定的是**严格 provider 收不收这份转写**，
    两个内核渲染出不同的会话就等于换内核时悄悄换掉了模型看到的问题。一条规则只写在
    其中一边，现在不是「可能」而是**没有地方可写**——两边都只是渲染。一条 parity 测试
    跑遍 7 种形状逐轮比对（角色/内容/工具 id/参数字节），并且已经抓住过「只在 eino 侧
    加规则」这种改动。

32. **换内核的接缝开在 `Runtime.Handle` 里，且所有策略只解析一次**：`Handle` 在第 5d 步
    分流——`rt.cfg.Kernel != nil` 时打上本轮工具 ctx 后直接交给 `runKernelTurn`，**eino 图
    一次都不会被构造**（这是接缝存在的全部意义：退役引擎可以在装配期就不存在）。关键是
    什么放在了接缝**之上**：人格过滤、角色/写闸门、协调员名单、技能与凭据、提示词组装、
    本轮 hints——全部在分流之前算完，两条路只共享结果，不各自再算一遍。第二遍解析正是
    「一个 viewer 的会话悄悄拿回一个变更类工具」发生的地方。
    随之落地的三件事：
    - `chatprompt` 独立成包（`internal/manager/biz/aiops/chatprompt`）：系统提醒块、语言
      指令、locale 归一化。以前这些散在 `graph/react.go` 里，换内核就意味着**复制**一份
      提醒规则——而复制出来的第二份会漂移。现在两个内核都调它。
    - `kernelSink.LastAssistant`：终端帧必须带**已落库的行 id**（控制台按它更新气泡，
      合成 id 在历史重载后就对不上了）。快照因此必须**活过 flush**——`buffered` 被 flush
      排空，读它只会读到空。四个测试钉住：行 id 正确、无助手轮时为 false、跨会话隔离、
      以及持久化关掉时由 done 帧释放的那条路径。
    - 每轮一次的工具包适配（`agentkernel.NewToolBag`）+ `ports.WithTurnTools` 盖章，
      内核的 `Host.Provider` 从 ctx 里取回**同一个**包，不再重新过滤。

33. **内核路径的审批闸门是「延迟闸门」：谁自己会问，就不替它再问一次**：
    `OPSKEEPER_AGENT_KERNEL=pig` 现在真的能跑（装配层已接线），而接线时撞上的
    第一个真问题是**一次调用两次人工审批**。内核只能看见工具的 class，看不见
    `cloud_bash` 在 `InvokableRun` 里自己 propose-and-await、看不见
    `apply_config_change` 要的是用户对**某个 draft hash** 的确认、也看不见
    `AgentTool`/`SendMessage`/`serve_page` 这类协调与输出原语从来就没审批过。
    于是闸门按「谁自己会问」分流：
    - `selfSettledToolNames()` 里的工具**直接放行**（带回显 digest——内核拒收
      不带绑定的批准，空 digest 等于把绑定关掉）；
    - 其余变更类调用交给 `InboxGate` 问人；
    - 没有内层闸门时一律**拒绝**（fail closed，不是放行）。
    **声明清单必须可被证伪**，所以另加一条装配期断言
    `checkMutatingToolsDeclared`：最终工具包（`chatRT.Tools()`，含
    post-construction 追加的 shell 工具与 MCP）里任何**非 read 且无人声明**的工具
    → **拒绝启动**并点名。理由：这类工具在运行期表现是「一次调用两张卡」，
    而运维看不出第二张卡是接线 bug 还是产品本该如此。清单因此紧挨着决定它的
    构造点，MCP 在自己的注册处 `Add`（它们的审批归属由 trusted 标志决定）。
    两个测试盯住这条：真 registry 的 `BuildBaseTools()` 一遍、post-construction
    的 8 个工具一遍；漏一个名字即失败并点名。
    顺带落地的还有：`ports.ApprovalRequest.SessionID`（否则 `Open(sessionID)`
    无从按会话列出队列）、`agentkernel.InboxUsecase`（biz/approval 的
    `Propose`/行状态轮询/按会话列队，kind `agent_tool_call` **故意不注册
    executor**——审批只记录决定，工具随后在内核里自己跑，注册 executor 就是跑两次）。

34. **eino 编排层删除落点：唯一循环 = PiG 内核，退役拼写不等于回退路径**：
    - `chatruntime` 不再是「两条路径 + 一处分流」，而是**宿主外壳 + 唯一内核**。
      `Runtime.Handle` 恒走 `runKernelTurn`；`Config.Kernel` 必填，缺内核即启动失败
      （一个不响的内核比一个报错的内核更难查）。上限由 `MaxIterations`（默认 30）
      承担，与旧图路径的 `MaxIterations` 同值，因此切换不改变可观察行为。
    - `KernelGraph` **保留为已退役拼写**：`ParseKernel("graph")` 仍返回它，
      `UsesChatRuntime()` 覆盖它。理由是运维无法被告知改环境变量名，而静默回退到
      legacy 是比「同一个内核换个名字」大得多的行为变更。因此 `main.go` 判定用
      `UsesChatRuntime()` 而不是 `== KernelPig`。
    - 告警草稿守卫**不随 callback 一起删**。它是产品规则（配置类变更不许以自然语言
      冒充落地），不是 eino 的实现细节。落点选在 `alertdraft` 包 + 两个消费点
      （`kernelSink` 流上净化、`Persister` 落库净化）而不是「一处净化 + 一个开关」，
      因为流上净化只能改 SSE 帧、落库净化才能改历史——**只做前者等于没做**，
      用户刷新一下页面草稿就回来了。
    - `go.mod` 中 eino 与 `go-openai` 均为零。两者的删除顺序是有依赖的：
      先删图与回调（换编排），再删路由模型（换模型选择），最后才 `go mod tidy`；
      反过来做会在中途出现「`llm.Client` 没有实现」的空窗。

35. **审计链落在 `biz/audit` 的写入咽喉上，而不是某个回调里**：
    - **为什么在咽喉上**：HLD-010 的每一行——中间件的登录失败、iam 的角色变更、
      内核 gate 的拒绝、审批回执——都经过 `bizaudit.EmitWithID` 这一个出口。把链
      挂在这里，它覆盖的是**整条账本**；挂在某一个 callback 上，它只覆盖那个
      callback 写的那几行，而中间件写的行落在链外，从外面看链是完整的——这种
      「看起来有防篡改」的假象比没有更危险。
    - **规范编码用长度前缀，不用分隔符**：摘要是对长度前缀编码取的，不是对
      拼接字符串取的。分隔符方案下 `target="device-1", action=""` 与
      `target="device", action="-1"` 是同一串字节，链照样通过校验，描述的却是
      两件事。长度前缀让每个字段的边界无歧义。
    - **链头用单行 CAS 推进，且 CAS 是事务里的第一条语句**：两个 manager 实例
      并发写时，输的一方 `RowsAffected=0`，重读链头**重算** seq/prev/hash 再重试
      （三者都依赖链头，不能只重试插入）。CAS 放第一条还有一个非显然的好处：
      事务从一开始就是写事务，不必把读事务升级成写——SQLite 上这种升级会撞
      `SQLITE_BUSY_SNAPTABLE`，而 busy_timeout 对它不重试（实测 8 并发下 80 条
      只落 5 条，改完 80/80）。
    - **先推链头再插行**：两步之间崩溃会留下「链头比最新行高一位」——校验时
      表现为一个可见的缺口。反过来先插行再推链头，留下的行其 `prev_hash` 指向
      一个表里已不存在对应者的摘要，与真实篡改**不可区分**。宁可要一个能看见的
      缺口，也不要制造一种无法分辨的篡改。
    - **保留策略只能裁前缀**：`occurred_at` 与 `seq` 会因为一次时钟回拨或一次
      重试插入而分叉，按时间删就会在链中间挖洞。洞两侧各自自洽，校验报出的断点
      与真实篡改无法分辨。因此 `TruncateExpiredPrefix` 从锚点向前走、遇到第一条
      未过期行就停——代价是「表里最老的一行可能因为前面有活行而留下来」，
      这是可描述的边界（`ChainState.AnchorSeq` 会报出来），而洞不是。
    - **没有 key 就不写摘要，但仍然写行**：`OPSKEEPER_AUDIT_HMAC_KEY` 未配置时
      账本照记，只是没有防篡改性。丢审计行比没有防篡改性更糟；此时
      `VerifyChain` 返回 `ErrChainDisabled` 而**不是** nil，控制台
      `GET /v1/admin/audit-logs/chain` 的 `intact` 字段是 `null` 而不是 `true`。
      「没有链」和「链完好」必须能被客户端区分，否则一个只读 `intact` 字段的
      前端会把前者显示成后者。

36. **把三个剧本接到新拓扑上，撞出的是一条楔死的链路，不是三条缺失的断言**：
   `internal/manager/biz/nodefleet/e2e/` 用真 `nodefleet.Fleet` + 真
   `AgentBridge` + 真 `policygate.Gate` + 真 `pigwire` 翻译器跑
   `alert_storm` / `rca_loop` / `recovery_verify`，6 个用例。
   - **`StartEvents` 的实现与它自己的契约相反**：`core/edge/biz/agent_rpc.go`
     的函数体是 `_ = proc.OnEvent(b.relay); <-ctx.Done()`——**阻塞**；而它自己的
     doc 与 `agent.go:245` 的调用点注释都写着「立即返回」。后果不是某个用例失败，
     是 `Agent.Run` 卡在第 245 行，其后的 changewatcher、升级哨兵
     `errUpgradeRequested`、`eg.Wait()`、优雅关闭**全部不可达**——而心跳与指标
     goroutine 已经在跑，节点在监控面板上看起来完全健康。修法是包一层 `go`，
     并在调用点标注「load-bearing」。这类缺陷单元测试看不见：被测函数返回了，
     测的是返回值；看不见的是**调用方还能不能往下走**。回归用例
     `TestStartEventsDoesNotBlockItsCaller` 已做变异验证（改回阻塞即失败）。
   - **替身在哪一层，必须写在包注释里**：真的部分是 Fleet、隧道 wire 类型、
     `AgentBridge`、`policygate`、生产用的 `pigwire` 翻译器、控制台帧契约——
     剧本断言的每一帧都是「翻译一个 PiG 事件 → 推过隧道」得来的，不是测试直接
     写的帧。替身的只有两处：传输（进程内回环而非 geminio）与 agent 进程
     （`pig --mode rpc` 的脚本替身）。**stdio JSONL 之上全是生产代码，之下不是**。
     不把这句写清楚，一个全绿的测试会让人以为端到端就是端到端。
   - **审批回执按「需不需要人」发，不按「被放行了」发**：脚本替身最初对每个被
     放行的调用都去 `ClaimReceipt`，于是只读工具全被拒成
     `refused: no approval receipt`。生产侧 `cmd/opskeeper-edge/policy.go` 的
     顺序是对的（先 `NeedsApproval` 再要凭据），错的是替身。但真正致命的是
     **当时的断言只检查「结果非空」**——拒绝对非空，于是绿灯。已把只读风暴的
     断言改成逐个比对**脚本给的值**，并新增「恢复先于验证」的有序断言。
     一个只断言非空的测试等于没有断言，这条比它发现的 bug 本身更值得记住。
   - **双检有效，但要知道是被哪一道挡住的**：单独打穿第一道检（忽略 `Admit`
     的结论）**打不穿**——凭据检兜住了，因为被放行的写调用没有回执可领。这是对
     设计的确认，不是测试无效。换成更贴近真实的单点变异（把节点策略改成
     「写即读」，即一个把写工具误标成只读的配置错误），6 个用例倒 5 个，每个
     带着自己的报错。第二处变异（每帧投递两次）被帧计数与顺序断言抓住。
   - **无人值守场景显式缩短审批 TTL**：生产的 15 分钟是给一个被叫醒的人留的
     时间，测试不能等那么久。`withApprovalTTL` 只改时钟不改分支——走的仍是
     生产里「无人应答即 fail-closed」那一条。无人在场的告警风暴正是最该被
     拒绝的场景：agent 自己觉得「重启一下服务让告警停下」是可以接受的。

37. **`harness` 拆成独立模块，代价是先承认 `ports.Chat` 表达不了工具调用**：
   `internal/harness` → `core/harness`（34 个 Go 文件 + 20 个 case.yaml），
   零外部依赖，只依赖 `core` 与标准库。`GOWORK=off` 下独立构建与测试通过。
   - **挡路的不是包结构，是那个 `llm` 依赖**：harness 只需要「要一次补全」，
     却 import 了整个 OpenAI/PiG/registry 依赖图。修法不是让 harness 自己
     声明一个接口（那会和契约层重复定义「一次补全」），而是先把
     `ports.Chat` 拆成 `ports.Completer`（`Complete` 一个方法）与 `Chat`
     （补全 + `Available` + `Resolve`）。
   - **为什么这个端口之前没人实现**：它把两件事塞在一个接口里。要一次补全
     的调用方（评测 judge）被迫去实现「有没有配好」「这次会用哪个模型」，
     而知道配置的实现方要反过来被塞一份评分细则。`Completer` 与配置发现
     分开之后，注入点就自然存在了。
   - **同一次修改还补上了 `LLMMessage.ToolCalls`**：没有它，一个转录本无法
     表达「助手请求了工具 → 工具返回结果 → 下一轮」，适配器只能从
     `LLMResponse` 反推，第二轮就会把工具结果变成孤儿。这不是洁癖：这是
     「适配器是否有损」的唯一检验。
   - **`MaxOutputTokens` 显式失败而不是被丢掉**：`llm` 包刻意不发
     `max_tokens`（推理模型会拒，而本地拒绝对调用点与 provider 400 无法
     区分）。端口承诺了一个上界，实现给不出，所以 `Complete` 返回
     `ErrMaxOutputTokensUnsupported`——悄悄丢弃并回报成功，等于对调用方
     谎报这次调用的成本。
   - **这个端口至今没有生产调用方**：`runner.LoopDeps{}` 在
     `cmd/opskeeper-eval` 里是**空结构体**，没有 judge、没有 LLM client。
     也就是说 LLM judge 从未真正跑过，默认永远落到 heuristic。类型改造
     因此零风险，但也说明「把依赖搬走」和「把功能接上」是两件事。
   - **顺手发现并修掉一处真实跨层泄漏**（下面单列，决策 37 的副产物）。

38. **BC 边界写下来了，但从来没有被执行过——把它变成会执行的东西时，它
    立刻抓到了三处真实越界**：
   - **事实**：`go-arch-lint` 没装，`make arch-lint` 静默 `exit 0`。这份
     配置会拒绝仓库里 **51 处**合法的 `core/{domain,ports,wire}` 导入
     （BC 的 `mayDependOn` 从来没允许过契约层），所以它就算装上也只会一直
     报红。一份拒绝 51 处合法导入的规则不是边界，是噪声——它真正的代价是
     让人学会忽略红色。
   - **处置**：契约层在每个 BC 的 `mayDependOn` 里显式列出。**不用 YAML
     锚点**：`*contracts` 展开成的是嵌套数组 `[["oxcore_domain", …]]` 而不是
     三个名字，`go-arch-lint` 拿到之后每一条都静默失配——配置看着干净，
     实际一行都不生效。冗长但正确的配置，胜过一个不生效的。
   - **可执行的那份放进 `modulecheck`**：BC 两两不可达、`core/manager/pkg`
     不得知道业务、`service → biz ← data` 的方向。`make arch-lint` 在工具
     缺失时改为**大声告警**并指向 `module-check`，不再假装通过。
   - **三处真实越界，第一处是真 bug**：`iam/service/service.go` 的包注释
     写着「must never import internal/iam/data/**（gospec red line）」，
     而它自己第 14 行就 import 了。更深一层是 `biz/membership` 的 `Repo`
     接口签名直接返回 `data/membership/store` 的类型——**biz 的公开契约
     里含着一个 data 层的类型**，于是 service 层为了给这个方法标返回值
     也必须 import data。修法是把 `MembershipWithOrg` / `MembershipWithUser`
     搬进 `iam/model`：它们本来就只由 `model` 类型组成。一个公开接口
     返回的类型不可能住在拥有这个接口的那一层之下——类型是契约的一部分。
   - **另两处是有意的跨上下文依赖，登记为逐条例外而不是删掉规则**：
     `iam/server` → `manager/{biz,model}/audit`（决策 35 把审计链放在
     manager 的唯一咽喉上，这是那条决策的**后果**而不是意外，也正是未来
     必须用审计端口解决的那条边）；`manager/server/imbridge` → `iam/model`
     （模型是两个上下文本来就该对齐的东西，复制一份就是拥有两个真相）；
     以及两个跨平面测试。例外按**精确 import 路径**登记，所以同一个包里
     **新增**一条跨上下文导入仍然是红的——已做变异验证：测试文件里多加
     一个 edgeagent 包、以及生产文件冒用测试专用例外，两次都变红。

39. **LLM judge 此前不是"没接线"，是任何模式都跑不到它——修的第一件事是让它
    跑在一个真有输出的地方**：
   - **它在结构上不可达**：`cmd/opskeeper-eval` 传的是空的 `runner.LoopDeps{}`；
     而 `RunLoop` 里 judge 只在 `deps.Orchestrator != nil` 时执行。dry-run 是 CLI
     唯一能到的模式（`--execution-mode=orchestrator` 强制要求 orchestrator，而
     那个 orchestrator 需要 advisory lock + DB + LLM，接进 eval 等于把整个
     manager BC 拖进评测工具），而 dry-run **刻意**跳过 judge——注释写着
     「heuristic 打分打在合成的空响应上是误导」。结论：`rca_accuracy` 一直
     等于「状态机有没有走到 postmortem」，与 agent 判断得对不对无关。
   - **`loopResultToAgentResponse` 根本不读入参**：函数体返回一个写死的结构，
     形参 `r` 从未被使用。就算 judge 跑起来，评的也是一个常数。
   - **真答案在 real-agentteams 模式的证据包里**：`realPostmortemEvidence`
     带 `root_cause` / `resolution` / `verification`，且都是必填非空。这是
     真实系统产出的结论。`validatedRealAgentTeamsEvidence` 现在把它一起带出来
     （不带的话 judge 只能给一个阶段时间线打分——"恢复"阶段跑过只说明机器动
     了，不说明它动对了）。
   - **`matchRatio` 是精确字符串相等**，而黄金 case 的期望根因是符号化的
     （`pg.lock_waits`），真实 postmortem 写的是一整句话。所以**启发式 judge
     在真实证据上必然接近 0**。这不是 bug，是它能力边界。因此：接上 judge，
     同时用 `judge_heuristic_on_free_text` 标志把这件事写进产物——一个读起来
     像"agent 判错了"的 0.0，比没有数字更糟。
   - **`--judge=llm` 在非 real-agentteams 模式下直接拒绝**，而不是接上一个随后
     被丢弃的模型调用：为一个会被扔掉的数字付费，比拒绝更糟。
   - **provider 目录构造从 `cmd/opskeeper` 抽到 `llm.ProviderConfigFor`**：
     那 50 行里有 base URL 默认值和模型白名单，评测工具复制一份就会静默地
     指向另一个端点/另一个默认模型，而症状只是"judge 评的是一个和被测系统
     不一样的系统"。现在两边问同一个函数，并有测试钉住默认值与覆盖优先级。
   - **`opskeeper-eval judge` 从空壳变成真命令**：`--case` + `--response`
     （`judge.AgentResponse` JSON，harness 自己的类型，不新造 schema），
     默认 heuristic（评测工具会被反复重跑，因为一个标志翻了就开始计费、
     而且替换在输出里不可见，是很坏的默认值）。`--judge=llm` 且没有可用
     provider 时**报错而不是退回启发式**——否则会把启发式分数记在模型 judge's
     名下。空响应也报错：那是"没有响应"，不是"一个得了 0 分的响应"。
   - **变异验证**三处：把 `agentResponseFromEvidence` 换成写死值 →
     「judge 没看到 run 自己的根因」失败；去掉诚实标志 → 断言失败；
     `buildJudge` 改成失败时退回启发式 → 两条 fail-loud 用例失败。
40. **把 judge 接上之后，它算出来的分是错的——三个字段从来没被解析过**。
    决策 39 让 judge 真的跑起来，下一步是拿它给一个正确答案打分，然后
    发现它给 0.667。查下去发现 `core/harness/schema/loader.go` 有三处
    静默失败，**每一个 case 都受影响，且都是朝着"看起来还行"的方向偏**：
   - **`root_cause_lines` 吞掉了兄弟键 `remediation_options`**。`extractList`
     只在遇到**没有缩进**的行时才结束列表，而 case.yaml 里这两个键是同级的
     （都缩进两格）。于是 `pg/lock-waits` 的期望根因读成
     `["pg.lock_waits", "pg.active_sessions", "pg.kill_session"]`——`pg.kill_session`
     从来不是根因。`matchRatio` 的分母因此偏大，**一个把期望根因全部答对的
     响应只能拿 2/3 = 0.667，低于该 case 自己的 0.85 阈值**。修法是按
     缩进层级判断列表结束（`indentOf(line) <= keyIndent`），而不是按"有没有
     缩进"。
   - **`expect.time_to_detect` / `time_to_remediate` 从未被解析**。两个字段在
     `Expect` struct 上声明了，`parseAndValidate` 里**没有任何一行给它们赋值**，
     恒为 0。而 `timeEfficiency(actual, 0)` 的第一行就是 `if expectedSec <= 0
     { return 1.0 }`——于是 **`time_efficiency` 对每一个 case、每一次 run 都白送
     满分**，整体分恒定多出 0.2。评测从此测不出"发现太慢"。现在缺失即报错，
     不再当作 0 静默通过。
   - **`rubric.no_collateral_damage` 从未被解析**，恒为 false，而
     `collateral_safety` 的判据是 `NoCollateralDamage && len(errors) > 0`。
     false 让这个维度恒为 1.0：**一次带 errors 的破坏性修复与一次完美修复
     得分完全相同**。
   - **为什么三个字段都栽在同一处**：loader 的文件头注释写着"完整字段解析
     留给 runner 阶段（届时引入 yaml.v3）"。于是 `Expect`/`Rubric` 上先声明了
     字段，解析器只实现了字符串和列表这两类最容易的，三种类型（bool / 嵌套
     标量 / 同级列表边界）**没有一个被测试覆盖**。已补的守卫是按类型选
     断言的：列表边界用 `extractList` 的单元测试钉住（不带数据 fixture，
     因为那是**解析器的性质**而不是某个文件的性质），两个时间基线和
     collateral 标志则对**全部 20 个 case** 断言，这样新增 case 漏填会立刻红。
   - **变异验证**三处：把缩进判断换回旧逻辑 → `TestASiblingKeyEndsTheListBeforeIt`
     失败；让时间基线不被解析 → 两个时间相关测试一起失败；短路
     `no_collateral_damage` 解析 → 对应测试失败。
   - **修完之后的判别力**（同一个 `pg/lock-waits`，同一份二进制）：
     正确答案 `overall=1.0`（四个维度全 1）；带 errors、400 秒才发现、修复
     方案也答错的响应 `overall=0.0`。修复前后者是 0.1，且 time_efficiency
     无论如何都是 1.0。
   - **一条方法论，写在这里给后面的人**：`opskeeper-eval judge` 的端到端测试
     当时只断言 `overall ∈ [0,1]`，所以"把根因全部答对却打 0.667"是**绿的**。
     补了一条 `rca_accuracy == 1` 的实质断言之后，这个 bug 才第一次可见。
     区间断言对"算错了"完全免疫——它只证明"算出了一个数"。凡是要靠数值
     判对错的地方，断言必须钉住那个具体数值。
41. **golden case 的能力词表与实现侧的词表从来没有被连接过——先量出来，再决定
    谁改**。决策 39/40 把 judge 接上之后，剩最后一环：谁产出
    `judge.AgentResponse`。追这条链时撞见的不是接线问题，是**两套不相通的
    词表**，因此先做的是把它们量出来：
   - **平台侧能力有四个来源，形状还不一样**：middleware adapter 按字面名注册
     工具（`pg.kill_session`），插件包声明的是**能力族**（`host`）而不是方法名，
     闭环的 `investigatorreal` 提修复动作（`pg.kill_backend`），而
     `investigatedOutputSchema` 里根因是**另一套 namespace**（`pg_lock`）。
   - **第一版闸门报 0/20，那个数字是错的**。它只拿语料去比
     `investigatorreal.RemediationActions`——而语料的 57 个符号里有 **36 个**由
     真实 middleware adapter 注册。拿单个子系统比整张能力表，结论必然是"全都
     不满足"，而**一个"全都不满足"的闸门和一个正常工作的闸门长得一模一样**：
     都打印数字、都非零退出、都没有异常。差点把一个错误的红线当成真实结论
     写进文档。
   - **正确的比较点是一个带出处的 provider 集合**（`core/harness/vocabulary`）：
     adapter 提供**精确符号**，插件提供**能力族**，精确匹配在全构建范围内优先
     于族匹配（按族连接是 `plugin-coverage` 已经论证过的刻意选择，逐方法名匹配
     会在包改名的瞬间产生假阴性；但全构建里只要有注册表按字面名注册了，报那个
     注册表才对，因为它才是能被精确指过去的子系统）。
   - **能力表是"跑出来的"不是"扫出来的"**：6 个 adapter 的 `RegisterTools` 真的
     跑进一个空 registry 再 `ListTools("")`。抓源码字符串是第二个解析器，会静默
     漏掉它没有建模的注册路径——正是能力闸门要防的失效。
   - **loop 侧两处声明都有防漂移**：`investigatorreal.RemediationActions` 配一条
     扫自己 AST 的**双向**漂移测试（能产出没声明→红；声明了没路径能产出→红）；
     `loop.RootCauseKinds()` 直接**派生**自模型被约束的那份 JSON schema，所以
     不存在漂移可能，restate 一份就会变成"带测试形状的注释"。
   - **真实结果：9/20 可满足**，缺口是 14 个具体符号。其中
     `redis.kill_client`（语料）vs `redis.client_kill`（adapter）**是改名未对齐，
     不是能力缺失**——两种读法差一个字。闸门按精确匹配把它算作缺口，这是对的：
     替人猜"这两个是同一个"是一次没有依据的语义判断，而这正是这类闸门最容易
     造成的伤害。该由人来裁决。
   - **`judge` 因此会拒绝打分**：可满足性不达标时直接报错并指名缺哪个符号，
     避免产出一个会被读成"agent 判错了"的 0；`--allow-unservable` 可以强打，
     代价是产物里永久带上 `unservable_case`。
   - **变异验证**：把 adapter 从能力并集里摘掉 → 5 条测试同时失败，其中包括
     `TestACaseWhoseSymbolsTheAdaptersRegisterIsReportedServable`——它存在的意义
     就是把"全判为 GAP"这个错误方向钉死。
42. **judge 闭环的最后一环补上了，于是它第一次给出了一个有意义的 0**。
    决策 41 量出了词表断层，本轮把断层那一端也接上：
   - **`core/harness/projection` 把生产契约投影成 `judge.AgentResponse`**。这是
     历史上第一次让 judge 评**系统真产出**的东西——此前每一个分数都来自手写响应文件，
     golden 语料从来没有被真正考核过。
   - **三套词表里只有一套能直接对上**：`remediation_options[].action` 是
     `pg.terminate_long_tx`，与 case 同构；`root_cause_object.kind` 是闭集 enum
     `pg_lock`；`evidence_chain[].tool` 是裸名 `query_promql`（跨族，不指名任何资源）。
     后两者**不猜**。
   - **根因映射是数据（`--kind-map`），不是代码分支**。断定 pg_lock 与
     pg.lock_waits 是同一个发现是一次关于**语义**的判断，只有领域 owner 能做；写成
     代码分支会让这个判断永远隐形，写成 JSON 才能被 review、diff、签字。
   - **没有映射就拒绝出响应**。输出 `root_cause_matched` 为空的响应会被 judge 打 0
     并读成"这次诊断什么都没找到"——那是一次凭空捏造的判决。
   - **镜像必须防烂**：`projection.Doc` 是 `loop.RootCauseJSON` 的手写镜像（评测面
     不得依赖控制面实现，是 `scripts/modulecheck` 的模块规则）。契约加字段时两边都
     还能编译，投影会悄悄少投一层。所以有两条**双向**测试：真实契约过线格式进镜像
     用**严格解码**（未知字段即失败），镜像写出的再读回控制面类型。变异验证：给
     `loop.RootCauseObject` 加一个字段 → 精确报错。
   - **映射文件本身会陈旧，且不会报错**。它是数据，工具改名后它静默指向不存在的
     名字，于是每次经过它的运行都在 `remediation_quality` 上打 0 而没有任何告警。
     `vocabulary --kind-map` 用构建自身的能力表校验它；仓库里的
     `docs/kind-map.example.json` 由 `TestTheShippedExampleKindMapHasNoStaleEntries`
     守着。变异验证：把 `k8s_oom` 指向 `k8s.top_pods` → 测试失败并指名该符号。
   - **最重要的产出是那个 0**：端到端跑通后，`pg/lock-waits` 得到
     `rca_accuracy=1`（kind 映射生效）、`time_efficiency=1`、但
     **`remediation_quality=0`**——闭环提的是 `pg.kill_backend`，case 期望
     `pg.kill_session`。这把"闭环提不出语料期望的修复动作"这件一直看不见的事，变成
     了一个可以指着数字讨论的事实。
   - **因此闸门多了一个数字**：平台可满足 **9/20**，闭环可满足 **0/20**。第二个才是
     预测 judge 行为的那个。两者必须分开报——只报 9/20 会让人相信有九个 case 可以
     端到端评分，而实际上一次都做不到。

43. **闭环的 8 个"没有实现"降到 0，靠的是三个真适配器，不是三个桩**。
    决策 42 把 `remediation_quality=0` 变成一个可以指着讨论的数字，本轮把数字
    背后的东西补上——补齐的每一步都有一条"不这么做的诱惑"被显式拒绝：
   - **k8s 适配器：不引入 client-go**。19 个工具全部走自建的 REST 客户端
     （`net/http` + bearer token），DSN 支持 `kubeconfig://` / `incluster://` /
     `https://host:port?token=` / 内联 kubeconfig。理由是依赖形态：client-go 会把
     整棵 informer / discovery / scheme 依赖树拖进控制面，而这里要的是十几次
     一次性 REST 调用。**kubeconfig 里的 `exec` / auth-provider 插件被拒绝**——
     那等于让一个运维凭据文件触发任意命令执行。
   - **无命名空间时靠全 ns 搜索唯一匹配，歧义就拒绝**。同名 Pod 跨 namespace
     时不猜哪一个，把候选列出来让人选。猜错的 `evict_pod` 是在驱逐别人的生产
     Pod。
   - **`evict_pod` 的 429 不是 error**：PDB 拒绝驱逐是一个**决策**而不是故障，
     所以报 `Success:false` 并在消息里点名 PodDisruptionBudget；404（Pod 已经
     不在了）反而报 `Success:true`，因为那正是调用方想要的结果。
   - **`resize_pvc` 只增不减**，缩容在本地拒绝；`drain` 默认拒绝 DaemonSet Pod
     与 static Pod（mirror 注解）；`cordon` 的消息里说明**它不驱逐**并报出仍在
     运行的 Pod 数——一个只改调度位却让人以为节点已空的返回值，是这类工具最
     常见的误导。
   - **`rollout_status` 不要求必填参数**，这是它作为 loop 动作可自动执行的关键：
     给了 deployment 查单个，不给就列出所有未完成的 rollout。
   - **MQ 用中立的 `mq.` 命名空间，而不是挂在 `kafka.` 或 `rabbitmq.` 下**。
     loop 说 `mq.drain_queue` 时不关心哪个 broker 服务这次事故；挂到某个产品
     命名空间下，会让闭环的动作集取决于这个租户碰巧跑的是哪个 broker。
     DSN scheme 选后端（`amqp(s)://` → RabbitMQ management HTTP API，
     `kafka://` → Kafka），未识别的 scheme **拒绝**而不是猜。
   - **RabbitMQ 侧要求 `confirm: "drain"` 是字符串**，不是 bool——布尔开关会
     被任何一层参数强制转换打开。`replayMessages` 里 `routed: false` 计为失败，
     因为消息被丢弃和消息被重投是两件相反的事。
   - **Kafka 侧要求消费组为 Empty 才允许重置 offset**，Stable / PreparingRebalance
     一律拒绝——活消费者下一次 commit 会覆盖掉重置。消息里明说"记录没被删除，
     只是组的位置变了"，因为把 offset reset 说成“清空队列”是最容易产生的误解。
   - **host 适配器 `restart_service` 三道闸门**：单元名正则白名单（排除前导 `-`，
     否则 `--now.service` 会被 systemctl 当选项解析）、节点侧 unit allowlist
     （`OPSKEEPER_HOST_UNIT_ALLOWLIST`，**为空拒绝一切而不是允许一切**）、
     `LoadState=loaded`。verdict 取**重启后的状态**而不是 systemctl 退出码——
     `systemctl restart` 对"起来后立刻崩"的 unit 返回 0。
   - **`garbage_collect` 无必填参数**（所以 loop 可自动执行），做的是
     `sync` + `vm.drop_caches`，**不删任何东西、不碰任何进程**；回收 0 字节时
     明说没变化，不粉饰成成功。
   - **闸门的报告本身有一个洞被堵上**：`emitActionText` 在"没有未实现动作"时
     直接 return，把"名字有实现但派发不出参数"的那一类**静默吞掉**——恰好在
     全名覆盖那一刻，参数问题成为唯一还剩的问题。现在两类独立打印。
   - **装配从"一个适配器"变成"按 DSN 装五个"**：`cmd/opskeeper/loop_adapters.go`
     按 `OPSKEEPER_LOOP_{PG,REDIS,K8S,MQ,HOST}_DSN` 各自连接并注册到同一个
     registry；坏 DSN 记录并跳过，不让一个修复便利把控制面拖下水。
   - **结果**：闭环动作未实现数 **8 → 0**，闭环覆盖 **0/20 → 3/20**（remediation 轴），
     剩下的 9 个是**参数缺口**（要 pid / pod / queue），不是实现缺口——这一层
     由决策 49 闭合。

44. **`ArgResolver` 从接缝变成实现：从证据里取值，候选多于一个就拒绝**。
    决策 43 留下的 9 个"名字有、参数没有"的动作里，`RegistryInvoker.ArgResolver`
    这个接缝此前**只有测试替身，没有任何生产实现**——一个没被任何生产路径走过的
    接口，等于一条声称存在的路。
   - **值从哪来**：`RootCauseJSON.EvidenceChain`。`pg.kill_session` 要的 pid 与
     `pg.connection_pause` 要的 role 本来就在 PostgreSQL investigator 采到的
     `pg_stat_activity` 行里（`SELECT pid, usename, application_name, state, ...`），
     只是从来没有人把它接到工具参数上。**先做这条，是因为数据已经存在**；
     给 `RemediationOption` 加定位器字段要动被校验的合同 schema，是更大的动作。
   - **不猜**：证据里只有一个候选 pid 才解析；两个以上就拒绝并在消息里列出
     `pid / user / app / state`——"杀掉其中一个"不是修复，是掷骰子。
     role 同理：跨多个 role 的暂停请求被拒绝，消息里点名是哪几个 role。
   - **拒绝消息不带 query 文本**：证据入库前已做脱敏，但拒绝消息会被打印到比
     证据更多的地方。
   - **没有声明提取器的动作返回 `(nil, nil)`**，把话让给 invoker 自己的缺参
     拒绝。解析器对它一无所知的动作返回错误，会变成"解析器声称自己对这些动作
     有权威"，而它没有。
   - **两种行形态都读**：合同表把证据存成 `interface{}`，investigator 构造的是
     `[]map[string]any`，经过 JSON 往返会变成 `[]any`。只认其中一种的解析器会在
     生产路径上失效——而它自己的单测会全绿。
   - **结果是 2/9 变成了真能派发**（pid、role），另外 7 个的前置条件被记录清楚：
     k8s 的 pod 名、mq 的队列名、host 的 unit、redis 的 addr **不在**任何现有证据
     里，得先让对应 investigator 采到，解析器才有东西可解析。
   - **闸门因此把 9 个缺口再分成两类**，在 `opskeeper-eval vocabulary` 的输出里
     逐条标注：`resolvable`（有提取器）对 `no evidence records this value`
     （值没被采集过）。今天两类都是拒绝，但需要的工作完全不同——给后者写解析器，
     是给一个从来没被观测到的 Pod 名写查找表。
     `ActionsResolvableFromEvidence()` 是这个分类的唯一来源，配一条测试要求它
     是 `investigatorreal.RemediationActions` 的子集：一个有提取器但 investigator
     永远不会提议的动作是死代码。

45. **`git` 适配器从骨架变成实现：用 git CLI，不用 go-git；并且不提供写路径**。
    骨架阶段留了三句话——"引入 go-git 替代 os/exec""真实实现 7 个 CLI 工具"
    "存量和增量 clone + watch"——这一轮把第一句和第二句都推翻重写了，理由
    记录如下，因为它们不是实现偏好，是这一层能不能被信任的前提。
   - **为什么是 CLI 而不是 go-git**：`deploy/Dockerfile.opskeeper` 已经装了 git
    （知识库的 clone 用它），所以"再引一个纯 Go 实现"不是省依赖，而是**在同一个
    容器里放两套对 packfile、ref、重命名检测和 textconv 各有各的 bug 的实现**。
    CLI 的成本是一次进程创建（~5ms）和 `runner.go` 这一个文件；换来的是
    agent 给出的答案与运维人员在提示符下敲 `git log` 得到的是同一个。
    `core/pig` 那边已经把"上游不稳定面收口到一个模块"，这里是同一个手法用在
    另一个上游上。
   - **安全属性落在 `runner.go` 一处**，且每条都有理由：
     - **完整替换子进程环境**，而不是继承后追加。manager 进程里握着 secretbox
       密钥、数据库 DSN 和每个 skill 拿到的凭据；一次 git 调用需要的是 PATH，
       别的都不需要。`GIT_PAGER` / `GIT_EXTERNAL_DIFF` / `GIT_ASKPASS` 这类
       变量能让 git 去执行东西，`GIT_CONFIG_GLOBAL` 能让它去读宿主的
       `~/.gitconfig`——`GIT_CONFIG_NOSYSTEM=1` **挡不住后者**，它只管
       `/etc/gitconfig`。两者都设成空/`/dev/null` 才是"读就是读"。
     - **`--no-optional-locks`**：这些是并发读者，跑在一个别的进程可能正在
       fetch 的仓库上；为了刷新 stat cache 去抢 index lock，会变成
       "another git process seems to be running"。
     - **所有路径 `-z`（NUL 分隔）**：git 的默认输出用 `:` 分隔字段，而仓库里
       可以有一个叫 `a:b/c.go` 的文件——`git grep` 的默认格式会把那条记录读成
       `rev=HEAD:a`、`path=b/c.go:12:…`。这不是理论问题，测试
       `TestSearchCode_ParsesAPathContainingAColon` 就是为此写的。
     - **搜索模式走 stdin（`-f -`）**：模式一旦进 argv 就会被 `ps` 公布，
       而被搜索的字符串经常正是凭据的形状。
     - **出口封顶**（stdout 1MiB / stderr 64KiB）+ 30s 超时 +
       `cmd.WaitDelay`：一个把十年历史倒出来的 `git log` 不是答案，它会把
       模型上下文占满。
   - **空结果是发现，不是失败**：`git grep` 用退出码 1 表示"没匹配到"，
     适配器把它转成 `rows: [], count: 0` 与一句"这个字符串在 <rev> 里不存在"。
     把"没有"报成错误，会教 agent 认为"仓库里没有这个东西"是工具坏了。
   - **每个工具一个独立的重命名/边界事实**：`git.diff` 用 `--numstat -z`，
     重命名记录是 `added TAB deleted TAB NUL old NUL new`——把空路径字段当普通
     行读，会把一个重命名的增删算到一个叫 "" 的文件头上。`git.blame` 读
     `--line-porcelain`，并把 `boundary` 标记成 `boundary: true`：那是浅克隆
     切断历史的位置，不标出来，调用方会把克隆本身的 commit 当成引入该行的改动。
   - **远端 DSN 用 full fetch，不用 `--depth=1`**：浅克隆会让 `git blame` 对
     每一行返回 boundary，并让"哪个改动引入了这一行"答成克隆。取全量历史是
     这个适配器唯一有意义的答案。远端 clone 到临时目录，`Close` 删除；
     本地路径就地读，`Close` 不动它（`TestAdapter_Close_RemovesOnlyWhatItCloned`）。
   - **`Execute` 永远拒绝**，即使 `ApprovedBy` 非空。这不是"待实现"，是决定：
     git 写操作（push / tag / reset）影响远端与不可逆历史，平台目前没有任何一条
     经过**审批闸门**的 git 写路径。一个拿到 `ApprovedBy` 就执行 push 的入口，
     会让审批退化成"检查了一个非空字段"，比没有入口更危险。
   - **闸门此前高估了这一块**：`cmd/opskeeper-eval vocabulary` 只按**名字**判断
     `git.*` 是否可服务，于是 8 个工具里有 7 个返回
     `not_implemented` 却仍被算作已覆盖。现在这 8 个都是实现，并且
     `ArgsSchema` 用 `!` 标出必填参数（`git.file_at_commit` 要 `path`、
     `git.search_code` 要 `pattern`、`git.blame` 要 `path`），闸门从
     live registration 读到的 `RequiredArgs` 因此与真实情况一致。
   - **顺手把这个洞补成一条常设守卫**：对一个按名字计数的闸门，"注册了但没实现"
     是它**结构上看不见**的一类缺口——这次是靠一次临时探针才发现的。新增
     `core/manager/middleware/adapter/skeleton_test.go`：用 `nil` 参数探测每一个
     已注册工具（真适配器会报参数校验或未连接，骨架会报它自己那句固定消息），
     要求找到的骨架**恰好等于**声明清单 `knownSkeletons`。
     清单**双向比对**：修好一个而不从清单里删，测试失败；新增一个骨架，测试
     立刻失败——清单不会比缺口活得更久。今天这份清单只剩 10 条，全部是
     `kafka.*` / `rabbitmq.*` 两套旧骨架包（见缺口 8）。

46. **`sdk` 补齐三件事：清单类型之外的注册 API 与版本协商，算术下沉到 `core`**。
    计划 D-1 要求 `sdk/` 是「插件清单 Go 类型 + 注册 API + 版本协商」。此前
    `sdk` 只有 `manifest.go`（解析/校验/`Admit`），后两件不在——而它们恰好是
    插件作者**在没有控制面、没有节点的时候**唯一能自查的两件事，缺了它们，
    SDK 就只是宿主内部结构体的别名导出。这一轮补齐，并顺手把一处**已经存在
    的分叉风险**收掉。
   - **版本比较下沉到 `core/domain/version.go`**：节点（`pluginmanifest`）与插件
     作者（`sdk`）必须跑**同一套算术**，而 `sdk` 按模块规则只能 import `core`。
     所以 `CompareVersions` 的实现搬到了 `core/domain`，`pluginmanifest/version.go`
     改成一层**转发壳**，签名与错误文案逐字节不变——既有 `version_test.go`
     一行未改仍全绿，这就是搬运没有改变行为的证据。留在 `pluginmanifest` 的只有
     「节点对操作员说的话」（升级哪个二进制），搬运之后两边不可能再对同一个
     版本得出不同结论。
   - **`sdk/negotiate.go`：构建期兼容性检查**。`Host{Edge, Pig}` +
     `Negotiate(manifest, host)` 一次报**两条轴**（`errors.Join`），因为作者要
     同时修两个字段，而节点侧只报第一条——操作员只需要知道先升哪个二进制。
     两边的**故意不对称**写在文件里：宿主版本未知时 `sdk` 不拒绝（否则一条
     命令在开发机上永远跑不通），节点则必须拒绝，因为**跑代码的是它**。
     需求串解析错误即使宿主未知也照样报出来，这样 YAML 打错字在作者机器上就
     被抓住，而不是等发到舰队上。
   - **`sdk/register.go`：注册 API，堵住清单与代码无声背离的两个方向**。
     `spec.tools` 是**手写的 YAML**，而扩展实际注册的工具集是**代码**，两者
     可以互相说谎且都不报错：注册了但没声明 → 模型看得见、闸门每轮都拒，
     看起来像工具坏了；声明了但没注册 → 审核通过了一个**不可能被调用**的能力。
     `Registry` 从**注册表**而不是 YAML 建立，`Check(manifest)` 一次报出三类
     分歧（未声明 / 未注册 / **类别不一致**）——第三类才是有牙齿的那类：
     清单里的类别才是闸门用来决定「要不要问人」的值，所以低报类别不是省一次
     审批，而是**改变一次写操作是否需要人文审批**。`ClassUnknown` 被显式拒绝
     而不是提升成 read：零值必须失败关闭。另给 `DeclaredManifest` 做生成侧，
     但**有意不碰 `capabilities` / `safety_level`**——那是操作员的判断，不是
     代码注册表的重新表述，生成它会把一次评审决定变成一次推导。
   - **不对称处留了一句注释**：`Check` 一次报全部三类而不是短路，因为作者要
     一次改完；节点侧的闸门则按「最坏一类」决定，因为闸门要的只是一个裁决。

47. **`sdk.Registry` 接进三个真实发布包：SDK 的检查必须在真数据上跑过**。
    决策 46 把注册 API 加进了 `sdk`，但一个只在自家 fixture 上跑过的检查，
    只能证明它在手工构造的数据上成立。真正的两个方向的分歧——扩展注册了但
    清单没声明、清单声明了但没人注册——都发生在**真实包**里，所以新增
    `core/floor/pluginmanifest/sdk_registry_wiring_test.go`，把
    `sdk.Registry` 建在三个发布包的 shipped toolset 上（名字从
    `tools.go` 里读，与节点构建的是同一份文件），再拿它们各自的
    `pig-ops.yaml` 去 `Check`。
   - **正向用例本身是弱断言**：`Check` 无条件返回 `nil` 也能过。所以三个
     **反证**才是真正的断言，每一个只破坏三类分歧中的一类，并要求 `Check`
     点名它：注册一个未声明的工具；从注册表里删掉一个已声明的工具；
     把 `host_restart_service`（本节点按 `write` 分类的真工具）在清单里
     改写成 `read`。
   - **类别从哪来决定了这个测试有没有意义**：如果类别也从清单里读，
     `Check` 的类别比对就成了同义反复。所以对于**本节点有执行器**的工具，
     类别取自执行器自己的 `EffectiveClass()`（就是调用点会用到的那个值）；
     只有控制面 upcall 工具（节点没有执行器、无法独立分类）才回退到清单。
     既没有执行器、清单里也没有的工具会**报错而不是猜一个类别**——给它填
     `read` 正是类别检查要抓的那种低报。
   - **最后一个用例钉住前提**：上面所有断言都假设
     `readShippedToolNamesFor` 读到的就是节点构建的文件。它加了一条
     canonical（`core/pig/extensions/`）与 packaged 副本的工具名集合比对，
     让这个假设在漂移测试之外也有一条直接的守卫。

48. **节点 agent 补 profile piglet：「拒绝」不等于「不提供」**。
    本轮审计节点 agent 的启动参数时发现一处安全缺口：节点以
    `pig --mode rpc` 启动，**没有带任何 profile**，于是 PiG 的内置工具
    `read / bash / powershell / edit / write / grep / find / ls` 全部对模型
    可见。宿主闸门（`core/edge/policygate`）确实会 Block 任何未绑定的调用，
    所以这不是一个可被利用的提权——但它仍然是一个缺口，理由有三：
      - 节点 agent 是**独立进程**、以节点权限运行，`bash` 在这个进程里等价于
        主机上的任意代码执行。安全边界不能建立在「反正会被拒」上。
      - 每次尝试都要多走一个来回，事故 transcript 里塞满本来就不可能成功的
        调用，噪声会淹没真正的证据。
      - 运维看到 agent 在够一个 shell，还要自己判断这是否被允许——把安全
        属性从闸门挪到人的判断上，是架构的倒退。
    实现落在 `core/edge/agentprofile`（新包，生成
    `opskeeper-node.piglet.yaml`，原子写、0640），由
    `cmd/opskeeper-edge/agent.go` 在 `writeAgentSettings` 之后写入，args 改为
    `{"--mode","rpc","--piglet",profilePath}`。两处设计是有意的：
      - `tools: []` 是**减法**而非加法。`piglet.ScopeTools` 中，profile 未点名
        的 extension 工具一律保留（`toolAllowed(nil, …) == true`），所以已
        准入的插件工具**完全不受影响**；这个字段只减掉宿主自己那个 shell。
      - `discovery.extensions/skills: []` 关闭环境发现，堵住「往 agent home
        里丢一个 skill 就多一个工具」这条路——节点的 skill 集合必须是评审过的
        集合。它**不影响** `settings.json` 里的 `packages`。
   - **`core/pig/pigprofile` 用 PiG 自己的解析器做契约测试**，而不是自写
     YAML 读取器：它直接调 PiG 的 `ParseBytes` + `ScopeTools`，证明内置工具
     确实被移除、extension 工具全部保留，并带三个反证控制。这样上游语义变了
     测试就会红——`agentprofile` 自己的测试则刻意不引入 `yaml.v3`
     （`modulecheck` 会拦），只用手写字段读取器断言**我们自己生成的文本**。
   - **真实二进制 A/B 验证**（PiG 0.3.0+0.87.1，`pig --mode rpc`）：

     ```
     带 profile： [piglet] Applied scoping (session_start): 0/8 tools active, 8 hidden
                {"id":"1","type":"response","command":"get_state","success":true,...}
     不带 profile： （无 scoping 行）
                {"id":"1","type":"response","command":"get_state","success":true,...}
     ```

     8 个内置工具被移除，RPC 指令通路不受影响，两条路径的 `get_state` 都成功。
     这一步补的是 `core/pig/pigprofile` 覆盖不到的部分：契约测试证明「按
     PiG 的语义应当如此」，二进制证明「PiG 确实如此」。
   - **`core/pig/go.mod` 里那条反向依赖是有意的**：`pigprofile` 需要
     `core/edge/agentprofile` 生成的真实文件，而 `edge → pig` 是既定方向。
     所以这里加的是**仅测试用**的 `require` + `replace`，并在文件里注明了
     方向是反的。`modulecheck` 仍然全绿，因为测试依赖不构成发布面依赖。

49. **证据链补上 subject：写动作派发不出去，不是因为找不到 pod 名**。
    本轮审计 9 个「有名有实现但派发不出参数」的动作，结论与字面读数相反。
    `cmd/opskeeper-eval vocabulary` 报的是 `no evidence records this value`，
    读起来像「平台观测不到 pod」，但真正的成因是：**证据链里根本没有
    subject 这一项**。investigator 记录的始终只有 `resource_alert`（告警
    id）、`query_promql`（一个数值）、`query_logql`（一行日志计数）——
    告警**指向哪个实体**这件事，从来没有进入过证据。
    而它是已知的：`alert_incidents.labels_json` 里就写着 `pod=order-svc-7d9`、
    `queue=payments-in`、`unit=nginx.service`，因为告警规则正是靠这些标签
    选中 firing 对象的。所以缺的不是观测能力，是**把已知事实带进证据链的
    那一步**。
   - **新增 `internal/manager/biz/loop/subject.go`**：`resource_alert_labels`
    证据项，逐字记录 firing 告警的标签。刻意与 `resource_alert` 分开命名，
    这样「哪条告警」与「哪个对象」在证据链里可分辨，subject 才能被独立
    重新盖章。
   - **别名表按字段声明，不是按动作随手取键**：`pod` 接受
    `pod/k8s_pod/pod_name/exported_pod/kubernetes_pod_name`，`replicas` 接受
    `replicas/desired_replicas/spec_replicas`，依此类推。**只有被声明的键才
    可能成为参数**——这正是防止别名表退化成注入面的性质：给系统加一个
    `queue` 标签不会让 `k8s.evict_pod` 拿到一个队列。
   - **拒绝规则与既有 pg 提取器同构**：
      - 缺 subject 项 → 拒绝，且消息**指名要记录什么**（`no recorded
        subject names the pod ... one of: pod, k8s_pod, ...`），而不是只说
        「缺参数」。
      - 两个别名键指向**不同对象**（`pod=a` 且 `k8s_pod=b`）→ 判定为矛盾并
        拒绝，把两个值都列出来。这是一条告警在描述本平台尚不理解的东西，
        猜工具要哪个就是在赌。
      - 两个别名键指向**同一对象** → 不是矛盾，折叠为一个。这是同义标签的
        常态。
      - `replicas` 非数字 → 拒绝，并说明它不是副本数。副本数是**决定**，
        证据要么记录了它要么没有；从不记录的数字里造一个目标值，是这套
        机制存在的反面。
   - **生产侧**：`investigatorreal.InvestigatorToolset` 增加可选的
    `AlertLabelsProvider`（`NewWithLabels`），在 `Investigate` 里记录 subject。
    标签查询失败只 warn 不阻断——事故仍可用指标与日志调查，代价是写动作
    带着「缺 subject」的消息拒绝，这比「无参数派发一个写操作」正确。
    `cmd/opskeeper/main.go` 用新的 `AlertLabelsAdapter`（按
    `alert_incidents` 主键回读 `Labels()`）接线。适配器对「id 不是数字 /
    事件不存在 / 标签非法 JSON」一律返回 error 而非空 map：空 map 在下游
    读起来是「这条告警确实没点名任何对象」，会把排查引向证据而不是引向
    这次失败的查询。
   - **宿主强制盖章**：合同里的证据链通常是 investigator LLM 回述的，而
    subject 是承重的——9 个 resolver 的参数全从它读。一个会改写或丢弃标签
    项的总结模型，会把每个写动作**静默地**变回拒绝。理由与宿主自持审计链、
    审批闸门相同：这个事实不能依赖模型对自己输入的记忆。因此
    `StampSubject` 从 plan 里的 toolset 证据把 subject 补回最终合同
    （已存在则不重复；链满 50 条时挤掉最旧一条，最旧观测离结论最远）。
    它被导出，因为这是**已定稿合同**的不变量，不是某个 worker 的私事。
   - **端到端测试走完整条路**（`investigatorreal/subject_e2e_test.go`），
    中间没有桩：告警标签 → 证据链 → 合同 → 参数 resolver → 派发带上
    `pod=order-svc-7d9f2`。反证同在：告警没点名时必须拒绝。
   - **一处被守卫测试挡下的过度设计**：本轮还顺手给 `k8s.rollout_undo` /
    `k8s.uncordon` / `k8s.drain` 加了 extractor（它们的 `deployment` /
    `node` 参数同样来自 subject）。`TestTheResolvableActionsAreAResolvable
    SubsetOfTheLoopVocabulary` 立即判红：这些动作在
    `investigatorreal.RemediationActions` 里没有，investigator 永远不会提议，
    extractor 是死代码。守卫是对的，已撤回。**顺序应当是 investigator 先
    学会提议，再要求派发能力**——反过来做就是为不可达的动作写代码。
     接回 subject 机制后补齐成本很低，但那是另一个决定。

   **效果与边界**：`vocabulary` 闸门的「Loop action executability」一段里，
   9 个动作从「无证据可依」全部变为 `resolvable`（2/9 → 9/9）。
   **但同一份报告的 remediation 轴仍是 3/20，两者是不同的缺口，不要混读**：
     - 本轮闭合的是**参数派发**：闭环已经会提议这些动作，但派发时拿不出参数。
     - 仍然敞开的是**提议本身**：14 个 case 期待的修复动作
       （`k8s.rollout_undo`、`k8s.uncordon`、`k8s.drain`、`kafka.restart_broker`、
       `rabbitmq.purge_queue`、`pg.vacuum_table`、`redis.scan_and_delete`……）
       investigator 根本不会写进合同。它们卡在两处，且**都不该用同一个动作解决**：
       一部分只是命名空间未对齐（`redis.kill_client` ↔ `redis.client_kill`，
       需人裁决），一部分是真实能力缺失（`kafka.*` / `rabbitmq.*` 仍是骨架，
       见路线第 8 条）。
   - 所以下一步不是继续加 extractor，而是**让 investigator 在证据支持的前提下
     真的提议这些动作**——顺序上提议先于派发能力，这也正是上面那条守卫测试
     坚持的（extractor 只能服务于已可提议的动作）。

50. **领域探针：让调查去问，而不是去猜**。
    决策 49 闭合了「派发不出参数」这一层，但同一份 `vocabulary` 报告的
    **remediation 轴仍是 3/20**。本轮查清了它为什么不涨，结论与读数方向相反：
    **不是缺适配器**——闭环要派发的每个工具都已实现；**不是缺参数解析器**——
    决策 49 之后 12 个动作全部 `resolvable`。真正的原因是 investigator
    **物理上无法区分不同故障**。
    它查 Prometheus 拿一个标量、查 Loki 拿一个日志行数，于是每一个
    PostgreSQL 事故都是同一个形状：
    `max(pg_stat_activity_max_tx_duration_seconds)` 加一个数字。表膨胀、
    vacuum 卡住、查询变慢是三种故障、三种修法，在那个分辨率下**完全同形**。
    现有分支靠阈值区分，而「调到刚好分开黄金用例」的阈值不是阈值，是答案本身。
   - **做法是去问**。`pg.table_bloat` 报告每张表的死元组比例，
     `pg.vacuum_status` 报告 vacuum 是否真的在推进，`k8s.node_list` 报告哪些
     节点 NotReady。每一条都是**被记录下来的观测**，由它门控的提议才说得出
     「为什么提这个」。与本仓库其余部分同一条规则：证据，不是推断；
     有歧义，就拒绝。
   - **`investigatorreal` 新增领域探针**（`domain_probe.go`）：按资源类型跑一
     份**只读**调用计划，把适配器返回的原样结果记成证据项。只读不是注释而是
     强制：调查发生在任何审批之前，探针若能到 L2 以上，就等于「为了看一眼而
     执行一次写操作」，所以 `probeRiskCeiling = "L1"` 在运行时校验每个工具的
     自评风险等级，**等级无法解析的按超限处理**——不肯说明自己做什么的工具，
     不该被无提示地调用。计划是数据，因此这条性质由测试守住而不是靠人读。
   - **探针失败只跳过，不阻断**。数据库拒绝连接恰恰是最该调查的事故；因为四
     个问题里有一个没答上来就什么都不记，会让它在最需要的时候最没用。链变薄，
     依赖那条证据的提议就不做。
   - **提议分支按观测门控**，五条，全部有反证：
     - `pg.table_bloat` 有行且 `dead_pct >= 20` → `pg.vacuum_table`（下界写成
       数字而不是按部署调：调到黄金用例刚好的阈值就是答案本身）
     - `pg.slow_log` 有行 → `pg.explain_query`，risk 记 **`safe`**——
       `EXPLAIN` 不带 `ANALYZE`，不执行语句，所以它不承诺任何东西
     - `k8s.rollout_status` 有 `rollout_complete=false` 的行 → `k8s.rollout_undo`
       （**二次过滤**：探针虽传了 `only_stuck`，但一个忽略该参数的适配器不该
       把健康 Deployment 放到回滚提议面前）
     - `k8s.node_list` 有 `status != Ready` 的行 → `k8s.uncordon` + `k8s.drain`
   - **五个 extractor 与提议同源**（`domain_extractors.go`）。三个过滤器
     （`BloatedTableRows` / `StuckRolloutRows` / `NotReadyNodeRows`）**从 loop
     包导出**并被两侧共用：investigator 因为过滤器命中而提议，resolver 只有在
     同一个过滤器命中时才肯指名目标。**两份阈值就是两次对「证据说了什么」产生
     分歧的机会**。规则照旧：单一候选解析，多个拒绝并列出候选，没有则拒绝并
     指名是哪个探针本该回答这个问题。`pg.vacuum_table` 的 `table` 取自膨胀
     最高的那张表——`pg_stat_statements` 本来就按时长排序，取最慢的那条是读
     数据库自己报的顺序，不是挑。
   - **装配顺序调整**：toolset 的构造从「适配器注册表之前」移到「之后」，
     因为它要通过那张表去探领域。`main.go` 用 `.WithProbes(middlewareReg)`
     接线。

   **效果**（`cmd/opskeeper-eval vocabulary`，均可复现）：
   - **remediation 轴 3/20 → 9/20**。解锁 6 个 case：`k8s/deployment-failed`、
     `k8s/node-notready`、`k8s/pod-oom`、`pg/slow-query`、`pg/table-bloat`、
     `pg/vacuum-stuck`。
   - blocked 列表 14 → 11。**平台轴仍 10/20**（本轮不涉及能力注册表）。
   - 剩下 11 个分两类，**不该用同一个动作解决**：
     - **工具不存在**（9）：`host.kill_process`、`host.remove_old_logs`、
       `k8s.cleanup_logs`、`redis.scan_and_delete`、`redis.scan_and_redistribute`、
       `redis.kill_client`（实为 `redis.client_kill` 改名未对齐，需人裁决）、
       `kafka.restart_broker`/`scale_consumer`/`repartition`、
       `rabbitmq.scale_consumer`。
     - **参数是决策而非观测**（1）：`redis/memory-burst` 期待
       `redis.config_set`（要 parameter + value）与 `redis.flushdb`（要 confirm）。
       这三个值都不是「证据里记录了什么」，而是**操作员要做的决定**。给它们写
       猜测式 extractor 就是本文件开头批评的那种事，正确做法是让它们在审批
       环节由人补齐，而不是让代码替人选。

51. **A 类缺口第一批：host 与 redis 的四个工具，两条「参数即决策」的提议**。
    决策 50 把 11 个缺口分成两拨，本轮处理 A 类里命名空间干净、无需人裁决的
    那一部分（mq 命名空间与 `redis.kill_client` 改名各是一次裁决，不在本轮）。
   - **host 适配器新增 4 个工具**（`core/manager/middleware/adapter/host/pressure.go`
     与 `pressure_ops.go`）：
     - `host.top_processes`（只读）：`ps -eo pid=,ppid=,user=,pcpu=,pmem=,comm=`
       在 Go 侧排序，**不依赖 GNU-only 的 `ps --sort`**——本仓库的探测命令
       要在别的平台上也能跑，下界 `cpuFloor = 50.0` 写死为「只返回超阈值
       进程」，不是「调参调到黄金用例刚分开」。
     - `host.old_log_files`（只读）：`find -xdev -mtime +N -printf`，**GNU find
       假设已在注释里写明**；`-xdev` 是为了不让挂载的别的文件系统里
       的文件混进来。
     - `host.kill_process`（**L3**）：**只发 SIGTERM，绝不自动升级 SIGKILL**。
       进程不该因为一次探测就死掉。读回状态；拒绝 pid ≤ 1、拒绝自己 / 父进程 /
       内核线程（`comm` 形如 `[kthreadd]`）。这四条拒绝路径各有反证用例。
     - `host.remove_old_logs`（**L3**）：**重新运行同一个 `find`，不接受调用方
       传来的文件列表**。调用方给的列表是上一秒的世界，删除按它执行就是
       删掉一个可能刚被重建的日志；有 `dry_run`，`protectedPaths` 拒绝系统
       目录。
   - **redis 适配器新增 1 个工具**（`keyspace_ops.go`）：
     `redis.scan_and_delete`（**L3**）。`min_bytes` **必填无默认值**——「删掉
     多少算大」是策略不是事实；`maxDeletePerCall = 100`，**超限拒绝而不是
     部分执行**，因为部分执行会让人以为已经删干净了。`dry_run` 可用；
     报告里明说 **SCAN 是抽样不是普查**，因此返回的字节数是下界不是上界；
     `humanBytes` 同时给舍入值和精确值。
   - **两条提议，其参数是决策，故**只提议**不写 extractor**：
     `redis.info` 的 `used_memory` ÷ `maxmemory` 得填充率，
     - 填充率 ≥ 0.90 且 `maxmemory-policy == noeviction` → `redis.config_set`
     - 填充率 ≥ 0.99 → `redis.flushdb`，risk 记 **`dangerous`**
     - `maxmemory == 0`（未设上限）时**不提议任何内存修复**：比例在无上限
       面前无意义，按比例提议就是拿无意义的量做决策。
     它们的 `parameter` / `value` / `confirm` 由审批环节由人补齐，
     executability 闸门因此诚实地报 `no evidence records this value`——
     **这是正确状态，不是缺口**。这与决策 50 的 B 类同一条规则：
     参数是观测才写 extractor，参数是决策就停在审批。
   - **`host.remove_old_logs` 的 `path` 来自 subject 而非探针**：探针只门控
     「有无可回收文件」，路径取 `subject` 的 `mount` / `path` / `dir` 标签。
     让探针去选目录等于让一次只读探测替人决定删哪里。
   - **`redis.scan_and_delete` 的 `min_bytes` 来自唯一超大 key 的实际大小；
     若不唯一则**拒绝**并列出全部**。取最小值会扫掉未观测的同尺寸 key。

   **效果**（`cmd/opskeeper-eval vocabulary`）：
   - **remediation 轴 9/20 → 13/20**（本轮 3 → 9 → 11 → 12 → 13）；
     **平台轴 10/20 → 11/20**。
   - **blocked 列表 11 → 7**：
     `k8s/pv-full`（`k8s.resize_pvc` 尺寸是决策 + `k8s.cleanup_logs` 工具不存在）、
     `mq/*` 四个（kafka 三个 + rabbitmq 一个，命名空间裁决）、
     `redis/hot-key`（`redis.scan_and_redistribute`——见下）、
     `redis/slow-cmd`（`redis.kill_client` 改名未对齐）。
   - **`redis.scan_and_redistribute` 本轮明确不实现**：Redis 没有
     「重新分配热 key」这种服务端操作。热 key 的真正修法在**应用层**
     （本地缓存 / key 加后缀分片 / 读副本）。造一个名叫 redistribute 却做
     别的事的工具，就是让名字说谎——本文件开头批评的那种失败。
     **需要人裁决**：是改工具名，还是调整黄金语料。
   - **17 个动作里 14 个 `resolvable`**，3 个停在审批
     （`redis.config_set` / `redis.flushdb` / 命名空间待定的 `mq.*`）。

52. **磁盘满：把「申请了多大」和「用了多少」拆成两个工具，量不出来就说量不出来**。
    决策 51 之后 blocked 剩 7 个，其中 `k8s/pv-full` 是唯一一个不需要人裁决的。
    它卡住的根因不是缺提议，而是**整条链上没有任何一处能看见文件系统**。
   - **Kubernetes API 不知道卷用了多少。** 这不是本仓库没实现，是 API 语义：
     `kubectl get pvc` 打印的是 claim **申请**的容量，API server 只把块设备交给
     pod，从不往里看。所以 `k8s.pvc_list`（L0，纯 API 数据）**刻意不带任何
     「 fullness」列**——凭申请量编一个占用率，就是让一个猜测长得像权威数字。
     它的 summary 直接写明这一点并指向测量工具。
   - **`k8s.pvc_usage`（L1）去量。** 做法是：找**正在运行且挂载了该 claim**
     的 pod → 从该 pod 自己的 `spec.volumes[].volumeMounts` 读出挂载点 →
     在那里跑 `df -P -k`。挂载点**不问调用方**：路径是对别人应用布局的猜测，
     猜错了就是「这个卷的名字 + 另一个文件系统的数字」。跑
     `df -P -k` 而非 `df --output=…`——前两个选项是 POSIX，BusyBox 与 coreutils
     都认；`--output` 是 GNU 扩展，distroless 和 Alpine 没有。
   - **它被评为 L1 而不是 L4**，理由与 `k8s.exec_into_pod` 评为 L4 是同一条
     线：**风险等级看调用方能改变什么，不看有没有起进程。** 这里的程序是本
     适配器写死的 `df`，调用方选的是「量哪个卷」，不是「跑什么」。argv 由调用
     方提供的那个仍然是 L4。
   - **量不到就是量不到。** claim 未 Bound、没有运行中 pod 挂载、raw block
     device 没有文件系统路径、distroless 镜像没有 `df`、同一个 pod 在两个路径
     挂载（那是应用的事实，不是集群的）——五种都返回 `measured: false` 加一句
     原因，**且 row 里根本不出现 `used_percent` 这个键**。把「未知」读成 0% 会
     让每一个无权 exec 的 namespace 都产出一条自信的扩容提议，这是本文件里
     最危险的一行，所以由测试钉死。
   - **多副本不构成歧义。** 三个副本挂同一个 PVC 看到的是**同一个**文件系统，
     按名字排序取第一个是**正确采样而不是猜测**，并把来源写进 `measured_from`。
     真歧义是「一个 pod 挂在两个路径」，那个拒绝并列。
   - **探针必须能链式展开。** 「这个卷满了吗」是关于某一个盘的问题，而那个盘
     要集群先回答「有哪些 claim」才知道。所以 `DomainProbe` 增加了
     `ForEach`：`k8s.pvc_list` 先跑，每个 row 展开成一次 `k8s.pvc_usage`，
     `pvc` 与 `namespace` 都取自**同一行**（claim 名只在 namespace 内唯一）。
     展开**不豁免只读闸门**——L3 或等级无法解析的工具在展开后一样不被调用。
     源探针没跑成 / 没行 / 缺列，就**一次都不调用**，绝不带空参数去量
     「凭据默认的那个 namespace」再把答案记在这个 claim 名下。
   - **`ProbeRows` 改为拼接同一工具的多次调用。** 一个探针被调用多次是合法的
     （每个 claim 一次），每次都是一条独立记录；只取第一条会让测了五个卷的
     链去回答其中一个卷的问题，而且**不报错**——它给出的不是更糟的答案，是
     另一个问题的答案。
   - **两条提议，其参数分属两类，都不写 extractor**：
     - `k8s.resize_pvc`（mutating）：`pvc` **可解**——extractor 读的就是 `df`
       真正跑过的那个 claim，并且**按 `namespace/name` 复合身份计数**
       （每个 namespace 都有一个叫 `data` 的 claim；按裸名折叠会把这个判成
       「一致」而解出一个谁都没量过的卷）。`size` **不可解**：扩多大是带 recurring
       bill 的预算决策，证据链里没有「谁决定花多少钱」。extractor **只给
       pvc + namespace**。
     - `k8s.cleanup_logs`（**dangerous**）：`path` **不可解**，且理由与 `size`
       不同——卷里哪个目录是日志，是别人应用的布局，任何文件系统读数都不知道。
       工具**自己**安全地回答这个问题：`dry_run` **默认 true**，先报「能回收
       多少」再等确认。
   - **`k8s.cleanup_logs`（L4）的三条不装饰的决定**：
     - **`truncate -s 0` 而不是 `rm`**：释放块、保留 inode，**正持有该文件
       追加写的应用不用重新学习「我的日志文件不见了」**。`rm` 破坏这个契约，
       而一个把写入方一起带下线的磁盘修复，是用一个更大的故障换了一个故障。
     - **`dry_run` 默认 true**：工具负责提案，人负责处置。**对着一个没被
       想过的默认值**拼出一条看起来合理的调用，正是这条默认值要防的。
     - **文件清单重新跑 `find` 得到，从不接收**：调用方给的是**上一秒的世界**，
       而日志在这中间会被轮转、重写、重建。按它执行就是 truncate 掉那些名字
       上**现在**的东西。报告跑一次，写入前再跑一次。
     - 另外：`path` **必须在发现的挂载点内**（否则会 truncate 容器自己的
       `/var/log` 却把卷的字节报成已回收）；挂载根目录本身被拒；`older_than_days`
       有**下界**（截断正在被追加写的日志会毁掉操作员正在读的那几分钟）；
       超过 `cleanupMaxFilesPerCall` **拒绝而非部分执行**（清 500 个报成功是
       比不清更糟的结果）；系统目录拒绝；`find` 没有 `-printf`（BusyBox）时报错
       而不是去清理它列不出来的东西；**清单有读不懂的行就整体拒绝**——
       部分可读的总和是一个操作员会相信的数字。

   **效果**（`cmd/opskeeper-eval vocabulary`）：
   - `k8s/pv-full` **`ok ... fully servable`**（此前是 blocked）。
   - **平台轴 11/20 → 12/20**；**remediation 轴 13/20 → 14/20**；
     **blocked 7 → 6**。
   - executability：19 个动作里 16 个 `resolvable`；3 个停在审批
     （`k8s.cleanup_logs` / `redis.config_set` / `redis.flushdb`），
     其中 `k8s.resize_pvc` 显示 `resolvable` 是因为 `pvc` 可从证据解出，
     `size` 仍留给审批人——这正是期望状态。
   - 修掉一个真实缺陷：`pvcRow` 曾把 `volumeMode` 的缺省值写成 `Block`，
     于是**每一个普通 claim 都被报成裸设备**，同时把「量它需要挂载点」这件事
     藏了起来。K8s 的缺省是 `Filesystem`，已修并加测试。

53. **产品命名空间委托给中立实现：一套实现、两个命名空间、每个名字一个所有者**。
    A 类缺口第三批处理 mq。这一轮挖出的问题比「4 个 case 没有提议」更深，
    值得按发现的顺序记下来。
   - **发现一：十个工具名在注册表里，但调用全部返回 `not_implemented`。**
     `core/manager/middleware/adapter/mq/{kafka,rabbitmq}` 注册了 10 个工具，
     每个 handler 都答 `not_implemented: pending Task 2.4`。真正的 Kafka /
     RabbitMQ 代码一直在**上一层**（`core/manager/middleware/adapter/mq`），
     挂在 `mq.` 前缀下。于是评测闸门按「名字是否注册」计数，把这 10 个算成
     平台能力。`core/manager/middleware/adapter/skeleton_test.go` 抓住了它并把它
     钉成了已知债务清单；**这一轮把那份清单清空了**。
   - **发现二（更严重）：那两个命名空间根本没有接进运行时的控制面。**
     `cmd/opskeeper/loop_adapters.go` 只接线 `postgres/redis/k8s/mq/git/host`。
     `kafka.` 与 `rabbitmq.` 的唯一调用方是评测闸门自己。也就是说**闸门在为
     一个不存在的机队打分**——报告里的每一个数字都关于一个没人能部署出来的
     部署。这与本文件反复引用的那句话直接冲突（「两个闸门描述同一支机队」）。
     已新增 `OPSKEEPER_LOOP_KAFKA_DSN` / `OPSKEEPER_LOOP_RABBITMQ_DSN` 两条
     独立可选的接线，并加了一条反向测试：闸门计数的每个命名空间都必须在
     控制面里接线，否则失败。
   - **为什么不能从 mq 包直接注册产品名**：`registry.RegisterTools` 拒绝名字
     前缀与资源类型不匹配的工具，这条规则是对的（它防止两个子系统抢一个名字）。
     所以**所有权留在产品包**，实现通过 `mq.Delegate` 到达。委托面的每个方法都
     校验连上的 broker 就是名字里的那个产品——命名空间是**闸门而不是标签**，
     否则 `kafka.repartition` 跑在 RabbitMQ 连接上会找到「最近的、存在的操作」
     并把命名空间变成错误行为上的标签。
   - **`kafka.partition_skew` 说清楚它测的是什么。** Kafka 管理协议**不提供**
     分区级字节/消息速率，所以「哪个分区流量最大」没有 API 可问。它报的是两个
     真实分布：每个分区**未消费记录数**（各 group 已提交 offset 与日志末尾之差，
     跨 group 求和是真实量而不是代理量——两个 group 在同一分区上确实各有未消费
     记录）与其占比，以及**副本同步**（ISR vs replicas）。每行带 `measured` 与
     `not_measured` 两个字段，因为读者找到一个数字而没有依据时，会当它就是自己
     在找的那个。
   - **`kafka.repartition` 是真实 API**（`AlterPartitionReassignments`），
     并拒绝三件 API 会照单全收的事：**副本数被意外改变**（目标列表长度必须等于
     当前副本数，否则要显式 `allow_replica_factor_change: true`——一个单副本
     分区离数据丢失只差一次 broker 故障）、**指向不存在的 broker**（先读活着的
     broker 集合再逐个校验）、**在已有迁移之上再提交一次**（先查 in-flight，
     两次重叠迁移不会报冲突，但会得到谁也没打算要的副本集）。还有一条诚实报告：
     **Kafka 接受请求后是异步迁移**，所以消息写的是「已提交，不是已完成」。
   - **三个名字没有注册，因为没有任何 broker 协议能做它们说的事**：
     - `kafka.restart_broker`——重启进程是 systemd / StatefulSet / 云实例的事，
       不是 broker 客户端的事。
     - `kafka.scale_consumer` / `rabbitmq.scale_consumer`——consumer group 的
       并行度等于加入它的客户端实例数，改它是一次部署。RabbitMQ 有一个形似的
       真实操作（policy 提高 prefetch），但那不是「扩消费者」。
     - `kafka.rebalance_history`——Kafka 暴露 group 的**当前**成员与分配
       （DescribeGroups），没有历史。报当前状态却叫 history 就是答非所问。
     这四个是**关于黄金语料或平台真实能力的命名裁决**，不是实现缺口。
     把它们注册成桩会让能力闸门回到发现一的状态，所以它们现在是能力报告里
     可见的缺口，而不是让报告说谎的桩。这与 `redis.scan_and_redistribute`
     是同一条规则（决策 51）。
   - **`rabbitmq.queue_depth` 与 `rabbitmq.consumer_status` 是两个问题，不是
     一个工具两个名字。** 管理 API 在同一个 queue 文档上同时返回深度与消费者数，
     所以底层是一次调用——但读到的东西不同、失败方式也不同。深度是「还堆着多少」，
     按最深优先；消费者覆盖是「谁在读」，按每条消息需要多少个消费者排序。40 万条
     消息 4 个消费者是健康的，同样的队列 0 个消费者是没人发现的事故，而**按深度
     排这两个队列在同一个位置的同一个理由上**。
   - **两条提议接进闭环**（remediation 轴 14/20 → 15/20）：
     `kafka.repartition` 由 `kafka.partition_skew` 观测门控，探针用
     `ForEach` 从 `mq.inspect_consumer_lag` 的行上展开 topic（一个 topic 没人
     落后就不该去量它的倾斜，否则会为一个健康 topic 提一条没有意义的复制）。
     extractor 给 `topic` + `partition`，**`to_brokers` 不给**——放哪台 broker
     需要活的 broker 列表、机架与既有负载，证据链里一条都没有。
   - 顺手修掉一个真实缺陷：`consumer_status` 的排序键用 `-1` 表示「没有消费者」，
     在**降序**排序里这个值排最后，与注释写的「排最前」完全相反；更糟的是它
     把一个不是任何东西计数的 `-1` 发布成了「每消费者消息数」。已改为
     「(stalled, 每消费者消息数)」双键排序，无消费者时不发布该字段。

   **效果**（`cmd/opskeeper-eval vocabulary`）：
   - **平台轴 12/20 → 13/20**（`mq/partition-skew` 变为 `fully servable`）；
     **remediation 轴 14/20 → 15/20**；**remediation 侧 blocked 6 → 5**。
   - `middleware-adapter` 符号数 **98 → 108**（10 个桩变成 10 个真实工具）。
   - 能力报告变得更诚实：`kafka.rebalance_history` 从「被算作有」变成一条
     可见的缺口。这是数字下降方向上的正确变化。

54. **根因轴上的两块证据：`pg.replication_status` 与 `k8s.top_pods`**。
    这一批补的是**诊断面**（观测），不是提议面（修复），所以 remediation 轴
    不动、平台轴 +1。两块工具的写法和它们拒绝做的猜测同样重要。

    - **`pg.replication_status`（L1）读 primary 的 WAL sender 视图**，不是
      standby 自己的 `pg_last_wal_replay_lsn()`。这个适配器连的是一条 DSN
      （通常就是 primary），而 `pg_stat_replication` 在 primary 上、一次连接
      就能看到**所有**连进来的 standby；standby 自身视角只回答它自己、而且要
      另一条 DSN。选前者是「一次连接回答整个集群的复制问题」。
    - **两个 lag 数字都报，因为它们回答不同的问题、且互不可推导**：
      `replay_lag_bytes` 是事故期间一直在长、运维据此估修复量的那个量；
      `replay_lag_seconds` 是 RPO 声明用的单位。一个 standby 可以落后 1 GiB
      却只慢 2 秒，也可以落后 1 MiB 却慢 20 分钟（时钟或长事务）。
    - **NULL 不是 0。** `pg_wal_lsn_diff` 在任一参数为 NULL 时返回 NULL，
      而 NULL 的 `replay_lsn` / `replay_lag` 意味着这个 standby 还没上报位置
      （刚建连、或还在从基础备份追赶）。适配器把这个 NULL 原样带出去，
      测试钉住「未知不能变成 0」——把没上报当成追平，正好会在最需要看见
      它的时候说"复制正常"。
    - **空结果不是零延迟**：空的 `pg_stat_replication` 最常见的意思是
      「这个实例不是 primary」或者「DSN 指错地方了」。
    - **顺手修掉一个真实缺陷（这一批唯一的非新增代码改动）**：`Diagnose`
      只在 `len(rows) > 0` 时附上 category 的 suggestion，而 replication 的
      suggestion 原文就是写给**空结果**的——它永远不会出现。修法不是把闸门
      改成"总是附上"：另外 6 条 route 的建议都是「怎么处理你看到的行」
      （"对阻塞别人的 pid 用 pg.kill_session"），挂在空结果上是误导。
      新增了 `emptySuggestion` 字段，只有声明了它的 route 才在空结果时说话。
    - **`k8s.top_pods`（L1）报的是"每个容器对其自身 limit 的占比"，不是
      节点容量占比。** 一台 40% 内存的节点上可以有一个容器正踩着自己 100%
      的 limit 等被杀；一台 95% 的节点上可能每个容器都在自己 limit 的一半、
      压力完全来自别处。节点容量回答"这台机器满了吗"，这个工具回答"哪个容器
      没地方了"——对 CrashLooping 的 Pod，这是同一个词写成的两个不同问题。
    - **OOM 证据读两处，因为两处都有用**：当前状态
      （`state.terminated.reason`）是 Pod 现在的样子，而一个被杀后已经重启的
      容器这里通常是空的；上一个状态（`lastState.terminated.reason`）能活过
      重启，它才是"这个容器死于 OOM 而不是坏镜像或探针失败"的证据。只报当前
      状态的列表会在 Pod 看起来健康时展示 restart_count 一路涨。
    - **无 limit 不是 0%。** 没有 limit 的容器被标成
      `mem_limit_pct_unlimited` 并且**不发布百分比**：这两种是相反的发现，
      无 limit 意味着它可以吃掉整个节点、更危险，而给它印一个 0% 会让它
      排在"最安全"的位置。排序也据此把未知 limit 放最后——它们不是安全，
      只是没被测量，排在最前会把真正要 OOM 的容器埋掉。
    - **没装 metrics-server 报错，不报空列表**：空列表在这个工具里的读数是
      "没有容器接近自己的 limit"，恰好是没人能判断的时候最不该说的话。
      错误消息点名 metrics-server 是可能的缺失项。
    - **没有 per-container 分解的 metrics 条目不丢**：Pod 级 usage 仍然是
      一次真实测量，丢掉它会让一个吃内存的 Pod 变得不可见；此时用它自己
      spec 里的第一个容器的 limit 作为最接近的答案，并保持 container 为空。
    - **测试**：k8s 侧 5 条（自身 limit / 无 limit 不是 0% / 未知 limit 排序 /
      无 per-container 分解 / metrics-server 缺失），postgres 侧 3 条
      （NULL 不是 0 且查询确实用 `pg_wal_lsn_diff` / 两个 lag 各报各的 /
      空结果不是零延迟）。每一条的断言都是"未知不是零"这一条规则的一个面。

    **效果**（`cmd/opskeeper-eval vocabulary`）：
    - `pg/replication-lag` **`ok ... fully servable`**；**平台轴 13/20 →
      14/20**；remediation 轴与 blocked 列表不变（15/20、5 个）。
    - `k8s/pod-oom` 从「1 root cause + 2 缺口」变成只剩
      `git-artifact.LinkK8sImage` 一条。
    - `middleware-adapter` 符号数 **100**（以闸门输出为准，不手工维护）。

55. **导入器有了入口，且入口不把它当成 install**。
    `pluginimport` 到决策 54 为止是一个**有实现、有测试、没有调用方**的库——
    仓库里最像"已完成"的那种缺口。这一轮补的是入口：`POST /v1/marketplace/import`
    （admin），multipart 上传一个存量容器 → 解包 → 转换 → 写到
    `OPSKEEPER_PLUGIN_IMPORT_DIR` 下的 `<包名>/`，响应带 `dest` + 转换报告 +
    **仍未决的清单**。
    - **它和 install 不是一件事，也不共用一条路。** install 的意思是"这个包
      已被准予使用"；import 的意思是"这个包得先说明自己是什么"。转换产物是
      **惰性的**：无工具、无 scope、无审批，`safety_level: L1`，
      `install.strategy: pin`。一个猜着填完这些字段的转换器，等于声称自己评审过
      它只读过文件名的代码；所以那些字段以 `Decision{Field, Question, Why}`
      的形式回到调用方，`Why` 说的是"为什么推不出来"，而不只是"缺个值"。
    - **写进 import root，不写进节点。** 转换的全部意义是把评审逼出来；
      直接下发会跳过这一步。运维答完 decisions，再走第 16 条那套
      `/v1/plugins/releases` 发布。
    - **这一轮挖出的真实缺陷（`descendSingleDir` 的无条件下钻）。**
      归档常见"外面套一层目录"，所以解包后要下钻一层；但原来的
      `descendSingleDir` 只要顶层**恰好只有一个目录**就下钻。而一个裸
      skills.sh 包打成 zip 时，顶层唯一的目录**正好是 `skills/`** —— 于是它被
      当成"套壳目录"钻进去，再对 `skills/` 本身找容器标记，得到一句
      "no recognized pack layout"。修法是：**只在顶层不被识别为容器时才下钻**
      （`chatruntime.DetectContainer` 判），而这是同一个识别意见，不是第二个。
    - **解包目录名要取归档名。** 裸 skills.sh 包没有 manifest，loader 从**目录名**
      合成包 id；解到随机 temp 目录里会给每个裸包起一个 temp 目录的名字。
      `archiveBaseName` 把归档名裁剪成包名允许的字符集（`../../etc` → `etc`），
      且这个名字只会拼到本进程刚创建的 temp 目录上——一个"推理过才知道安全"的
      名字，离被拼到别处只差一次重构。
    - **同名包拒绝覆盖（409）**。第一次转换里的 decisions 可能已经被人答过，
      覆盖/合并会留下没人评审过的文件（与 `pluginimport.Import` 自己的拒绝
      同一条规则）。**未配置时答 503 并点名 `OPSKEEPER_PLUGIN_IMPORT_DIR`**：
      500 会把运维引去找一个根本没打开的转换器的 bug。
    - **测试 6 条**：非 admin 不得到达转换器、未配置是 503 且点名配置项、
      `.claude-plugin` 容器端到端落盘（`pig-ops.yaml` 存在、`commands` →
      `prompts` 的改名生效、decisions 含 `spec.tools`、根目录只剩那一个包）、
      第二次同包 409 且旧包未被动过、裸包按归档名命名、非容器 400 且不留残留。

56. **内置 ops piglet 建出来了，而且它必须待在它所编排的包里**。
    C 阶段此前只有**节点侧生成 profile**（`core/edge/agentprofile` 在准入之后
    按"这台机器实际收下了哪些包"写 `opskeeper-node.piglet.yaml`），没有一个
    **第一方组合**可供开发者在工作站上直接跑，也没有一个可供节点 profile 对照的
    参照物。这一轮补上
    `plugins/pig-ops/opskeeper-sre-readonly/pig-opskeeper-ops.yaml`：一句
    `pig --mode rpc --piglet <该文件>` 就得到一个**已上闸门**的只读运维 agent。
    - **它和节点 profile 的关系是"同一条安全线，不同的名单"。** 两者都
      `tools: []`（摘掉 PiG 8 个内置工具含 `bash`），都 `discovery: []`
      （无环境发现）；分歧只在**点名了哪些 extension**——节点 profile 说
      "这台机器收下了什么"，内置 piglet 说"第一方组合是什么"。节点 profile
      依然由 `core/edge/agentprofile` 生成，两者不是谁替代谁。
    - **它必须在 `opskeeper-sre-readonly/` 包内，不能点名兄弟包。**
      PiG 的 loader 拒绝任何逃出 piglet 自身目录的 `local:` origin
      （报 `escapes the Piglet anchor`），所以一个点名兄弟包的组合**先得把那个
      包复制进来**。这里没有东西可复制——origin 本来就是本包自己的
      `extensions/` 与 `skills/`。跨包组合要付一次生成式复制，那是这份组合
      不需要的机械。**observability 包与 repair 包有意不在其中**：前者按节点
      装，后者是 L2 写操作。
    - **每条 skill 单独成 entry，指向自己的目录。** 包自己的 Pi manifest 可以
      让一个 entry 指到父 `skills/` 目录，但 piglet 的一个 skill entry 解析成
      **一个** skill：PiG 的 baker 读那个路径，发现多于一个定义、名字又对不上
      就拒绝——父目录形态"能通过校验、然后构建失败"。逐条点名也让 8 个 skill
      （`diagnose-readonly` + 7 个 persona）变成一张可评审的清单，而不是一次
      目录 glob。
    - **工具清单写死、不留给 extension。** 信使 extension `opskeeper-gate`
      的 `tools: []` 是**显式写出**而非省略：省略的意思是"这个 extension
      注册什么就是什么"，而对那个职责是坐在每次调用前面的 extension 来说，
      "它以后注册的"恰恰是最不该承诺的东西。只读工具集 `opskeeper-sre-readonly`
      的 18 个名字同样写死，于是 extension 后来加一个工具，会先以**一行 diff**
      出现在这里（reviewer 得接受），而不是作为一个能力凭空出现在每台节点上。
    - **两个模块各验一半，各有负向对照。**
      - `core/pig/pigprofile/piglet_contract_test.go`（PiG 侧）：PiG 自己的
        parser + `Validate()` 接受它、builtin 工具列表恰好为空、每个 origin 经
        `ResolveSkills`/`ResolveExtensions` 解析成功、每个 skill 目录有
        `SKILL.md` 且目录名 == entry 名、每个 extension 目录有 `go.mod`。
        最硬的一条是用 `piglet.ScopeTools` 比对：**从 extension 的 `tools.go`
        AST 抓 `Name:` 字面量**得到注册集，断言它恰好等于 piglet 声明的列表，
        且 `bash` 与 8 个 builtin 都不在。负向对照两条已经实测：删掉声明的
        `host_mtr` → scope 测试报 "piglet allows […] vs registers […]"；
        删掉 `opskeeper-critic` skill → 解析报缺 critic。
      - `core/floor/pluginmanifest/piglet_composition_test.go`（治理侧）：
        每个 origin 反推回包根 → `Load(root)`，断言
        `HighestCapability() == ClassRead`、`SafetyLevel == L1`、且
        `Spec.Tools[].Class` 全是 `ClassRead`；再用 `toolsNotMatching`
        **双向**断言 piglet 的工具名集合 ↔ `pig-ops.yaml` 的 tools 集合相等
        （missing 与 extra 都要报）。负向对照：`toolsNotMatching` 能说不。
        这里用**宽松的 `yaml.Unmarshal`**——piglet schema 是 PiG 的，PiG 加
        字段不应让治理检查失败。注意 `local:extensions/opskeeper-gate` 会被解析
        到**同一个包根**：gate 是包内 extension，不是独立包。

57. **manager 模块迁移卡在一个计划没写的结构问题上：共享底座放哪儿**。
    计划 §四 A-1 说"按 2.1 迁移包路径"，2.1 的表把 `internal/manager` +
    `internal/iam` 划给 `core/manager`。这一轮把这条搬迁的实际形状量了出来，
    结论是**它不是一次纯路径重写**——`internal/manager` 没法只带着自己的目录搬走。
    - **它依赖哪些根模块代码**：27 个 `internal/pkg` 子包（`errs` 169 处、
      `tenantctx` 73、`tunnel` 48、`llm` 25、`promquery` 22、`logquery` 16、
      `prom` 14、`auth` 11、`notify` 10、`tracequery` 8、`qdrantx` 8、`dbx` 8……），
      外加 `internal/middleware`（27 文件）、`internal/skill`（22）、
      `internal/control`（18）、`internal/dataguard`（12）、`internal/knowledge`（6）、
      `internal/agentteams`（3）、`internal/observability`（1）。
    - **量级**：`internal/manager` 128,532 行 + `internal/iam` 4,504 行（845 个 Go
      文件，含测试），再加上面这些树（`internal/pkg` 15,592、`internal/middleware`
      15,833、`internal/skill` 3,598、`internal/control` 2,819、`internal/knowledge`
      1,925、`internal/agentteams` 1,539、`internal/dataguard` 1,303、
      `internal/observability` 141），合计约 **17.6 万行**，占全仓非测试 Go 的
      **76%**。它们一起搬走之后，根模块就只剩 `cmd/`。
    - **共享面其实很小**：把两侧（manager/iam 与 edgeagent/`cmd/opskeeper-edge`）
      各自用到的 `internal/pkg` 子包求交，只有 **4 个**——`config`、
      `pluginmanifest`、`prom`、`tunnel`——再加 `internal/skill`（edge 侧 9 文件）。
      底座基本可以按平面切开，卡住的只是这 5 个。
    - **为什么这 5 个进不了 `core`**：`core` 的规则是 standard library only
      （`scripts/modulecheck` 强制）。而 `prom` 拉 `prometheus/client_golang`，
      `tunnel` 拉 `geminio`（网络库），`pluginmanifest` 拉 `yaml.v3` + `sdk` +
      `core/edge/policygate` + `internal/skill`。真正能进 `core` 的只有契约形状的
      那两个：`errs` 与 `tenantctx`。
    - **于是只剩三种放法，每种违反一条既定规则**：
      - **(a) 加第 7 个模块**（如 `core/floor`），只装这 5 个包 → 违反计划
        "6 个 `go.mod`"的字面，但保住依赖方向（`manager → floor ← edge`）
        与"`core` 零基础设施"。
      - **(b) 底座留在根模块，`core/manager` 反向 require 根模块** → `cmd/`
        又要 require `core/manager`，形成模块级 require 环。Go 允许，
        但两个模块从此不能独立发布，"模块级收口"这个目的就没了。
      - **(c) 底座放进 `core/edge`，manager 依赖 edge** → 得到
        `manager → edge` 这条计划没写的边：控制面反过来依赖节点面，方向是反的。
    - **推荐 (a)**：它是唯一保住计划"依赖方向"那一段
      （`manager → pig`、`edge → pig`、`manager/edge/harness → core`、
      `plugin-sdk → core`）的选择——多出来的 `manager/edge → floor` 是这套规则的
      延伸，不是例外。代价是"6 个模块"变成"6 个架构模块 + 1 个共享底座模块"。
    - **这一轮不动手**：选 (a)/(b)/(c) 会直接决定 `core/manager` 的依赖清单长
      什么样；先搬再改等于把 17.6 万行搬两遍。裁决之后搬迁本身是机械的，
      `scripts/modulecheck` 就是闸门，可以分批搬、每批全绿。

58. **把声明真的跑起来：两个"会跑的"闸门里，一个从来没跑过，另一个有一条规则从来没生效**。
    计划 A-1 说"依赖方向落到 `.go-arch-lint.yml`"。这一轮没有假定它成立，而是把两
    条声明都执行了一遍，结果两条都有问题。
    - **`make arch-lint` 说"go-arch-lint 没装就静默跳过"，但它没有跳过——它报错。**
      那个 guard 写成一个 `|| { …; exit 0; }` 块，而 `exit 0` 只结束那一行的
      shell：下一行 `go-arch-lint check` 照跑，于是"静默跳过"实际是
      `make: *** [arch-lint] Error 1`（exit 2）。改成单个 `if` 块之后，
      该 target 真的按文档行为返回 0 并打印警告；另加 `make arch-lint-run`
      用 `go run github.com/fe3dback/go-arch-lint@latest check` 把它跑起来
      （不安装、首次需要网络）。
    - **`.go-arch-lint.yml` 在当前 linter（v1.19，支持 arch file 1..3）下根本跑不起来**：
      它声明了一个不存在的目录 `core/edge/data/**`，还有三条
      `mayDependOn: []` 的规则——validator 把"空列表且没有任何
      `anyProjectDeps`/`anyVendorDeps` 标志"判为误配置。"不依赖任何东西"只能用
      **省略规则**表达，这一点连同"每条规则必须列出它自己"（这个 linter 不把
      组件对自身的依赖当作隐式允许）一起写进了文件注释。另外补了
      `allow.depOnAnyVendor: true`：这份文件讲的是**组件**方向，vendor 图是
      go.mod 的事，为每个组件再维护一份 vendor 白名单不产生任何架构陈述。
    - **修好之后第一次真正运行：1272 条 → 修掉配置错误后 210 条**（都是真实的
      组件方向问题，不是解析噪声）。分布：`cmd` 75（依赖 15 个**未声明**的树：
      `internal/skill`、`internal/middleware`、`internal/dataguard`、
      `internal/control`、`internal/knowledge`、`internal/agentteams`、
      `internal/observability`、`internal/migrate`、`internal/migrator`、
      `internal/higress`、edgeagent 的采集器/插件子树、
      `core/harness/{projection,vocabulary}`、`core/edge/agentprofile`）、
      `manager_biz` 39、`manager_server` 32、`shared_pkg` 32、
      `oxharness_injector` 14、`iam_biz` 5、`manager_service` 4、
      `edgeagent_biz` 3、`iam_server` 3、`manager_data` 2、`oxpig_agent` 1。
      这一轮已经把**缺失的组件**（11 个 shared floor + edgeagent 数据面 +
      tests/scripts + 4 个 2.0 包）补进 components。**没有**做的是：给这些组件定
      "共享底座不得依赖任何 BC"这条策略——那是一个设计判断（哪些树是 floor），
      不是机械补全；在那之前把当前 import 全量写进 `mayDependOn` 会把闸门变成
      一份现状快照。
    - **收尾（本轮完成）：210 条 → 0 条，`make arch-lint` 现在 exit 0。** 清零
      分四步做，每一步都留下可复查的依据，没有一步是"把数字按下去"：
      1. **先定 floor 语义，再补 `mayDependOn`**：`internal/{skill,middleware,
         knowledge,control,dataguard,agentteams,observability,migrate,migrator,
         higress}` 与 `core/{domain,ports,wire}` 被认定为**共享底座**，规则写成
         "底座不得依赖任何 BC，只允许依赖契约层和自己"。补进去的每一条都是
         *已经存在* 的合法 import，不是新授权；反向（底座 → BC）仍然禁止，
         并且有 `scripts/modulecheck` 独立复核。
      2. **测试与生成物排除，各有理由（写进 `excludeFiles` 注释）**：`_test.go`
         是边界被演练的地方而不是被违反的地方，与 modulecheck 的例外同一把尺；
         `plugins/pig-ops/*/extensions/**` 是 `scripts/sync-pig-ops.sh` 从
         `core/pig/extensions/**` 复制出来的分发副本（同一份代码已被 `oxpig_ext`
         覆盖）；`web/node_modules/**` 是第三方 vendored 代码。排除后 438 → 261，
         再补规则到 0。
      3. **真实违例进债务账本，不静默放行**：清零后剩下的**生产级**违例只有
         4 个文件 6 条 import——`internal/iam/server/http.go` 复用 manager 的
         HTTP middleware 与 audit（3 条）、`internal/manager/server/imbridge/http.go`
         复用 `iam/model`（1 条）、`internal/iam/biz/sso/service.go` 直接 import
         `iam/data/sso`（1 条）、`manager/biz` 6 个文件直接 import 自己的
         `data/*/store` 或反向 import `manager/service`（6 条，其中 5 条已由
         modulecheck 的 `layerDebt` 记账）。这些以 `mayDependOn` 放行，但每条都
         配一段 `# !!! 已知债务` 注释写明**具体文件**，并注明"arch-lint 的白名单
         是组件粒度的，同组件新增同类导入不会再报警"——这是账本，不是例外。
         它们全部落在决策 57 的 manager/iam 迁移范围内，迁移时随条目一起删。
      4. **`deepScan` 显式关掉，并说明关掉的是什么**：打开后它报 41 条，
         其中绝大多数是假阳性——六边形结构本身（`core/ports` 的接口在 `cmd`
         里由 `manager_biz` 实现，被算成 "oxcore_ports 依赖 manager_biz"）与装配
         层（`cmd` 定义的类型注入 `manager_biz`，被算成反向依赖）。但它也抓到
         4 条**真问题**：`internal/pkg` 与 `internal/skill` 通过包级
         `Set*Resolver`（`skillbuiltin.SetWebSearchConfigResolver` 等）反向拿到
         `manager/biz/setting` 的实现，绕过了 import 图。用 `mayDependOn` 修这
         类问题等于给 `internal/skill` 发一张 import manager 的许可证——所以
         它**不**进白名单，而是逐条记进本文件的「当前真实缺口」，修法是拆掉
         包级 setter（同属决策 57 范围）。
         关掉之后逐条核对，41 条里只有 **9 条**需要记账，其余 32 条是上面两类
         假阳性（`gate` 是 `cmd/main.go` 的装配点，或 `core/ports` 的接口被
         实现）。9 条按性质分三组：
         - **端口被实现（合法，架构本来如此）3 条**：`core/ports` 的
           `Completer` 被 `cmd/opskeeper-eval/judge.go` 与
           `core/harness/runner/loop.go` 满足。`core/ports` 是接口，实现方
           落在 harness 里是端口的用途，不是 import 违规。**不记账。**
         - **已进白名单的层跨越 4 条**：`manager_server`/`manager_service` 直接
           注入 `manager_data` 的 `store.Repo`——与决策 58 第 3 步记的
           `manager_biz → manager_data` 是同一族债务（手术未做，账已记）。
         - **装配层注入 2 条**：`cmd` 定义的 `edgeAuthAdapter`、`slogAdapter`
           等被注入 `manager_server`——方向与 import 相反（是 cmd 依赖 manager，
           不是 manager 依赖 cmd），属假阳性。**不记账。**
         - **真实且未记账的 1 类（新发现）**：`internal/pkg` 自己持有
           `manager_biz` 的实现类型——`core/manager/pkg/llm/pigsettings.go:41` 的
           `NewSettingsSource(catalog ProviderCatalog)` 收的是一个
           **`pigmodel.SettingsSource`**，也就是 `internal/pkg`（共享底座）
           反向依赖 `core/pig`；同族的还有 `core/manager/pkg/llm/client.go:165`、
           `core/manager/pkg/prom{query,write}/client.go` 的 setter。
           `internal/pkg → core/pig` 正是决策 57 说的"共享底座放哪儿"问题，
           在此又被独立复现一次。已记入「当前真实缺口」。
    - **`scripts/modulecheck` 的第四、五条规则从来没生效过。** 它把
      service→biz←data 的方向写成 `strings.HasPrefix(rel, from+"service/")`，
      而 `from` 是 BC 的**标签**（`iam`）、`rel` 是仓库相对**文件路径**
      （`internal/iam/biz/sso/service.go`）、`rest` 是**导入路径**
      （`internal/iam/data/sso`）——三者没有一个以 `iam/service/` 开头，所以那两条
      case 永远为假。它一直在报告"边界全部成立"，因为它什么都没检查。
      - 修法：新增 `bcDir(label)` 返回 BC 的目录前缀（`internal/iam/`），用它
        比较；加回归测试 `TestABizPackageMayNotReachItsOwnDataLayer`（先红后绿，
        反证已实测）。
      - **一修好就抓到 12 条真实违规**：6 个生产文件（`iam/biz/sso/service.go`、
        `manager/biz/aiops/proposal/expiry.go`、`manager/biz/audit/{chain,usecase}.go`、
        `manager/biz/edge/changeevent/usecase.go`、
        `manager/biz/loop/contractloader/contractloader.go`）与 6 个测试。
      - 测试豁免这条规则（一个 biz 测试装一个真 store 是集成测试，与跨平面
        例外同一把尺子）；6 个生产文件进 `layerDebt` 账本，每个带原因，并由
        `TestTheLayerDebtLedgerIsCurrent` 守着——文件被删、或不再 import 自己的
        data 层，条目就过期，测试报"债务已还清，删掉这一条"。这是**债务清单，
        不是例外清单**：这 6 条里没有任何一条是某个决策要求的。
    - **净效果**：`make module-check` 从"4 条规则（2 条是死的）"变成"5 条规则全部
      生效 + 一份可核销的债务账本"；`.go-arch-lint.yml` 从"跑不起来"变成
      **"跑得起来且报 0 条"**（过程数字：1272 → 210 → 438【含测试/生成物】→
      261 → 0）。两个闸门现在是互补的：arch-lint 管**组件方向**（含跨 BC import、
      层跨越），modulecheck 管**模块边界 + PiG 收口**（哪些 go.mod 能 import
      PiG、service→biz←data 的层规则、债务账本是否过期）。数字清零不等于债务清零
      ——债务被逐条写进了两处账本，可查、可核销。

59. **中间件适配器打包成节点插件包 `opskeeper-sre-middleware`：一个生成器、两份
    排除账本、53 个只读工具**。
    - **为什么要单独成包**：控制面 registry 里一直有 pg / redis / k8s / mq / kafka /
      rabbitmq 六个适配器的只读工具，但它们只存在于控制面，节点侧 persona 看不到，
      golden case 的 pg / redis / k8s / mq 家族因此一条包都命中不了——`plugin-coverage`
      上一轮是 **2/20**。这一轮把它们（52 个 middleware 读 + `git.find_runtime_link`）
      生成进一个独立 extension 包，数字变成 **20/20**。
    - **真相源是适配器本身，不是清单**：`core/manager/middleware/toolset.Registry()` 跑活
      注册（8 个适配器、53 个读工具），`core/pig/extensions/opskeeper-sre-middleware/tools.go`
      由它生成，`TestToolsetMatchesTheAdapters` 逐字节比对；重新生成是
      `OPSKEEPER_UPDATE_TOOLSET=1 go test ./core/manager/middleware/toolset/ -run Toolset`。
      手写清单会漂，而漂的方式恰好是"包声明了一个适配器已经改名的工具"——一个只有
      在节点上才会暴露的失败。
    - **两份排除账本，各有理由**：`NotPackagedFamilies = {host}`——host 适配器是
      remediation 适配器（以 root 执行写），本机的只读探针已经由
      `opskeeper-sre-readonly` 的 18 个工具 + `get_host_load` 服务；`NotPackaged` =
      7 个 `git.*` 仓库读——与 `opskeeper-sre-observability` 的 `source` 家族重复。
      两份账本都由 `TestTheNotPackagedLedgerIsCurrent` **双向**守着：条目过期报错，
      没被解释的读工具也报错。
    - **`git-artifact` 从"非包家族"里删掉了**：它原本被写成"任何包都服务不了的关联"，
      于是 `k8s/pod-oom` 结构上永远不可通过。实际上 `git.find_runtime_link` 就是服务
      它的那个工具，所以它是一条能力家族（`CapGitArtifact`），不是一条例外。删掉
      这条账目之后 `MiddlewareFamilies` 从 6 个缩到 2 个（`host`、`git`）。
    - **upcall 通道只放读**：`cmd/opskeeper` 的 `runMiddlewareTool` 在派发前**重新推导**
      风险等级（L0/L1 放行，L2/L3/L4 拒绝并指向审批路径），因为这条通道没有审批队列。
      装配层因此有**两个 registry**——aiops registry 答"关于集群的问题"，middleware
      registry 答"伸进系统的工具"，生命周期不同；派发按**前缀路由**而不是
      lookup-order fallback，否则同名工具会互相遮蔽，而两个 surface 是不同的人
      为不同的理由审的。
    - **已知精度代价**：`plugin-coverage` 的连接是**家族级**的——一个包声明了 `pg`，
      任何 `pg.*` 期望都算被覆盖。这是 `CoverageOf` 写明的选择（"一个错的缺口报告
      比一个慷慨的更糟"），所以 20/20 的含义是"这个家族有包在服务"，不是"每个方法
      都存在"；真正按名字判定的轴是 `loopActionExecutability`（只认精确符号）。
      把家族声明升级成逐方法声明是 `sdk` 的后续能力，不是这一轮的假设。

60. **决策 57 的 (a) 落地：第 7 个模块 `core/floor`，以及 modulecheck 的一个盲区**。
    决策 57 把三种放法量了个遍，推荐 (a) 但没动手，理由是"先搬 17.6 万行再改等于搬两遍"。
    这一轮把 (a) 执行了：共享底座不再是一个待决问题，而是一个目录。
    - **搬了什么**：`internal/pkg/{config,httpserver,logger,pluginmanifest,prom,tunnel}`
      与 `internal/skill`（含 `builtin/` 的 13 个 host 探针与 `restart_service`），
      共 7 棵树。全仓 181 个文件的 import 路径按**最长前缀优先**重写，
      `.go`/`.yml`/`.yaml`/`.sh`/`Makefile` 一并覆盖。
    - **为什么叫 floor 而不是继续叫 pkg**：它现在是一个能自己发布的模块，
      名字要能回答"谁能依赖我"。floor 描述的是位置（两个平面共用的地基）
      而不是内容，内容会继续长——`internal/pkg` 这个名字在 2.0 里只会越用越少。
    - **为什么不放进 `core`**：`core` 的规则是 standard library only
      （`scripts/modulecheck` 强制）。`prom` 拉 `prometheus/client_golang`，
      `tunnel` 拉 `geminio`，`pluginmanifest` 拉 `yaml.v3` + `sdk`——放进 `core`
      等于让每个只想读一个契约类型的插件都去解析 Prometheus 客户端。
    - **`internal/skill` 为什么也进来**：它不是 manager 的私产，也不是 edge 的私产。
      edge 侧要跑 13 个 host 探针，manager 侧要用同一份权限等级表与溢出处理；
      两边各拷一份就是两份会漂的真相。它进 floor 之后，
      "宿主持有工具实现、插件只声明工具名"这条 4.3 的架构约束第一次有了
      编译期载体。
    - **`core/edge/policygate` 只做 test-only 依赖**：`pluginmanifest` 的
      治理检查要断言"审批半径"的语义与 gate 的策略引擎**完全一致**——两边
      各自解释 `pod`/`namespace`/`cluster` 就是一次迟早会漂的翻译。
      这个依赖只出现在 `_test.go` 里，floor 的 `go.mod` 因此显式 require
      `core/edge` 并写明原因；生产图上 floor 与 edge 无依赖。
    - **`oxfloor` 的允许集**：`core` 的契约子包、`sdk`、以及三个它真的在说的
      格式/协议库（`prometheus`、`geminio`、`yaml.v3`）。没有 BC，没有
      `internal/*`，没有 `core/pig`——`internal/pkg → core/pig` 那条决策 58
      发现的反向边，在 floor 里是模块级编译错误。
    - **modulecheck 补了两处**：① 加第 7 条 module rule
      （`core/floor` 的 Allowed 集）；② 补一个**盲区**——`checkBC` 原本只走
      `internal/`，而 floor 下的文件恰好不在里面，于是"共享底座不得 import
      BC"这条规则对 floor 从未生效过。现在 `sharedPkgs` 是两条前缀
      （`internal/pkg/` 与 `core/floor/`），两个根都走一遍。
      反证实测：往 `core/floor/config` 扔一行 import `internal/manager/biz/audit`，
      modulecheck 报 2 条（模块级 + BC 级），删掉即绿。
    - **根模块第一次全绿**：`internal/skill` 搬走之后，根模块 `go test ./...`
      从 182 ok / 1 failed 变成 **177 ok / 0 failed**。原来那 3 个
      `TestTruncateOrSpill_*` 失败不是环境的错——降级判据写在 `MkdirAll` 上，
      而"目录已存在但只读"时 `MkdirAll` 返回 nil，于是 `/var/tmp` 写入失败、
      SpillPath 为空。测试一直是对的，代码不是；改成"写失败才降级"之后
      三个用例在 floor 里全绿（测试意图没变，只是第一次被执行）。
    - **这一步之后立刻做的**：`internal/edgeagent` 也搬完了（决策 61），
      所以决策 60 结尾说的"两块"只剩一块——`internal/manager` +
      `internal/iam` 仍在根模块。它们不再是"卡在裁决上"，而是"还没搬"：
      路径重写与依赖核对，搬完跑一次全量测试。

61. **`internal/edgeagent` → `core/edge`：14K 行的节点面第一次成为真正的模块**。
    决策 60 把共享底座搬进 floor 之后，A 阶段剩下的"两块机械迁移"里，节点面这块
    先做了。它比 manager 那块小两个数量级，但它是**第一次让一个完整平面**（采集器、
    插件、bash、沙箱、webshell、技能分发、升级、RPC 桥）落在自己的模块里——此前
    `core/edge` 只有 pigsupervisor / policygate / gatesocket / toolbroker 五个包，
    真正的节点实现在根模块。
    - **搬了什么**：`internal/edgeagent` 全部 12 个子树、62 个文件、14,057 行
      （`bash` / `biz` / `changewatcher` / `cmdpolicy` / `collector` / `host_files` /
      `model` / `plugins` / `restart_service` / `service` / `skill` / `webshell`），
      路径一一对应到 `core/edge/<同名>`。
    - **为什么这次几乎零风险**：节点面的依赖面本来就是干净的——它只 import
      `core/{ports,wire}` 与 `core/floor/{prom,skill,tunnel}`，**没有一条**
      `internal/*` 依赖。所以迁移只是 31 个文件的 import 重写，没有一处需要
      重新裁决"这个包该跟谁走"。这也反过来验证了决策 60 把 floor 先切出来的
      价值：切完之后，节点面的外部依赖只剩一个已经定好归属的模块。
    - **`core/edge/go.mod` 新增的允许集**：`core`、`core/floor`，加上四类它真的在
      说的东西——`gopsutil`（进程/主机指标）、`prometheus/{client_model,common}`
      （exposition 格式）、`golang.org/x/sync`（errgroup）、`yaml.v3`（bash 策略
      文件）。版本与根模块**逐一对齐**（例如 `gopsutil v3.23.6`）：`go.work` 会把
      整个 workspace 收敛到最高版本，若这里写新版本，等于顺手把根模块的
      gopsutil 升上去，那是一次没人要求的行为变更。
    - **modulecheck 的两处收尾**：`core/edge` 的 Allowed 加上四类第三方；
      `internal/edgeagent` 从 BC 表里删掉（它不再是根模块内的 BC），随之删掉
      两条 cross-plane exception——从 manager 测试 import `core/edge/*` 现在是
      普通跨模块 import，`bcOf` 返回空、自然放行。这正是迁移买来的东西：
      原先靠 allow-list 守的边界，现在由模块图守。
    - **`.go-arch-lint.yml` 同步改名改路径**：`edgeagent_{service,biz,model,runtime}`
      → `oxedge_{service,biz,model,runtime}`（与既有 `oxedge_pigsup` 等同一命名族），
      路径指到 `core/edge/**`。规则本身没放松：`oxedge_biz → oxedge_{model,runtime}`、
      `oxedge_service → oxedge_biz`、runtime 只碰契约层与 floor。改完
      `go-arch-lint check` 仍是 **0 warnings**。
    - **实测**：`core/edge` 19 包 ok（原 5 包 + edgeagent 的 14 个有测试包）；
      根模块 177 → **163 包 ok / 0 failed**（14 个测试包跟着搬走）；全仓总数仍是
      **214 包 ok / 0 failed**，说明这次搬迁没有丢测试、也没有新增失败。
    - **一个已知的、可接受的副作用**：`core/edge` 与 `core/floor` 之间形成了
      **模块级 require 环**——floor 的 `pluginmanifest` 测试 import
      `core/edge/policygate`（决策 60 记录的那条 test-only 依赖），而 edge 的生产
      代码 import `core/floor/{prom,skill,tunnel}`。包级 import 图无环
      （policygate 只碰 `core/{domain,ports,wire}`），`go build`、`go mod tidy`、
      workspace 构建全部通过，发布时两个 tag 可以同时打。若要把环彻底去掉，
      修法是把那次"审批半径语义一致"的断言从 floor 的测试挪到 edge 侧，或降级为
      契约测试——它不阻塞 A 阶段，但记在这里，免得下次有人把它当成新问题。
    - **没做的**：`internal/manager` + `internal/iam`（128.5K + 4.5K 行）仍在根模块。
      它们才是 A 阶段的主菜：manager 还依赖 27 个 `internal/pkg` 子包与
      `internal/middleware`、`internal/control`、`internal/knowledge`、
      `internal/dataguard`、`internal/agentteams`、`internal/observability`，
      这些树的归属要在那一次搬迁里一并定。

62. **控制面基础设施先搬：`core/manager` 建起来了，`internal/pkg` 从此不存在**。
    决策 57 把 manager 迁移卡点归结为"共享底座放哪儿"，决策 60 解掉了两个平面
    共用的那一半（floor）。剩下的一半——**只有控制面用的那些树**——这一轮搬完了，
    于是 A 阶段最后一次搬迁（`internal/manager` + `internal/iam`）不再有前置裁决。
    - **搬了什么**：29 个 `internal/pkg` 子包 → `core/manager/pkg/*`；再加上
      `middleware`、`control`、`knowledge`、`dataguard`、`agentteams`、
      `observability`、`higress`、`migrate`、`migrator` 九棵树 → `core/manager/*`。
      新模块 `core/manager` 的 go.mod 落地，`go.work` 与根 go.mod 的 require/replace
      同步。全仓 381 个文件的 import 路径被重写。
    - **为什么是 manager 而不是 floor**：这次不是拍脑袋，是数出来的。用 import
      图统计每一棵树的实际使用者：`internal/pkg` 的 375 处引用里，
      manager 占绝大多数（`errs` 158、`tenantctx` 71、`llm` 25、`promquery` 22），
      其余是 iam、dataguard、cmd——**全是控制面**，节点面一处都没有。
      floor 的定义是"两个平面都要用的基础设施"，这些不是；它们是一个平面的地基，
      跟着那个平面走。
    - **为什么必须现在搬**：如果先搬 `internal/manager` 而把这些树留在根模块，
      `core/manager` 就要 require 根模块，而根模块又要 require `core/manager`
      ——正是决策 57 判为不可接受的 (b) 方案。底座先行，是让最后一步只剩
      import 重写的唯一顺序。
    - **`go mod tidy` 在这里是个陷阱，必须点出来**：新模块里第一次 tidy 会挑每个
      依赖的**最新版本**（`miniredis v2.39`、`otel v1.46`、`pgx v5.11`、`gorm v1.31.2`
      ……），而 `go.work` 的构建列表是整个 workspace 取**最高版本**——也就是说，
      一个"只加了新模块"的动作会把根模块和节点面的依赖一起升上去，没有任何人
      要求过。修法：先在 `core/manager/go.mod` 里按根 go.mod 的版本逐个写死
      require，再 tidy；tidy 之后逐项验证 workspace 的解析结果仍是旧版本
      （`miniredis v2.38.0`、`otel v1.43.0`、`pgx v5.8.0`、`gopsutil v3.23.6`）。
      **这一步是这次搬迁里唯一一处会静默改变运行时行为的风险，值得写进流程。**
    - **顺手修掉一个我自己引入的检查器漏洞**：`modulecheck` 的 BC 规则一直写死
      走 `internal/`。上一轮为了让 floor 也被检查，我把这一次调用改成了"遍历
      `sharedPkgs`"——而 `sharedPkgs` 当时是 `{internal/pkg/, core/floor/}`，
      于是 BC 之间的规则与 service→biz←data 的层规则**整整一轮没有跑过**，
      检查器照旧打印"all module boundaries hold"。这一轮把遍历根改成从
      `bcs` + `sharedPkgs` **推导**（`bcWalkRoots`），不存在的根跳过但列表必须
      来自规则表；并补了两个测试：一个断言每个 BC 与每个 floor 目录都在遍历
      列表里，一个构造 fixture 断言"manager 的包 import iam 的 data 层"确实
      会被报出来。反证已实测：把 BC 那半段从 `bcWalkRoots` 摘掉，两个测试都红。
    - **两处"数 `..` 层级"的测试断了**，这是搬迁的必然产物，修法是消掉这个模式：
      `control/incident` 的 dataset 测试与 middleware 的 toolset 生成器都靠
      `../../../` 定位仓库根，包一深一层就找错地方。前者改成向上找 `go.work`，
      后者同理——生成器尤其重要：它原本靠"第一个 go.mod"定位仓库根，
      搬进 `core/manager` 之后第一个 go.mod 变成了模块自己的，模板会被生成到
      `core/manager/core/pig/extensions/...`。`go.work` 才是"仓库根"的判据。
      生成产物随后用 `OPSKEEPER_UPDATE_TOOLSET=1` 重生成并跑 `sync-pig-ops.sh`
      同步，插件包里的两份副本一起更新。
    - **modulecheck 的 `AnyVendor`**：控制面天生坐在一大堆库上（gorm、redis、
      pgx、kafka、otel、casbin、lark……）。给 `core/manager` 逐条列 vendor 白名单
      是第二份依赖清单，且不含任何架构陈述。所以规则新增 `AnyVendor` 开关：
      **第三方 import 不设限，OpsKeeper 模块之间的 import 仍然逐条判定**
      （`core/*` 与 `sdk` 是它的全部允许集），PiG 的直接 import 仍由
      `checkPiGBoundary` 全仓拦。`floor`/`edge`/`sdk` 的小 vendor 面保持逐条列出
      ——那是它们的设计约束，不是噪声。
    - **实测**：`core/manager` 50 包 ok；根模块 163 → **113 包 ok / 0 failed**；
      13 个模块目录总数仍是 **214 包 ok / 0 failed**——三次搬迁前后总数一次没变。
      `modulecheck` 全绿、`go-arch-lint` 0 warnings、`gofmt` 干净、两个 eval 闸门
      仍是 20/20。
    - **还剩什么**：`internal/iam` → `core/manager/iam`，`internal/manager/{biz,data,
      model,server,service}` → `core/manager/*`，然后删掉空掉的 `internal/`。
      这一步不再有设计问题，只有 import 重写与依赖核对——**决策 63 做完了**。

63. **A 阶段收口：`internal/` 不存在了，`core/manager` 就是控制面模块**。
    决策 62 之后只剩最后一块源码，这一轮搬完：`internal/iam` →
    `core/manager/iam`，`internal/manager/{biz,data,model,server,service}` →
    `core/manager/{biz,data,model,server,service}`，两级空目录一并删掉。
    `rg 'opskeeper/internal/' -g '*.go'` 现在 **0 命中**；全仓改动的 1489 个文件里，
    668 个的 import 指向了 `core/manager`（1378 个文件是以重命名形式记录的）。
    - **规模**：`core/manager` 现在 1100 个 Go 文件（413 个测试），222 个包；
      根模块从 113 包掉到 18 包。13 个模块目录串行跑完 **302 包 ok / 0 failed**。
    - **`modulecheck` 的 BC 表不能只改路径，这是这一轮最容易做错的地方**。
      `bcs` 原本是两条前缀 `{internal/iam/, internal/manager/}`——旧布局里它们
      是兄弟，一条前缀就是一棵树。现在 `iam` 是 `core/manager` 的**子目录**，
      若仍写"`core/manager/` 属于 manager 上下文"，那么 `core/manager/pkg`
      （共享底座）、`core/manager/dataguard`、`core/manager/control`、
      `core/manager/middleware`……会被全部吞进业务上下文：iam 用 dataguard
      会被报成跨上下文 import，底座引用自己会被报成"底座在猜业务"。
      所以 `bcs` 拆成 `{label, dirs, layerRoot}`：**manager 上下文只拥有
      `biz/ data/ model/ server/ service/` 五个目录**，而层规则需要的
      `layerRoot` 是它们的父目录 `core/manager/`（`service → biz ← data`
      比较的是 `layerRoot + "service/"` 这类路径）。第一次跑出来 66 条误报，
      改完是 0 条。
    - **底座方向的豁免要写明**：旧布局里 `internal/pkg` 不匹配任何 BC 前缀，
      `bcOf` 自己返回空串，规则静默放行。`core/manager/pkg` 匹配 manager 前缀，
      这条豁免就得显式写出来，否则 `pkg/auth` 引用 `pkg/tenantctx` 会被报成
      "底座在猜业务"。豁免的**方向**是有意的：底座可以服务所有人，底座自己
      **不许** import 任何 BC——后者仍是红的，有测试守着。
    - **新增一条"只允许测试用"的跨模块边**：`core/manager` 的 go.mod 里出现了
      `core/edge`。原因是三个测试：`biz/nodefleet/e2e` 的两个用例要驱动真
      `policygate` 跨回环隧道（这是边界被**演练**的地方），`biz/aiops/tools`
      还要断言管理面的 `when_to_use` 与节点面 `cmdpolicy.DefaultReadOnly()`
      不漂移。Go 没有"仅测试的 require"，所以这条边就是存在的，而注释拦不住它。
      于是 `modulecheck` 加了 `testOnlyImports` 表：**`core/manager` 只能在
      `_test.go` 里 import `core/edge/`**，非测试文件命中即红，违规信息里带
      上理由。配套用例正反两面都测了（生产文件红、测试文件绿）。
    - **检查器不是空转，这一条是实测的**：把 `exceptions` 表清空重跑，
      报出来的正好是 4 条真实跨上下文边——`iam/server/http.go` →
      `{biz/audit, model/audit, server/middleware}`，`server/imbridge/http.go`
      → `iam/model`。装上例外表即 0 条。也就是说"全绿"这句话背后确实有 4 条
      被裁决过的边，不是"什么都没扫到"。
    - **两处"数 `..` 层级"的测试又断了一次**（`biz/demo/preview_executor_test.go`
      与 `control/incident/dataset_test.go`），修法与决策 62 相同：向上找
      `go.work`，或者按新的包深重算。
    - **`.go-arch-lint.yml` 同步**：`iam_*` / `manager_*` 十个组件的路径从
      `internal/**` 改成 `core/manager/**`（`manager_biz` 是 `core/manager/biz/**`，
      不含 `pkg`——`pkg` 是 `shared_pkg`）。`go-arch-lint check` 复跑：
      **OK - No warnings**。
    - **搬迁把一条真问题带进了 `-race` 闸门，顺手修了**：`make module-race`
      包含 `core/manager`，而在 `biz/audit` 搬进来之前，那个模块里没有审计链。
      第一次跑 `-race` 就红了：`TestChain_ConcurrentAppendsAllVerify`（8 个
      writer × 10 次追加）丢了 1 行——`data/audit/store` 的 CAS 重试上限是
      **固定 8 轮**，`-race` 把每次尝试拖慢之后，8 轮不够用，于是
      `Emit` 报错、这行审计被丢弃。轮数本身就是错的度量：**一个 writer 输几次
      取决于同时有几个 writer，而不是它愿意等多久**。改成"限时 15s + 指数
      退避 + 抖动"，仍然把失败告诉调用方（"账本悄悄放弃"和"没人写账本"看起来
      一样，这是追加路径唯一不能做的事）。改后 `./... -race` 222 包全绿。
    - **`gofmt` / `go vet` 的既有例外不动**：`core/floor/skill/builtin/spill_helper.go`
      的格式与 `middleware/adapter/k8s/cleanup.go:203` 的 `append with no values`
      都是搬迁前就在的，属于别人的账。

### 当前真实缺口

- **A 阶段（模块化地基）已经收口**——决策 63 把最后一块源码搬完，`internal/`
  不再存在，七个模块 + 5 个 extension 全部落地，13 个目录 302 包全绿。
  代码侧没有已知的未完成项。A 阶段**剩下的是债务而不是缺口**，逐条列在
  下面两条里（共享底座的包级 setter、arch-lint 债务清单缺守卫）。
- **arch-lint 的 white-list 粒度问题（决策 58 第 3 步的已知代价）**。
  `mayDependOn` 是组件粒度：账本里为 `manager_biz → manager_data` 开了口子之后，
  **同一个组件**里新加一条 `manager_biz → manager_data/新store` 不会再报警。
  modulecheck 的 `layerDebt` 有 `TestTheLayerDebtLedgerIsCurrent` 守着（文件删了或
  债务还清就报错），arch-lint 这一份**没有对应的守卫**——这是当前两个闸门之间的
  一处不对称，修法是给 modulecheck 加一条"arch-lint 债务文件清单"检查。
- **`internal/pkg → core/pig` 的反向边（deepScan 独立复现）**。
  `core/manager/pkg/llm/pigsettings.go:41` 的 `NewSettingsSource` 返回
  `pigmodel.SettingsSource`，`core/manager/pkg/{llm,promquery,promwrite}` 另有 3 处包级
  setter 接收 manager_biz 的实现类型。前者是"共享底座依赖 PiG 适配模块"，后者是
  "共享底座通过包级 setter 反向拿到业务实现"。两者都不在 import 图里（所以
  `check` 报 0 条），但都在装配图里。修法同决策 57：底座与适配层解耦，
  包级 setter 换成显式构造参数。
- **B1/B2/B3 已闭环**：18 个节点本地只读工具、12 个可观测只读工具、**53 个中间件
  只读工具**、5 个写工具均已打通。写工具全部经控制面 reviewer，且要消耗一次性
  审批回执；`host_restart_service` 的本地执行被证明确实锁死（回归测试可复现该
  失败）。可观测 12 工具覆盖 PromQL / LogQL / TraceQL / 数据库源 / 代码仓库 /
  审计历史；中间件 53 工具覆盖 pg / redis / k8s / kafka / rabbitmq / mq 的当下
  状态与 `git.find_runtime_link`（见决策 59）。
- **`plugin-coverage` 现在是 20/20，但这个数字是家族级的**：它问的是"这个 case
  的每个期望家族有没有包在服务"，不是"每个方法都真实存在"。`redis/slow-cmd`
  期望的 `redis.kill_client`（适配器实际叫 `redis.client_kill`）现在被
  `opskeeper-sre-middleware` 声明的 `redis` 家族覆盖，于是这个 case 从"结构性
  不可通过"变成"可评分"——判分能不能过，是另一回事。按名字判定的那条轴是
  `loopActionExecutability`（只认精确符号，当前 0 缺口）。把包的能力声明从
  "家族"升级到"逐方法"是 `sdk` 的后续能力，见决策 59 末段。
- **B2 原本计划走 MCP，PiG 不支持，已改为 extension toolset**：PiG 的 `mcp`
  包类型**只是声明**——PiG 全仓中所有 MCP 引用都在
  `coding/packagecontent/packagecontent.go` 与 `cmd/pig/package_*.go`
  （解析/校验/清单），**没有** JSON-RPC 客户端、**没有** `initialize` /
  `tools/list` 握手、**没有**把声明的 MCP server 接进 agent 工具集的桥。
  所以「可观测栈 → MCP server」在当前 PiG 上需要一个从零写的 MCP 运行时，
  而 upcall 通道已经端到端跑通 65 个工具（12 可观测 + 53 中间件）且带鉴权、
  审计、白名单与回归（`runMiddlewareTool` 的读写分界见决策 59）。
  这是基于「PiG 是什么」的事实修正，不是对计划意图的重新解释；
  补一个 MCP 客户端是后续独立决策，不是本包依赖的假设。
- **可观测覆盖的真实边界**：`opskeeper-sre-observability` 的 12 个 upcall 工具
  **不含** K8s 对象与消息队列——它读的是控制面已采集的指标 / 日志 / trace /
  代码仓库 / 审计历史，K8s 只在指标层面出现。控制面 registry 里现在**有**
  K8s 与 MQ 的只读工具（k8s/mq 适配器），但它们未进这个包的清单，所以对
  persona 而言仍然不可见。仓库内没有假装覆盖——persona 被要求显式声明
  「K8s 对象、消息队列不在这些工具能看到的范围内」，
  `TestTheObservabilityProfileShipsAPersonaThatKnowsItsOwnLimits` 守住这条。
- **发布运输通道已闭环**：`plugin.install` / `plugin.remove` /
  `plugin.list` 三个方法走同一条 tunnel；节点侧 `pluginStore` 按
  fetch → digest → 解包 → 审核 → 激活 → 重发布 的顺序执行；控制面
  `service/plugin.Manager` 负责选节点、开波、停手与回滚，HTTP 侧 6 条
  admin 路由（`/v1/plugins/releases`）。`halt` 与 `rollback` 是两个动作：
  前者停手但保留已装内容，后者才真的摘包。
- 节点回执里的 `Replaced` 是回滚能"还原"而不是"删除"的唯一依据：安装
  之后的列表里该包是新版本，管理器无法从中推出旧版本。拒绝安装的节点与
  本波次未触达的节点都不发 remove——否则会摘掉运维方自己装的包。
- **`min_edge_version` 已真正生效**：此前每个已发布清单都声明它，但没有任何
  代码读取。现在 `pluginmanifest.MeetsMinEdgeVersion` 做点分数字比较（不是
  semver——本仓库的版本来自构建元数据，一台开发机的 `dev` 不是版本），并在
  审核管线里作为独立的 `version` 步骤运行，位于 `admission` 之后。两边任一
  版本读不出来即拒绝：节点在猜，而猜向宽松的那一边会装上它带不动的包。
  节点自己的版本取 `OPSKEEPER_EDGE_VERSION` → 构建标签 → `unknown`；
  `unknown` 会让声明了最小版本的包被拒，这正是"我说不清我是什么"该有的结果。
- **手动停手要能立刻生效**：`Dispatch` 只在读取波次时持锁，不再跨节点请求持锁。
  否则一个波次就是每节点一次请求，而 `Halt`——金丝雀出问题时运维第一时间
  会按下的那个方法——会阻塞到整个波次答完。会等待它所停止之物的急停不是急停。
- **eino 已移除**（决策 34）。依赖侧：`cloudwego/eino` 与
  `eino-contrib/jsonschema` 从 `go.mod`/`go.sum` 双双消失，`rg eino go.mod` 无命中。
  代码侧：删除 `internal/manager/biz/aiops/graph/` 整目录（14 文件、约 5710 行，
  含 `callbacks/` 的 persistence/sse/metrics/audit/chain/alert_draft_guard 六件套
  与全部测试）、`core/manager/pkg/llm/{eino_routing,budget_callback}.go` 及其测试；
  provider 常量与 `ErrUnknownProvider` 搬到新的 `core/manager/pkg/llm/providers.go`。
  `chatruntime.Config` 删掉 `ChatModel`/`GraphCfg`/`CallbackDeps`，新增 `MaxIterations`，
  `ports.Agent`（`Kernel`）成为**必填**——缺内核直接报错而不是静默走图；
  `Runtime.Handle` 恒走 `runKernelTurn`，`worker.go` 改为直接驱动 `cfg.Kernel.Run`。
  测试侧：`scriptedChatModel` 退役，`scriptedKernel` 扩出 `replies`/`onRun`/`runCount`，
  `runtime_test.go` + `runtime_worker_test.go` 全量迁移；
  `TestBuildEinoHistory_*` 更名 `TestBuildKernelHistory_*`。
  告警草稿守卫没有跟着 callback 一起消失，而是搬进
  `internal/manager/biz/aiops/alertdraft/draftguard.go`（纯函数 + 有状态 `Guard`），
  由 `chatruntime` 的 `kernelSink`（流上 `Sanitize`）与 `agentkernel.Persister`
  （落库时 `Sanitize`）**两条路径各自推导状态**，因此不需要新增 Config 字段，
  也不需要两个实例互相注入——它们读的是同一条有序事件流。
  新增用例 31 个（alertdraft 23 + chatruntime 4 + agentkernel 4），
  并做过变异验证：分别注掉两处 `Sanitize`，对应用例即失败。
  顺带修掉一个真实回归：`investigator.isMaxStepsError` 原本只认
  `exceeds max steps` / `exceededmaxsteps` / `graphrunerror`，而内核报的是
  `exceeded max iterations`——RCA 抢救路径会静默失效。已补齐两种措辞并加测试钉住。
- ~~`manager` / `iam` 的机械式包迁移未做~~ **已完成（决策 63）**。`harness`
  已在 `core/harness`（决策 37），`edgeagent` 已在 `core/edge`（决策 61），
  控制面基础设施与两层业务代码都在 `core/manager`（决策 62/63），`internal/`
  已删除。
- `docs/module-architecture.md` 尚未补 `policygate`/`gatesocket`/信使/`spec.tools`
  （arch-lint 侧已齐，见「待决的大动作」）。

---

## 七、未来路线图

### 下一步（A 收口之后，按优先级）

A 阶段已在决策 63 收口，路线图上不再有"先把模块拆出来"这一条。剩下的按
优先级排是五件事，前两件是**欠账**，后三件是**新能力**：

1. **PiG 换成固定 tag**（B 阶段收尾）。`core/pig/go.mod` 现在 `replace` 到
   本地 checkout `/Users/louloulin/appx/PiG`——开发期是对的，发布不可行。
   换 tag 之前需要一份**契约测试套件**，只针对本仓库实际用到的 API 面
   （`ai.Model`、`agent` 事件、`extensions/sdk`、`rpcclient`），把"上游升级后
   哪些行为变了"变成一条可执行的断言，而不是一次人工读 diff。
2. **共享底座的两条反向边**（决策 57 遗留）。`core/manager/pkg/llm/
   pigsettings.go` 的 `NewSettingsSource` 返回 `pigmodel.SettingsSource`
   （底座依赖适配层），`pkg/{llm,promquery,promwrite}` 另有 3 处包级 setter
   接收 `manager_biz` 的实现类型（底座反向拿到业务实现）。修法是把 setter
   换成显式构造参数。这两条**不在 import 图里**，`modulecheck` 报不出来，
   所以只能靠人记着——直到修掉。
3. **arch-lint 债务清单的守卫**。`layerDebt` 有 `TestTheLayerDebtLedgerIsCurrent`
   守着（账还清了就必须删条目），arch-lint 那份 `mayDependOn` 白名单没有
   对应的守卫：同一个组件里新加一条越层 import 不会再报警。修法是让
   modulecheck 把 arch-lint 的债务文件清单也读进来做同样的检查。
4. **MCP 运行时**（D 阶段的可选加速器）。PiG 的 `mcp` 只是声明，没有
   JSON-RPC 客户端、没有握手、没有把 MCP server 接进 agent 工具集的桥。
   当前 65 个工具走 extension toolset 已端到端跑通（带鉴权、审计、白名单、
   回归），所以要不要补 MCP 是"接不接第三方 MCP 生态"的产品问题，不是债。
5. **插件市场与节点页面的前端**（E 阶段唯一纯前端工作）。
   `/v1/marketplace/*`（7 条）与 `/v1/plugins/releases`（6 条）已经有后端与
   测试，Web 控制台还没有"插件市场"和"节点 Agent"两个页面。

### D 阶段续：把声明变成实现

0. ~~**`sdk/` 发布（计划 §四 D-1）**~~ ✅ 已完成（决策 46）：`plugin-sdk` 的
   三个发布物——插件清单 Go 类型、注册 API、版本协商——全部在 `sdk/`，且
   `sdk` 仍只依赖 `core` 与 `yaml.v3`。版本比较算术下沉 `core/domain`，
   节点与插件作者共用同一份实现。

1. ~~**B1 只读工具集**~~ ✅ 已完成：拓扑 4 件套、`query_alert_rules`、
   13 个 host 探针全部打通。实现留在宿主（`core/floor/skill/builtin` 与
   控制面 registry），agent 进程只做路由。
2. ~~**B2 外部系统**~~ ✅ 已完成（载体由 MCP 改为 extension toolset）：独立 L1 包
   `opskeeper-sre-observability`，12 个只读工具（`query_promql` / `query_logql` /
   `query_traceql` / `analyze_database_status` / `list_database_sources` /
   `list_metric_catalog` / `get_edge_summary` / `get_host_load` /
   `query_change_events` / `list_repo_sources` / `read_source` / `grep_source`）。
   `approval.required: false`、`install.strategy: rolling`、12 个工具全部 upcall。
   **schema 由控制面 registry 的 `Info()` 生成**（`OPSKEEPER_UPDATE_TOOLSET=1`
   重新生成 + `scripts/sync-pig-ops.sh`），不手抄。
   关键不变量：清单里任何一个名字都不得出现在节点自身 skill registry 中——
   否则一个 read-class 工具会被节点本地执行，用一台机器的证据回答全舰队问题。
   由 `TestNoToolInTheObservabilityPackageHasALocalExecutor` 守住。
3. ~~**B3 写操作**~~ ✅ 已完成：独立 L2 包 `opskeeper-sre-repair`，
   5 个工具（`host_restart_service` / `apply_config_change` / `recovery.execute`
   写，`verify_recovery` / `draft_config_change` 读），`approval.required: true`、
   `max_blast_radius: pod`、`install.strategy: pin`。审批回执保证「一次批准 =
   一次执行」，且全部写工具经控制面 reviewer。
4. ~~**审核流水线**~~ ✅ 已完成：`Review` 三段审核、`TreeDigest` +
   ed25519 信封、`TrustStore`（配置加载/吊销/换 key 拒绝）、`PlanRollout` +
   `Rollout` 灰度闸门，并已接进节点 `admitPackages`。
5. ~~**发布运输通道**~~ ✅ 已完成：`plugin.install` / `plugin.remove` /
   `plugin.list` 三个 tunnel 方法；节点 `pluginStore`（fetch → digest →
   解包 → 审核 → 激活 → 重发布，激活是对**包目录**做 rename）；控制面
   `service/plugin`（`NodeFleet` 适配 + `Manager` 选节点/开波/停手/回滚）；
   HTTP 6 条 admin 路由。**发布不重启节点**：包被写入 agent 的包列表，由
   下一次包加载生效——重启会让滚动发布变成滚动中断。

6. ~~**控制面适配器真实化**~~ ✅ 已完成（决策 43）：k8s（19 工具，自建 REST
   客户端，不引 client-go）、mq（6 工具，中立命名空间，RabbitMQ management API
   与 Kafka offset reset）、host（7 工具，`local://` / `ssh://`）三个适配器从骨架
   变成实现；连同既有的 pg / redis，五个适配器按 `OPSKEEPER_LOOP_*_DSN` 装进同一
   个工具 registry。闭环动作未实现数 **8 → 0**。
7. ✅ **参数解析器（`ArgResolver`）——已闭合（决策 44 + 49）**。原先 9 个动作
   "名字有实现、参数派发不出去"：`pg.kill_session` 要 pid、`k8s.evict_pod` 要
   pod、`k8s.scale` 要 deployment+replicas、`mq.drain_queue` 要 queue、
   `k8s.rolling_restart` 要 deployment、`mq.replay_messages` 要 queue、
   `pg.connection_pause` 要 role、`redis.client_kill` 要 addr、
   `host.restart_service` 要 unit。`RegistryInvoker` 接缝两侧的来由：
   - **参数从哪来**。`RemediationOption` 只有 `Action` + `Target`
     （形如 `"pg:alert-17"`），定位信息不在其中。决策 44 用
     `pg_stat_activity` 证据行解决 pid 与 role；决策 49 用 firing 告警的
     `labels_json` 解决其余 7 个——**证据链新增 `resource_alert_labels`
     subject 项**，investigator 记录、`StampSubject` 宿主强制补回最终合同。
     两条来源都是观测而非推断，缺证据或证据自相矛盾时一律拒绝。
   - **为什么走证据而不是给 `RemediationOption` 加定位器字段**：前者的数据
     已经存在（`k8s.pod_list` 证据里本来就有 pod 名与 namespace，
     `alert_incidents.labels_json` 里一直有 pod/queue/unit），后者要动被校验的
     合同 schema。
   - **每个动作一张"证据字段 → 工具参数"的表**，并且**缺一个就拒绝**，
     绝不填默认值。把猜出来的 pid 打进 `pg_terminate_backend`，就是把"终止那个
     长事务"变成"终止 planner 碰巧先读到的那个后端"。别名表按**字段**声明
     （`pod` 接受 `pod/k8s_pod/pod_name/...`），只有被声明的键才可能成为
     参数；两个别名键指向不同对象时判为矛盾并拒绝。
   - **两条来源都已落地**：`EvidenceArgResolver` 从
     `RootCauseJSON.EvidenceChain` 取值。决策 44 覆盖 `pg.kill_session`(pid) 与
     `pg.connection_pause`(role)，取自 `pg_stat_activity` 证据行（合同表里既是
     `[]map[string]any` 又可能是 JSON 往返后的 `[]any`，两种形态都读）；决策 49
     覆盖其余 7 个，取自 subject 证据项。**候选多于一个就拒绝并列出候选**。
     没有声明提取器的动作返回 `(nil, nil)`，把话留给 invoker 自己的缺参拒绝——
     解析器不该对它一无所知的动作声称有权威。
8. ⏳ **旧 `mq/kafka` + `mq/rabbitmq` 骨架的去向**：两个包仍在，`kafka.` /
   `rabbitmq.` 命名空间保留未动，与新的中立 `mq.` 并存。可选方案是让它们委托到
   新包（一套实现、两个命名空间），或明确标为待删。**这是命名空间的治理决定，
   不是技术障碍**。这 10 个工具是**当前唯一**还在报 `not_implemented` 的注册项，
   已全部登记在 `core/manager/middleware/adapter/skeleton_test.go` 的
   `knownSkeletons` 里并附了理由：这条守卫会拦住任何新增骨架，也会在这 10 条里
   任何一条被实现后要求把它从清单里删掉。反过来说，能力闸门对这 10 个名字的
   计数**今天仍然是虚高的**——它们是已知的、被点名的那一类虚高，不是未知的。
9. ~~**`git` 适配器仍是骨架**~~ ✅ 已完成（决策 45）：8 个工具全部真实
   （`connect` / `list_repos` / `commit_history` / `file_at_commit` / `blame` /
   `diff` / `search_code` + `find_runtime_link`），走 git CLI，只读，
   `ArgsSchema` 按真实必填参数声明，并按 `OPSKEEPER_LOOP_GIT_DSN` 接进
   `cmd/opskeeper/loop_adapters.go`。闭环动作表里仍然没有 `git.*`（所以闭环
   数字不变），它服务的是"拿证据去仓库核对"这一类根因证据，例如
   `git-artifact.LinkK8sImage` 指向的镜像改动到底进了哪个 commit。
10. ~~**`sdk/` 发布面（计划 D-1）**~~ ✅ 已完成（决策 46）：清单 Go 类型
    （`sdk/manifest.go`，既有）、**注册 API**（`sdk/register.go`：`Registry` /
    `Register` / `ToolDecls` / `Check` / `DeclaredManifest`）、**版本协商**
    （`sdk/negotiate.go`：`Host` / `Negotiate` / `RequireVersion`），版本比较算术
    下沉到 `core/domain/version.go` 供节点与作者共用。`sdk` 仍然只依赖 `core`
    与 `gopkg.in/yaml.v3`，模块边界检查覆盖。

11. ✅ **领域探针（决策 50）**——提议从「猜阈值」改为「问领域」。这是把
    remediation 轴从 3/20 推到 9/20 的那一项，详见决策 50。
12. ✅ **A 类缺口第一批：host + redis 工具（决策 51）**——新增 5 个工具
    （`host.top_processes` / `host.old_log_files` / `host.kill_process` /
    `host.remove_old_logs` / `redis.scan_and_delete`）与 2 条只提议不写
    extractor 的修复。remediation 轴 **9/20 → 13/20**，平台轴 **10/20 → 11/20**，
    blocked 列表 **11 → 7**。
14. ✅ **A 类缺口第二批：`k8s/pv-full`（决策 52）**——`k8s.pvc_list`（L0）+
    `k8s.pvc_usage`（L1）+ `k8s.cleanup_logs`（L4）三个新工具，探针链式展开
    机制，以及两条提议。**平台轴 11/20 → 12/20，remediation 轴 13/20 → 14/20，
    blocked 7 → 6。**
16. ✅ **A 类缺口第三批：mq 命名空间（决策 53）**——10 个 `not_implemented`
    骨架变成真实工具（产品命名空间委托到中立 `mq.` 实现），新增
    `kafka.partition_skew` / `kafka.repartition` / `rabbitmq.purge_queue`，
    并把 `kafka.` / `rabbitmq.` **真正接进控制面**（此前只有评测闸门在数它们）。
    **平台轴 12/20 → 13/20，remediation 轴 14/20 → 15/20，blocked 6 → 5。**
    已加反向测试：闸门计数的每个命名空间都必须在控制面接线。
17. ✅ **A 类缺口第四批：诊断面两件套（决策 54）**——`pg.replication_status`（L1，
    primary 的 `pg_stat_replication` 视角，两个 lag 维度都报）与 `k8s.top_pods`
    （L1，每个容器对其**自身 limit** 的用量，含 current/last terminated 两处
    OOM 证据）。**平台轴 13/20 → 14/20**（`pg/replication-lag` 变为
    `fully servable`）；`k8s/pod-oom` 的缺口从 2 个降到 1 个（只剩
    `git-artifact.LinkK8sImage`，那是连接器不是工具）。remediation 轴不变。
    顺手修掉一个真实缺陷：`Diagnose` 只在**有行**时才附 suggestion，而
    replication 的 suggestion 恰恰是写给空结果的（见决策 54）。
18. **剩余 5 个 blocked，全部需要裁决，代码侧无阻塞**：
    - **A. 三个名字承诺了 broker 交付不了的操作（3 个 case）**——
      `kafka.restart_broker`（重启进程是编排层的事）、
      `kafka.scale_consumer` / `rabbitmq.scale_consumer`（并行度=客户端实例数，
      改它是一次部署）。RabbitMQ 的 policy/prefetch 是形似的真实操作但不是它。
      选项：**(a) 改语料**（把这些 case 的期望换成真实可做的操作）、
      **(b) 在插件层提供编排动作**（k8s 删 pod / 调整 Deployment 副本，
      但那属于 `k8s.` 命名空间）。**推荐 (b)**，因为「重启 broker」在 K8s 上
      确实有真实实现，只是不在 broker 命名空间里。
    - **B. `kafka.rebalance_history`（并入 A 的 case）**——Kafka 只有当前
      分配没有历史。要么改语料，要么新增一个采集器把多次 DescribeGroups
      的结果存成历史（那是采集器的事，不是 broker 客户端的事）。
    - **C. 工具名承诺了 Redis 交付不了的修复（1 个 case）**——`redis/hot-key`
      → `redis.scan_and_redistribute`。**已判定不实现**：Redis 没有「重新分配
      热 key」的服务端操作。需裁决：改工具名，还是调黄金语料。
    - **D. 改名未对齐（1 个 case）**——`redis/slow-cmd` → `redis.kill_client`。
      适配器注册的是 `redis.client_kill`。决策 43 的先例是**适配器名是规范**。
      三个选项：(a) 改适配器（破坏现有调用方）、(b) 注册别名（不裁决）、
      (c) 改语料（有先例）。**不要单方面裁决。**
    - **E. `k8s/pod-oom` 的根因轴缺口（决策 54 后只剩一个）**——
      `git-artifact.LinkK8sImage` 属知识库/连接器而非工具：`gitartifact`
      有四种 linker 类型（`pg_query` / `redis_cmd` / `k8s_image` /
      `http_route`），它是把证据连起来的东西，不是一个可注册的
      `<family>.<method>` 工具。要裁决的是「linker 算不算一种平台能力」——
      要么在词汇表闸门里为它注册一个 provider（承认算），要么改语料。
      `k8s.top_pods` 一侧已经补齐。这是根因轴而非 remediation 轴。
    - 这些做完之后，remediation 轴的上限是 **20/20**；在那之前，每一步都应
      重跑 `cmd/opskeeper-eval vocabulary` 看两个轴各自的读数，而不是看合计。

19. ✅ **导入器有了入口：`POST /v1/marketplace/import`（决策 55）**——
    转换器此前只是库（`internal/manager/biz/pluginimport` 有实现、有测试、
    没有调用方）。admin 上传 → 解包 → 转换 → 写进
    `OPSKEEPER_PLUGIN_IMPORT_DIR` 下的 `<包名>/`，响应带 dest + 报告 + 未决清单；
    未配置答 503。同时修掉一个真实缺陷：无条件下钻会把一个只含 `skills/` 的
    裸 skills.sh 包判成"不是容器"。详见决策 55。

20. ✅ **内置 ops piglet 建出来了（决策 56）**——
    `plugins/pig-ops/opskeeper-sre-readonly/pig-opskeeper-ops.yaml`：第一方只读
    组合（18 只读工具 + 信使 extension + 8 skill），`tools: []` + `discovery: []`
    与节点生成 profile 同一条安全线。它**必须在包内**（PiG 拒绝逃出 anchor 的
    `local:` origin），**每条 skill 单独成 entry**（baker 一个路径一个定义），
    并由 PiG 侧（AST 抓注册名 × `ScopeTools`）与治理侧（能力/L1/工具名双向）
    两组契约测试钉住，各带负向对照。有意不含 observability 与 repair（后者 L2）。

21. ✅ **两条"会跑的"边界声明都真的跑过了，而且都绿了（决策 58）**——
    `make arch-lint` 的 guard 修好（原本会 `Error 1`），`.go-arch-lint.yml` 修到
    能被当前 linter 解析（删掉不存在的组件、把"不依赖任何东西"改成省略规则、
    显式 `depOnAnyVendor`），`modulecheck` 的 service→biz←data 规则从死代码修活
    并抓到 12 条真实违规（6 个测试豁免 + 6 个生产文件进 `layerDebt` 债务账本）。
    收尾：给 11 个共享底座 + edgeagent 数据面 + tests/scripts + 4 个 2.0 包定了
    `mayDependOn`（**底座不得依赖任何 BC** 这条规则没有被放松），测试与生成物
    进 `excludeFiles`，`deepScan` 显式关闭并写明关掉的是什么。结果：
    `go-arch-lint check` → **0 notices，exit 0**（1272 → 210 → 438 → 261 → 0）。
    剩下的真实违例（6 条 import + 4 条包级 setter 反查）逐条进了债务记账，
    见决策 58 与「当前真实缺口」。

### E 阶段：生态治理

- **兼容矩阵**（`core/floor/pluginmanifest/version.go`）：两个**独立**轴。
  - edge 轴：`min_edge_version` × 节点 `selfVersion()`（构建版本或
    `OPSKEEPER_EDGE_VERSION`）。
  - **PiG 轴**：`min_pig_version` × 节点启动的 `pig` 版本
    （`OPSKEEPER_EDGE_PIG_VERSION`，未设时回退到 `core/pig/pigrpc.PigVersion`
    ——`pig` 与 edge 同源同发，链接进去的发行线就是节点启动的东西）。
    两轴在"未配置时能否自报版本"上**故意不对称**：二进制报不出没给它的 tag，
    所以 edge 轴没有回退，`dev` 不是版本。
  两轴是两条**不同的函数**，因为拒绝时要说清升级哪个二进制——一条消息
  会有一半时间把人送去错的组件。两侧都是点分数字比较，不是 semver：
  `dev` 不是版本，未知即拒绝。节点把 `pig --version` 的复合串
  (`0.3.0+0.87.1`) 剥掉 `+` 元数据后再比，因为构建元数据正是 semver 规定
  不参与优先级比较的部分，而 `+` 左边才是扩展 API 出现时推进的那条线。
- **跨云迁移模板**（`core/floor/pluginmanifest/profiles.go` +
  节点 `OPSKEEPER_EDGE_PROFILE`）：
  - `finance-strong-consistency`：L2 上限 / `pod` 半径 / pin 安装 /
    不给 `k8s.exec`、`db.write`、`mq.write`。
  - `saas-multitenant`：L3 上限 / `namespace` 半径 / rolling 安装 /
    scope 全给。
  - profile 与单字段环境变量**矛盾即拒绝**（`nodeProfilePolicy`），因为
    半套用 profile 的主机跑的是没人写过的策略；"哪个文件最后改的"不能
    成为一台主机的权限来源。narrowing（少给 scope）同样拒绝——它导致的
    失败是隐形的：处处装好、只有这里没装，没有任何报错。
- **覆盖率闸门**（`core/floor/pluginmanifest/coverage.go` +
  `opskeeper-eval plugin-coverage`）：把 golden case 的
  `<family>.<method>` 期望与插件包能力做对照，缺口必须**被解释**。
  当前真实结果：**20/20 全绿**——四个只读包（`opskeeper-sre-readonly`
  的 host/alert/topology、`opskeeper-sre-observability` 的
  database/source/observability、`opskeeper-sre-middleware` 的
  pg/redis/k8s/kafka/rabbitmq/mq/git-artifact、`opskeeper-sre-repair`
  的 recovery）合起来盖住了全部 20 个 case。
  两个口径要分清：这个闸门是**家族级**的（包声明 `pg` 就覆盖 `pg.*`），
  按方法名判定的那条轴是 `opskeeper-eval vocabulary` 的
  loop-action 可执行性（只认精确符号）。家族声明之外还剩
  `MiddlewareFamilies = {host, git}` 两族由控制面 adapter 服务，
  每一族都带理由；`NonPackageFamilies` 这个名单已经删掉——它当初把
  `git-artifact` 说成"任何包都服务不了的关联"，于是 `k8s/pod-oom`
  永远不可通过（决策 59）。
- 双向漂移测试：每个**己方**工具必须有能力族归类；表里每个条目必须有
  对应工具。任一方向漂移都是红色。

### 验收闸门找到了什么

三个新闸门不是形式化的确认，各自都查出了真问题：

- **SSE golden 双向比对**：`core/pig/pigwire/testdata/translator-session.golden`
  与 `core/pig/pigagent/testdata/mapper-session.golden` 由**两份独立输入**
  产生（一份是 PiG 放到线上的原始事件 JSON，一份是 PiG 的 Go 事件对象），
  两份文件必须逐字节相同。测试本身也互相独立——共用一个 fixture 会让一个
  适配器的 bug 定义另一个的期望。两份 golden 之外还有 4 条性质测试：
  seq 无空洞、事件类型覆盖完整、不可渲染事件保持沉默、以及 `blocked`
  必须是独立的终态。
- **7 provider 冒烟**：第一次运行就查出 **Gemini 的 base URL 丢掉了 API
  版本路径**。`pigmodel` 把设置里的 base URL 原样交给 PiG 的 Google provider，
  而 PiG 把「显式 base URL」当作运维已知情，于是不再自己补 `v1beta`，请求打到
  `/models/...` 上 404；同时设置与环境变量的默认值给的是 **OpenAI 兼容端点**
  (`.../v1beta/openai`)，那是另一套 API。两者都只在生产出现——所有单测都指向
  自己写的 URL。修复：`geminiBaseURL` 统一归一化，`+ /v1beta` 或剥掉
  `/openai`，已经带版本路径的（代理/固定版本）原样不动。
- **端到端剧本接拓扑**：第一次运行直接卡住——`AgentBridge.StartEvents` 声明
  「立即返回」却阻塞调用方，`Agent.Run` 从此不再往下走，而节点的心跳与指标
  goroutine 照常运行，**在监控上看起来完全健康**。第二次运行查出替身 agent
  对只读调用也要审批回执，于是所有诊断工具返回「无回执」——而当时的断言只检查
  「结果非空」，拒绝对非空，于是绿灯通过。详见决策 36。
- **judge 的算分本身**：把 judge 接上之后（决策 39），拿它给一个把期望根因
  **全部答对**的响应打分，得分是 0.667。查下去发现不是 judge 的问题，是
  `loader.go` 有三个字段从来没被解析过——`root_cause_lines` 吞掉兄弟键
  `remediation_options`（分母偏大）、两个时间基线恒为 0（`time_efficiency`
  于是对每个 case 白送满分）、`no_collateral_damage` 恒为 false
  （`collateral_safety` 于是对带 errors 的响应也白送满分）。三个 bug **都朝着
  "看起来还行"的方向偏**，所以测试一直是绿的。详见决策 40。
- **能力可满足性闸门**：第一次运行报 **0/20 全不可满足**——而这个数字本身
  是闸门错了：语料的 57 个符号里有 36 个由真实 middleware adapter 注册，它只
  拿语料去比闭环 investigator 的修复动作表。**"全都不满足"的闸门和一个正常
  工作的闸门在输出上无法区分**（都打印数字、都非零退出），这个错误方向因此被
  一条测试钉死。修正后的真实结果是 **10/20 平台可满足**，缺口是 11 个具体符号。详见决策 41。
- **闭环动作可执行性闸门**（决策 43）：语料/能力表报的是"平台能不能做"，
  这个闸门报的是"**闭环写进合同的修复动作有没有实现**"。第一次运行报
  **14 个动作里 8 个没有实现**——`k8s.rollout_status`、`k8s.rolling_restart`、
  `k8s.scale`、`k8s.drain`、`host.garbage_collect`、`host.restart_service`、
  `mq.drain_queue`、`mq.replay_messages`。补齐 k8s/MQ/Host 三个真实适配器后
  降到 **0**。闸门同时把"名字有实现但没有参数可派发"单列一类，这一类不需要
  写适配器，需要参数解析器——把两类混在一起会让人去实现一个已经存在的工具。
  决策 49 补上 subject 证据项后，该类从 **9 个** 降到 **0 个**：9 个动作
  全部标记为 `resolvable`（2/9 → 9/9），即每个动作的参数都有明确的证据来源
  与拒绝规则。
- **插件全链路**：第一次运行查出 **节点会同时发布同一个包的多个版本**——
  升级保留旧目录是对的（回滚要用），但重发布把两个目录都写进了 agent 的包
  列表，"每个包名一个版本"这条规则从来没被表达过。第二次运行又查出**回滚
  在协议上无法执行**：管理器没有旧版本的 URL，而 `Install` 需要它。

### 待决的大动作

- ~~**机械式模块迁移只剩最后一块**~~ **已完成（决策 63）**：`internal/manager`
  + `internal/iam` → `core/manager`，`internal/` 已删除，13 个模块目录
  302 包全绿。`harness` 在 `core/harness`（决策 37），节点面在 `core/edge`
  （决策 61），共享底座在 `core/floor`（决策 60），控制面在 `core/manager`
  （决策 62/63），`sdk` 独立。`manager → floor ← edge` 这条主方向已固化在
  `scripts/modulecheck` 与 `.go-arch-lint.yml` 里。剩下的不是迁移，是两条
  债务（底座的包级 setter、arch-lint 债务清单无守卫），列在「当前真实缺口」。
- **LLM judge 已接上，但只在 real-agentteams 模式有意义**（决策 39）。
  `orchestrator` 模式仍然不可达：它需要一个带 advisory lock + DB + LLM 的
  Orchestrator，CLI 不提供。dry-run 的 `rca_accuracy` 仍然是"有没有走到
  postmortem"的合成值，`run-loop` 现在会把这个事实打印出来。**要让
  orchestrator 模式可用，得先把 loop.Orchestrator 做成 CLI 能装配的东西。**
- **闭环的修复动作表已与适配器对齐**（决策 42/43）。`investigatorreal` 现在写的
  是适配器真正注册的名字（`pg.kill_session` 而不是 `pg.kill_backend`、
  `k8s.rollout_status`、`mq.drain_queue`……），并且 `RemediationActions` 由一条
  扫自己 AST 的测试双向钉住，改名漂移不可能再无声发生。闭环覆盖从 **0/20**
  升到 **3/20**（remediation 轴），根因轴 13/20，两轴同时满足 3/20。
- **闭环的提议能力已从「猜阈值」改为「问领域」**（决策 50）。remediation 轴
  **3/20 → 9/20**，blocked 从 14 降到 11。原因是 investigator 此前只查
  Prometheus 与 Loki，物理上无法区分表膨胀 / vacuum 卡住 / 查询变慢——三者
  产生同一个标量。现在它用只读工具**去问**（`pg.table_bloat`、
  `pg.vacuum_status`、`pg.slow_log`、`k8s.rollout_status`、`k8s.node_list`），
  把答案记成证据，由证据门控提议；提议与派发共用同一组过滤器，两侧不可能对
  「证据说了什么」产生分歧。剩下的 11 个缺口分两类：
  - **工具不存在**（9）：`host.kill_process`、`host.remove_old_logs`、
    `k8s.cleanup_logs`、`redis.scan_and_delete`、`redis.scan_and_redistribute`、
    `kafka.restart_broker` / `scale_consumer` / `repartition`、
    `rabbitmq.scale_consumer`。
  - **参数是决策而非观测**（1）：`redis/memory-burst` 的 `redis.config_set`
    （parameter + value）与 `redis.flushdb`（confirm）。这些值不是「证据记录了
    什么」，而是操作员要做的决定；给它们写猜测式 extractor 正是决策 50 批评的
    那种事，正确做法是在审批环节由人补齐。
  - `redis.kill_client` ↔ `redis.client_kill` 仍是需要人裁决的改名，闸门按精确
    匹配算缺口是有意的。
- ~~**闭环能提议、但派发不出参数的 9 个动作**（决策 43/44）~~ **已闭合（决策 49）**。
  这一层的根因不是实现缺失，是**证据链里没有 subject**：`RemediationOption`
  只有 `Action` + `Target`，而 `pg.kill_session` 要 pid、`k8s.evict_pod` 要 pod、
  `mq.drain_queue` 要 queue，这些值从来没有被记录进证据——尽管 firing 告警的
  `labels_json` 里一直写着。`RegistryInvoker` 在
  `ArgResolver` 缺失时**拒绝派发并说明原因**，不会用默认值猜——把猜出来的 pid
  打进 `pg_terminate_backend` 就是把"终止那个长事务"变成"终止 planner 碰巧先
  读到的那个后端"。生产侧的解析器已经落地（决策 44）：`EvidenceArgResolver`
  从**调查已经记录的证据链**里取值，`pg.kill_session` 的 pid 与
  `pg.connection_pause` 的 role 取自 `pg_stat_activity` 证据行，
  **候选多于一个就拒绝并列出候选**——"随机挑一个"不是修复。
  剩下 7 个动作（k8s/mq/host/redis）曾卡在同一个前置条件上：**证据里根本没有
  那个资源名**。决策 49 补上了 subject 证据项（firing 告警的 `labels_json`），
  7 个动作的 pod / deployment / replicas / queue / unit / addr 全部有来源，
  闸门读数 **2/9 → 9/9 resolvable**。
- ~~**语料与能力表的 14 个符号缺口**（决策 41）~~ **在家族口径下已清零**
  （决策 54 补上 `k8s.top_pods` / `pg.replication_status` 两个根因工具，
  决策 59 把 `git-artifact` 与六个中间件家族收进
  `opskeeper-sre-middleware`，`opskeeper-eval vocabulary` 现在读
  **20/20 fully servable**）。清零的是"这个家族有没有人服务"这个口径，
  不是"每个方法都存在"：`redis.kill_client` ↔ `redis.client_kill` 的改名
  仍然无人裁决，只是它现在被 `redis` 家族声明兜住了。要按方法名看真实
  覆盖，读 `opskeeper-eval vocabulary` 的 loop-action 可执行性一节
  （只认精确符号）与「闭环的提议能力」那一节的缺口清单。
- **审计端口**：`iam/server` 为了写审计行而 import `manager/{biz,model}/audit`
  （决策 38 登记的例外）。决策 35 把审计链放在 manager 的唯一咽喉上是对的，
  但代价是 iam 依赖 manager。要真正解开，需要一个两边都能依赖的审计端口。
- 文档补齐：更新 `docs/module-architecture.md`，把 `policygate`/`gatesocket`/
  准入信使/`spec.tools`/`harness` 写进去。
  **arch-lint 不需要为它们新增条目**——`oxedge_policygate` 与 `oxedge_gatesocket`
  与 `oxharness_*` 已在 `.go-arch-lint.yml` 里成对登记；
  而 `core/pig/extensions/opskeeper-gate` 是**独立 go module**，它的边界由 Go
  模块系统与 `scripts/modulecheck` 强制，再加一条 arch-lint 组件条目是重复覆盖。
- **`.go-arch-lint.yml` 与 `scripts/modulecheck` 有意重复**：前者是给人读的
  声明，后者是会跑的。`go-arch-lint` 在本机没装，所以只有后者在生效；
  两者必须一起改（决策 38）。

---

## 八、风险与假设

**风险**

- **PiG 0.x 双重不稳定**：缓解靠 `core/pig` 模块级收口 + 固定 commit +
  针对实际使用 API 面的契约测试套件。
- **插件安全是最大风险面**（高权限 bash + 第三方扩展）：缓解靠宿主强制三件套——
  能力注入式凭据、tool 白名单、宿主侧审计与审批。插件 **never** 获得审批放行权。
- **NodeFleet 连接规模**：每 edge 常驻 RPC 流，需要连接池上限、心跳重连、风暴抑制
  （沿用现有 tunnel 机制）。
- **RPC 延迟**：诊断类工具多一跳 stdio，需对交互式会话做流式背压
  （PiG 的 `EventCh` + `EventDone` 已支持背压丢弃）。

**假设**

- PiG 继续跟踪 Pi 0.87.1；插件契约以 Pi 为准，PiG 特有能力（fused/cellpack）
  只用于官方插件。
- **PiG 的 `mcp` 包类型当前只是声明，不是运行时**。这是本计划中唯一一处
  载体被事实修正的地方（可观测栈原计划走 MCP server）。若后续决定自建
  MCP 客户端，它是一个独立的新决策，不应被当作已有能力来规划。
- RAG、遥测采集、审计链不插件化，长期保留在宿主。
- Web 控制台不重写。已加「插件发布」页（`/admin/plugins`）；
  「插件市场」页沿用已有 `/skills?tab=install`（`settings/Marketplace.tsx`）
  及其 `/v1/marketplace/*` 后端；「节点 Agent」页复用现有
  `/devices/:edgeId` 与 `/admin/runtime`，未新开。
- 存量 Nacos Skill Registry 转为插件索引/灰度通道，不再是插件本体格式。
- AgentTeams 生态暂不并入 PiG 插件体系，作为独立集成保留。

---

## 九、可插件化边界一览

| 能力域 | 判定 | 载体 |
|---|---|---|
| 50 个运维 BaseTool | ⚠️ 部分 | 35 个已打包（只读 18 / 可观测 12 / 修复 5）；控制面 registry 的 `middleware-adapter` 另有 **100 个符号**（pg/redis/k8s/mq/git）**未被任何包服务** |
| 7 个 Worker persona | ✅ 可插件化 | package `agents` + `skills` |
| Skill Registry | ⚠️ 降级为分发源 | Nacos 只做索引/灰度 |
| System prompt 组装 | ⚠️ 拆分 | 骨架留宿主，能力清单由 `before_agent_start` 注入 |
| 安全策略 / HITL 审批 | ✅ 可插件化 | `tool_call` 事件 Block/Reason；裁决权留宿主 |
| 告警规则 / 草稿 | ✅ 可插件化 | extension tool + command |
| 拓扑图 | ✅ 可插件化 | extension tool |
| 可观测栈（Prom/Loki/Tempo） | ✅ 可插件化 | extension tool（**已用**）；PiG 的 `mcp` 仅声明 |
| 中间件适配（pg/redis/k8s/mq/git） | ✅ 可插件化 | 控制面 registry 里**真实存在**（`middleware-adapter` 100 个符号）；**尚未打包成插件包**——`plugin-coverage` 现在报的 18 个 GAP 全部来自这一条 |
| Web 控制台 | ❌ 不可 | 保留 manager 侧 |
| 身份/租户/权限 | ❌ 不可 | 保留宿主 |
| 审计 HMAC chain | ❌ 不可下放 | 宿主强制，插件只读 |
| 提案/审批工作流 | ❌ 不可 | 保留宿主 |
| Edge 遥测采集器 | ❌ 模型不同 | 保留 supervisor，改为打包分发 |
| RAG | ❌ 不在 PiG 范畴 | 保留，可包成 MCP 暴露 |
| 评测 harness | ⚠️ 独立模块 | 保留 |

**结论**：Agent 侧能力几乎 100% 可插件化（约占代码量 60%）；控制面
（身份/审批/审计/工作流）与数据面（遥测采集）不可插件化。架构上这条边界
被固化成模块依赖方向——插件能碰的与碰不到的，靠编译器而非纪律保证。
