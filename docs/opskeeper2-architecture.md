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

        唯一允许 import github.com/MichaelKinsy/PiG 的地方：core/pig
```

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

**为什么插件不自己实现工具**：`internal/skill/builtin` 里 13 个 `host_*`
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

| 模块 | 结果 |
|---|---|
| 根模块 `go test ./... -count=1` | **180 包 ok / 3 failed（88 包无测试）**——3 个失败仍是既有的 `spill_helper` |
| `core` | 全部 ok（3 包） |
| `core/pig` | 全部 ok（4 包） |
| `core/edge` | 全部 ok（4 包） |
| `core/harness` | 全部 ok（13 包） |
| `sdk` | 全部 ok（1 包） |

3 个失败全部位于 `internal/skill/builtin` 的 `TestTruncateOrSpill_*`，在干净
检出上同样复现，属既有问题，与本次改造无关。
`go run ./scripts/modulecheck .` → 边界全部成立（模块规则见决策 37，BC 规则见决策 38）。
`make module-test` 一次跑完全部模块。

| 阶段 | 内容 | 状态 |
|---|---|---|
| **A 模块化地基** | `go.work` + 6 个独立 `go.mod`；`core`（domain/ports/wire）；`core/edge`；`core/pig`；**`core/harness`**（决策 37）；`sdk` 清单准入 | ⚠️ 部分——除 `manager` 外全部拆出 |
| **B PiG 适配层** | `pigmodel`（settings→`*ai.Model`）、`pigagent`（含 `buildPrompt` 历史回放，见决策 25）、`pigrpc`（`pig --mode rpc` 客户端）、`pigwire`（SSE 帧翻译）；**`go-openai` 已整包移除**（`internal/pkg/llm` 自持 HTTP wire，见决策 22）；**工具治理已抽成与内核无关的装饰器**（见决策 24）；**PiG 支撑的 `llm.Client` 已落地并接入装配层**（`internal/pkg/llm/pigclient.go` + `pigsettings.go` + `pigregistry.go`，`OPSKEEPER_LLM_BACKEND=pig` 切换，见决策 26）；**内核侧宿主绑定已落地**（`internal/manager/biz/aiops/agentkernel/`：`ToolBag` + `Persister`（同时是 `ToolCallRecorder`）；`internal/manager/biz/aiops/chatruntime/kernelsink.go`：`ports.EventSink` → 控制面事件，含准入/结算两帧的 join，见决策 27/28；审计/预算/审批/依赖装配四件套落在 `agentkernel`，见决策 30；历史回放改为一计划两渲染，见决策 31；**换内核接缝已开**：`Runtime.Handle` 第 5d 步分流 + `kernelpath.go` 驱动 `ports.Agent`，见决策 32） | ⚠️ 部分——模型接口与**编排接缝**都已就位，**装配层已接线**（`OPSKEEPER_AGENT_KERNEL=pig`，见决策 33）；**eino 已彻底移除**：`go.mod`/`go.sum` 中 `cloudwego/eino` 与 `eino-contrib/jsonschema` 双双消失，`chatruntime` 只剩内核一条路（见决策 34） | ✅ 已落地 |
| **C 节点 Agent** | `pigsupervisor`（崩溃重启/退避/Degraded）、`policygate`（白名单+审批+digest）、`gatesocket`（unix socket 准入）、准入信使 extension、tunnel 7 个 `agent.*` 方法 + `agent.decide`、控制面 `NodeFleet` + `Service.Decide` + HTTP 决策端点、per-session 角色表 | ✅ 已落地 |
| **D 插件生态** | L1 只读 profile（18 工具 + 7 persona + 信使）、`pluginimport` 导入器、**B1 只读工具集**（工具集 extension + `toolbroker` + `agent.tool` 反向调用 + 双向漂移测试）、**B2 可观测工具集**（12 只读工具，schema 由控制面 registry 生成，全量 upcall）、**B3 修复包**（L2/5 工具/`approval.required`/`pod` 半径/pin 安装 + 审批回执 + 写操作全部走控制面）、**审核流水线**（ed25519 树签名 + 信任库 + 签名→清单→准入三段审核 + 灰度波次闸门 + 节点侧 `admitPackages` 接线）、**发布运输通道**（`plugin.install` / `plugin.remove` / `plugin.list` + 节点 `pluginStore` + 控制面 `ReleaseManager` + 6 条 `/v1/plugins/releases` 路由）、**控制面适配器真实化**（pg/redis/k8s/mq/host 五条，见「闭环修复派发链路」） | ✅ B1/B2/B3/审核流水线/运输通道全部完成；`git` 适配器仍是骨架 |
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

五个中间件适配器各自独立按环境变量装配，装到**同一个 registry** 上——
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

参数缺口在闸门里被**再分成两类**：`resolvable`（有提取器，值在证据里）与
`no evidence records this value`（值根本没被采集过）。两者今天都是拒绝，但
需要的工作完全不同——给后者写解析器，是给一个从没被观测到的 Pod 名写查找表。

装配代码 `cmd/opskeeper/loop_adapters.go`，测试 `loop_adapters_test.go`
（无 DSN 空注册表 / 坏 DSN 被跳过不影响其它适配器 / `local://` 真的注册了
`host.garbage_collect` 与 `host.restart_service` / 环境变量名是部署契约）。

### 验收闸门（计划 §五）现状

| 阶段 | 闸门 | 状态 |
|---|---|---|
| A | 6 模块 `go build` + 全量 `go test -count=1` 绿；arch-lint 拦住逆向依赖 | ⚠️ 部分——**6 个模块里 5 个已拆出**（`core`/`pig`/`edge`/`harness`/`sdk`），只有 `manager` 还在根模块；逆向依赖由 `modulecheck` 真正拦住（决策 37/38） |
| B | **SSE 帧 golden 逐帧一致** | ✅ 两条路径各有一份 golden，且互相逐字节相等（见下） |
| B | **eino / go-openai 依赖清零** | ✅ `rg eino go.mod` 无命中；`internal/manager/biz/aiops/graph/`（14 文件）与 `internal/pkg/llm/eino_*.go`、`budget_callback.go` 全部删除；`chatruntime` 测试全量迁到 `scriptedKernel`；`OPSKEEPER_AGENT_KERNEL=pig` 与退役拼写 `graph` 解析到同一内核（见决策 34） |
| B | **7 provider 冒烟** | ✅ `pigmodel/smoke_test.go`：7 个 provider 各起一个 httptest SSE 源，真实走 `Provider.Stream` |
| B | `go test -race` 无泄漏 | ✅ `core/pig/...`（198）、`service/plugin` + `edgeagent/biz`（60）、`cmd/opskeeper-edge`（118）、`internal/pkg/llm`（107）全部 `-race` 通过 |
| B | **`llm.Client` 换实现（PiG 支撑）** | ✅ `pigclient_test.go` 用 PiG 真实 provider 栈跑 httptest：选择/注册表/转写/请求体/流式/回传全链路，含 tool-call 往返与预算「先扣后发」；`router.go` 的子客户端工厂让**每个** provider 都走 PiG（见决策 26） |
| C | 端到端剧本 `alert_storm` / `rca_loop` / `recovery_verify` 在新拓扑下通过 | ✅ `internal/manager/biz/nodefleet/e2e/` 6 个用例接在真 `NodeFleet` + 真 `AgentBridge` + 真 `policygate` + 真 `pigwire` 上跑（见决策 36） |
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
   `internal/skill/builtin` 已有实现；`get_topology` / `query_alert_rules`
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
22. **LLM SDK 已从请求路径上移除，只剩 eino 的 agent 编排**：`internal/pkg/llm`
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
    （如 `internal/pkg/llm/pigclient.go`），落在 `core/pig` 就是反向依赖。
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
    - **设置源适配器放在根模块**（`internal/pkg/llm/pigsettings.go`）。`core/pig`
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
   - **`StartEvents` 的实现与它自己的契约相反**：`internal/edgeagent/biz/agent_rpc.go`
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
   - **可执行的那份放进 `modulecheck`**：BC 两两不可达、`internal/pkg`
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
     剩下的 9 个是**参数缺口**（要 pid / pod / queue），不是实现缺口。

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

### 当前真实缺口

- **B1/B2/B3 已闭环**：18 个节点本地只读工具、12 个可观测只读工具、5 个写工具
  均已打通。写工具全部经控制面 reviewer，且要消耗一次性审批回执；
  `host_restart_service` 的本地执行被证明确实锁死（回归测试可复现该失败）。
  可观测 12 工具覆盖 PromQL / LogQL / TraceQL / 数据库源 / 代码仓库 / 审计历史。
- **B2 原本计划走 MCP，PiG 不支持，已改为 extension toolset**：PiG 的 `mcp`
  包类型**只是声明**——PiG 全仓中所有 MCP 引用都在
  `coding/packagecontent/packagecontent.go` 与 `cmd/pig/package_*.go`
  （解析/校验/清单），**没有** JSON-RPC 客户端、**没有** `initialize` /
  `tools/list` 握手、**没有**把声明的 MCP server 接进 agent 工具集的桥。
  所以「可观测栈 → MCP server」在当前 PiG 上需要一个从零写的 MCP 运行时，
  而 upcall 通道已经端到端跑通 12 个工具且带鉴权、审计、白名单与回归。
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
  与全部测试）、`internal/pkg/llm/{eino_routing,budget_callback}.go` 及其测试；
  provider 常量与 `ErrUnknownProvider` 搬到新的 `internal/pkg/llm/providers.go`。
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
- `manager` / `edgeagent` / `harness` 的机械式包迁移未做（当前仍在 `internal/`；
  `core/edge` 目前含 gatesocket/pigsupervisor/policygate/toolbroker）。
- `docs/module-architecture.md` 尚未补 `policygate`/`gatesocket`/信使/`spec.tools`
  （arch-lint 侧已齐，见「待决的大动作」）。

---

## 七、未来路线图

### D 阶段续：把声明变成实现

1. ~~**B1 只读工具集**~~ ✅ 已完成：拓扑 4 件套、`query_alert_rules`、
   13 个 host 探针全部打通。实现留在宿主（`internal/skill/builtin` 与
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
7. 🟡 **参数解析器（`ArgResolver`）**：闭环还剩 9 个动作"名字有实现、参数派发不
   出去"——`pg.kill_session` 要 pid、`k8s.evict_pod` 要 pod、`k8s.scale` 要
   deployment+replicas、`mq.drain_queue` 要 queue、`k8s.rolling_restart` 要
   deployment、`mq.replay_messages` 要 queue、`pg.connection_pause` 要 role、
   `redis.client_kill` 要 addr、`host.restart_service` 要 unit。`RegistryInvoker`
   已经有 `ArgResolver` 接缝，缺两样东西：
   - **定位信息从哪来**。`RemediationOption` 只有 `Action` + `Target`
     （形如 `"pg:alert-17"`，只有资源类型与告警 ID），而 pod / pid / role / queue
     都不在其中。要么让 investigator 在证据里带上定位信息（例如 `k8s.pod_list`
     的证据里本来就有 pod 名与 namespace），要么给 `RemediationOption` 加一个
     定位器字段——**前者的数据已经存在，后者要动被校验的合同 schema**，因此
     先做前者。
   - **每个动作一张"证据字段 → 工具参数"的表**，并且**缺一个就拒绝**，
     绝不填默认值。把猜出来的 pid 打进 `pg_terminate_backend`，就是把"终止那个
     长事务"变成"终止 planner 碰巧先读到的那个后端"。
   - **已落地的那一半**（决策 44）：`EvidenceArgResolver` 从
     `RootCauseJSON.EvidenceChain` 取值，覆盖 `pg.kill_session`(pid) 与
     `pg.connection_pause`(role)。`pg_stat_activity` 的行在合同表里既是
     `[]map[string]any` 又可能是 JSON 往返后的 `[]any`，两种形态都读。
     没有声明提取器的动作返回 `(nil, nil)`，把话留给 invoker 自己的缺参拒绝——
     解析器不该对它一无所知的动作声称有权威。
8. ⏳ **旧 `mq/kafka` + `mq/rabbitmq` 骨架的去向**：两个包仍在，`kafka.` /
   `rabbitmq.` 命名空间保留未动，与新的中立 `mq.` 并存。可选方案是让它们委托到
   新包（一套实现、两个命名空间），或明确标为待删。**这是命名空间的治理决定，
   不是技术障碍**。
9. ⏳ **`git` 适配器仍是骨架**（7 + 1 工具，只有 `find_runtime_link` 真实），
   闭环动作表里没有 `git.*`，所以它不阻塞闭环，但会阻塞
   `git-artifact.LinkK8sImage` 这类根因证据。

### E 阶段：生态治理

- **兼容矩阵**（`internal/pkg/pluginmanifest/version.go`）：两个**独立**轴。
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
- **跨云迁移模板**（`internal/pkg/pluginmanifest/profiles.go` +
  节点 `OPSKEEPER_EDGE_PROFILE`）：
  - `finance-strong-consistency`：L2 上限 / `pod` 半径 / pin 安装 /
    不给 `k8s.exec`、`db.write`、`mq.write`。
  - `saas-multitenant`：L3 上限 / `namespace` 半径 / rolling 安装 /
    scope 全给。
  - profile 与单字段环境变量**矛盾即拒绝**（`nodeProfilePolicy`），因为
    半套用 profile 的主机跑的是没人写过的策略；"哪个文件最后改的"不能
    成为一台主机的权限来源。narrowing（少给 scope）同样拒绝——它导致的
    失败是隐形的：处处装好、只有这里没装，没有任何报错。
- **覆盖率闸门**（`internal/pkg/pluginmanifest/coverage.go` +
  `opskeeper-eval plugin-coverage`）：把 golden case 的
  `<family>.<method>` 期望与插件包能力做对照，缺口必须**被解释**——
  要么归 `MiddlewareFamilies`（控制面 adapter，不是包），要么归
  `NonPackageFamilies`（关联器，不是工具族）。当前真实结果：
  **2/20 全绿**（host 两例由只读包覆盖），其余缺口全部落在控制面
  adapter 上——这是仓库真实状态，不是回归。
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
  降到 **0**。闸门同时把"名字有实现但没有参数可派发"单列一类，当前 **9 个**
  （`pg.kill_session` 要 pid、`k8s.evict_pod` 要 pod……），这一类不需要写适配器，
  需要参数解析器——把两类混在一起会让人去实现一个已经存在的工具。
- **插件全链路**：第一次运行查出 **节点会同时发布同一个包的多个版本**——
  升级保留旧目录是对的（回滚要用），但重发布把两个目录都写进了 agent 的包
  列表，"每个包名一个版本"这条规则从来没被表达过。第二次运行又查出**回滚
  在协议上无法执行**：管理器没有旧版本的 URL，而 `Install` 需要它。

### 待决的大动作

- **机械式模块迁移只剩 `manager`**：`internal/manager` + `internal/iam` →
  `core/manager`（775 文件 / 约 200K 行，423 处 `internal/pkg` 引用加 7 个
  兄弟包，整仓重构级别）。`edgeagent` 已在 `core/edge`，`harness` 已在
  `core/harness`（决策 37），`sdk` 独立。
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
  剩下的缺口是**语料词汇与适配器词汇仍未对齐的少数符号**
  （`redis.scan_and_delete`、`redis.scan_and_redistribute`、`kafka.restart_broker`、
  `rabbitmq.scale_consumer`、`k8s.cleanup_logs` 等）——其中
  `redis.kill_client` ↔ `redis.client_kill` 仍是需要人裁决的改名，闸门按精确
  匹配算缺口是有意的。
- **闭环能提议、但派发不出参数的 9 个动作**（决策 43/44）是闭环当前最后一层缺口：
  `RemediationOption` 只有 `Action` + `Target`，而 `pg.kill_session` 要 pid、
  `k8s.evict_pod` 要 pod、`mq.drain_queue` 要 queue。`RegistryInvoker` 在
  `ArgResolver` 缺失时**拒绝派发并说明原因**，不会用默认值猜——把猜出来的 pid
  打进 `pg_terminate_backend` 就是把"终止那个长事务"变成"终止 planner 碰巧先
  读到的那个后端"。生产侧的解析器已经落地（决策 44）：`EvidenceArgResolver`
  从**调查已经记录的证据链**里取值，`pg.kill_session` 的 pid 与
  `pg.connection_pause` 的 role 取自 `pg_stat_activity` 证据行，
  **候选多于一个就拒绝并列出候选**——"随机挑一个"不是修复。
  剩下 7 个动作（k8s/mq/host/redis）卡在同一个前置条件上：
  **证据里根本没有那个资源名**，Pod 名与队列名不在现有的 prom/log 证据里，
  需要先让对应 investigator 采到它。
- **语料与能力表的 14 个符号缺口**（决策 41，`make eval-vocabulary` 可复现）：
  根因 3 个（`k8s.top_pods`、`pg.replication_status`、
  `git-artifact.LinkK8sImage`），修复 11 个（`k8s.uncordon`、`k8s.drain`、
  `k8s.resize_pvc`、`k8s.cleanup_logs`、`kafka.restart_broker`、
  `kafka.scale_consumer`、`kafka.repartition`、`rabbitmq.scale_consumer`、
  `redis.kill_client`、`redis.scan_and_delete`、`redis.scan_and_redistribute`）。
  其中 `redis.kill_client` ↔ `redis.client_kill` 看起来是**改名未对齐**而不是
  能力缺失——这一条需要人来裁决，闸门按精确匹配算缺口是有意的。
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
| 50 个运维 BaseTool | ✅ 可插件化 | extension tool（**已用**）；MCP 需自建运行时 |
| 7 个 Worker persona | ✅ 可插件化 | package `agents` + `skills` |
| Skill Registry | ⚠️ 降级为分发源 | Nacos 只做索引/灰度 |
| System prompt 组装 | ⚠️ 拆分 | 骨架留宿主，能力清单由 `before_agent_start` 注入 |
| 安全策略 / HITL 审批 | ✅ 可插件化 | `tool_call` 事件 Block/Reason；裁决权留宿主 |
| 告警规则 / 草稿 | ✅ 可插件化 | extension tool + command |
| 拓扑图 | ✅ 可插件化 | extension tool |
| 可观测栈（Prom/Loki/Tempo） | ✅ 可插件化 | extension tool（**已用**）；PiG 的 `mcp` 仅声明 |
| 中间件适配（DB/Git） | ✅ 可插件化 | extension tool（**已用**） |
| 中间件适配（K8s/MQ） | ❌ **无只读工具** | 控制面 registry 里不存在，仓库内不假装覆盖 |
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
