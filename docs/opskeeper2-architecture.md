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

## 四、设计主线（架构的全部争议点都在这里）

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

### 4.5 AI 层说 PiG 的话：没有第二套模型词汇（决策 67）

这是 B 阶段收口之后剩下的那一层，也是「彻底改成 PiG 风格」与「加个适配层」
真正分开的地方。

**改造前，模型调用有两套词：**

```go
// core/ports —— OpsKeeper 自己的
type LLMRequest struct { Model, Provider string; Messages []Message; ... }
type LLMResponse struct { Content string; Usage Usage; ... }

// core/manager/pkg/llm —— 另一个
type Client interface { Chat(ctx, ChatReq) (*ChatResp, error) }
type ChatReq struct { Model, Provider string; Temperature float64; Messages []Message }
```

**改造后，只有一套：**

```go
// core/pig/pigmodel —— PiG 的 ai 类型，别名以外没有任何东西
type Request struct {
    Selection domain.ModelSelection   // 「用我们配置里的哪个模型」是宿主问题
    Messages  []ai.Message            // 已经是 PiG 的
    Tools     []ai.ToolSchema
    Tune      func(*ai.StreamOptions)
}
type Completer interface {
    Complete(ctx, Request) (*ai.AssistantMessage, error)
}
```

被删掉的是 `ports.LLMRequest` / `LLMResponse` / `Conversation` / `Message`、
`pkg/llm` 的 `Client` / `MultiClient` / `Router` / `Wire` / `Metrics`，
以及 `llmpig` 里的 `pigclient.go` / `pigregistry.go`。**这一层当初买到的
只有一件事**——调用方可以在不 import PiG 的前提下描述一次请求；**它付出的
代价是每个字段写两遍、每次调用做一次转换、而每次转换都是一个可以静默丢掉
`tool_call` id 或 thinking block 的地方。症状不是报错，是模型悄悄不再调用
工具。**

**四条留下来的硬约束：**

| 约束 | 为什么 OpsKeeper 必须自己扛 |
|---|---|
| **模型白名单在宿主**（`pigcoding.ErrNoSuchModel`） | PiG 对未列出的 slug 是放开的——对 CLI 合理，对一个多租户平台的成本与合规管控不合理 |
| **凭据不进 `auth.json`** | 服务器没有交互式登录；`NewAuthStorage` 还会强制创建一个空的 `auth.json`（0600）。设置表 → `ModelRegistry.SetProvider`，轮换即重注册，无缓存可失效 |
| **`TranscriptUsage` ≠ `ai.Usage`** | 前者是存储行（4 个 token 数 + provider 上报总数 + 成本），后者是 provider 线格式（含 reasoning / cacheWrite1h / 五项 cost）。合并意味着每加一个 provider 就要重写全部历史行 |
| **每请求 temperature 走 `Tune`** | `StreamOptions.APIKey` 是每请求注入的，registry 填完凭据之后 `Tune` 才跑——抽取器要 0、翻译器要 0.1、judge 要 0，理由各自不同 |

**`pigcoding` 做什么、不做什么**（这是它与「适配层」的分界线）：

```
做：  决定 PiG 状态放哪   —— 私有 agent 目录（不写 ~/.pig）、内存 session log（不写 .pig/sessions）
      决定生命周期       —— 一 Runtime/Services 进程级；close 顺序严格：Session → Runtime → Services
      re-export 类型     —— alias，零运行时行为
不做： 不定义任何 OpsKeeper 形状的 request/response
```

**为什么 `core/pig/pigai` 这个 alias 面是必需的**：PiG 只允许被 `core/pig`
一个模块 import（`modulecheck` 强制）。宿主需要 `ai.Message`，如果直接 import
`PiG/ai`，闸门会失败；而**放宽闸门比造 alias 更糟**——它拿一条真不变量换一次
安静的构建，账单在 PiG 下次改内容块时到达。所以宿主写 `pigai.Message`，
它是 `ai.Message` 的**同一个类型**，不是翻译。加进这个列表的每一项都是一句
声明：「PiG 对这个概念的定义就是 OpsKeeper 的定义」——所以名单是手写且短的。

**`harness` 的依赖被有意放宽**（`core/harness` → `core/pig`）。judge 收到的是
`*pigai.AssistantMessage` 并从内容块读文本，在 harness 外面再声明一个单方法
接口、背后放一个手写响应结构，买不到任何东西：PiG 的消息类型照样得 import，
只是躲在第二套词汇后面，而那套词汇每次 PiG 改形状都要跟着改一次。真正保住
的属性是**不引入 provider SDK**——`pigmodel` 是契约不是客户端。

### 4.6 控制面下发的 prompt 不是命令：RPC 的斜杠劫持（决策 68）

**这是通读 PiG SDK 文档时挖出来的、生产可达、且阶段 D 会放大的缺陷。**

`pig --mode rpc` 的 `prompt` 指令**不是提交文本的方式**。在接纳一个 turn
之前，agent 会先问自己的命令目录「这条消息是不是斜杠命令」
（`cmd/pig/rpc_mode.go:634`，解析在 `extensionCommand`
`cmd/pig/rpc_mode.go:121-141`）。判据是纯词法的：**首字节是 `/`，且第一个
token 匹配任一已加载扩展命令的 invocation name**。命中就执行命令，turn
根本不发生——操作员看到的是一条命令在跑，不是一次排查，而且**响应里没有
任何东西说明这次请求被改读了**。

第二个门更窄但同类：`ExpandSkillCommand`
（`internal/codingagent/skills.go:267-270`）把开头的 `/skill:<name>`
改写成该 skill 的正文。

**为什么这是生产可达的，而不是理论风险**：`ports.AgentProcess.Prompt`
的契约写的是「一次用户 turn」，控制平面也确实照做——控制台文本从
`nodeagent.Service` 经 `nodefleet.Fleet` 原样下发到 agent
（`core/manager/biz/nodeagent/service.go:253`）。**整条路径都不知道目的地
有命令派发器。** 而文本不只有操作员口语：告警、日志片段、RCA 上下文都以
来源系统的原样开头，运维数据里首字符是 `/` 很常见——文件路径、URL、
label selector。任何一个被粘贴进会话，就差一次碰撞。

**而且碰撞面随插件生态增长**：每个扩展注册的命令都是可匹配的名字，触发前缀
集合等于第三方包愿意发布什么。这是**阶段 D 的东西漏进了阶段 C 的数据
路径**——错误的发现顺序。

**为什么中和而不是拒绝**：拒绝更安全，但是错的。文本是合法的——
「/var/log 快满了」就是操作员真会打的东西；而这里防的失败是**沉默**，不是
恶意。拒绝一条合法消息只会教会操作员去改写自己的输入，而他们下一步就是
把斜杠删掉，那恰好毁掉排查需要的证据。

**做法**：`core/pig/pigrpc` 的 `Prompt` 与 `Steer` 都过
`sanitizePrompt`——首字节是 `/` 就前置一个空格。两个门都测首字节，所以一
个空格同时废掉两个；模型看到的是一行带前导空格的文本，没有推理依赖它，而
操作员的原话完整到达。改写**在流上发一个 `opskeeper_prompt_rewritten`
帧**再提交 turn，所以通知排在被改写 turn 产生的事件之前。通知**不是日志行**：
`ports.ProcessEvent` 的契约明说词汇是开放的、未知类型照转发，所以审计拿到
的是已经在记录的那条流，payload 带**原文**——「无害的粘贴」和「第三方插件试图
截获 turn」只有靠原文才能区分。

**无条件生效，不是开关**：控制平面没有任何「用 agent 的命令语言说话」的
功能，所以走到这一层的首字符斜杠，按构造就不是命令。留一个关掉守卫的选项
只是把危害重新打开的开关。

**顺带修正的一处文档漂移**：`llmpig/doc.go` 与 `module-architecture.md`
此前都写「一个 turn 是 `pigcoding.Session`」。不是。PiG 把 agent 暴露在两个
高度上，而它们**不可互换**：`agent.Agent` 接受控制平面必须自己拥有的四个钩子
（`OnEvent` 流式、`OnMessagePersist` 落库、`BeforeToolCall` 策略闸门、
`FinishTurn` 预算、`DefaultStreamFn` 每请求凭据），而
`coding.SessionStartOptions` 只暴露 `BeforeToolCall` 与 `ExtraTools`，另外
三个一个都没有。内核直接嵌 `agent.Agent` 是必需的，不是偏好。`coding` SDK
仍然承重，在下一层：它拥有 `Services` 容器与每次模型解析都要经过的
`ModelRegistry`。

### 4.7 覆盖报告按方法 join，不按家族（决策 69）

**`plugin-coverage` 曾经报 20/20。实际是 0/20。**

旧 join 把 case 期望的**家族**（`pg.lock_waits` → `pg`）和插件包声明的
**家族集合**比对。而只读包发了 `pg.lock_waits`，于是 `pg.kill_session`
被判为「已覆盖」——**舰队里从来没有任何节点跑过这个工具**。20 个 case 每个
都同时命名诊断与补救，所以每一个补救期望都被这样抹平：真实覆盖是
**44/80 期望、0/20 case 完整**，而报告说的是满分。

**为什么这类假绿特别危险**：它不会在舰队发生变化时移动。舰队从「什么都不发」
变成「发了写工具」，报告仍然是 20/20；反过来舰队删掉一个工具，它仍然是
20/20。**一个对输入变化不敏感的指标不是指标，是一个装饰。**

**修法是精确 join，不是更严的启发式**：期望按工具名匹配，或经
`ExpectationAliases` 指向真正提供它的那个工具。两条都是精确的——这里不
从共享前缀推断任何能力。别名表刻意只有两条，且两条的目标都是**今天某个包
真的发了的工具**：

- `host.host_load` → `get_host_load`（可观测包；节点没有理由为了知道自己
  的负载去连一个 host-exporter）
- `git-artifact.LinkK8sImage` → `git.find_runtime_link`（linker 的 API 名
  是被测能力，包发的是承载它的适配器）

`redis.kill_client` **故意不在表里**。它确实对应适配器的
`redis.client_kill`，是真实改名——但在某个包真的发出这个写工具之前，
别名它等于宣称一个没有节点能跑的能力覆盖了，正是这次要消灭的假绿，
从侧门又进来了。诚实的报告行是「没有包提供它」。

**别名必须是双向被测的**：`TestEveryAliasPointsAtAToolSomePackageShips`
断言每个别名的目标确实被某个包声明。这条断言存在的原因就是上一段。

**缺口报告也从家族级升到方法级**，并且三种原因不再被折叠（这是旧代码
把它们混为一谈的地方）：
1. 「家族有人服务，但没有这个方法」→ 要不要打包，是个决策；
2. 「这个家族是控制面的适配器，没有包提供」→ 已经有人做过的决策；
3. 「没有任何包服务这个家族」→ 有人给不存在的工具写了 case，是 bug。

`CaseCoverage.Reasons` 与 `Uncovered` **并行排序**（一起排序再拆开），
因为理由是读者唯一会据此行动的部分，理由挂错行会把人引去打包错的工具。

**顺带补上的一处虚假守卫**：middleware 包（53 个工具，全舰队最大）的
`pig-ops.yaml` 注释声称有
`TestTheMiddlewareProfilesDeclaredToolsAreExactlyWhatItShipsToTheModel` 守着
「声明 == 实际」，并引用了一个不存在的路径
（`internal/middleware/toolset`）。**那个测试不存在**——注释是一个没有装上的
守卫，而它守的偏偏是最宽的一个包。现在装上了（`middleware_profile_test.go`，
7 项），并附带 L1 / 无审批 / 全 read / 只读 scope / 不写审计链 / 只装 edge /
有序唯一七条断言。

**33 个缺口不是 B3 未做**。计划里 B3 的定义是「`restart_service`、服务重启、
配置变更」，修复包五个工具已经覆盖。缺口全部来自 golden case 要求了计划
从未要求过的中间件写操作（`pg.kill_session`、`k8s.drain`、`kafka.restart_broker`、
`redis.flushdb`……）——**适配器早就实现了它们**，只是没有包声明它们。
所以这是 **case 跑在舰队前面**，不是计划落后。报告现在能一字不差地说出
是哪 33 个方法，因为那才是能据此写包的信息。

### 4.8 profile 是天花板，但没有人写下它装什么（决策 70）

**金融 profile 装不上它存在的理由。**

`ProfileFinance` 授予 `host.write`，`Intent` 写明理由是「重启单台主机上的单个
unit 是这个机队最需要自动化的操作」。而全目录里**唯一**使用 `host.write`
的是 `opskeeper-sre-repair`——**它同时还需要 `alert.write`**（包里的
`apply_config_change` 提交已确认的告警规则草稿）。finance 没有授予
`alert.write`。于是：

- `sdk.Admit` 在 finance 下**拒绝**修复包；
- finance 的 `NotGranted` 写的是「k8s.exec, db.write, mq.write」，
  **没有提到 alert.write**——被拒的操作员会被告知一个不相干的 scope；
- 每个字段单独看都自洽，整份 profile 却回答不了自己任何一句话。

**为什么一直没被发现**：9 个 profile 测试**全部在测 profile 自身**（层级、
半径、策略、措辞），**没有一个把 profile 和实际插件目录放进同一个断言**。
天花板是对的，组合根本不存在，所以「这个机队到底装哪些包」这个问题在整个
系统里没有任何地方回答。

**修法是补上「组合」这一半**。`Profile` 新增 `Composes`：这个 profile 安装
的包集合，有序、去重、被测试对着真实目录校验。于是三件事同时变成可证的：

| 断言 | 抓住的失败 |
|---|---|
| 组合里的包必须真实存在 | 模板第一次用就失败（装机时、生产上、事故中） |
| **组合里的包必须真的被该 profile 准入** | 文档描述了一个搭不出来的部署 |
| **被拒的 scope 必须在 `NotGranted` 里点名** | 操作员被告知一个不相干的 scope |
| 没有 profile 组合的包 = 永不上机 | 包能加载、能签名、能灰度，然后从未被执行 |
| finance 组合 ⊆ saas 组合 | 两个 profile 悄悄对调 |

**修的是 profile，不是测试。** finance 现在也授予 `alert.write`，理由写进
`Intent`：修复包把「重启 unit」和「提交告警规则草稿」捆在一起，只给一个
scope 会让旗舰包在唯一为它而设的部署里装不上；而这两个 scope **都不触及
交易状态**——一个重启 systemd unit，一个改规则何时触发。层级仍是 L2、
半径仍是 pod、仍需人工审批，**爆炸半径没有变宽一分钱**。

**这三条测试是承重的，不是装饰。** 我把修复回退后重跑，三个测试从三个
独立角度同时失败：

```
--- FAIL: TestEveryComposedPackageIsActuallyAdmittedByItsProfile
      finance composes opskeeper-sre-repair but admits it with nothing
--- FAIL: TestEveryProfileNamesEveryScopeItRefusesToGrant
      finance refuses "alert.write" to a shipped package but its
      NotGranted ("k8s.exec, db.write, mq.write") does not name it
--- FAIL: TestTheFinanceProfileCanActuallyRunTheRepairItExistsFor
      not granted: alert.write
```

一个在修复前后都绿的测试什么都没证明，所以这一步是必须做的。

### 4.9 兼容矩阵：让管理侧预检与节点裁决是同一个函数（决策 71）

**E 闸门的后半句「版本矩阵兼容性检查」此前只有算术，没有出口。**

`MeetsMinEdgeVersion` / `MeetsMinPigVersion` 实现完整、措辞考究，但**只被
`cmd/opskeeper-edge` 调用**——也就是只有节点会拒绝。这本身是**设计**，
`version.go` 写得很清楚：「manager 可以要一个包，只有节点能说自己跑不跑得了」。
真正缺的是：操作员在**开始发布之前**问不出这个问题。

于是一个混合机队的现实是：滚动发布打到 3 台旧节点上，操作员看三波失败，
再从失败日志里读出控制台本该一次告诉他的事——那 3 台是 0.7.2，这个包要
0.8.0。

**做法是投影，不是第二个裁决。** 新增 `pluginmanifest.CheckVersions`，
把两条轴按节点的顺序跑一遍；`Review` 改成调它。管理侧的 `Evaluate` 也调它。
**两侧不可能分歧，是因为根本没有第二份实现可以存在**——这不是靠测试维持的
性质，是靠结构。

> 两份各自正确的两轴检查，是两个等待分叉的答案。

**`Review` 的重构本身是被证明行为不变的**：仓库里原有的 11 项版本测试
（节点说不出版本 → `StepVersion`、只升级 edge 不能清掉 agent 步……）在重构
后全绿。另外新增一张 8 行等价表，对每组 (包要求, 节点版本) 同时跑 `Review`
和 `CheckVersions`，断言 allowed / step / reason 三者全等——四种可能分叉的
情形（两轴都失败、只有 agent 旧、只有 edge 旧、都不旧）各占一行。

**矩阵是管理侧唯一的新概念**：`Matrix{Hostable, Refused}` 分区而不是一个
带 flag 的列表，因为两者回答的是不同问题——「几个节点能装」和「我先修什么」。
拒绝行携带**节点自己会说的那句拒绝理由**，所以操作员在控制台和在节点上读到
的是同一句话，不是两句今天碰巧一致的话。

**三条 fail-closed，都是这个端点最容易犯、代价最大的错**：

| 情形 | 行为 | 理由 |
|---|---|---|
| 节点报不出版本 | **拒绝**，不计入 hostable | 未报告 ≠ 已知良好。计入会把它送进第一波金丝雀，在发布中失败 |
| 管理侧没接版本快照 | **503**，不返回空矩阵 | 这个端点存在的意义就是拦住一次坏发布；静默成功是最坏的失败 |
| 快照读取失败 | **报错**，不返回空矩阵 | 「机队是空的」和「读不到机队」是不同答案，塌缩成一个空矩阵会让操作员把发布发向虚空 |

**一个刻意的限制，以及它为什么不是缺陷**：管理侧**不持有 manifest**——
发布带的是 URL、摘要和签名，节点自己去取包、自己验签、自己审查。所以矩阵的
requirement 来自调用方，并由 `Matrix` **原样回显**在响应里：问错问题的人拿到
的是**看得见的**错答案，不是一个貌似合理的。

**我特意没有把 requirement 塞进 `PluginSpec` 送进节点**。那会让节点用控制面
的声明代替它从签名清单里读到的值——直接削弱签名模型。一个更方便的 API 换掉
一条安全性质，不做。

**agent 轴目前是空的，且是决定**。控制面不缓存 PiG agent 版本：
`Fleet.Health` 是逐节点实时 RPC，每次操作员打开页面就问一遍每个节点，是把读
变成故障的最短路径。所以这一轴**不猜**——`CheckVersions` 对读不出的版本
fail closed，说的是「无法判断」而不是「太旧」。现有包**没有一个声明
`min_pig_version`**，所以今天没有节点因此被拒；哪天有包开始声明，它会被
全机队拒绝并给出诚实理由——这是正确行为，也是「控制面需要健康缓存了」的
可见信号。**决策 73 补上了这个缓存，且刻意没有走轮询路线**（见 4.11）。

### 4.10 SDK 原文学习：两处缺口，和一个已经存在的调度层（决策 72）

决策 67 定了「AI 层说 PiG 的话」，决策 68 顺手修正了一处文档漂移——**turn 跑在
`pigagent.Kernel`（`agent.Agent` 之上）而不是 `pigcoding.Session`**。这一轮把
PiG SDK 文档与 `coding` 源码逐行对过，结论是：**这个选择当时是对的，但它现在
欠着两笔账，而其中一笔的上游答案已经自己长出来了。**

**先说 SDK 到底是什么。** 它是三层，不是「一个 API」：

```
coding.Services      进程级共享依赖：settings、认证、模型注册表、路径
      ↓
coding.Runtime       拥有扩展进程，创建 Session
      ↓
coding.Session       一个会话：Send / Prompt / Steer / FollowUp / Abort
                     Events() 事件流 · Entries() 追加式日志 · SetModel
```

官方明确要求「不是 Go 程序、或需要进程边界时才用 `pig --mode rpc`」。OpsKeeper
的**节点面**已经走了 rpc（阶段 C 的 `PigSupervisor`），**控制面**走的是进程内
`coding`——两者都合法，因为它们是两种不同的嵌入方式，不是一个该被消灭的错。

**账一：`MaxTurns` 在 `coding` 里没有接线。**

```go
// agent/agent.go:565 —— 0 = unlimited, as upstream, which has no turn cap
MaxTurns int
// agent/agent_loop.go:159 —— 注释自称 "A pig-only safety cap"
if limit := r.a.opts.MaxTurns; limit > 0 && r.turnIndex >= limit {
// coding/session.go:391 —— NewAgent 的 37 行 options 里没有这一项
```

`pigagent.Kernel` 传了 `MaxTurns`，所以控制面今天有 `defaultMaxIterations = 30`
和 persona 的 `MaxTurns` 覆盖。**换成 `coding.Session` 这两个上限会静默消失**，
而症状不是报错——是一个在生产节点上无限循环查中间件的 agent。补法是 PiG 自己的
动作：`BeforeToolCall` 钩子数轮次，到顶调 `Session.RequestAbort()`，让 SDK 自己
停；`SessionStartOptions.BeforeToolCall` 是现成字段。不做「让模型自己收手」那种
软上限。

**账二：`OnMessagePersist` 挂不上去。**

```go
// agent/agent.go:1329-1335 —— 构造后只有工具前后两个钩子
func (a *Agent) AddBeforeToolCallHook(h BeforeToolCallHook)
func (a *Agent) AddAfterToolCallHook(h AfterToolCallHook)
// 没有 SetOnMessagePersist
```

现在每条 assistant 行是在 `OnMessagePersist` 里落库的，**行 id 在落库那一刻生成
并回填到 SSE 帧**，控制台的气泡就是靠它绑定消息。SDK 路径的等价物是
`SessionManager`（`coding/session_manager.go:12` = `icodingagent.Session`，追加式
日志，不是消息快照）。改从 `Entries()` 读回意味着 **id 分配时机从「消息产生时」
变成「turn 结束后」**，SSE 帧的 `message_id` 与控制台气泡的绑定时序会变——这是
B 阶段验收闸门「SSE 帧 golden 逐帧一致」直接命中的改动。

**所以这一轮没有动控制面的 turn。** 两个缺口都真实、都能补，但补法会改帧契约，
而帧契约是前端零改动承诺的锚点。正确顺序是：先定 id 时序（要么行 id 改由
`SessionManager` 的 entry id 承载并接受帧变化，要么维持「产生时分配」而从
`Entries()` 做增量回填），再换驱动。这不是一个可以在没有结论的情况下顺手做的
重构。

**账三：真正的发现——调度层上游已经有了。**

PiG 最新提交 `f36a17c` 加入 `piglets/company`（`pig-cmd`），它做的正是计划 §二
里 OpsKeeper 自己要画的那张图：

| OpsKeeper 2.0 计划里的东西 | `pig-cmd` 里的对应物 |
|---|---|
| 每节点一个基于 pig 的运维 Agent | 一个 `pig-cmd` 节点，`company.yaml` 定义 roster / 预算 / 组织策略 |
| 协调者 + 7 个 Worker persona | coordinator 模型调 `delegate_task(agent, objective, max_tokens)`，委派只允许一层 |
| 节点 Agent 独立进程、崩溃不拖垮节点 | **一个 session 就是一个 virtual actor**，actor 重启就是进程重启，状态是 pig 自己写的 JSONL |
| 爆炸半径 / 成本治理 | `--max-total-tokens` / `--max-cost` / `--max-concurrent`，且是**常量而非配置**（编码的是观察到的失败模式） |
| 跨节点 agent.prompt / agent.state 路由 | `/v1/run` `/v1/delegate` `/v1/status` 控制面 + `--cluster` 的 session-owner 路由 |
| 未来接别的 Agent 生态 | **A2A**（JSON-RPC 2.0 over HTTP，protocolVersion 1.0），远端专家与本地专家同一套记账 |

它的设计立场值得直接抄进 OpsKeeper：**「Go 层只准入、路由、记账、恢复，从不决定
怎么拆任务——那是协调者模型的决定，所以没有会漂移的编排代码。」** 这正是决策 67
「不写第二套编排词汇」的同一条线。

**它同时回答了被搁置的 protoactor 问题**：`--cluster` 就是官方实现，挂在
`internal/mesh`，用 Proto.Actor Cluster，session id 就是集群身份，零迁移零交接。
而且上游的结论与我们一致——**先做核心**：`spikes/clusterpreview` 明确写着
「Not shipped behavior; nothing imports this module」，Cluster 上游自己标 Alpha，
多机是 Phase 3。OpsKeeper 保持「跳过 protoactor、先补核心功能」是对的，现在
还多了个理由：**不用自己写，它在 `pig-cmd --cluster` 里，而且是个 piglet——
`pig-cmd` 不 import 任何 PiG Go 包。**

**对计划的影响（不改架构方向，改的是自研量）**：`pig-cmd` 是 Piglet 而非 Stock
PiG（D85 记为 inert capability），所以 OpsKeeper 要么把它当外部进程编排（跟节点
agent 同级，走已有 tunnel/RPC 面），要么 fork 它改造成运维语义。**前者是默认
选择**——它已经满足「不 import 任何 PiG 包」这条 OpsKeeper 对第三方最看重的
性质。计划 §四 D 阶段因此少一项自研、多一项集成验证。

---

### 4.11 agent 轴的答案：让节点自己说，而不是让控制面去问（决策 73）

决策 71 把 `CheckVersions` 变成一个函数，矩阵两侧共用它，于是只剩一个真问题：
**控制面从哪里得到节点的 PiG 版本**。当时的答案是「没有」，并写下了为什么不能
轮询 `Fleet.Health`。这一条把那个「没有」补上了。

**为什么是心跳，不是 register_edge**。第一反应是把版本塞进已有的
`RegisterEdgeRequest`——它已经带着 `AgentVersion`，加一个字段是同构的。但
**AgentVersion 之所以在 register 上报，正是因为它在握手之后不再变；pig 版本
不是**。节点升级 → 进程重启到新二进制 → 从此以后一直在心跳。握手时抓的值会
永久冻结在节点**启动时**的构建上，恰好是操作员做发布决策时最不想要的那个数。
心跳本来就是「我当前是什么」的周期性自述（插件健康已经这么捎带），版本属于它
是构造上的必然，不是新增机制。

```
node ──heartbeat{edge_id, ts, plugins[], pig_version}──► manager
                                                       └─► edges.pig_version
                                                            └─► 兼容矩阵（一次查询，零 tunnel 流量）
```

**上报的是节点自己的答案，不是运行中进程打印的版本**。取值是 `pigSelfVersion()`
——和安装期 `Review` 判 `min_pig_version` 用的是同一个值。预检用一个版本、
节点用另一个版本裁决，正是兼容矩阵存在的意义所要消除的分歧。supervisor 的
`Health().Version` 更贴近「此刻跑的是什么」，但它一旦与 `pigSelfVersion()`
不一致（换过 PATH 上的二进制、升级失败后回滚），矩阵就会和节点自己的裁决对
不上——**用一个更「准」的数换一个会撒谎的答案，不做**。

**三条不做默认值的理由，逐条对应一个真实故障方向**：

| 若这样实现 | 后果 |
|---|---|
| 空版本写入空串 | 某一拍丢包 → 全机队 `min_pig_version` 包被拒，看起来像全域不兼容而不是一列空了 |
| 未变化也写 | 心跳 30s/节点/全机队，永远把一个常量列写成它已有的值；`edges` 同时承载所有 liveness 时间戳 |
| 读不出来就默认兼容 | 把「控制面没接上」变成一次静默的全绿放行——而这是唯一决定 agent 能否加载插件扩展的那一轴 |

**写失败不回传给节点**。心跳对节点的契约是 liveness，节点收到 error 会累加
连续失败并最终自杀重启。用一次进程重启换一列版本字段是反向交易——liveness 撑
起节点上所有其他功能，这一列只是发布页面的便利。**读-比-写**守卫是让这一列
保持被动的唯一办法：一次查询只在版本真的动了（升级）时才发生。

**测试按失效方向逐条钉，且逐个回退实现验证过承重**：

| 失效方向 | 测试 |
|---|---|
| 空值抹掉最后已知版本 | `TestANodeThatStopsReportingDoesNotLoseTheVersionItLastReported`（三种「空」形态） |
| 未变化仍每次写 | `TestAnUnchangedPigVersionDoesNotWriteOnEveryHeartbeat`（50 次心跳 0 写，且升级仍落地） |
| 写失败拖垮心跳 | `TestAFailedPigVersionWriteDoesNotCostTheNodeItsHeartbeat` |
| 线上字段名漂移 | `TestTheHeartbeatCarriesTheConfiguredPigVersionOnTheWire`（绕 `map[string]any` 断言 `pig_version`——两端同结构解码会让错 tag 也「通过」） |
| 从错误列取值 | `TestTheCompatibilityInventoryReadsBothVersionAxesOffTheEdgeRow` |
| 迁移没建列 | `TestMigrateCreatesAPigVersionColumnDistinctFromTheAgentVersion`（查 `pragma_table_info`） |

### 4.12 第二个闸门第一次有了读者（决策 74）

A 阶段只剩的那条债：`.go-arch-lint.yml` 是仓库里**唯一一处以组件为单位**授予
跨树权限的地方，而**没有任何东西读它**。modulecheck 读模块表和代码，go-arch-lint
读这份 yml——但 yml 自己不被检查。

**组件粒度为什么在这里是问题**。`manager_biz: mayDependOn: [manager_service]`
说的是「biz 整棵树可以看 service 整棵树」。作为架构陈述这是对的。一旦某条边
**只是因为某一个文件需要**才被加进来，它就顺带授权了之后所有同类导入：

- 那条 import 被删掉后，授权还在，**永久静默**；
- 下一个同类 import 加进来时，**不会有任何东西变红**。

yml 自己在第 20 行写着这句话（「已知债务：arch-lint 的 mayDependOn 是组件粒度
白名单，同组件新增同类导入不会再报警」）——**债务被写下来了，但没有被关掉**。

**做法：给 modulecheck 一个 yml 读取器，两条不变式。**（决策 77 后来补上第三条，
把这两条从「各管一头」变成对称的一对；下表保持决策 74 当时的记录。）

| 不变式 | 回答的问题 | 今天的违规数 |
|---|---|---|
| **每条授权都必须在用** | 有没有没人行使的权限？ | **104** |
| **每条逆向边都必须记名到文件** | 有没有跨层倒边没人命名？ | **1** |

第一条把 yml 从「权限清单」变回「对代码的陈述」。**104 条授权里没有一条有
真实 import 在用**——删掉之后 `make arch-lint-run` 仍然零告警。**这是本轮最
重要的验证**：提议删除的是我的检查器，裁决的是 go-arch-lint 自己。两个工具
独立确认同一份配置，不靠我的检查器自证。

第二条记的是**逆向边**：biz → service、model → data 这类指向自己上方层次的
import。这条规则以前根本不存在。写第一版时它当场抓到了一个活的、无人知晓的
倒边——

```
core/manager/biz/imbridge/adapter.go  →  core/manager/service/aiops
```

一个 use case 持有 HTTP 层的具体 service。modulecheck 的层规则只管
service→data 和 biz→data，管不到这一条；yml 的组件授权放它过去。**它不是
「暂时还没发现」，是「没有任何闸门会看这里」。**

**逆向边为什么写成清单而不是算层级**。层不是全序：data 与 biz 是**并列**的
（biz 声明接口、data 实现它），所以 data → biz 是正确方向，只有反向才是债。
用 rank 函数会把这个方向也一起报错。`upwardEdges` 因此是一条写出来的清单，
`data → biz` 缺席——它由另一条规则管着，重复写只会让两处漂移。

**两个在实现里撞上、并且决定了设计的问题**（都是「检查器自己错了」而不是
「代码错了」）：

1. **`filepath.Walk(".")` 的根目录 base 名就是 `"."`**，被自己的 dot-dir 规则
   整棵树跳过。失败是**静默且彻底**的：遍历正常结束、每个组件看起来都不导入
   任何东西、104 条授权全被报成死的。**方向和真问题正好相反**——它会诱使人
   去删除配置。为此专门写了 `TestTheWalkVisitsTheTreeWhenTheRootIsADot`。
2. **import 路径没有尾斜杠**。组件声明为 `core/edge/service/**`，而
   `core/edge/service` 目录自身的 import 是不带尾斜杠的裸路径。只匹配树形
   会让那个包不属于任何组件，**63 条 cmd 的授权因此被误报为死的**——其中包括
   `cmd/opskeeper-edge/main.go:37` 明确 import 的 `core/edge/service`。
   **如果照着那份报告执行，会删掉架构文件三分之一的授权。** 这也是为什么
   「死授权」这条规则的删除建议必须由 go-arch-lint 复核，而不能由检查器自己
   拍板。

**台账与既有惯例一致**：`layerInversion` 按文件记名并写理由，
`TestTheLayerInversionLedgerIsCurrent` 双向守卫（条目失效即报错、文件不存在
即报错），和 `layerDebt` 同一套约定——因为它们守的是同一条边界的两侧。

**四个变异验证承重**：

| 回退 | 结果 |
|---|---|
| 还原 dot-root 跳过 bug | ❌ dot-root 回归测试红 |
| 删掉死授权检查 | ❌ `TestADeadGrantIsReported` 红 |
| 删掉逆向边检查 | ❌ `TestAnUplistedUpwardEdgeIsReported` 红 |
| 真实仓库自检 | ✅ 零违规（35 tests 全绿） |

------

### 4.13 SDK 文档复读：一次把「待决」变成「已决」的核对（决策 75）

决策 72 读的是源码，72 留下的 A/B/C 悬着。这一轮读的是官方 SDK 页
（`pi-in-go.dev/docs/latest/sdk/`），目的是一件很窄但很致命的事：**核对
「latest 文档」和「我们锁定的 v0.3.0」是不是同一个东西。** 如果文档描述的
API 在 v0.3.0 上不存在，那么所有照着文档做的规划都是空中楼阁——而 v0.3.0 是
PiG 明确要求 pin 的版本（「Pin an exact release or commit when you embed it」）。

核对结果：**一致，且顺带查出一处必须更正的记账。**

| 文档里的能力 | v0.3.0 源码位置 |
|---|---|
| `coding.NewServices` / `NewRuntime` | `coding/services.go:96` / `coding/runtime.go:125` |
| `ai.NewFauxProvider` | `ai/faux.go:121`（签名是 `NewFauxProvider(FauxConfig)`，不是裸构造） |
| `coding.NewInMemorySessionManager` | `coding/session_manager.go:15` |
| `coding.NewInMemorySettingsManager` | `coding/settings.go:7` |
| `coding.CreateModelRuntime` | `coding/model_runtime_create.go:29` |
| `ObserveEvents` | `ai/stream_observation.go:337` |
| `SetScopedModels` | `coding/session_scoped_models.go:29` |
| `CreateAgentSessionRuntime`（工厂式 Session 替换） | **不存在**；v0.3.0 只有 `Session.SetRebindSession`（`coding/session_extension_replacement.go:14`） |

最后一行是这一轮唯一对不上的地方，而它恰好是最有用的一行：它说明**官方文档已经
跑到 v0.3.0 前面去了**。所以本节能给出的结论只能是「v0.3.0 上有这些」，不能是
「PiG 有这些」——这个区分就是「锁定版本」这条纪律买到的东西。

**一处更正：账二的理由是错的，结论碰巧是对的。**

决策 72 账二写的是「`agent` 包构造后只有 Before/After 两个钩子，没有
`SetOnMessagePersist`」。前半句对、结论对，**理由错**。钩子是一个字段，不是
构造后的 setter：

```go
// agent/agent.go:565 / 650
MaxTurns        int // 0 = unlimited, as upstream, which has no turn cap
OnMessagePersist func(AgentMessage) error
```

`pigagent.Kernel` 正是靠这两个字段活着的：

```go
// core/pig/pigagent/kernel.go:173-183
ag := agent.NewAgent(agent.AgentOptions{
    Model:           model,
    MaxTurns:        maxTurns,
    OnEvent:         gate.onEvent,
    OnMessagePersist: func(msg agent.AgentMessage) error { return gate.persist(msg) },
    BeforeToolCall:  []agent.BeforeToolCallHook{gate.beforeToolCall},
    ...
```

所以真正的缺口窄得多、也硬得多：**`coding.SessionStartOptions` 里没有
`OnMessagePersist` 字段**（`coding/runtime.go` 的结构体里没有），而
`coding.NewSession` 用它自己的持久化把这个接线点占掉了——
`internal/codingagent/session.go:633` 的注释自己承认 OnMessagePersist 是
「single persistence」入口。

为什么这个更正要紧：原来说法暗示「PiG 没有这个能力，我们只能从 `Entries()`
反推」。更正后的事实是「**能力在，面板没开**」。这句话直接决定了下一段。

**待决的 A/B/C 收敛为 C，而且是被证据收敛的。**

| 选项 | 裁决 | 依据 |
|---|---|---|
| A：行 id 改由 entry id 承载 | ❌ **不可实现** | `type SessionManager = icodingagent.Session`（`coding/session_manager.go:12`）是**类型别名到具体结构体**，不是接口。Go 里别名无法被我们的类型满足，注入自有日志这条路在 v0.3.0 上不存在，除非 fork PiG |
| B：从 `Entries()` 增量回填 | ❌ **没有动机** | `OnMessagePersist` 在 `agent.Agent` 层可用且 `Kernel` 已在用它分配行 id 并回填 SSE 帧。「等 turn 结束再反推」是纯粹自找的退化 |
| C：维持 `Kernel`，`pigcoding` 限定在设置与模型目录 | ✅ **成立** | 换过去会同时丢掉 turn 上限和行 id 时序，而这两样都是 B 阶段闸门要验的东西 |

A 的出局是这一轮最实质的收获：它把一个**看起来开放**的选项关掉了，关掉的理由是
一个类型别名——三行源码，一锤定音。

**账一（`MaxTurns` 不在 `SessionStartOptions`）随之自动作废。** C 成立意味着
Kernel 一直带着 `MaxTurns`，控制面的 `defaultMaxIterations = 30` 和 persona 上限
都在。仍然记一笔，只是为了不让下一个读源码的人重新发现一遍。

**`SessionStartOptions` 的完整字段 vs 我们 `pigcoding.Start` 暴露的字段。**
差异里有几个和 OpsKeeper 隔离模型直接相关，值得记下来但**现在不动**：

- `AllowedTools` / `ExcludedTools` / `ActiveBuiltinTools`：Session 层的工具
  白/黑名单。我们今天在 Kernel 侧用 `BeforeToolCall` 逐次裁决，那是**裁决**不是
  **面**——一个越权工具仍然会被放进工具表，只是每次调用被拒。这三个字段是第二道
  防线。但**不能当唯一防线**：一旦靠它们，面板一开，审批裁决就得整体搬家。所以
  记为 `pigcoding.Start` 的待补字段。
- `SkipExtensionTools`：控制面 Session 应当默认置位。与决策 74 记的那条活的
  倒边（`biz/imbridge` 持有 `service/aiops.Service`）是同一个方向的问题：控制面
  不该被节点插件影响。
- `ResourceLoader` / `SystemPromptResources`：与决策 67「能力清单由
  `before_agent_start` 注入」同一条线。我们目前自己组装 `SystemPromptSections`，
  PiG 也有资源加载器这条正路。

**两个能直接用、且正好落在闸门上的能力。**

其一，`ai.NewFauxProvider(FauxConfig)`（`ai/faux.go:121`）——官方文档说它
「把每个排队的响应按正常事件生命周期流出」。核对下来 **Kernel 的测试早就在用它跑
真实 turn 了**（`core/pig/pigagent/kernel_test.go` 的 `newFauxModel` /
`fauxResolver`），文档这一条对我们的边际价值只剩一件事，而且这件事不小：
`ai.splitByTokenSize` 用 `math/rand` 在 `MinTokenSize` 与 `MaxTokenSize` 之间抽
分块宽度，所以**流式帧序列天生不可复现**。`Min == Max` 时 `rand.IntN(1)` 是唯一
不随机的取值，golden 才有可能存在。§4.14 记的就是用上这个事实之后立刻查出的
一个真缺陷。

其二，`coding.CreateModelRuntime(ctx, opts)`（`coding/model_runtime_create.go:29`）
能不建 Session 就查模型、刷目录。`llmpig` 现在自己维护 catalogue
（`core/pig/pigcoding/provider.go` 的 `providerConfig` / `slugsOf`）。换不换属于
「控制面要不要多依赖一层 `coding`」的同一笔账，**与本节结论一致：不换**，记为
候选。

**不变的部分。** 文档原话仍然成立：「不是 Go 程序、或需要进程边界时才用
`pig --mode rpc`」——阶段 C 的 `PigSupervisor` 不动。另外记下一条对 SSE 有价值
但暂时兑现不了的语义：`AssistantMessageEventStream.Result` 与事件迭代**独立**
完成并保住终结消息的身份（`ai/stream_observation.go:337` 的 `ObserveEvents`
配套）。等控制面真的在 Session 路径上流式时它才有用。

**对进度的影响。** 本轮不改运行时代码，§六 的百分比不动（A 100% / B 100% /
C 95% / D 95% / E 95%）。变的是两件事：一个「待决」变成「已决」，以及 B 阶段
闸门补上了它真正缺的那一半（§4.14）。**待决清单因此从五项降到四项。**


### 4.14 补上闸门缺的另一半，以及一个自己差点报错的教训（决策 76）

决策 75 的核对把「B 阶段闸门已通过」这句话重新看了一遍，然后发现它**只对了一半**。

**闸门实际由两个测试扛着，它们各自只覆盖一半。** `golden_test.go` 把 Mapper 钉在
一份**手写的** `agent.AgentEvent` 脚本上，逐帧比字节；`kernel_test.go` 通过
`ai.NewFauxProvider` 跑**真实**的 PiG 事件流，但只断言「有没有 `tool_end` 帧」
这类存在性。**没有任何一个测试把两者钉在一起**——而这次迁移的全部风险恰恰就在
接缝上。手写脚本对「PiG 实际吐了什么」一个字都没说：它不能发现
`message_update` 与 `message_end` 之间多了一种事件，不能发现 tool call 的参数以
delta 到达而 mapper 没转发，也不能发现 usage 跑到了另一个帧上。

**先解决可复现性。** `ai.splitByTokenSize` 在 `MinTokenSize` 与 `MaxTokenSize`
之间用 `math/rand` 抽分块宽度，所以真实流的 delta 数量天生不稳定。`Min == Max`
时 `rand.IntN(1)` 是唯一不随机的取值——这是把「随机」变成「可钉」的那把钥匙。
变异验证：去掉这两个字段，golden **连跑 6 次全红**；加回去，全绿。

**为什么不逐帧钉 delta 的边界。** delta 边界是 provider 的分词器，不是我们的
契约：控制台在 `assistant_end` 重绘整轮，4 字节一块和整段一次到达都是对的。钉边界
会让这个 golden 变成 PiG 分词器的绊线，而不是 mapper 的绊线，失败时给出一份没人
能行动的 diff。所以 `coalesceDeltas` 合并连续 delta，钉的是**帧的种类、顺序与
载荷**。为了不让合并把丢失也一起吞掉，`TestStreamDeltasReassembleTheAnswer` 另外
断言拼回来的文本等于 `assistant_end` 的内容：掉一个 chunk 会变成内容不符，而不是
一条悄悄变短的 golden。

**查出来的东西，和我一开始读错的地方。** golden 一生成就出现了三条空内容的
`assistant_end`：

```
002 assistant_end pending=0 id="" ""      <- 运维自己那句提问
003 assistant_end pending=1 id="" ""      <- 模型的 tool call（这条是对的）
007 assistant_end pending=0 id="" ""      <- 又一次，提问
```

`agent_loop.go:112-116` 为**每一条 prompt 消息**发一对
`MessageStartEvent` / `MessageEndEvent`，`appendMessage` 对 **tool result** 也发
一对；`summarize()` 读到「没有文本、没有 tool call 的消息」，mapper 就渲染成一个
已结束的 assistant 气泡。**我据此写下了一条「每个 turn 多一个空气泡」的缺陷，并
准备记成待决项。**

**那是错的，而错得有价值。** 顺着帧往上游走，`chatruntime.kernelSink` 就是两者
之间的那道折叠：它**直接丢弃** `assistant_start` 与 `assistant_delta`（注释原文
「emitting either would show a phantom empty bubble」），并且每个 session **只保留
最新的一个** `assistant_end`（`kernelsink.go`，规则本身由
`TestASecondAssistantEndSupersedesTheFirst` 钉住）。所以控制台每轮看到的是**恰好
一个**已落库的气泡。**kernel 的帧流不是控制台的帧流**，中间有折叠；那些空帧是这条
规则的输入，不是运维会看到的输出。

真正缺的不是修复，是**写下这件事**。手写脚本只喂过「带 assistant 消息」的
`message_end`，所以真实流的形状从未暴露；一个不知道有折叠的人读这份 golden，会
报一个不存在的 bug——**我刚刚就是那个人**。所以注释写进了
`streamgolden_test.go`，让下一个读者不必重新走一遍这条路。这条比一个假缺陷值钱：
它记的是「我们自己的两层适配之间有一道折叠，而折叠规则只以注释形式存在过」。

**三条变异验证（逐个改坏再改回）**

| 变异 | 结果 |
|---|---|
| 去掉 `Min/MaxTokenSize` 固定 | ❌ golden 连跑 6 次全红 |
| mapper 丢弃 text delta | ❌ golden + delta 重装 + 覆盖检查 三红 |
| mapper 把 blocked 报成 error | ❌ golden 红（覆盖检查不红，因为 blocked 判定在其后覆盖） |

最后一行值得留着：它说明**覆盖检查不是承重的那一道**，golden 才是。有一道测试
能顶住、另一道顶不住，是正常分工；把两者当成互相印证才是误读。

**本轮新增**：`core/pig/pigagent/streamgolden_test.go`（3 个测试）+
`testdata/stream-turn.golden`。闸门跑法不变，仍然不依赖网络与真实 provider。
**待决清单没有因为这一轮变长**——多出来的那一项在核实后不成立，已就地撤掉。

---

### 4.15 读者补上了对称的另一半：授权与依赖（决策 77）

路线图把「arch-lint 债务清单的守卫」列为下一件欠账，理由是「同一个组件里新加
一条越层 import 不会再报警」。**这个定位是错的**，而错的方式值得写下来。

**先说它错在哪。** 决策 74 已经让 modulecheck 读 `.go-arch-lint.yml` 了，而且
`TestTheLayerInversionLedgerIsCurrent` 就在 `scripts/modulecheck/archlint_test.go`
里——清单是活的，会因为条目失效而报错。所以缺的不是「时效性守卫」。

**真正缺的是对称。** 决策 74 的两条不变式各看一头：

| 不变式 | 问的问题 |
|---|---|
| 每条授权都必须在用 | 有没有没人行使的权限？ |
| 每条逆向边都必须记名到文件 | 有没有跨层倒边没人命名？ |

**没有任何一条问「这条实际存在的依赖被授权了吗」。** 一次真实发生的跨组件 import，
如果导入方没有对应授权，modulecheck 一声不吭——而 go-arch-lint 会报。两条不变量
各自都是对的，合起来漏掉的正是它们之间的那一格。

**这个洞最该先被堵上的位置，恰好是决策 74 刚刚造出来的那五个组件。**
`iam_model`、`oxedge_model`、`oxpig_coding`、`oxharness_schema`、
`oxharness_leaderboard` 的授权被清空后，改用 `anyVendorDeps: true` 表达
「不依赖任何项目组件」。这五个组件从那天起就处在洞的正上方：给它们加一条跨组件
import，这个工具不会响。

**为什么不让 go-arch-lint 一个人管。** 它确实会报。理由不是「多一道保险」，而是
**两条闸门必须同时跑才拦得住一个错误的代价太高**：`make module-check` 是一条
Makefile 目标，`make arch-lint-run` 是另一条；只挂一条的那个总会在某次「先跑快的
那个」里被跳过。而且 modulecheck 的两条不变式**把授权当成需要解释的东西**（要么
有人用、要么记名到文件），go-arch-lint 只回答允不允许——同一个 yml，两种深度。

**落法。** `checkArchLint` 加第三条：遍历真实存在的边（`edges`，文件粒度），
凡是没被 `mayDependOn` 授权、也不在 `layerInversion` 里的，报出来。**`layerInversion`
在这里是一种授权而不是一种容忍**：被记名到文件意味着有人看过这条边。

**顺带解决了一件事：重复报告。** 逆向边同时满足检查 1 和检查 3 的条件——它既缺
`layerInversion` 记名，又缺 `mayDependOn` 授权。两条都报，同一个错误给两个不同的
建议修法，而人只会读一遍。所以把「这条边是不是逆向」抽成 `isUpwardEdge` 供两条
检查共用，检查 3 跳过它。**两条检查对同一个问题的判断必须一致**，否则一个只在一
半配置下工作的组件会被放过。

**真实树的答案是零违规。** 这本身是个结果：决策 74 删掉 104 条死授权之后，剩下
的每一条边都还能对上自己的授权。这条检查的价值不在于它今天抓到了什么，而在于
**它让「每条边都被解释」从一个愿望变成一个断言**。

**三条变异验证**

| 变异 | 结果 |
|---|---|
| 摘掉检查 3 整段 | ❌ 两个方向性测试红 |
| 检查 3 不再跳过逆向边 | ❌ `TestAnUpwardEdgeIsReportedOnceNotTwice` 红 |
| 真实树自检 | ✅ 零违规（modulecheck 39 tests 全绿） |

其中第一条**第一次跑时只红了一个测试**：另一个方向性测试本来指向 `data → service`，
而那是逆向边，被检查 1 抢先报了，所以它在检查 3 被删掉的情况下依然绿。这正是
「测试看起来像覆盖、其实不是」的典型——把它改成 `data → biz`（**规定方向**，
`upwardEdges` 故意不含它）之后，两个方向才各自只能被对应的检查抓住。

---

### 4.16 一个节点不是掉线才丢隧道：它是被自己的退避表打死的（决策 78）

计划 §六 把 C 阶段剩下的一项写成「NodeFleet 的连接规模：每 edge 常驻 RPC 流，需做
**连接池上限、心跳重连、风暴抑制**」。这一条查下来，三项里有一项是**真的缺**，
而且缺的方式正好是那种在单机开发环境里永远看不见的。

**已有的部分**：`core/floor/tunnel/client.go` 的 `Dial` 已经是指数退避，
1s → 2s → 4s → … 封顶 60s，日志里写着 `tunnel: dial failed; will retry`。
心跳重连也已经有（geminio 的 `RetryEnd` 透明重连 + `reconnectCallbacks` 让节点
在重连后重新 `register_edge`）。**缺的只有抖动。**

**为什么没有抖动是致命的，而不是「不够好」。** 指数退避是教科书形状，而它对一
支**节点队伍**恰恰是错的：每个节点从同一个失败里算出同一个等待，所以控制面重启
之后，全部边在同一毫秒发起重连；如果这时控制面还没就绪，60 秒后它们**再次**同时
发起——而且因为都到了封顶，间隔全部相等，这个同步**永远不会散开**。

**封顶本来是保险，恰恰是它让同步变成永久的**：在封顶以下，间隔本身会随翻倍而
自然拉开；到了封顶，所有间隔重新相等。所以修法不是调参数，是让等待**被抽出来**
而不是被算出来：上限（单个节点最坏多久能回来）保留，分布交给抖动。

**取 full jitter 而不是 half jitter。** half jitter（上限的一半 + 随机的一半）把
分布砍掉一半，低端还留着一个所有节点一起跨过的地板；full jitter 让两个同时失败
的节点在真正无关的时刻回来，这正是要的。抽到 0 不是忙等——它只是省掉一次 sleep，
而它前面那次拨号是真实网络操作，本来就要时间。

**一个看起来等价、其实不行的实现。** 从「上一次 wait」里抽而不是从「ceiling」里
抽，读起来完全一样，但连续的小随机数会把调度表一路拉回 1s，节点就以 1s 的频率
永远重试——而那正是封顶要防的频率。`TestTheCeilingDoesNotShrinkAfterASmallDraw`
用一个「永远返回 0」的抖动打这个洞，40 次之后 ceiling 必须还在 60s。

**接线也是被测的，这是本条最花力气的地方。** 抽出 `dialBackoff` 之后，
「调度是抖的」和「`Dial` 用了这个调度」是**两个不同的断言**，而只有前者是关于这
个文件的。第一次写的 herd 测试直接调 `fullJitter`，于是它在「把 `wait := b.jitter
(ceiling)` 改成 `wait := ceiling`」这个变异下**依然全绿**——它测的是一个辅助函数，
不是行为。改法是让断言穿过 `next()`，并且为此给 `Dial` 开了一个 `sleep` 接缝：
对着一台没人监听的端口（`127.0.0.1:1`）跑真实的 `Dial`，把每次等待读回来，
5 次重试在 1 毫秒内跑完。于是 8 秒的 ceiling 也能在单测里被断言。

**这一条自己踩了两次同样的坑，值得写下来**：
1. herd 测试测的是辅助函数 → 变异下不红 → 改成穿过 `next()`。
2. 方向性测试（决策 77）指向 `data → service`，那是逆向边，被检查 1 抢先报了，
   于是它在检查 3 被删掉时依然绿 → 改成规定的 `data → biz` 方向。
两次都是**测试看起来像覆盖、其实不是**。判据是「删掉被测的那段代码，哪个测试会
红」，不是「测试读起来是否在测那件事」。

**六条变异验证**

| 变异 | 结果 |
|---|---|
| `wait := ceiling`（不抖动） | ❌ 3 个测试红（含 herd） |
| 从上一次 wait 抽（ceiling 缩水） | ❌ ceiling 不缩水测试红 |
| 封顶改成 10 分钟 | ❌ ceiling 曲线测试红 |
| `Dial` 不用 `dialBackoff`，写死 30s | ❌ 两个 Dial 接线测试红 |
| `Dial` 忽略注入的 jitter | ❌ pinned 曲线测试红 |
| 真实树自检 | ✅ `core/floor/tunnel` 20 tests 全绿（其中 8 条是本轮新增的退避测试） |

**这一条没有改变架构，只把一个已经写好的退避循环从「单机正确」变成「队伍正确」。**
C 阶段的连接规模三项里，**风暴抑制已完成**；剩下的是**连接池上限**（`Fleet.Open`
对每条边的常驻会话数没有上界），由 §4.17 接手。

---

### 4.17 会话上界，以及一个证明不了的断言（决策 79）

决策 78 做完风暴抑制，C 阶段连接规模三项里剩下的是**连接池上限**。这一条比它
看起来小，实施过程中却撞到本项目迄今为止最难的一次坦白，所以两件事一起记。

**上界本身。** `Fleet.Open` 此前对会话数**没有任何上界**，而一个节点 agent 是
单进程多路复用会话的，所以一次无界的 `Open` 等于请控制面无限量地持有**某一个
节点**的状态。不需要任何 bug：一个控制台重连循环，或者一个开了四十场排查的
运维，就够了。

**两个上界，不是一个。** 两者约束的是不同的东西：**每节点上界**收住单个节点
（一个行为异常的 agent 不能让 manager 持有超过它那份的状态）；**全局上界**收住
manager 本身（无论怎么分布，进程持有的 handle / sink / relay 数量有界）。只有
每节点上界时，N 个节点各开满就等于没有上界；只有全局上界时，一个节点就能吃掉
全部预算。默认 32 / 512，可配置，**负值直接拒绝**——「不限制」写成 -1 是那种
从环境变量里一路敲错到生产的配置。

**拒绝而不是驱逐。** 到顶就返回一个带数字的 `*LimitError`（哪个 scope、哪个
节点、开了几个、上限几个），HTTP 层映射成 **429**。不驱逐最老的会话，是因为
驱逐会静默杀掉某个人正在进行的排查，而 429 把「该关掉哪一场」交还给操作员——
消息里带着节点号和计数，那是一句指令，「内部错误」只是一张派给别人的工单。
**503 是错的**：等一分钟重试不会有任何不同，已经开着的会话不会自己关掉。

**难的那一半：一个我证明不了的断言。**

上界检查必须和插入在**同一个临界区**里。检查挪到加锁之前，就是一个
check-then-act 竞态。我把它当变异装上去跑，**测试全绿**——先是 8 goroutine 跑
5 次，再加到 64 goroutine × 100 次（共 6400 次尝试），一次都没抓到。加 `-race`
也抓不到，而且**理应抓不到**：那个变异是用 `RLock` 读的，锁本身是对的，这是逻辑
竞态而不是数据竞态，race detector 不负责推理两个各自正确的临界区之间的关系。

**我不能把一个证明不了原子性的测试写成证明了原子性。** 所以做三件事：

1. 测试改名为 `TestTheCapHoldsWhenManyConsolesOpenAtOnce`，并在注释里写明它
   证到的是什么、证不到的是什么——它证的是「一群控制台同时开会话时上界仍然
   成立」，证不到的是「检查与插入共享临界区」。
2. `Open` 里那段注释从「所以测试也必须是并发的」改成如实陈述：原子性是**代码
   摆放位置的属性**，和文件里其它锁边界一样靠评审；旁边的测试断言的是上界的
   行为，不是它的原子性。
3. 补了一条**能确定性验证**的相邻性质：被拒绝的 `Open` 不留下任何东西
   （`SessionCount` 不变，且**原来那个会话仍然可用**）。后者是真正会坏的地方——
   如果 relay 在拒绝之前就挂到了 handle 上，计数是对的，会话却是废的。

**这一条同时是本项目第三次撞上同一个形状，值得单独立一条规矩：**

| 轮次 | 测试写成 | 变异 | 结果 |
|---|---|---|---|
| 决策 76 | 直接调 `fullJitter` | `wait := ceiling` | 🟢 假覆盖 |
| 决策 77 | 指向 `data → service` | 删掉检查 3 | 🟢 假覆盖（被检查 1 抢先报） |
| 决策 79 | 并发 `Open` 计数 | 检查移出临界区 | 🟢 抓不到，且 `-race` 也抓不到 |

三次都不是「测试写错了」，而是**「删掉被测的那段代码，哪个测试会红」这个问题在
写测试时没有被问**。前两次能救回来，第三次不能——所以规矩是：并发性质的断言
必须先问「我的变异能被抓到吗」，抓不到就**降级为它真正证明的那句话**，而不是
让它继续假装。

**三条变异验证**

| 变异 | 结果 |
|---|---|
| 完全不做上界检查 | ❌ 5 个测试红 |
| 上界按「历史开过的总数」计（Close 不释放名额） | ❌ 关闭后重开测试红 |
| 429 改成 503 | ❌ 三个 HTTP 子用例红 |
| 检查移出临界区 | ⚠️ **抓不到**，已在代码与测试注释里如实记下 |

`nodefleet` 42 tests、`server/nodeagent` 16 tests 全绿。C 阶段连接规模三项
（**连接池上限 / 心跳重连 / 风暴抑制**）至此全部落地。

### 4.18 18 个 GAP 是真的，但结论不是「缺接线」（决策 80）

上一轮把 `plugin-coverage` 的 18 个 GAP 读成了「26 个中间件写工具注册了
却谁也调不到」，并据此把「控制面审批路径 → adapter registry 的接线」
列为下一步。**这个结论是错的，接线早就存在。** 本轮把它查到底：

```
cmd/opskeeper/main.go:2147   middlewareReg := middlewareregistry.NewRegistry()
             2148           adapterClosers := wireLoopRemediationAdapters(rootCtx, log, middlewareReg)
             2154           agentTools.middleware = middlewareReg      // 同一个 registry，不是第二个
             2195           loopRemediationInvoker = loop.RegistryInvoker{Tools: middlewareReg, ...}
```

三个事实：

1. **八个家族全部接线**。`loopAdapterSources()` 覆盖 postgres / redis / k8s /
   mq / kafka / rabbitmq / git / **host**，各配一个 DSN 环境变量。18 个 GAP 里
   5 个 `host.*` 与另外 13 个不在同一条路上——`host` 也是接线的。
2. **写工具的唯一调用点是 approved phase**。`RegistryInvoker.Invoke` 全仓库
   只有 `approved_worker.go:467` 一个调用方。`RegistryInvoker` 构造在 approved
   派发链上，而审批回执与审计链在它之前。这正是 `runMiddlewareTool` 注释里
   说的「reachable through the closed loop's approved dispatch, where a
   reviewer sees the blast radius」——那句话**有接线支撑，不是空头声明**。
3. **upcall 通道与闭环共用同一个 registry**，这是刻意的（`main.go:2152`
   注释原文：a node that could reach an adapter the loop cannot 会有两个答案）。
   所以「节点调不到写工具」和「闭环调得到写工具」不是两条断裂的链，
   是**同一条链上的两个门，门后面有没有人守着不同**——`main.go:2150` 的注释
原文就是在解释为什么必须是同一个 registry 而不是两个。

因此 GAP 是真的，理由是**口径**而不是**缺件**：`plugin-coverage` 量的是
「插件包能提供什么」，节点包按设计只读（`tools.go` 是生成文件，只出 L0/L1；
`toolset_gen_test.go` 的 `TestTheMiddlewareToolsetIsReadOnly` 强制写工具永不
进入只读清单），
所以每一个补救期望都必然落空。20 个 case 每个都同时命名诊断与补救，
于是 0/20。`coverage_test.go:363` 的 `TestNoShippedCaseIsReportedAsCovered...`
断言 `complete == 0` 正是这个意思——**0/20 是被测试钉住的正确答案，不是待修的缺陷。**

**由此否掉两条本来要走的路**：

- ❌ 把写工具打成节点 L2 包来消 GAP。这会同时违反两处写明理由的决策
  （`tools.go` 的 second door / no queue behind it，和 `runMiddlewareTool`
  重新复算工具等级的那道检查），并把一条有审批队列的写路径换成一条没有的。
- ❌ 恢复家族级豁免（决策 59 删掉 `NonPackageFamilies` 正是因为它让
  `k8s/pod-oom` 永久不可通过）。豁免只能精确到方法，且必须能证明该方法
  在审批路径上可调——而**这正是 registry 已经做到的事**，只是报告没在说。

**真正改掉的是报告的结语**：`plugincoverage.go` 原来收在「each line above
names a tool that could be packaged」，把结论指向「去打包」。这句话与架构
决策相反，容易诱导下一个人去做上面那条被否掉的路。结语改为陈述事实：
这些写能力经审批路径可达，打包进节点包不是补缺口而是删掉队列；本报告的
口径是「节点 agent 能不能碰到」，对写操作答案本就该是「不能」。

**留到下一轮的真实待决**（不是这一条）：golden case 的期望集合该不该把
「经审批可达」也算作已覆盖——那会改 harness 的语义（case 判分要不要区分
「走节点 agent」与「走闭环审批」），属于 §六 D 阶段的范围决策，不在闸门
口径里顺手改掉。

---

### 4.19 节点 Agent 页面，顺带修掉一个跨全站的错误信封缺陷（决策 81）

路线图 §七 第 4 条写着「插件市场与节点 Agent 两个页面」，其中**节点 Agent
页此前完全不存在**：后端 9 条 `/v1/node-agents/*` 路由与测试都在，前端没有
客户端、没有页面、没有入口。本轮把它建起来了，但真正值得记的不是页面本身
——是它逼出来的两个缺陷，两个都不在节点 Agent 这条路上。

**一、`client.ts` 只读扁平的错误信封，两个 2.0 handler 的错误码全站丢失。**

控制面的错误体有两种形状，而且**两种都还在线上**：

| 形状 | 写出方 |
|---|---|
| `{"error": "…", "code": "…"}` | marketplace / skill / monitor / alert / topology 等既有 handler |
| `{"error": {"message": "…", "code": "…"}}` | `nodeagent/http.go` 的 `writeErr`、`plugin/http.go`（两者都是 2.0 新写的） |

而 `web/src/api/client.ts` 只认扁平的：`typeof obj.error === 'string'` 才取
message，`obj.code` 才取 code。喂进嵌套体时 `obj.error` 是 object，两条都落空，
于是 **429 变成一句 `HTTP 429`**，错误码整个丢掉。

这不是装饰性的缺口。错误码正是页面区分「哪种拒绝」的依据，而**已有页面就在
按它分支**：`Tasks.tsx` 匹配 `not-wired-yet`，`marketplace.ts` 匹配
`invalid-argument`，本轮的节点 Agent 页匹配 `conversation_limit` 与
`not_streaming`。最后一个尤其要命——它决定文案是「去关掉一个会话」还是
「稍后重试」，而后者会把运维引到唯一无效的那个动作上（节点的会话不会自己关闭）。
修法是加 `readErrorEnvelope` 同时读两种形状，扁平的先查（它更老更常见，
同时带两者的 handler 是 bug 不是形状）。

**二、`attachStream` 不把 `signal` 交给 `fetch`，改为取消 reader。**

页面一渲染就崩：`RequestInit: Expected signal ("AbortSignal {}") to be an
instance of AbortSignal`。查下来是 **jsdom 的 `AbortController` 与 vitest 下
的 fetch 不是同一个 realm**——这不是页面 bug（浏览器里两者同源），但它挡住了
测试。修法没有去对齐 realm，而是换掉那个惯用法：**响应头已经拿到了，要停的
只剩 body 流，所以 `reader.cancel()` 才是精确的仪器**——它立刻解开挂起的
`read()` 并断开连接，而把 signal 挂到 Request 上是对一个请求早已离开的阶段
挥更重的锤子。顺带 signal 不再跨进 `fetch`，realm 问题自然消失。

顺带发现同形问题：`scrollIntoView` 在 jsdom 里不存在，页面一有气泡就整页崩。
加了可选调用——跟随滚动是锦上添花，渲染不是。

**三、三条断言是「像覆盖但不是」，已按规矩处理。**

- 「挂流前不能发消息」原本只断言 textarea 禁用。变异验证（M6：去掉 Send
  按钮自己的 `!attached` 守卫）**抓不到**——因为 textarea 禁用时 `input`
  恒为空，`!input.trim()` 已经把按钮禁掉了。两个守卫同时成立，不等于覆盖了
  任何一个。补了一条**可达**的用例：流在输入途中断掉（operator 打到一半，
  节点的 agent 进程没了），此时 `input` 非空而 `attached` 已为 false，
  M6 才抓得住。断流的时刻由测试持有 `ReadableStream` 的 controller 显式
  触发，不用定时器——一个主题是「此刻什么可用」的测试不能让主题由调度决定。
- M7（去掉 `onError` 里的 `setAttached(false)`）**仍然抓不到**，原因是
  `onClose` 也会清 `attached`。这是**真实冗余**（流报错和流正常关闭两条路
  都会让控制台脱钩，属于该有的双保险），所以这条断言只声称它真正证明的那句
  话——「断流之后发送被禁用」——没有声称是哪条内部路径做的。不降级，因为
  表述本来就没有越界。
- 「delta 累积」原本也没被覆盖。已有两条都发 `assistant_end`，而它按设计
  覆盖累积值，所以把 `b.content + chunk` 改成 `chunk` **两条都照样绿**——
  测的是一个恰好被终帧掩盖的分支。而「发了若干 delta、终帧永不到达」是
  流式最常见的情形（`wire/events.go` 明说非流式产出方可以省略终帧），
  只读终帧的页面会在每一次这样的轮次里显示一个空气泡。补的用例断言
  **一个元素同时含两半**——分开断言即便第二个 delta 覆盖了第一个也会通过，
  而那正是 bug：运维会读到一句从未被说出口的话。

其余 7 条变异（M1–M6、M8–M10）全部被抓；M7 是真实冗余，见上。

**四、页面本身。** `web/src/api/nodeAgents.ts` + `web/src/pages/NodeAgents.tsx`
+ 路由 `/node-agents` + 侧边栏入口（在「助理」下面而不是里面：节点 Agent 是
**正在跑的** agent，助理是**能装什么**的目录，两者回答不同问题）。9 个用例，
全量前端 **11 文件 / 80 测试**通过，`vite build` 通过。

页面上有两块是**专门为了不说谎**而存在的，砍掉它们这个页面就只是个聊天 UI：
**丢帧计数**（背压丢帧和「agent 不说话了」在界面上长得一模一样，而
`listSessions` 是这个数字唯一存在的地方）与**节点健康与 agent 状态分列**
（`degraded` + 非零重启数是 supervisor 已经放弃重启的样子，一个只显示
"running" 的页面会在整个这段时间里报健康）。

---

### 4.20 插件市场页，顺带统一了导入响应的字段命名（决策 82）

决策 81 之后路线图 §七 第 4 条还剩插件市场这一半。核查发现**后端齐了、前端
一个客户端都没有**：`POST /v1/marketplace/import`（决策 55 落的入口，
`main.go:2542` 确实在生产装配里挂上了，未配置时答 503）与
`GET /v1/plugins/{name}/compatibility`（E-1 要求的 PiG 版本 × edge 版本矩阵）
两条路由都存在、都有 Go 测试，**前端从未调用过**。本轮把这两条接上，做成
`web/src/pages/PluginMarketplace.tsx`（路由 `/plugins`）。

**一、导入响应的字段命名原本是混的，已在源头统一。**

`importResp` 内嵌 `*pluginimport.Report`，而 `Report` 与 `Decision`
**没有 JSON tag**——于是同一段 JSON 里，`LoadWarning` 答 `{"path","reason",
"code"}`，紧挨着的 `Report` 答 `{"Name","Decisions"}`。控制面两种约定并存，
控制台就得为它单独记一份形状。

修在 Go 侧而不是前端做兼容：这条路由**至今没有过任何消费者**，所以没有线
兼容性要保，补 tag 是纯增量。而且顺序上扁平的先查、嵌套的后查是有理由的——
扁平更老更常见，同时带两者的 handler 是 bug 不是形状。

钉它的测试是新写的 `TestImport_AnswersTheReportInTheSameNamingAsTheWarnings
BesideIt`，**故意解成 `map[string]any` 而不是结构体**：同文件里其它测试用的
结构体带着同一批 tag，改名时两边一起动，套件照样全绿而控制台静默读到
`undefined`。手写期望名才没有东西跟着一起动。三条变异（去掉
`Decisions` 的 tag、去掉 `Decision.Why` 的 tag、把 `extensions` 改成
`extension`）全部被抓。

**二、页面上两处「不许说谎」的地方。**

- **转换成功不是完成。** 存量容器里没有工具清单、没有安全级别、没有 scope、
  没有爆炸半径，转换器拒绝编造它们（`importer.go` 的包注释原话：*invents
  nothing*）。所以页面把**未决清单做成最大的那块**，资源计数压成一行小 chip
  ——反过来排会让「12 skills / 3 extensions」读起来像一个成品包。空清单
  也不报成功，而是提示「这个容器自带了治理声明，值得确认一下它是不是真的」：
  真实存量容器**必然**产生未决项，空清单本身就是异常信号。
- **`not-wired` 停在错误态，绝不渲染成空矩阵。** 管理面拿不到节点版本快照时
  它对「谁能装」没有答案；渲染成「Hostable (0) / Refused (0)」会被读成
  「全网都太旧」，于是去升级整个机队——一个由控制面从未说出口的句子引发的
  机队级变更。同理导入的 503 文案明说「与上传的文件无关」（否则运维会去
  重新打包一个没问题的归档），409 明说「导入不覆盖任何东西」（否则就是诱导
  重试，而重试会毁掉已答过问题的旧包）。

**三、10 条变异全部被抓**，含两条「丢帧」型：不渲染未决清单、丢掉 `why`
（只留问题）、不显示落地路径、`not-wired` / 409 退化成通用文案、不显示是哪条
轴被拒、不显示节点自己的拒绝理由、空清单说成「转换完成」、空清单静默、丢掉
加载器告警。拒绝理由按节点原句显示而不是在页面另写一份措辞——两份措辞必然
漂移，而 `version.go` 的注释已经写明这个句子是 node 侧审核与 manager 预检
**共用同一次实现**的产物。

**四、一处 HEAD 既有的 lint error 顺手修了。** `pluginReleases.ts` 的
`ReleaseNodeState` 用了 `string & {}`，`@typescript-eslint/ban-types` 报
**error**（不是 warning），`npm run lint` 因此一直是红的。它就在本轮修改的
同一个文件里，改成 `string & Record<never, never>`——同一个惯用法、同一组
补全行为，类型上不再是「任意非 null 值」。

**验证**：`tsc` ok；`eslint`（本轮文件）0 问题；**全量前端 12 文件 / 87 测试
全绿**；`vite build` 通过；`marketplace` / `pluginimport` / `service/plugin`
三个 Go 包全绿；`modulecheck` 边界成立；`gofmt` 干净。

**仍然缺、且本轮没做的**：节点上「实际装了什么」的清单面。后端有
`agent.tool` 通道与 `pluginStore`，但**没有任何 HTTP 端点**能列出某台节点上
的包——现在唯一能看见的窗口是一次发布的状态。要补就得在 tunnel 上加一个
`plugin.list` 的读取面（该方法存在于节点侧，控制面没有暴露），这是后端工作，
不在前端这一轮里顺手做掉。

---

### 4.21 节点插件清单面：端点、哨兵，以及两个只有测试找得到的缺陷（决策 83）

决策 82 结尾点名的那个缺口。查下来比记录的更具体：**管道是通的，缺的只是
HTTP 面**——`NodeFleet.Installed(ctx, edgeID)` 已实现、已测试
（`manager_test.go:627`），节点侧的 `MethodPluginList` 处理器也完整，但
`Installed` **没有任何生产调用方**，`/v1/plugins/*` 的 7 条路由里没有一条能
回答「这台机器上装了什么」。运维想知道某台主机有没有吃上上周那个包，唯一的
办法是发起一次发布——而发布期间恰恰是没人问这个的时候。

新增 `GET /v1/plugins/nodes/{edgeID}/installed`，并把 `NodeFleet` 同时交给
release manager 与这个读面（同一个适配器，不是两个，这样「一个节点能响应
发布却不能响应清单」在构造上就不可能）。

**一、`ErrNoTunnel`：同一句话的四个副本，一个都匹配不了。**

`NodeFleet` 的四个方法各自 `fmt.Errorf("the control plane has no tunnel to
the fleet")`。这不只是重复：**「这个管理面够不着任何节点」与「这台节点不应
答」是两个事实、两种修法**，而调用方分不开就只能把两者渲染成同一个失败。
运维会被送去错的地方——因为控制面从来没接过隧道，去重启一台 agent 好好的
机器。现在它是哨兵，读面据此答 503 而非 502。

**二、这个端点把三种答案分开，测试逐条钉住。**

| 答案 | 含义 | 运维该去哪儿 |
|---|---|---|
| 200 + `[]` | 节点答了，它没装任何包 | 关于节点的**事实** |
| 503 | 这个管理面没有隧道，对谁都不知道 | 配置 |
| 502 | 这一台不应答，或答了读不懂 | 节点（或版本偏斜） |

合并后两者就是 500，运维会去翻管理面日志，而真相是一台正在重启的机器。
`packages` 在 JSON 里**恒为数组、绝不为 `null`**：`infosOf` 对空集返回 nil，
Go 会把它序列化成 `null`，而 JS 里 `null` 最自然的读法是「字段不存在」——
于是「节点没装」与「没人问过这台节点」又变成同一件事。handler 里归一成
`[]`，这条用 `map[string]any` 解码来验（结构体解码看不见 `null` 与 `[]` 的
区别）。7 条用例、7 条变异全被抓。

前端第三张卡片「节点上装了什么」把这三种答案分开说，其中两条是硬要求：
502 的文案明说「**这不是『它没有装插件』**」，503 的文案明说「**这不等于所有
节点都是空的**」——后者会一次性对全机队做出一句从没问过的断言。

**三、两个只有测试找得到的缺陷，都不是测试的问题。**

- **页面上出现了两个都叫「查询 / Check」的按钮。** 兼容矩阵一张卡、节点清单
  一张卡，两张卡各有一个 `Check`。测试用 `getByRole` 报「找到多个元素」——
  那不是测试脆弱，是**页面对运维也说不清刚按的是哪一个**。改成「检查兼容性」
  与「查看节点已装」，两边同时好了。
- **digest 被截断到 19 字符。** 我先写了个 `digest.slice(0, 19)` 让它塞得进
  那一行，测试立刻指出断言对不上。想清楚之后发现截断本身是错的：这个 digest
  存在的**全部意义**就是与发布意图里的那个比对，截断之后运维在看得见的那几
  位上分不出匹配与不匹配。改成完整展示 + 允许换行。

**四、变异验证的锚点陷阱，这已经是第二次。**

前两轮（决策 81 的 M8、决策 82 的 N8）我都写了「只改中文文案」的变异，而
测试跑在 `en-US`，于是报 MISSED——**那是坏变异，不是覆盖缺口**。本轮 Q1/Q2
又踩了一次。三次同一个形状，所以立成规矩：**变异锚点必须落在测试实际读取的
那个 locale 上**；写完变异先问「测试跑的是哪个 locale，这段文案它读得到吗」。
换英文锚点重做后，5 条全部被抓。

**验证**：`go build ./...` 通过；`gofmt` 干净；`server/plugin` /
`service/plugin` / `core/edge/...` 全绿；`modulecheck` 边界成立；前端
**12 文件 / 91 测试**全绿、`tsc` ok、`eslint` 干净、`vite build` 通过。

### 4.22 复读 SDK 文档：决策 75 的「已决」被推翻了（决策 84）

用户要求「彻底改成 pig 风格，而不是适配」。这句话与决策 75 的结论正面冲突，
所以这一轮不是继续实现，而是先去查那条结论到底还成不成立——**在一个被记成
「已决」的问题上按新指示动手之前，先验证它是不是真的已决**。

**一、决策 75 说了什么，为什么当时是对的。**

它把控制面的 turn 挂在 `pigagent.Kernel`（直接嵌 `agent.Agent`）而不是
`pigcoding.Session`（文档里的 SDK），列了两笔账：

- **账一**：`coding.SessionStartOptions` 里没有 `MaxTurns`，换过去 turn 上限
  静默消失。
- **账二**：`coding.SessionManager` 是 `= icodingagent.Session` 的**类型别名
  到具体结构体**（`coding/session_manager.go:12`），不是接口，注入不进去；且
  `SessionStartOptions` 没有 `OnMessagePersist` 字段，而 Kernel 正靠它分配行
  id 并回填 SSE 帧。

两笔都成立，结论是选项 C：`pigcoding` 留在设置与模型目录。

**二、账二在 HEAD 上依然成立，但它的推论是错的。**

在 PiG `c7c2fe0`（v0.3.0 之后 5 个提交）逐条复核：

- 类型别名确实没法注入——**这条仍然成立**。
- 但**行 id 时序并不依赖它**。行 id 是 OpsKeeper 自己发的序号，需要的是
  「每条落定消息按顺序回调一次」，而 `agent.TurnEndEvent`（`agent_loop.go:404`）
  在 `Session.Events()` / `Agent.Subscribe()` 上**每轮恰好发一次**，带
  `TurnIndex`、本轮的 `Message` 与 `ToolResults`；失败轮由 `agent.go:1405`
  补发。顺序由事件流本身保证，不需要任何人替我们分配。
- 顺带查实一件相关的事：`TurnEndEvent` 虽然有 `MessageEntryID` /
  `ToolResultEntryIDs` 字段，但**两处发射点都没填**，恒为空。所以「改用 PiG
  的 entry id」这条路是不存在的——但它本来也不需要存在。**决策 75 把「PiG 的
  entry id」和「行 id 的顺序」当成了一件事**，这是账二推论错误的根因。

**三、账一有一个我们已经在用的面板上的答案。**

`MaxTurns` 字段确实不在 `SessionStartOptions` 里（复核无误）。但上限不需要
字段：`BeforeToolCallHook` 返回的 `ToolCallHookResult` 自带
`Block` / `Reason` / `Terminate`，而 OpsKeeper 的策略闸门**本来就在这个面板
上**。

链路查实：`tool_execution.go:282` 把被拒的调用变成一条 `Terminate` 的错误
工具结果 → `shouldTerminateToolBatch`（同文件 196）在**每个**结果都要求停止
时返回 true → `agent_loop.go:275` 的 `hasMoreToolCalls = !batch.terminate`
结束这一轮。模型仍收到一条说明拒绝原因的工具结果，所以这一轮是**落定**的，
不是卡死的。

**四、这一轮真正查到的、比上面两笔都更要紧的东西。**

- **调用方钩子是被追加而不是被替换的**：`SessionStartOptions.BeforeToolCall`
  在 `session_ops.go:144` 进 `callerHooks`，`session_extension_hooks.go:25`
  用的是 `AddBeforeToolCallHook`。**策略闸门在 SDK 路径上原样存活**——这是
  整个迁移里最要紧的一条，没有它其余都不必谈。
- **`SetFinishTurn` 是陷阱，不能碰**：`Session.installAgentBoundaryHooks`
  （`session_boundaries.go:22`）自己 `SetFinishTurn` 并把 `previous` 包在
  里面。OpsKeeper 若通过 `Session.Agent()` 覆写它，会**连扩展的 `turn_end`
  边界一起砸掉**。所以上限必须走 `BeforeToolCall`，这也和第三节的结论一致。
- 节点侧**早就是 pig 原生的**：`pig --mode rpc` 内部用的就是 `coding.Session`。
  所以「适配」只存在于控制面，不存在于节点面——这缩小了改造的真实范围。

**五、本轮落地（决策 84 的代码部分）。**

| 落点 | 内容 |
|---|---|
| `core/pig/pigcoding/budget.go` | `TurnBudget`：按工具轮次计费，第 `max+1` 轮**软拒绝**（告知预算耗尽、令其收束），第 `max+2` 轮**硬终止** |
| `composeBeforeToolCall` | 预算钩子**排在调用方钩子之前**，且抽成可测函数 |
| `Start` 补面板 | `MaxRounds` / `SkipExtensionTools` / `AllowedTools` / `ExcludedTools` / `NoTools` / `SessionID` |
| `Session.Budget()` | 让「为什么停了」有一个数字，而不是一段空白 |

**为什么软硬两段而不是一步到位**：一步终止会扔掉最后一轮的发现，而最后一轮
通常正是有答案的那一轮；只软拒绝又会让不听话的模型一直重试。前者让调查没有
结论，后者让循环没有上界。两段都有，且上界是硬的。

**为什么预算必须排在闸门之前**：闸门拒掉的调用**也是模型花掉的一轮**。闸门在
前的话，一个「什么都拒」的门会让模型无限重试而预算一次都不 spend——**上界被
它本该保护的防线关掉了**。这个顺序写成了 `composeBeforeToolCall` 里的代码，
并有测试直接断言（`TestTheBudgetIsSpentBeforeThePolicyGateSeesTheCall`），
而不是只写在注释里。

**六、这一轮没有做的，以及它还差什么。**

**内核没有换。** 控制面的 turn 仍然跑在 `pigagent.Kernel` 上。本轮拿掉的是
「不能换」这个理由，不是「换」这件事本身。真要换，还差：

1. `pigagent.Mapper` 与 `ports.AgentMessage` 这一层平行形状要拆掉——turn 的
   结果与事件应当直接是 `agent.AgentMessage` / `agent.AgentEvent`，SSE 帧
   仍由 `pigwire` 翻译（那是 wire 契约，不是适配层）。
2. 行 id 改由 `TurnEndEvent` 顺序分配，SSE 帧 golden 要逐帧重验。
3. `chatruntime` / `agentkernel` / `loop` / `investigator` 四处装配要重接。

**七、留了一个已知的弱断言，没硬撑。**

`TestTheHostGateBlocksAToolCall`（决策 76 之前就有）用 `PIG_TEST_FAUX=1`，
而该 provider 不接受脚本化响应，所以那次 send **可能根本不产生工具调用**——
它断言的是「如果发生了，闸门拦住了」。这条不变，但本轮新增的用例都刻意
**不依赖**它：预算算术是纯函数断言，面板接线用 `SessionID` 验（PiG 在调用方
不指定时会自己生成，不接线就必然不匹配），闸门顺序直接断言组合结果。

**验证**：`go build ./...` 通过；`gofmt` 干净；`core/pig/...` 10 个包全绿；
`go test -race ./core/pig/pigcoding/` 无竞态；`modulecheck` 边界成立；
**6 条变异全部被抓**（闸门顺序对调、上限提前一轮、软拒绝变硬、硬终止被摘、
无上限也造预算对象、`SessionID` 不接线）。

过程中测试抓到一个真缺陷：`NewTurnBudget(0)` 返回非 nil，使「无上限」与
「有上限但未花费」在唯一会读它的地方无法区分。按注释的承诺改成
`MaxRounds <= 0` 时根本不造预算对象。

---

### 4.23 MCP 运行时一直都在，缺的是第三条路——以及一次真实的命名分叉（决策 85）

上一轮结尾把两件事挂在那里：内核换不换（决策 84 已拆掉理由），以及 MCP 运行时。
后者此前的记述是「**缺口**：PiG 的 `mcp` 只是声明，没有 JSON-RPC 客户端、没有
握手、没有把 MCP server 接进 agent 工具集的桥」。这一轮去写它，第一步就撞上
了事实错误。

**一、它一直都在。查了五处，全是完整的。**

| 位置 | 是什么 |
|---|---|
| `core/manager/pkg/mcpclient/client.go` | stdlib-only 的 MCP 客户端：`Initialize` / `ListTools` / `CallTool` / `TextContent` / SSE 解包 |
| `core/manager/biz/mcp/usecase.go` | 注册 CRUD、凭据解析与 header 模板展开、连接探测、`ToolsCacheJSON` 回写 |
| `core/manager/server/mcp/http.go` | `/v1/mcp/servers` 全套 admin 面（HLD-018） |
| `core/manager/biz/aiops/tools/mcp_basetool.go` | 一个 MCP 工具 = 一个 `basetool.BaseTool`，`MCPToolClass` 按动词推断风险等级，trusted 与否决定同步还是入审批 |
| `cmd/opskeeper/main.go:2878` | **启动期发现**：连每个 enabled server，`tools/list`，把每个工具挂进聊天工具袋 |

连审批执行器都注册了（`main.go:2748`，`mcp_call`）。所以「65 个工具走
extension toolset」不是 MCP 的替代方案，而是**并行的另一条路**。

此前的记述错在一个推理跳步：它观察到「PiG 没有 MCP 运行时」，于是写成了
「所以要从零写一个」。这两句之间隔着 OpsKeeper 自己那一套，而没人去核实它。
本轮改正了三处正文（B2 注、路线图第 3 条、假设清单）。

**二、那真正缺的是什么。**

`basetool.BaseTool` 是 OpsKeeper 自己的形状。决策 84 定的方向是控制面内核换成
`coding.Session`，而那之后工具必须是 `agent.AgentTool`。**这就是第三条路**：
PiG 原生的 MCP 接入。所以本轮补的是它，不是「从零写 MCP 运行时」：

```
core/ports/mcp.go          MCPCatalogue 端口 + 命名规则（wire 契约，零依赖）
   ↑ 实现                              ↑ 消费
core/manager/biz/mcp/      Usecase.Tools / Call      core/pig/pigmcp/  → []agent.AgentTool
（读探测缓存，不重连）                                 （不持端点、不持凭据）
```

端口必须落在 `core/ports` 是模块方向逼出来的：`core/pig` 是唯一能 import PiG
的模块且**不能** import manager，两边要看见同一份契约只有这一种形状。

**三、这一轮抓到的真 bug：同一个能力，两个名字。**

我先按 `model.Server.Name` 的注释把命名写成了 `<server>__<tool>`。生产里的
权威实现是 `tools.MCPToolName`，它产出 **`mcp__<server>__<tool>`，而且两边
都做 sanitize**（小写、非字母数字一律变下划线）。

那条注释是**过时的**（现已改）。它同时是错误认知的源头：注释说
`github__create_issue`，代码说 `mcp__github__create_issue`，而读者只有打开
`mcp_basetool.go` 才知道该信哪个。这就是本文件里唯一一个**我自己踩中**的坑，
也是为什么它值得写下来而不是直接改掉：

- 目录给出的名字是**模型会调的名字**，执行方接受的名字是**会跑的名字**。
- 两者不一致时，同一个 Grafana 查询在模型手里是两个名字，其中一个谁也不执行，
  而且**没有任何诊断会提到这件事**。
- 修法不是把两处改一致，是把规则**收进 `core/ports.ComposeMCPToolName`**，
  让 `tools.MCPToolName` 改为转调。规则只有一份，两边不可能再分叉。

**四、sanitize 不是免费的，它带来两个必须处理的后果。**

| 后果 | 处理 |
|---|---|
| `"My-Server"` → `my_server`，服务器收到 `my_server` 会答「没有这个工具」 | `Call` 经**探测缓存**把工具名还原成服务器自己的拼写再发出去 |
| `"My-Server"` 与 `"my_server"` 是两个不同字符串，DB 唯一索引都收，却都 compose 成 `mcp__my_server__*` | 目录侧**碰撞检测**并拒绝，报错点名两个服务器 |

第一版我把碰撞检测当成死代码删掉了（当时认为服务器名校验已经排除了它）。
加上 sanitize 之后它重新变成可达——**同一个判断在两个不同的规则下得到了相反
的结论**，这正是规则必须收在一处的又一个理由。

**五、闸门没有被绕开，而且是被测出来的。**

`core/pig/pigmcp` 里**没有端点、没有 header、没有 token、没有连接池**，只有一个
`ports.MCPCatalogue` 引用。工具进的是 session 的普通工具袋，因此
`BeforeToolCall` 照常对它生效。这条不写在注释里就算数：

- `TestAnUngatedMCPToolCallReachesTheCatalogueAndTheTranscript`——真实
  `agent.Agent` 循环 + 脚本化假模型，无闸门，目录被调到一次，答案进对话。
- `TestTheHostGateBlocksAnMCPToolCall`——同一个循环加 `BeforeToolCall`，断言
  闸门看见了 `mcp__grafana__query_dashboard`，且**目录一次都没被调到**。

一条会绕过闸门的桥会保住本仓库其余所有保证，同时还是一个洞，所以这条必须有测试。

**六、这一轮没有做的。**

`pigmcp` 已就位但**尚未接进任何装配点**。`main.go:2878` 的启动期发现仍然直接
产 `basetool.BaseTool`，聊天工具袋里现在只有那一条路。要让 MCP 同时出现在
PiG 原生的 session 工具袋里，需要在决策 84 的内核切换里一起接——在那之前
这两条路服务于两个不同的内核，接上去只会产出两份同能力工具。**这也是本轮
只补桥不接线的原因**：桥是内核切换的前置，不是它的替代。

**验证**：`go build ./...` 通过；`gofmt` 干净；`core/ports`、`core/manager/biz/mcp`、
`core/manager/biz/aiops/...`、`core/pig/...` 共 25 个包全绿；`go test -race`
三包无竞态；`modulecheck` 边界成立；**19 条变异全部被抓**（含 3 条专门打命名
分叉：丢前缀、sanitize 不小写、执行方绕开共享规则）。

其中两条值得单列，因为它们**先漏了、补测试后才抓到**：契约测试只证明两边
**一致**，证明不了**一致的拼写没变过**——两边转调同一个函数后，删掉 `mcp__`
前缀它照样通过。补的是 `core/ports` 里的**字面量钉死测试**：这个字符串是对
模型、对控制台、对存量 transcript 的 wire 契约，改它会让运行中的部署上每一个
MCP 工具名一起变。

### 4.24 两个内核并存：SDK 驱动落地，以及三处「以为知道、其实不知道」（决策 86）

§4.22 推翻了决策 75 的结论、定了方向，本节把内核真正换掉，并把换的过程中
撞见的三个事实记下来——它们的共同点是：**每一条在写代码前都显得已经清楚了**。

#### 4.24.1 为什么是两个内核，不是一个

新增 `pigagent.SessionKernel`（`core/pig/pigagent/session_kernel.go`），驱动
`coding.Session`；`pigagent.Kernel` 保留，驱动 `agent.Agent`。两者实现同一个
`Agent` 端口，共用 `Mapper`、`runState`、`buildPrompt`、`NewAdapters`。

保留 `Kernel` 的理由不是保守，是它有 SDK 覆盖不到的位置：单元测试、不能持有
扩展进程的后台 worker、根本不需要 turn 的报表查询。`SessionKernel` 的存在
理由也不是「更完整」，而是**一个 PiG package 里的插件认得 `coding.Session`**：
扩展 runner、session log、`turn_end` 边界、steering 队列、abort signal 都在
那儿，对着 `agent.Agent` 写的东西一样也拿不到。

不变量因此变成可测的：**两个驱动对同一脚本必须产出逐帧相同的控制台帧、相同的
账本行、相同的 transcript 行。**

#### 4.24.2 共享的与不共享的

共享：帧词汇（`Mapper`）、整套策略闸门（`runState`：审批 / 预算 / 审计 /
recorder / 用量折叠）、prompt 组装、工具适配器。这些都不知道底下跑的是哪个
循环，复制其中任何一个都等于给控制台的 wire 契约再写一份第二实现。

为此把 `runState.k *Kernel` 拆成 `runHost{persist, now}`——`runState` 只用到
这两个值，让它与循环选择彻底解耦。

**不共享**：`FinishTurn`。`agent.Agent` 直接接受这个钩子，`coding.Session` 没有
这个面板，因为 `Session.installAgentBoundaryHooks` 自己装了一个并会包裹
`previous`；覆写会连带删掉每个 PiG package 生命周期都依赖的扩展 `turn_end`
边界。`agent` 包里也没有 `AddFinishTurnHook`——只有 `AddBeforeToolCallHook` 与
`AddAfterToolCallHook` 是追加语义。所以轮次上限落在 `BeforeToolCall` 上，走
`pigcoding.TurnBudget`。**这条是本轮唯一被允许的帧差异**，见 4.24.4。

`SessionStartOptions` 也没有 `AfterToolCall` 面板，只有 `BeforeToolCall`。审计
写进 `BeforeToolCall` 会把随后被拒的调用也记成已执行，包装进工具又会把宿主
职责塞进控制台正在渲染的那个工具里。因此新增 `pigcoding.Session` 三个面板：

| 面板 | 底层 | 为什么必须暴露 |
|---|---|---|
| `Run(ctx, opening)` | `RunAgentPrompt` | 宿主要自己组装对话（历史窗口、脱敏、丢弃过期工具批次） |
| `Acknowledge(ev)` | `coding.AcknowledgeEvent` | 不应答内部 barrier 的话每轮都会**死锁** |
| `AddAfterToolCallHook(h)` | `Session.Agent().AddAfterToolCallHook` | SDK 面板缺失，且只能追加不能替换 |

#### 4.24.3 三处「以为知道、其实不知道」

**其一：`start` 回调不是钩子，是那次运行本身。**
`RunAgentPrompt(ctx, start)` 的 `start` 必须执行底层运行（PiG 自己是
`agent.BeginSendMessages(ctx, msgs).Run()`）。我第一版让它只
`return buildPrompt(req), nil`，编译通过、测试通过、`Run` 返回 nil error——
**一帧都没出**。回合静默结算为空。`pigcoding.Session.Run` 因此收成消息切片
而不是透传回调：`Session.Agent()` 是这个包存在的意义，不该让调用方去碰。

**其二：Session 会把系统提示回灌成一条 transcript 消息。**
`SystemPrompt` 在 SDK 路径上落成一条 system entry，循环照例为它开/关一次
生命周期，于是多出一帧空 `assistant_end` 和一行 `system` transcript。
`Mapper` 之前把**每一条** `MessageEndEvent` 都渲染成 assistant 气泡——bare
`agent.Agent` 从不产生 system 消息，所以这条路径一直是死的。修在
`Mapper`：system 消息不出帧；consumer 侧同样不落库。**两处都要改**：只过滤
帧会留下一行控制台渲染不出来的记录，只过滤行会留下一个背后无内容的气泡。

**其三：审批卡相对 agent 帧的位置不是契约。**
`beforeToolCall` 跑在工具自己的 goroutine 上，直接写 sink（它要同步地问人一个
决定，没法排队）；agent 产生的帧走另一条路——bare loop 是内联 `OnEvent`，SDK
driver 多一跳转发 goroutine。两条路之间**没有顺序**。

我一度断言了这个顺序并写成了测试：单独跑反复通过，**把本包其他测试一起编进来
之后每次都失败**。pass three 原本按到达顺序比较每个调用的帧，同样六次里挂一次。
两处都改成比较集合。这条写在这里，是因为「作者机器上绿的 golden」正是 golden
最该防的事故。

#### 4.24.4 唯一被允许的帧差异：轮次上限

| | 机制 | 终态 |
|---|---|---|
| `Kernel` | `agent.AgentOptions.MaxTurns` | `ErrMaxTurnsReached` → `failTurn` → 失败回合 |
| `SessionKernel` | `pigcoding.TurnBudget`（`BeforeToolCall`） | 模型被拒后**用已有证据作答**，回合正常结束，`Stopped = max_iterations` |

`SessionKernel` 在结算时读 `sess.Budget().Spent()` 并回填
`TurnMaxIterations`，因为 `chatruntime/kernelpath.go:181` 正是按这个值决定要不要
道歉——把「跑太久」报成 `end_turn`，等于让操作员把一次被截断的排查读成完整结论。
两边对控制台是同一件事，只是 SDK 这条更好。

#### 4.24.5 顺带修掉一个真实数据竞争

`Mapper.Approval()` / `Error()` / `Notification()` 从工具 goroutine 调用时
**没有持锁**，而 `Map()` 持锁——两者写同一个 `m.seq++`。表现是两张卡拿到同一个
序号，控制台按序号排序，于是卡片渲染在工具之前或不渲染，**没有任何报错**。
`go test -race` 在 Session driver 跑带审批的工具调用时抓到。

修法是拆出 `frameLocked`，`Map()` 用它、外部调用者用会加锁的 `frame`。回归
测试 `TestFramesFromTheRunStateAndTheMapperDoNotCollide` 刻意写成**不依赖
-race 也能失败**：200 轮 × 3×64 帧的并发争用，要求序号是 `1..n` 的一个排列。
去掉锁后它在第 39 轮命中。

#### 4.24.6 接线：驱动成为部署选项

`SessionKernel` 已接进装配点，驱动由 `OPSKEEPER_AGENT_KERNEL` 选择：

| 值 | 驱动 | 运行时 |
|---|---|---|
| `pig` | `pigagent.Kernel`（`agent.Agent`） | 不需要 |
| `pig-sdk`（别名 `pig_sdk` / `sdk`） | `pigagent.SessionKernel`（`coding.Session`） | 需要进程级 `*pigcoding.Runtime` |

**为什么新增一个值而不是改掉 `pig`。** 两个驱动共享 mapper、策略闸门、
prompt 组装与工具适配器，差分 golden 也把它们按住了——但它们不是同一个进程
形状。Session 带着扩展 runner 与 session log，意味着一个 PiG package 可以给
控制面回合贡献工具与生命周期钩子，也意味着一回合会占住一个 runtime 直到关闭。
操作员应当能开、也能关，而一个在旧拼写下静默换掉行为的值会把这个选择拿走。

`newAgentKernel` 现在返回 `pigagent.Agent` 接口。**宿主绑定（工具袋解析、
账本、闸门、预算、persister）对两个驱动完全相同**——决定「一个回合是不是
OpsKeeper 的回合」的一切都发生在循环之外，而驱动只换底下的那一层。

两个判定谓词刻意分开：

- `UsesChatRuntime()`：回合是否走 chatruntime 而不是 legacy for-loop——两个
  PiG 驱动都是。
- `UsesPiGSession()`：底下的循环是不是 `coding.Session`——只有它需要 runtime。

用前者回答后者的问题，会给 Session 驱动一个 nil runtime。

**piG runtime 是穿线进去的，不是这里新建的。** 两个 runtime 就是两个扩展
runner，插件装进 A 却由 B 驱动的回合执行，看起来和「插件加载了但什么也没做」
一模一样。

#### 4.24.7 `pigmcp` 去哪：控制面**不**接

决策 85 留下的桥**没有**接进控制面，这是结论而不是搁置。

控制面的 MCP 已经通过 `aiopstools.MCPTool`（`basetool.BaseTool`）挂在
chatruntime 的工具袋上，换成 Session 驱动后它经 `NewAdapters` 原样进
`pigcoding.Start.Tools`——**分类、闸门、审计、审批全部照旧**。再把 `pigmcp`
接进同一个 session，同一份能力会有两个名字、两条执行路径，而其中一条没有
宿主记账。这正是决策 85 自己标注的「产出两份同能力工具」。

`pigmcp` 的位置是**节点侧**：那里跑的是 `pig --mode rpc`，插件是 PiG package，
MCP 由 PiG 自己配置、不在 OpsKeeper 的库里。它的桥形状（只持有 catalogue，
不持有端点/凭据/连接池）对那个场景是对的，对控制面是多余的。

因为没有任何单测能跨这条边界（`core/pig` 不能 import manager，根模块不能
import PiG），这个结论拆成两侧各自钉死：

| 位置 | 断言 |
|---|---|
| `core/pig/pigagent` | `TestAnMCPToolIsClassifiedAndGatedLikeAnyOther`：`mcp__grafana__query_dashboard`（read）**不弹审批卡**并执行；`mcp__k8s__delete_pod`（destructive）在无闸门时**被拒且不执行** |
| `core/manager/biz/aiops/tools` | `TestAnMCPToolAnnouncesTheComposedNameAndAnInferredClass`：wire 名是 `mcp__<server>__<tool>`、类由动词推断、未知动词 → destructive、空 schema 仍是 JSON object |

两个方向都要断言。往宽松错了 = 代理执行了未审批的 MCP 变更；往严格错了 =
每次看仪表盘都弹卡，操作员会学会无脑点通过——**和没有审批是一样的结果**。

read 那一侧断言的是「**没有**卡」而不是「有结果」：只看结果的测试分不清
「自由执行」和「被一个恰好总是同意的闸门放行」。

#### 4.24.8 验证与已知缺口

**验证**：`go build ./...` 通过；8 个模块 `go test -count=1` 全绿；
`modulecheck` 边界成立（`.go-arch-lint.yml` 显式授权 `oxpig_agent →
oxpig_coding`，单向，反向由模块图禁止）；`go test -race` 全 `core/pig` 加
`chatruntime` 无竞态。

**变异验证，分四组**：

| 组 | 变异 | 结果 |
|---|---|---|
| SDK 驱动 | mapper 的 system 过滤、consumer 的 system 落库、策略闸门钩子、预算 stop reason、审计钩子体、tool-result 落库、barrier 应答、序号锁 | 8/8 抓 |
| 接线 | `pig-sdk` 解析成裸循环、驱动分支被摘掉、nil runtime 被容忍、`UsesPiGSession` 扩大 | 4/4 抓 |
| MCP 契约 | 未知动词变 read、wire 名丢前缀 | 2/2 抓 |
| 帧序号 | 序号计数器不再前进 | 抓 |
| **SDK 语义契约** | 见 §4.24.11 | **9/9 抓** |

#### 4.24.9 差分 golden 的盲区，以及它被证伪的次数

**差分比较只看「分歧」**。两个驱动**共有**的回归对它按构造是不可见的——而这不是
理论担忧，是实测的：把 mapper 里的 `tool_start` 帧删掉（**一行**，在两个驱动
共用的代码里），本文件的差分闸门**全绿**，`TestStreamGoldenMatchesTheConsoleContract`
转红。控制台会丢掉它渲染的每一个工具调用，而差分闸门报「完美一致」，因为两个
驱动确实一致。

所以 `stream-golden` 的绝对 golden 与本文件的差分 golden **互不可替**：
绝对的那个钉住「帧是什么」，但它无法告诉你「第二个驱动产出同样的帧」；
差分的那个覆盖前者看不见的一半。**成对才构成闸门。**

同一批变异还证伪了我自己两次断言，值得逐条记下：

1. **审批卡的位置。** 我断言了它并写成测试，单独跑反复通过，**把本包其他测试
   编进来后每次失败**。已改为比较集合。
2. **nil runtime 检查的理由。** 我写的是「否则会静默结算空回合」，变异把检查
   摘掉后测试仍通过——因为 `NewSessionKernel` 本来就在构造时拒绝。**注释里的
   理由比代码更自信。** 检查保留的理由改成它真正提供的价值（错误信息点名驱动），
   测试随之改为断言信息。

还有两次是我自己的锚点没落在测试真正读取的位置：`afterToolCall` 的变异两次都
是**编译错误**而不是行为失败（第一次把 `json` 引用删没了），而序号测试第一版
断言「连续 1..n」，实际帧流是 `coalesceDeltas` 合并过的——**断言的是一个不
存在的要求**。

#### 4.24.10 自我更正与已知缺口

**关于 C 阶段**：写下这一节时我一度以为 C 阶段尚未实现，准备把「PigSupervisor
与 NodeFleet 还没接」记成 B 阶段的剩余项。核对装配点时发现是错的——
`nodefleet.New` 在 `cmd/opskeeper/main.go:1250`，`pigsupervisor.New` 在
`cmd/opskeeper-edge/agent.go:215`，两条都已接线；`nodefleet/e2e/` 的三个剧本
也接在真 Fleet + 真 policygate + 真 pigwire 上跑。**C 阶段是 95%，不是 0。**
记在这里而不是悄悄改掉，因为「核对之前先写下结论」正是这类文档里最贵的错误
类型——它不会以编译错误的形式出现。

**一条变异存活**：`afterToolCall` 里的 `isBlocked` 分支删掉后观测结果不变
——blocked 行由 `onEvent` 那条路径写入。这是既有冗余，不在本轮范围内，但事件
审查时值得知道审计行有两个来源。

**已知的既有偶发失败**（与本轮无关，未能复现）：全量扫描中 `core/edge` 出现过
一次、`core/manager/data/chatdiagnose/store` 的两个 SQLite 用例出现过一次
失败；两者所在模块本轮未改动，随后各 20+ 次串行与并发重跑（含刻意加载）均未
复现。记在这里而不是当作「全绿」——偶发的红和稳定的红在闸门里不是同一件事。

**B 阶段剩余**：无。`coding` 的形状由 `pigcontract/contract.go` 钉住，类型系统
表达不了的语义由 `pigcontract/session_contract_test.go` 钉住（§4.24.11），两者
合起来覆盖了控制面实际用到的 API 面。往后只剩**跟随上游新增能力增量补钉**，不是
缺口。

---

#### 4.24.11 SDK 语义契约：形状钉不住的四种假设

`contract.go` 钉的是形状——名字还在不在、字段还在不在。它钉不住的是**名字没变
但含义变了**的漂移，而那恰恰是能安静地走进生产的漂移：一次空帧、一个永不触发
的策略钩子、一次被当成内部标记丢掉的真实帧。

于是有了 `core/pig/pigcontract/session_contract_test.go`。它不是复述
`pigcoding` 的行为，而是**在真的 `coding.Session` 上、拿 PiG 的 faux provider 驱动
真实的 turn**，断言四条我们真正依赖的假设：

| # | 假设 | 断言的是什么 | 假设错了会怎样 |
|---|---|---|---|
| 1 | `AddBeforeToolCallHook` **追加**，`SetFinishTurn` **替换** | 两次追加后按序跑成 `[first, second]`；两次设置后 getter 是第二个且第一个不再运行 | 轮次上限若走 finish-turn，会在某条构造路径上被静默关掉——turn 无闸门 |
| 2 | `AcknowledgeEvent` **只**应答 barrier，且 barrier **只在有人调 `FlushEvents` 时才存在** | 无 `FlushEvents` 的 turn 里 claimed == 0；调了则**先阻塞后释放** | 消费端漏应答 = 每次 turn 挂死；反过来若上游开始无条件发 marker，控制台每一帧都被当标记丢掉 |
| 3 | `NoSession` **只**丢文件 | `ID() != ""` 且 `Path() == ""` 且有内存消息 | 连 id 一起丢 → 一次会话散成互不相干的行 |
| 4 | `SkipBuiltinTools` **只**删 PiG 自带工具 | 跳过后的工具集是未跳过集的子集，且**不含** `bash/read/write/edit` 字面量；调用方自己的工具仍在 | 修法写成「清空整个注册表」也能过第 4 条的前半句——所以后半句断言调用方工具存活 |

**变异方式是真改上游**：把 `v0.3.0` 从 module cache 复制到 `/tmp`，在 `go.work`
里临时 `replace` 过去，逐条把 PiG 改坏，看测试是否转红。共 9 条：

| 变异 | 抓它的测试 |
|---|---|
| `AddBeforeToolCallHook` 改成替换 | 假设 1 |
| `SetFinishTurn` 只保留第一个 | 假设 1（getter 半） |
| `SetFinishTurn` 改成链式叠加（getter 仍报最后一个） | 假设 1（行为半） |
| `AcknowledgeEvent` 对所有事件返回 true | 假设 2 |
| `FlushEvents` 不等待直接返回 | 假设 2 |
| `NoSession` 连 id 一起清空 | 假设 3 |
| `SkipBuiltinTools` 变成空操作 | 假设 4 |
| `SkipBuiltinTools` 连调用方工具一起清 | 假设 4（后半句） |

9/9 全抓。**第 3 条变异是刻意设计的**：链式叠加时 getter 依然返回最后安装的那个，
只靠 getter 的测试会全绿——所以那个测试必须**两半都断言**，而这正是
§4.24.9 说的「两个闸门互不可替」在单个测试内部的同一个道理。

**关于 fixture 的一个坑**（第一版全部超时，值得记）：`Session.Events()`
的 channel **只在 Session 关闭时关闭**，不在 turn 结束时关闭。等 channel 关闭来
判定「这一轮的事件我全看过了」会永远等下去。正确的边界是 `agent.TurnEndEvent`。
现在 `eventTap` 把这个形状写死了：常驻 goroutine 消费到 Session 结束，用
`TurnEndEvent` 作为 settle 信号——**和 `SessionKernel` 的真实消费循环同构**。


---

### 4.25 决策 87：把一个不会动的数字变成闸门

#### 4.25.1 0/20 是个决定，不是一次测量

`opskeeper-eval plugin-coverage` 报告的是「golden case 的能力期望 vs 插件包能提供
什么」。它报的是 **0/20**，而且它会永远报 0/20。

原因是结构性的，不是欠账：20 个 case 每个都同时命名**根因**与**补救**，而节点侧
的包按设计只出只读工具——写操作的审批队列在隧道另一边的控制面，节点的 upcall
通道背后没有队列。`TestTheBrokerRefusesAToolNoManifestDeclares` 就是这条设计的
守卫。

问题不在这个结论对不对，而在**它是一个不会动的数字**。fleet 掉了 `query_promql`
之后它还是 0/20；fleet 打包了 20 个工具之后它还是 0/20。**一个不能上升的数字
抓不到回归**，而一个抓不到回归的闸门不是闸门，是注释。计划 §四 E-3 要的
「插件纳入黄金集回归」要的就是一个会动的数字，而这条命令当时给不出。

#### 4.25.2 拆轴

`CaseCoverage` 现在沿 case 文件自己的接缝分成两半：

| 轴 | 是什么 | 今天的读数 | 是不是闸门 |
|---|---|---|---|
| **诊断** | case 期望 agent **找出**什么 | **16/20** | **是** |
| 补救 | case 期望 agent **提出**什么 | 0/20 | 否，答案本来就该是否 |
| 联合（可通关） | 两半都要 | 0/20 | 保留给 `--fail-on-gap`，语义未放松 |

`CoverageOf`（旧的平铺签名）**没有改变任何现有调用的含义**：调用方没说是哪一半，
两个轴就报同一个答案。拆开是 `CoverageOfCase` 的 opt-in 行为。

#### 4.25.3 第一个诊断缺口就是一个真实缺陷

拆开的当天，诊断轴报 **15/20**，5 个缺口。逐条查下去，其中一条不是「没打包」，
是**打错了包**：

```
k8s/deployment-failed  k8s.describe_pod
  the k8s family is served by opskeeper-sre-middleware,
  which ships no tool named k8s.describe_pod
```

适配器里明明有它——`makeTool("k8s.describe_pod", adapter.RiskL2SoftWrite, ...)`。
翻 `runDescribePodTool` 的实现：一次 GET pod、一次 GET events，**不发第三个请求，
集群里什么都不变**。它被划成 L2「软写（如 analyze）」，理由是「生成诊断报告」。

而**节点包只发 L0 与 L1**（`toolset.PackagedReadTools`）。所以一个只读工具因为
「输出看起来像写」被排除在节点包之外，节点 agent 看不见它，
`k8s/deployment-failed` 于是成了整个节点舰队都无法诊断的 case。

按该文件自己写下的规则（「The mapping is by blast radius, not by API verb」），
`describe_pod` 的 blast radius 是零。改成 `RiskL1Diagnostic`，重新生成 toolset、
`sync-pig-ops.sh`、更新清单，诊断轴 **15/20 → 16/20**。

k8s 适配器现在**没有 L2 工具了**，这不是观察，是结论：这个适配器在「读」与
「改变工作负载跑在哪」之间没有中间态。

#### 4.25.4 剩下的 4 个缺口：登记表，不是容忍

| 缺口 | 为什么还开着 |
|---|---|
| `host.host_processes` / `host.top_cpu_procs` | host 家族整体不进节点包，理由是写着的：适配器以 root 跑在控制面被指向的那台机器上，它的读回答的是**那台**主机而不是节点自己。后者由只读包自己的探针（`host_lsof` / `host_read_journal` / `host_strace`…）和 `get_host_load` 覆盖——这也是 `host.host_load` 有别名而这两个没有的原因 |
| `host.top_cpu_procs` 另有一层 | 它还是个**改名**：适配器管这个读叫 `host.top_processes`。**故意不注册别名**，因为别名会让一个节点根本没装的工具声称覆盖了能力——和 `redis.kill_client` 不别名到 `redis.client_kill` 是同一条理由 |
| `host.host_files` | 两侧都没有等价物。`host.old_log_files` 回答的是更窄的问题（哪些旧日志），且随 host 家族一起排除；没有任何地方读节点的文件清单 |
| `kafka.rebalance_history` | Kafka 只有**当前**分配，没有历史。要回答得有一个把多次 DescribeGroups 结果存下来的采集器——那是采集器的活，不是 broker 客户端的。未裁决 |
| `redis.hot_keys` | **这个构建里根本不存在这个名字的实现**，适配器也没注册。是工具名错还是语料错，未裁决 |

它们写在 `pluginmanifest.DiagnosisGaps`，**两个方向都有守卫**：

- `TestTheDiagnosisAxisIsEitherCompleteOrAnOwnedGap`——没登记的缺口 = 红灯
- `TestTheDiagnosisGapLedgerHasNoStaleEntries`——登记了但已经不缺了 = 红灯

第二条同样重要。只增不减的登记表会变成「曾经为真的东西」的清单，而过期条目
**比没有条目更糟**：它在没人再看着那个工具的那一刻，正好读成「有人决定这样可以」。

#### 4.25.5 闸门为什么叫「未登记」

`make eval-coverage` 现在跑的是 `--fail-on-unrecorded-diagnose-gap`，不是
`--fail-on-diagnose-gap`。差别是这轮改动的核心教训：

先按「任何诊断缺口都失败」接进 Makefile，**当场就红了**——4 个已登记的缺口。
一个从写出来那天起就永久红的闸门会被关掉，而被关掉的闸门与没有闸门无法区分。
这正是上一版联合读数 0/20 被默默看了几个月没人发现它已经不测量任何东西的原因。

所以登记表就是容忍项，闸门只对**回归**开火。今天绿，fleet 掉了工具就红。

#### 4.25.6 变异验证（6 条，全抓）

| 变异 | 抓它的测试 |
|---|---|
| 从包、生成物、清单三处同时删掉 `k8s.events` | floor 三条同时转红，并点名四个受影响的 case；`make eval-coverage` 退出码 2 |
| 从 `DiagnosisGaps` 悄悄删掉一条 | `TestTheDiagnosisAxisIsEitherCompleteOrAnOwnedGap` |
| 往 `DiagnosisGaps` 加一条没人查过的 | `TestTheDiagnosisGapLedgerHasNoStaleEntries` |
| 闸门把所有缺口都当未登记 | CLI 两条（红的理由点名了 `redis.hot_keys`） |
| 把两轴塌回一轴 | CLI 两条 |
| 报告不再打印诊断轴 | CLI 两条 |

第一条值得单独说：它是**真的**把 fleet 改小，而不是改测试。三处一起改是刻意的——
toolset 与清单的一致性测试会先响，而**只有诊断闸门**能说出「哪四个 case 现在
没人能诊断了」。这正是它存在的理由。

**验证**：`go build ./...` 通过；8 个模块 `go test -count=1` 全绿；
`modulecheck` 边界成立；`gofmt` 干净；`make eval-coverage` 绿；
`make eval-vocabulary` 平台轴 20/20、补救轴 15/20（与本轮无关，未动）。

### 4.26 决策 88：导入器的覆盖面，以及一个不能复制的东西

#### 4.26.1 少一个类，报告读起来一模一样

`pluginimport` 是存量生态的入口：`.claude-plugin/plugin.json`、
`openclaw.plugin.json`、`skills.sh` 容器由它翻成 PiG package。它按目录复制资源，
认不出的目录**跳过**——不报错、不警告，于是转换结果看起来和「这个容器本来就
没带那类资源」完全一样。

上一版能复制的类写死在 `resourceDirs` 里，共 7 个：skills / agents / commands /
prompts / mcp / extensions / hooks。PiG 声明的是 8 个
（`coding/packagecontent/packagecontent.go:30-37`）。差的正是 **`themes`** 与
**`agent-environments`**：容器带了主题，转换后节点上没有主题，而报告里一个
字段都不提。

这就是这次改动的全部动机：**一个丢掉的类必须留下痕迹**，哪怕痕迹只是一行计数。

#### 4.26.2 名单住在 `core`，因为权威在 PiG，而唯一能读 PiG 的模块是 `core/pig`

名单不能和转换器住在一起，理由和 PiG 适配层的收口是同一条：这份名单的权威是
`coding/packagecontent` 的 `Kind` 常量，能对着它做断言的只有 `core/pig`，
而需要读它的是 `core/manager/biz/pluginimport`。两边都要读，所以它得住在两边
都能 import 的 `core`——`core/domain/pkgresources.go`。

`PackageResources` 同时承担两件事：

- `Kind` 是 PiG 的类名，也是包内目录名；
- `LegacyNames` 是旧容器可能用的拼写，**第一个是恒等拼写**（按 PiG 写的包把
  资源放在 PiG 期望的地方），其余是需要改写的。

今天只有一处改写：`commands → prompts`。测试
`TestTheCommandsRemapIsStillDeclared` 专门钉住它：没有任何东西能推导出这条
映射，删掉它，每个转换后的插件的斜杠命令都会静默消失。

#### 4.26.3 派生，而不是手写

`pluginimport` 的 `resourceDirs` 现在从 `domain.PackageResources` 展开，删掉了
手写列表。差别不在于短几行，而在于**失败模式**：手写列表只会「落后」，而落后
的后果不是报错——是一个不认识的目录被静默跳过。

PiG 侧的对账在 `core/pig/pigcontract/pkgresources_test.go`，三条：

| 测试 | 钉住什么 |
|---|---|
| `TestTheConverterCoversEveryResourceClassPiGDeclares` | 对**集合**比较（排序后逐项）：少一个类红，多一个 PiG 不会读的类也红 |
| `TestEveryLegacySpellingResolvesToItsOwnClass` | 每个旧拼写都能解析、且两个拼写不能落到同一个目录；第一个拼写必须是类名本身；不认识的目录必须回 false 而不是空串 |
| `TestTheCommandsRemapIsStillDeclared` | `commands` 仍解析到 `prompts` |

第二条的「必须回 false」不是洁癖：返回空串会让转换器写到包根，那是最难在
review 里看出来的错误。

#### 4.26.4 读源 `package.json`，但不复制

这是本轮最不直观的决定。PiG 的发现规则有两条，而且是互斥的
（`coding/packagecontent/packagecontent.go:125-180`）：

- 有 `pi` 块的 `package.json`：**只**加载它声明的 Pi 类
  （extensions / skills / prompts / themes），没声明的类加载**零个**，
  插件元数据也不能往里加；
- 没有 `pi` 块：按目录约定发现。

`pig` 块同理管着 PiG 自己的类（hooks / mcp / agent-environments）。所以一个
`pi` 块里只声明了 1 个 skill、而 `skills/` 目录里有 9 个的容器，加载的是
**1 个**。

于是复制源 `package.json` 到目标包，等于把**抑制**一起带过去：节点上仍然只服务
1 个，而且没有任何人知道为什么。反过来，不复制它，转换后的包按约定发现，
找到的是 9 个——**超集**，方向安全，且对 reviewer 可见。

所以 `Report.SourceManifest` 是**读而不复制**：

- `Present` / `Declares` / `Classes` / `Entries` 把源清单声明了什么记下来；
- 声明了资源块时，`decisionsFor` 追加一条 Decision，问的正是那个 reviewer 才能
  回答的问题：容器原本只服务声明的那几个，转换后会多出一些，这些多出来的是
  不是本来就想服务的？
- 清单**不可读**时报 `(unreadable package.json)` 并把 `Declares` 置真——
  「可能有声明、但读不出来」是唯一一种沉默最糟的状态：包按约定加载了一个
  超集，而没有人知道那从来不是一个决定。

#### 4.26.5 两种拼写撞一个类，现在会说话

`commands` 与 `prompts` 都写进 PiG 的 `prompts` 类。旧实现用
`report.Prompts > 0` 判断「已经写过了」——之所以能工作，只是因为当时 `commands`
是唯一的改写、且它的计数恰好先被写。现在用 `written` map 跟踪，两类撞名时产生
`resource_directory_collides` 警告，并指出**哪一个是先写的、被留下的是哪一个**。
静默挑一个正是这个文件要防的那类失败，只是上移了一层。

#### 4.26.6 前端把两个新类和一个「超集」提示露出来

`ImportReport` 增加 `themes` / `agent_environments` / `source_manifest`，
插件市场页多两个 Chip；`source_manifest.declares_resources` 为真时，在资源
折线之上加一行提示：转换后会多出一些资源。三个用例
（`PluginMarketplacePage resource accounting`）钉住它。

#### 4.26.7 变异验证（7 条，全抓）

| 变异 | 抓它的测试 |
|---|---|
| 从 `domain.PackageResources` 删 `themes` | pigcontract 1 红 + pluginimport 1 红 |
| `readSourceManifest` 什么都不读 | pluginimport 3 红 |
| 把源 `package.json` 复制进包 | pluginimport 2 红 |
| 撞名警告恢复成静默跳过 | pluginimport 1 红 |
| 不可读清单报成「已知非声明」 | pluginimport 1 红 |
| 删两个新 Chip（前端） | 1 红 |
| 删超集提示（前端） | 1 红 |

**验证**：`go build ./...` 通过；8 个模块 `go test -count=1` 全绿；
`modulecheck` 边界成立；`gofmt` 干净；`npx tsc --noEmit` exit 0；
`npx vitest run` 94/94 全绿。

### 4.27 决策 89：一个没人挂上的组件，和两个都没说的闸门

#### 4.27.1 复跑闸门，不是复读文档

计划 §五 A 的验收是「arch-lint 拦住所有逆向依赖」。文档在决策 58 与 74 里
记过 `go-arch-lint check → 0 notices, exit 0`，但那是**当时**的读数。本轮按
验收清单重跑，第一次就红：

```
File /core/pig/pigmcp/tools.go not attached to any component in archfile
total notices: 1
```

`core/pig/pigmcp` 是决策 85 落地的，它**没有在 `.go-arch-lint.yml` 里注册组件**。
于是这个包里的任何 import 都不受 `mayDependOn` 约束：它今天只 import
`core/ports`（正确），但一个将来想 import `core/pig/pigagent` 的改动不会被
任何规则拦住——而「MCP 桥不能自己建 Session、自己注册工具」正是这个包注释
写明的边界。

**这就是「闸门没跑」而不是「闸门坏了」**：`make arch-lint` 在二进制没装的
时候只打印一段警告然后 `exit 0`。一段警告不是闸门。

#### 4.27.2 被强制的那个闸门为什么没抓到

`make module-check`（不需要装任何东西，`scripts/modulecheck` 自带）已经在读
`.go-arch-lint.yml`，检查三件事：上行边要在台账里记名、每条授权要有真实
import 在用、每个真实 import 要有授权。三条都以「文件属于哪个组件」开头——
而 `checkArchLint` 对 `from == ""` 的处理是 **`return nil`**，静默跳过。

```
from := archLintComponentOf(cfg, names, rel)
if from == "" {
    return nil        // ← 未挂载的文件在这里消失
}
```

所以两个闸门各有一半：装了二进制的那一个会报未挂载文件，但默认不跑；
每次都在跑的那一个看不见未挂载文件。

修法是给 `modulecheck` 加第 4 条检查：**每一个非测试 `.go` 文件必须属于某个
组件**，否则报违规并点名文件。两个闸门从此回答同一个问题，而**被强制的那个
是更严的那个**。

配套的 yml 改动是把 `oxpig_mcp` 注册成组件，并给它一条精确授权
`mayDependOn: [oxcore_ports]`。**故意不加 `oxpig_agent`**：一个能建 Session 的
桥就能自己注册工具、绕过宿主闸门。

#### 4.27.3 变异验证

| 变异 | 结果 |
|---|---|
| 从 yml 删掉 `oxpig_mcp` 组件（代码不变） | `make module-check` 两条违规：未挂载文件 + 死授权；`go-arch-lint` 1 notice |
| fixture：组件外放一个 `.go` 文件 | `TestAFileNoComponentClaimsIsReported` 红，且**只有**这一条违规（证明其余三条检查确实看不见它） |
| fixture：文件都在组件内 | `TestEveryFileInsideAComponentIsNotReportedAsUnattached` 绿（负向对照） |

第一条是真实仓库上的变异，第二条是 fixture 上的——两条都要，因为真实仓库
只有一个当前状态，而「检查会不会误报」只能在对造的树上问。

#### 4.27.4 顺带确认的其它验收读数

本轮把计划 §五 里还没在本轮跑过的条目也跑了一遍：

| 闸门 | 读数 |
|---|---|
| A：`make arch-lint-run` | **OK - No warnings found**（修完之后） |
| A：`make module-check` | all module boundaries hold |
| B：7 provider 冒烟 | `core/pig/pigmodel/smoke_test.go`，逐 provider 有 fixture，且断言「注册表提供的 provider 数量 == 冒烟表条目数」，缺一个即红 |
| B：`go test -race` | `make module-race` 六个模块，无 data race |
| C：三个剧本 | `core/manager/biz/nodefleet/e2e/scenarios_test.go`：`alert_storm` ×2、`rca_loop` ×3、`recovery_verify` ×1，在 `go test ./...` 里正常跑（无 build tag） |

**验证**：`make arch-lint-run` 零告警；`make module-check` 绿；
`scripts/modulecheck` 全部测试绿（含真实仓库零违规那条）；`gofmt` 干净。

#### 4.27.5 计划 §四 逐条对照（每一条都要能指到当前状态）

验收不是「文档写过」，是「现在跑得出」。下表每一行的右侧都是本轮实际跑出来的读数
或实际读到的代码位置。

**阶段 A**

| 计划条目 | 当前状态 |
|---|---|
| `go.work` + 模块拆分、依赖方向落进 `.go-arch-lint.yml` | `go.work`（本地、不入库）+ 13 个 `go.mod`；`make arch-lint-run` OK；`make module-check` 绿 |
| `go build` + 全量 `go test -count=1` | 根模块 `go build ./...` 通过；8 个位置逐个 `go test ./... -count=1` 退出码 0 |
| 发布条件（无 workspace、只用 tag） | `make module-standalone-check` 退出码 0，13 个模块各自 `GOWORK=off` 构建并测试 |
| `core` 抽出跨模块 DTO | `core/domain`、`core/ports`、`core/wire`（events/gate/toolbroker） |

**阶段 B**

| 计划条目 | 当前状态 |
|---|---|
| Go 1.26 / PiG 固定版本、无本地 replace | `core/pig/go.mod`：`go 1.26.0`、`github.com/MichaelKinsy/PiG v0.3.0`，无 replace 指向本地路径 |
| `pigmodel` + `pigagent` | 齐；另有 `pigcoding`（SDK 驱动，决策 86） |
| 删除 eino 与 go-openai | 全仓 `go.mod`/`go.sum` 无 `eino`/`go-openai`/`sashabaranov` |
| 调用方零改动 | 第二套模型词汇已整体删除，调用方直接用 `pigmodel.Completer`（决策 67，比计划更彻底） |
| SSE golden 逐帧一致 | `pigwire` 10 条 golden 测试 + `pigagent` mapper 套件全 PASS；两份 golden 由独立输入产生、逐字节比较 |
| 7 provider 冒烟 | `core/pig/pigmodel/smoke_test.go`，并断言冒烟表条目数 == 注册表 provider 数 |
| `go test -race` | `make module-race` 六个模块退出码 0 |

**阶段 C**

| 计划条目 | 当前状态 |
|---|---|
| 运维 profile + 只读 piglet（L1） | `plugins/pig-ops/opskeeper-sre-readonly/pig-opskeeper-ops.yaml`：4 拓扑 + 13 host 探针 + 告警查询 = 18 工具，`safety_level: L1` |
| `PigSupervisor` | `core/edge/pigsupervisor`：`Start`/`Stop`/`Restart`/`Health`/`OnRestart`；崩溃重启、退避、降级共 33 条测试 |
| `NodeFleet` + `agent.*` 隧道方法 | `core/floor/tunnel/agent.go`：`agent.prompt` / `agent.steer` / `agent.abort` / `agent.state` / `agent.set_model` / `agent.event` / `agent.health` / `agent.decide` |
| 策略闸门 | `core/edge/policygate` + `gatesocket` + `toolbroker`；`core/manager/biz/nodefleet/e2e` 六条剧本（含「agent 不能靠重启压掉风暴」「无正确 digest 的裁决被拒」）全 ok |
| 连接规模三项 | 决策 78（full jitter 风暴抑制）、79（连接池上限）；心跳重连在 tunnel client |

**阶段 D**

| 计划条目 | 当前状态 |
|---|---|
| `sdk/` 三个发布物 | `sdk/manifest.go`（清单类型）、`sdk/register.go`（注册 API）、`sdk/negotiate.go`（版本协商），只依赖 `core` + yaml |
| 导入器（容器 → PiG 包） | `core/manager/biz/pluginimport` + `POST /v1/marketplace/import`；8 类资源全部覆盖、源清单读而不复制（决策 88） |
| B1/B2/B3 插件迁移 | `plugins/pig-ops/`：readonly(18) / observability(12) / middleware(53) / repair(5) |
| 审核流水线 | `pluginmanifest/{review,signing,trust,rollout}.go` + `service/plugin` 发布全链路 + 6 条 HTTP 路由 |

**阶段 E**

| 计划条目 | 当前状态 |
|---|---|
| 插件市场：清单索引 / 版本矩阵 / 兼容矩阵 | `marketplace` 路由（installed/registries/import/upload/bindings）；`pluginmanifest/version.go` 的两轴 `CheckVersions`；`GET /v1/plugins/{name}/compatibility` + 前端兼容矩阵卡片 |
| 跨云迁移模板 | `pluginmanifest/profiles.go`：`finance-strong-consistency` / `saas-multitenant`，profile × 实际目录组合校验（决策 70） |
| 评测接入 harness | `make eval-coverage`（诊断轴 16/20，`--fail-on-unrecorded-diagnose-gap`）进构建 |

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
| A 模块化地基 | 20% | **100%** | 13 个模块落地、`internal/` 清空、`modulecheck` + `go-arch-lint` 两个闸门可执行且非空转——**决策 89 补上了最后一块：`modulecheck` 现在也检查「每个文件必须属于某个组件」，两个闸门回答同一个问题，而每次都会跑的那个是更严的那个**（§4.27）、共享底座的两条反向边已清并由 `floorIsolation` 钉住（决策 66）、**两个闸门之间的最后一处不对称已消除：`.go-arch-lint.yml` 有了读者，104 条无人行使的授权已删，逆向边按文件记名**（决策 74）。A 阶段无剩余项 |
| B PiG 适配层 | 20% | **100%** | `pigmodel` / `pigagent` / `pigrpc` / `pigwire` 四件套齐、eino 与 go-openai 清零、内核接缝（决策 32/33）打开、契约套件 `core/pig/pigcontract` 落地（决策 64）、**PiG 已换成固定 tag 并在发布条件下被验证**（决策 65）、**AI 层已原生化：第二套模型词汇全部删除，宿主直接用 PiG 的 `ai` 类型**（决策 67，见 §4.5）。**决策 84 把这个 100% 重新打开：控制面的 turn 仍跑在 `pigagent.Kernel`（自研装配 + `ports` 平行形状）而不是文档里的 `coding.Session`，「彻底改成 pig 风格」这一条尚未完成**。决策 75 当年判「维持 Kernel」的两条理由已在 §4.22 被逐条推翻，方向已定、内核未换，剩三步（拆 Mapper/ports 形状、行 id 改由 `TurnEndEvent` 分配并重验 SSE golden、四处装配重接）。**决策 86 落地了 SDK 驱动**：`pigagent.SessionKernel` 跑 `coding.Session`，与 `Kernel` 并存、共用 `Mapper`/`runState`/`buildPrompt`/`NewAdapters`，逐帧 golden + 逐行 transcript 的差分闸门已绿（见 §4.24）。**决策 86 已完成接线**：驱动由 `OPSKEEPER_AGENT_KERNEL` 选，`pig` 走裸循环、`pig-sdk` 走 `coding.Session`，`newAgentKernel` 返回 `Agent` 接口且宿主绑定对两者相同（§4.24.6）。`pigmcp` **判定不接控制面**（控制面的 MCP 已经过 `basetool` 路径到达 Session driver，再接会产出两份同能力工具），其位置是节点侧 `pig --mode rpc`（§4.24.7）。顺带修掉一个真实数据竞争（`Mapper` 序号计数器在工具 goroutine 上无锁）。**B 阶段已 100%**：`coding` 的形状由 `pigcontract/contract.go` 钉住，类型系统表达不了的四条语义假设由 `pigcontract/session_contract_test.go` 在真 `coding.Session` 上钉住，9 条变异全抓（§4.24.11）。往后只剩**跟随上游增量补钉**，不是缺口 |
| C 节点 Agent | 20% | **95%** | `pig --mode rpc` 运维 profile + supervisor + `policygate` + 7 个 `agent.*` 隧道方法 + `NodeFleet` + 只读 piglet，三个剧本在新拓扑下通过；连接规模三项（连接池上限 / 心跳重连 / 风暴抑制）已全部落地（决策 78/79）。**决策 85 更正了此处的「剩下」**：MCP 运行时**一直都在**（`mcpclient` + `biz/mcp` + `tools.MCPTool` + 启动期发现），此前把「PiG 没有」误记成「我们没有」。本轮补的第三条路 `core/pig/pigmcp`（PiG 原生工具形状）**已就位，且已判定不接控制面**：控制面的 MCP 已经过 `basetool` 路径到达 Session driver，再接会产出两份同能力工具；它的位置是节点侧 `pig --mode rpc`（§4.24.7）。详见 §4.23 |
| D 插件生态 | 25% | **95%** | B1/B2/B3 全部闭环（18 + 12 + 53 + 5 个工具）、审核流水线（签名 → 清单 → 准入 → 灰度 → 回滚）、运输通道 6 条路由、`sdk` 三个发布物、**能力声明已从「家族」升级到「逐方法」，四个包的「声明 == 实际」全部有守卫**（决策 69）。**覆盖率闸门从「冻结的 0/20」拆成两条轴，诊断轴成为真正的回归闸门**（决策 87，§4.25），并由它查出一个真实缺陷：`k8s.describe_pod` 被误划为 L2 软写，导致节点只读包发不出这个工具、`k8s/deployment-failed` 无法诊断。**导入器的覆盖面已收口**（决策 88，§4.26）：8 类资源全部派生自 `domain.PackageResources`，`core/pig/pigcontract` 对着 PiG 的 `Kind` 常量逐类核对，`themes` / `agent-environments` 不再被静默丢弃，源 `package.json` 改为「读而不复制」（复制会把清单的发现抑制带到节点上），撞名目录从静默跳过变成可读警告。剩下：更多插件迁移 |
| E 生态治理 | 15% | **95%** | 兼容矩阵（edge 轴 × PiG 轴）、金融 / SaaS 两个 profile 模板、profile × 实际目录的组合校验（决策 70）、**发布前兼容矩阵 API，管理侧预检与节点裁决共用 `CheckVersions`**（决策 71）、插件 × golden case 覆盖报告、发布全链路（Start/List/Status/Advance/Halt/Rollback）。**兼容矩阵 agent 轴不再是「无法判断」：节点随心跳自报 PiG 构建，控制面一次查询读取（决策 73）**。**计划 E-3「插件纳入黄金集回归」已落地**：`plugin-coverage` 的诊断轴由 `--fail-on-unrecorded-diagnose-gap` 把进构建（§4.25），剩下的 4 个缺口逐条登记在 `pluginmanifest.DiagnosisGaps` 并附理由，登记表两个方向都有守卫。**两个前端页面已经落地**：插件市场 + 同一个页面上的兼容矩阵卡片（决策 82，§4.20）、节点已装插件清单面（决策 83，§4.21）——此前记在这里的「插件市场前端页面、兼容矩阵前端页面」是过期条目，不是欠账。剩下：发布流程的定时/触发自动化（唯一的实现项，且不在原计划 §四 E 的三条里） |

加权合计 ≈ **98.0%**（20×1.00 + 20×1.00 + 20×0.95 + 25×0.95 + 15×0.95）。

**A 阶段到此 100%，且它是唯一「完成」而不是「差最后一点」的阶段**——计划 §五
对 A 的三条验收（模块化 + 全量测试绿 + arch-lint 拦住逆向依赖）现在都由**两个
互相独立的闸门**分别守着，其中一个在本轮之前是空转的。

**这个数最容易被误读的地方**：C 与 D 的 90% 里，"能跑通"和"能上生产"之间
差的是规模验证与运维面（插件市场 UI、连接规模、发布自动化），不是核心链路。
核心链路——告警 → 诊断 → 审批 → 修复 → 验收——是通的，也是本仓库测试盯得
最紧的一段。

| 模块 | 结果 |
|---|---|
| 根模块 `go test ./... -count=1` | **18 包 ok / 0 failed**——`internal/` 已清空（决策 63），根模块只剩 `cmd/`、`scripts/`、`tests/` 与 web 的 Go 工具包，全部是装配层与测试 |
| `core` | 全部 ok（3 包） |
| `core/pig` | 全部 ok（10 包，含契约套件 `pigcontract`、`pigcoding` 的 7 个离线端到端用例、决策 67 新增的 `pigai` re-export 面与 `pigtools` 桥） |
| `core/edge` | 全部 ok（26 包）——`internal/edgeagent` 的 62 个文件整体迁入（决策 61），原有的 pigsupervisor/policygate/gatesocket/toolbroker/agentprofile 与它同模块 |
| `core/floor` | 全部 ok（9 包，6 个有测试）——`spill_helper` 的 3 个用例在这里被修好（见决策 60） |
| `core/harness` | 全部 ok（13 包） |
| `sdk` | 全部 ok（1 包） |
| `core/manager` | **全部 ok（223 包）**——控制面基础设施（决策 62）之后，`biz`/`data`/`model`/`server`/`service` 与 `iam` 也在这一轮迁入（决策 63）。1100 个 Go 文件，其中 413 个测试文件 |
| 5 个 extension 模块 | 各 1 包，全部 ok |

13 个目录（根模块 + 7 个已拆模块 + 5 个 extension）串行跑完：**308 包 ok / 0 failed**。四次搬迁（决策 60/61/62/63）前后总数一次没变，说明搬的是位置，不是测试；变的只是包落在哪个模块里——根模块 113 → 18，`core/manager` 50 → 223。`core/floor/skill/builtin` 的三个
`TestTruncateOrSpill_*` 曾长期在 `/var/tmp` 存在但不可写的机器上失败——降级判据写在
`MkdirAll` 而不是写入上，所以那条降级路径从未生效；决策 60 顺手修好了它（测试一直是
对的，代码不是）。
`cmd/opskeeper-eval` 的两个闸门现在都是绿的：`plugin-coverage` 按**方法名**
join，读数 **0/20**（18 个 GAP，原因逐条给出；这是被
`TestNoShippedCaseIsReportedAsCovered...` 钉住的正确答案，不是待修的缺陷，
见决策 69 与决策 80），`vocabulary` `--fail-on-gap` 在真语料上通过、在
一个合成的不可满足语料上必须失败（新增的正反两面，见决策 59）。
`go run ./scripts/modulecheck .` → 边界全部成立（模块规则见决策 37，BC 规则见决策 38）。
`make module-standalone-check`（决策 65）→ 13 个模块在 `GOWORK=off` 下逐个
`go build` + `go test` 通过，即**关掉 go.work、只用 go.mod 里的 tag 与
replace、纯模块缓存（`GOPROXY=off`）也能构建**。这是 CI 与发布真正走的路，
也是唯一能在发布前发现 go.mod 写错的检查。
`make module-test` 一次跑完全部模块。
`make module-race`（`core` / `core/pig` / `core/edge` / `core/floor` / `core/harness` 各自
`go test ./... -count=1 -race`）→ 全部 ok，`pigsupervisor` 的重启循环这条
并发热点没有数据竞争。sdk 无并发测试。

| 阶段 | 内容 | 状态 |
|---|---|---|
| **A 模块化地基** | `go.work` + 7 个模块 `go.mod`（`core` / `pig` / `edge` / **`floor`**（决策 60）/ **`manager`**（决策 62+63）/ `harness` / `sdk`）+ 5 个 extension 模块；`core`（domain/ports/wire）；`sdk` 清单准入 | ✅ **已完成**——`internal/` 已清空（决策 63），模块依赖方向由 `modulecheck`（可执行）+ `.go-arch-lint.yml`（文档）双重钉住；遗留两条债务（底座包级 setter、arch-lint 债务清单无守卫）见「当前真实缺口」 |
| **B PiG 适配层** | `pigmodel`（settings→`*ai.Model`）、`pigagent`（含 `buildPrompt` 历史回放，见决策 25）、`pigrpc`（`pig --mode rpc` 客户端）、`pigwire`（SSE 帧翻译）；**`go-openai` 已整包移除**（`core/manager/pkg/llm` 自持 HTTP wire，见决策 22）；**工具治理已抽成与内核无关的装饰器**（见决策 24）；**PiG 支撑的 `llm.Client` 已落地并接入装配层**（`core/manager/pkg/llm/pigclient.go` + `pigsettings.go` + `pigregistry.go`，`OPSKEEPER_LLM_BACKEND=pig` 切换，见决策 26）；**内核侧宿主绑定已落地**（`core/manager/biz/aiops/agentkernel/`：`ToolBag` + `Persister`（同时是 `ToolCallRecorder`）；`core/manager/biz/aiops/chatruntime/kernelsink.go`：`ports.EventSink` → 控制面事件，含准入/结算两帧的 join，见决策 27/28；审计/预算/审批/依赖装配四件套落在 `agentkernel`，见决策 30；历史回放改为一计划两渲染，见决策 31；**换内核接缝已开**：`Runtime.Handle` 第 5d 步分流 + `kernelpath.go` 驱动 `ports.Agent`，见决策 32） | ⚠️ 部分——模型接口与**编排接缝**都已就位，**装配层已接线**（`OPSKEEPER_AGENT_KERNEL=pig`，见决策 33）；**eino 已彻底移除**：`go.mod`/`go.sum` 中 `cloudwego/eino` 与 `eino-contrib/jsonschema` 双双消失，`chatruntime` 只剩内核一条路（见决策 34） | ✅ 已落地 |
| **C 节点 Agent** | `pigsupervisor`（崩溃重启/退避/Degraded）、`policygate`（白名单+审批+digest）、`gatesocket`（unix socket 准入）、准入信使 extension、tunnel 7 个 `agent.*` 方法 + `agent.decide`、控制面 `NodeFleet` + `Service.Decide` + HTTP 决策端点、per-session 角色表、**profile piglet**（`tools: []` 摘除 PiG 8 个内置工具含 `bash`，真实二进制 A/B 验证 0/8 active，见决策 48）、**内置具名 piglet**（`plugins/pig-ops/opskeeper-sre-readonly/pig-opskeeper-ops.yaml`：18 只读工具 + 8 skill + 信使，见决策 56） | ✅ 已落地——节点侧生成 profile 与内置具名 piglet 并存（决策 56） |
| **D 插件生态** | L1 只读 profile（18 工具 + 7 persona + 信使）、`pluginimport` 导入器（`/v1/marketplace/import` 入口，见决策 55；覆盖面见决策 88：8 类资源全部派生自 `domain.PackageResources`，源清单读而不复制）、**B1 只读工具集**（工具集 extension + `toolbroker` + `agent.tool` 反向调用 + 双向漂移测试）、**B2 可观测工具集**（12 只读工具，schema 由控制面 registry 生成，全量 upcall）、**B2 中间件工具集**（`opskeeper-sre-middleware`：53 个只读工具，由 `core/manager/middleware/toolset` 从适配器活注册生成；此包把 `plugin-coverage` 从 2/20 带到 20/20，但那 20/20 是**家族级 join 的产物，已被决策 69 推翻**，按方法名 join 的真实读数是 0/20，见决策 69 / 80）、**B3 修复包**（L2/5 工具/`approval.required`/`pod` 半径/pin 安装 + 审批回执 + 写操作全部走控制面）、**审核流水线**（ed25519 树签名 + 信任库 + 签名→清单→准入三段审核 + 灰度波次闸门 + 节点侧 `admitPackages` 接线）、**发布运输通道**（`plugin.install` / `plugin.remove` / `plugin.list` + 节点 `pluginStore` + 控制面 `ReleaseManager` + 6 条 `/v1/plugins/releases` 路由）、**控制面适配器真实化**（pg/redis/k8s/mq/host 五条，见「闭环修复派发链路」）、**git 适配器真实化**（8 工具全实现，只读，见决策 45）、**`sdk` 发布面**（清单类型 + 注册 API + 版本协商，见决策 46） | ✅ B1/B2/B3/审核流水线/运输通道全部完成；`git` 适配器 8/8 工具真实；`sdk` 三个发布物齐全；**四个只读包**（readonly / observability / middleware / 修复包的只读半边）在 `plugins/pig-ops` 下齐备 |
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
| A | 7 模块 `go build` + 全量 `go test -count=1` 绿；arch-lint 拦住逆向依赖 | ✅ **13 个模块目录 `go build` 全过、全量 `go test` 303 包 ok / 0 failed**；`internal/` 已清空（决策 63）；逆向依赖由 `modulecheck`（可执行，含"只允许测试的跨模块边"一条）与 `go-arch-lint`（0 warnings）双重拦住（决策 37/38/60/62/63） |
| B | **SSE 帧 golden 逐帧一致** | ✅ 两条路径各有一份 golden，且互相逐字节相等（见下） |
| B | **eino / go-openai 依赖清零** | ✅ `rg eino go.mod` 无命中；`core/manager/biz/aiops/graph/`（14 文件）与 `core/manager/pkg/llm/eino_*.go`、`budget_callback.go` 全部删除；`chatruntime` 测试全量迁到 `scriptedKernel`；`OPSKEEPER_AGENT_KERNEL=pig` 与退役拼写 `graph` 解析到同一内核（见决策 34） |
| B | **7 provider 冒烟** | ✅ `pigmodel/smoke_test.go`：7 个 provider 各起一个 httptest SSE 源，真实走 `Provider.Stream` |
| B | `go test -race` 无泄漏 | ✅ `core/pig/...`（198）、`service/plugin` + `core/edge/biz`（60）、`cmd/opskeeper-edge`（118）、`core/manager/pkg/llm`（107）全部 `-race` 通过 |
| B | **`llm.Client` 换实现（PiG 支撑）** | ✅ `pigclient_test.go` 用 PiG 真实 provider 栈跑 httptest：选择/注册表/转写/请求体/流式/回传全链路，含 tool-call 往返与预算「先扣后发」；`router.go` 的子客户端工厂让**每个** provider 都走 PiG（见决策 26） |
| B | **PiG 固定 tag，且在发布条件下成立** | ✅ 6 个 go.mod 去掉本地 replace，只留 `require v0.3.0`；`make module-standalone-check`（`GOWORK=off`，13 个模块逐个 build + test）绿；契约套件 `core/pig/pigcontract` 在**打 tag 的 PiG** 上通过（见决策 64/65） |
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
      根模块从 113 包掉到 18 包。13 个模块目录串行跑完 **303 包 ok / 0 failed**。
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
  不再存在，七个模块 + 5 个 extension 全部落地，13 个目录 303 包全绿；
  决策 66 把共享底座剩下的两条反向边清掉，并让它们不再会长回来。
  代码侧没有已知的未完成项。A 阶段**剩下的是债务而不是缺口**，只剩一条，
  见下面 arch-lint 那一项。
- **B 阶段已收口，包括决策 67 的 AI 原生化**。`ports.LLMRequest` /
  `ports.LLMResponse` / `ports.Conversation` / `ports.Usage` / `ports.Agent` /
  `ports.TurnResult` 全部删除；`pkg/llm` 只剩 `ProviderConfig` 与预算；
  `llmpig` 变成"设置表 → PiG ModelRegistry"的单向发布器
  （`Sync.Publish` / `CatalogView`），不再持有第二套 client。
  **这一轮顺带修掉一个真实的 nil 解引用**：`agent.AssistantMessage.ObserveUsage()`
  在 provider 未上报用量时返回 `nil`，而三处调用点都直接解引用——
  本地模型与中断的 turn 正好走这条路径。现由 `pigagent.UsageOf` /
  `pigmodel.UsageOf` 各自在归属的包里收口。
  剩下的不是"发布条件"问题，而是下面这一条。
- **"发布条件"目前只对外部依赖是真的 tag**。PiG 与
  `extensions/sdk` 已经是 `v0.3.0`；`core` / `edge` / `floor` / `harness` /
  `manager` / `pig` / `sdk` 七个兄弟模块之间仍是 `v0.0.0` + 相对 `replace`
  （决策 60/62/65 的设计如此）。要把这一层也变成真 tag，需要先给七个模块
  打 v0.x 版本并把 replace 换成版本号——那是一次**发布动作**，不是代码动作，
  也不该在没有版本号的仓库里硬做。
- ~~**arch-lint 的 white-list 粒度问题**~~ **已完成（决策 74）**。
  `mayDependOn` 是组件粒度，账本里开一个口子之后同组件新增同类导入不会报警，
  而且**没有任何东西读这份 yml**。决策 74 给 modulecheck 加了 yml 读取器和两条
  不变式：**每条授权都必须有真实 import 在用**（当场删掉 104 条，go-arch-lint
  复核零告警）、**每条逆向边都必须记名到文件**（当场抓到一个活的、无人知晓的
  `imbridge → service` 倒边）。见 4.12。
- ~~**`internal/pkg → core/pig` 的反向边（deepScan 独立复现）**~~
  ✅ 已完成（决策 66）。`pigsettings.go` / `pigregistry.go` / `pigclient.go`
  搬进 `core/manager/llmpig`，`web_search` 的六个包级 setter 换成不可变构造
  + `skill.Replace`。**这两条当初为什么 `modulecheck` 报不出来**——共享底座到
  适配层的方向是模块内合法的（manager → pig），包级 setter 更是根本不进
  import 图——现在各有一条专门的规则：前者是 `floorIsolation`，后者是
  构造器本身不再存在。deepScan（`allow.deepScan`）仍然能报出装配层的边，
  但它没有被打开，因为那会同时报出 54 条已知噪声；真正需要它的那四条
  （`manager_biz → shared_pkg` 与 `cmd → shared_pkg`）已经在决策 66 之后
  缩到只剩 `cmd → shared_pkg` 一条装配层调用。
- **B1/B2/B3 已闭环**：18 个节点本地只读工具、12 个可观测只读工具、**53 个中间件
  只读工具**、5 个写工具均已打通。写工具全部经控制面 reviewer，且要消耗一次性
  审批回执；`host_restart_service` 的本地执行被证明确实锁死（回归测试可复现该
  失败）。可观测 12 工具覆盖 PromQL / LogQL / TraceQL / 数据库源 / 代码仓库 /
  审计历史；中间件 53 工具覆盖 pg / redis / k8s / kafka / rabbitmq / mq 的当下
  状态与 `git.find_runtime_link`（见决策 59）。
- ~~**`plugin-coverage` 现在是 20/20，但这个数字是家族级的**~~ **已被决策 69 推翻**：
  join 改成按方法名之后读数是 **0/20**，下面这段描述的是它被推翻之前的样子，
  保留是为了说明「家族级豁免」这条路为什么走不通。`redis/slow-cmd`
  期望的 `redis.kill_client`（适配器实际叫 `redis.client_kill`）当时被
  `opskeeper-sre-middleware` 声明的 `redis` 家族覆盖，于是这个 case 从"结构性
  不可通过"变成"可评分"——而舰队里从来没有任何节点跑过这个工具。判分能不能过，
  是另一回事。按名字判定的那条轴是
  `loopActionExecutability`（只认精确符号，当前 0 缺口）。把包的能力声明从
  "家族"升级到"逐方法"是 `sdk` 的后续能力，见决策 59 末段。
- **B2 的 MCP 载体，本仓库早就自己写了一套**（决策 85 更正了此前的记述）：
  PiG 的 `mcp` 包类型确实**只是声明**——全仓 MCP 引用都在
  `coding/packagecontent/packagecontent.go` 与 `cmd/pig/package_*.go`
  （解析/校验/清单），没有 JSON-RPC 客户端、没有握手、没有桥。**但这句话
  只关于 PiG。** OpsKeeper 侧一直有一套完整的 MCP 运行时：
  `core/manager/pkg/mcpclient`（stdlib-only 的 `initialize` / `tools/list` /
  `tools/call` + SSE 解包）、`biz/mcp`（注册 CRUD + 凭据解析 + 探测 +
  `ToolsCacheJSON` 回写）、`tools.MCPTool`（一个 MCP 工具 = 一个
  `basetool.BaseTool`，带 `MCPToolClass` 风险分级与审批入队）、以及
  `cmd/opskeeper/main.go:2878` 的启动期发现（连每个 enabled server，把工具
  挂进聊天工具袋）。
  此前把「PiG 没有」直接写成「我们没有」，是这一条被记成缺口的唯一原因——
  而 upcall 通道那 65 个工具（12 可观测 + 53 中间件）是**并行的另一条**路，
  不是替代品。两条都在，见 §4.23。
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

64. **契约套件落地：`core/pig/pigcontract`（计划 §六风险 1 的最后一块）**。
    PiG 是 pre-stable 0.x，而 `core/pig/go.mod` 现在 `replace` 到本地
    checkout——开发期正确，发布不可行。换固定 tag 的前提不是"读一遍上游
    diff"，而是**把本仓库依赖的那部分 API 面变成可执行的断言**。这个包就是
    那份断言，它分两层：

    - **形状（`contract.go`，编译期）**：把每个用到的 PiG 符号写成包级
      `var _ = ...` 初始化式——`ai.OpenAIConfig` 的每个字段（含
      `DetectCompat` 的返回类型）、`ai.Model`/`ai.StreamOptions` 的字段集、
      `ai.ToolSchema` 的 `Parameters`、`agent.AgentOptions` 的 15 个字段、
      三个钩子的**精确签名**、`agent.StreamFn`、7 个事件类型、
      `rpcclient` 的选项与 6 个动词、`piglet` 的 4 个解析函数、
      `extensions/sdk` 的注册面。用调用形状写（`run, err :=
      ag.BeginSendMessages(...)`）而不是命名返回类型，是为了在不复制 PiG
      内部类型的前提下钉住方法签名。选 `var _ =` 而不是函数体，是为了让任何
      linter 都删不掉它——每个 pin 每次 `go build` 都被类型检查一遍。
    - **语义（`contract_test.go`，测试期）**：类型系统表达不了的值变化——
      `ai.ThinkingOff` 等 6 个档位的字符串（settings 行按字符串比对）、
      `ai.EventTextDelta` 等流判别式（`pigwire` 只留 `text_delta` 一条）、
      内容块 JSON 的 `type`/`text` 键（`pigwire` 靠它统计 pending 工具调用）、
      `sdk.EventTurnStart` 等 7 个节点线名（`pigwire` 的 switch 字面量）、
      `sdk.EventToolCall` 这个闸门注册的钩子名、`ToolModeParallel` 的拼写、
      两个版本字符串非空。**断言对象是"我们的代码写的字面量"与"上游的常量"，
      不是字面量与自己**，所以一次上游改名会红在契约里，而不是变成一个没人
      看见的空帧。

    **本包是叶子**：不 import 任何 OpsKeeper 包，也没有任何包 import 它，所以
    它只约束自己。`go build ./...` 变红时的修法在 `core/pig`——改适配层去
    追上游，或者（上游删了我们需要的东西）这就成了与 PiG 的对话，而不是
    一次全仓重建。**实测**：`cd core/pig && go build ./... && go test ./...
    -count=1 -race` 全绿；新包使 13 个模块目录的包总数从 302 变成 **303**，
    全量 `go test ./... -count=1` 依然 **303 包 / 0 failed**；
    `modulecheck` 与 `go-arch-lint`（新增 `oxpig_contract` 组件）均 0 警告。
    **没做的**：契约只覆盖我们实际用到的 API 面，上游新增能力不会自动进
    契约；换 tag 本身（改 `replace` 指向 `v0.3.0`、CI 里在无本地 checkout 的
    机器上跑一遍）仍是 §七第 1 条。

---

65. **PiG 换成固定 tag，并且"没有 workspace 也能构建"变成一条会跑的闸门**。
    决策 64 落了契约套件，这一件事才做得成。改了三处，其中第一处是本轮真正的
    内容，另外两处是它逼出来的。

    - **`core/pig/go.mod` 与 5 个 `core/pig/extensions/*/go.mod` 去掉了
      `github.com/MichaelKinsy/PiG => /Users/louloulin/appx/PiG`**，只留
      `require github.com/MichaelKinsy/PiG v0.3.0`。**实测**：`GOWORK=off
      GOPROXY=off`（纯模块缓存、无网络、无本地 checkout）下六个模块全部
      `go build` + `go test` 通过——`pigcontract` 的 7 个语义断言在**打 tag 的
      PiG 上**同样成立，这才是"换 tag 安全"的证据，而不是本地 checkout 上成立。
      节点侧早就是 tag 路径：`scripts/sync-pig-ops.sh` 生成的包内 go.mod 从来
      不带 replace，这次只是让仓库里的规范副本与它一致。
    - **`go.work` 承担本地覆盖，并且它是不入库的**。开发 PiG 本身的人跑
      `make pig-dev-pin PIG_DEV_PATH=/path/to/PiG`（`go work edit -replace`），
      撤销用 `make pig-dev-unpin`。`PIG_DEV_PATH` 故意**不给默认值**：把某台
      机器的路径写进 Makefile，就是 replace 指令悄悄回来的路径。覆盖期间跑的
      测试对 tag 不作证，target 自己会把这句话打在屏幕上。
    - **新增 `make module-standalone-check`**：13 个模块逐个 `GOWORK=off` 构建
      并测试。这不是 `module-test` 的重复。`go.work` 在 `.gitignore` 里，
      workspace 构建通过 go.work 解析兄弟模块与 PiG，而 CI 与发布没有这个
      文件——两种构建**在 go.mod 写错的时候才会分歧**，而那正是发布时才暴露的
      一类错。**它当场抓到了两个真缺陷**：
      （1）`sdk/go.sum` 缺 `gopkg.in/yaml.v3` 的 go.mod 哈希，workspace 下被
      掩盖；（2）根 `go.mod` 里 `core/floor` 的 require 版本与其余五个兄弟模块
      不一致（`v0.0.0` vs `v0.0.0-00010101000000-...`），只有 go.work 兜着。
      两条都修掉了，现在 13 个模块在 readonly 模式下离线全绿。

    **顺带把 Go 1.26 落到了所有构建入口**。决策 34 之后每个 go.mod 都写着
    `go 1.26.0`，而 CI、两个 Dockerfile 与 `Dockerfile.dev` 还钉在 1.25——
    靠 `GOTOOLCHAIN=auto` 静默下载一个没人审过的工具链才能构建。现在：
    `ci.yml` 与 `release.yml` 用 `1.26.x`；`Dockerfile.opskeeper` 装
    `GO_VERSION=1.26.2` 并设 `GOTOOLCHAIN=local`（依赖抬高下限就在这里报一行，
    而不是产出一个用了别的工具链的镜像）；edge 与 dev 镜像换 `1.26-alpine` /
    `1.26-bookworm`；文档与 PPT 里的版本号一并对齐。

    **CI 顺带修好了**：它此前只跑根模块的 `go build`/`go vet`/`go test`——
    根模块只剩 18 个装配层包，**下面 12 个模块一次都没被编译过**；而它唯一
    的失败叙事（PR #123）指向的 `internal/manager/biz/edge/...` 路径在决策 63
    之后已经不存在。现在 CI 跑 `go vet`（根）、`make module-check`（模块边界）、
    `make module-standalone-check`（13 个模块，发布条件），并把这条故事改写成
    仍然成立的样子。

    **没做的**：`core/manager` 与 `core/floor` 仍只打 `v0.0.0` 的本地
    replace（决策 62/60 的设计：发布时换成打好的 tag），所以"发布条件"目前
    只对 PiG 与 `sdk` 这类**外部**依赖是真正的 tag，对**兄弟模块**仍是路径。
    要彻底关掉，需要先给 7 个模块打 v0.x tag 并把 replace 换成版本号——那是
    一次发布动作，不是代码动作。

---

66. **共享底座的两条反向边清掉了——但清掉的方式是把"记住它"变成"跑它"**。
    §七 第 1 条列了两件事：一件是共享底座依赖 PiG 适配层，一件是共享底座用
    包级 setter 反向拿到实现。**只做搬迁，两条都会原样长回来**——下一次有人
    为了省事在 `pkg/llm` 里 import 一次 `pigmodel`，或者再加一个
    `SetXxx`，没有任何东西会响。所以这一条做的是两半：搬，加上让搬成为不变量。

    ### 第一半：`core/manager/llmpig`

    `core/manager/pkg/llm` 里的 `pigclient.go` / `pigregistry.go` /
    `pigsettings.go`（576 行非测试 + 419 行测试）搬进新包
    `core/manager/llmpig`，**方向反过来了：llmpig → pkg/llm**。

    - **`EstimatePromptTokens` 从底座导出**。它原来叫
      `estimatePromptTokens`，是 `pkg/llm` 的私有函数，被 HTTP 客户端和
      PiG 客户端共用。搬包之后它必须跨包可见——而这暴露了一件本来就该说清
      的事：**同一个 `llm.Client` 接口的两个实现必须用同一个估算值**，
      否则预算按 A 估算、按 B 记账，谁也说不清。
    - **`PigRegistry` → `Registry`、`NewPigRegistry` → `NewRegistry`**。
      `llmpig.NewPigRegistry` 是 stutter，包名已经说明了它是什么。
    - **装配点只有两处**：`cmd/opskeeper/main.go:865` 的
      `llmpig.NewRegistry(llmpig.NewSettingsSource(...), log)`，和
      `kernelWiring.Models` 的类型。`core/manager` 里只有两个包还认识
      `core/pig`——`llmpig`（这道缝）与 `biz/aiops/agentkernel`（业务层，
      方向本来就对）。**`core/manager/pkg` 与 `core/floor` 现在对 `core/pig`
      的引用是 0 条。**
    - **不变量**：`scripts/modulecheck` 新增 `floorIsolation` 规则表，
      两条（`core/manager/pkg/`、`core/floor/`），任何 `.go` 文件——**包括
      测试文件**——import `core/pig` 都报违规。这条规则带两个测试：一个用
      故意写坏的 fixture 证明它会响（生产文件、测试文件各报一次，合法的
      `llmpig` 与 `pkg/llm/wire.go` 不报），一个对着真实仓库跑，证明规则
      描述的目录还在——后者是防"规则指向一个不存在的目录，于是永远通过"。
    - **arch-lint** 新增 `shared_llmpig` 组件，并且**删掉了 `shared_pkg`
      的 `mayDependOn` 里那条 `oxpig_model` 例外**。例外存在的原因是
      "PiG 支撑的 llm.Client 必须落在控制面"；现在这个实现有了自己的名字，
      底座重新变回一个不认识 agent 内核的底座。`cmd` 加了
      `shared_llmpig`——装配层第一次同时看到底座和它的实现，因为
      `NewRegistry(NewSettingsSource(...))` 这个组合本来就只发生在一处。

    ### 第二半：`web_search` 的六个包级 setter

    `core/floor/skill/builtin` 里有 14 个内置技能，**13 个是 `init()` 里注册
    的零值结构体**，只有 `WebSearch` 是一个可变的包级单例，六个 setter
    （`SetWebSearchConfigResolver` / `SetWebSearchHTTPClient` /
    `SetWebSearchTavilyEndpoint` / `SetWebSearchBraveEndpoint` /
    `SetWebSearchKeyResolver` / `SetWebSearchEndpoint`）在注册**之后**改它。
    代价不是"不优雅"，是具体的：这个技能的配置是**进程在某一次调用时的
    状态**而不是对象的属性，所以两个调用方不可能持有不同配置；测试必须先
    `resetWebSearch()` 才能跑，因此整个包**不能并行**；半配置状态在
    `Execute` 里被 `RLock` 快照出来是可以发生的。

    改法是一个不可变执行器加一个显式构造点：

    - `WebSearchDeps{Resolver, HTTPClient, TavilyEndpoint, BraveEndpoint}` +
      `NewWebSearch(deps)`，字段构造后只读，`Execute` 里的 `RLock` 快照
      整段删掉。
    - `init()` 仍然注册一个**默认实例**，所以没接线的进程照样有
      `web_search`（SearXNG 默认地址、无 key）——行为不变，只是"没接线"
      从一个运行时事故变成了一个显式状态。
    - 装配层改成 `skillcore.Replace(builtin.NewWebSearch(...))`。
      `skill.Registry` 因此多了一个 `Replace`：**换**已有 key 的实例，
      而 `Register` 仍然对重复 key 崩溃。`Replace` 对未注册的 key 也会崩，
      所以装配层写错一个 key 会在启动时炸，而不是安静地什么都没做。
      新增 `registry_test.go` 六个用例：换实例、目录不增长、三种该崩的
      情况、以及 `Register` 的重复 key 保护**没有被削弱**。
    - 16 个用例全部改成自建实例，`resetWebSearch` 消失。**测试现在可以
      并行**，因为它们之间不再共享可变状态。

    ### 实测

    `core/manager` 222 包 + `core/floor` 9 包 + 根模块 18 包全绿；
    `core/floor/skill/...` 与 `core/manager/llmpig` 的 `-race` 全绿；
    `modulecheck`、`go-arch-lint`、`gofmt` 全部干净。

    **没做的**：`core/floor/skill` 的**目录**仍然是全局的
    （`globalRegistry`），这是全仓 14 个内置技能共用的设计，不是这次的问题。
    `Replace` 是这个设计下唯一需要的例外，已经被限制成"同一个 key 换一次"。
    真正要换成 per-instance 目录，是一次牵动所有技能与 HTTP 路由的改动。

---

## 七、未来路线图

### 下一步（A 收口之后，按优先级）

A 阶段已在决策 63/66 收口，B 阶段在决策 64/65 收口（契约套件 + 固定 tag）。剩下
的按优先级排是三件事，第一件是**欠账**，后两件是**新能力**：

1. ~~**共享底座的两条反向边**~~ ✅ 已完成（决策 66）：PiG 适配搬进
   `core/manager/llmpig`、`web_search` 的六个包级 setter 换成不可变构造 +
   `skill.Replace`，并由 `modulecheck` 的 `floorIsolation` 与 arch-lint 的
   `shared_llmpig` 组件钉住，不再靠人记。
2. ~~**arch-lint 债务清单的守卫**~~ ✅ 已完成，但**问题定位与当初写的不一样**
   （决策 77）。当初以为缺的是「债务清单的时效性守卫」，实际上决策 74 已经
   给了 `TestTheLayerInversionLedgerIsCurrent`，清单是活的。真正缺的是**对称性**：
   读者问「每条授权有没有人在用」，没人问「每条实际依赖有没有被授权」。详见
   §4.15。
3. ~~**MCP 运行时**~~ **此前记为缺口，决策 85 更正：它一直存在。**
   `mcpclient` + `biz/mcp` + `tools.MCPTool` + 启动期发现是完整的一整套，
   65 个工具的 extension toolset 是**并行的另一条**路而非替代品。真正缺的
   是**第三条路**：PiG 原生的那条（`core/pig/pigmcp`，本轮新增），因为
   `basetool.BaseTool` 形状在控制面内核换成 `coding.Session` 之后接不上。
   详见 §4.23。
4. **插件市场与节点页面的前端**（E 阶段唯一纯前端工作）——**一半完成**。
   - ✅ **节点 Agent 页**（决策 81）：`web/src/api/nodeAgents.ts` +
     `pages/NodeAgents.tsx` + 路由 `/node-agents` + 侧边栏入口，9 个用例，
     7 条变异验证抓到。顺带修掉 `client.ts` 读不懂嵌套错误信封（两个 2.0
     handler 的错误码此前在全站丢失，`Tasks` / `marketplace` 的分支都受影响）。
   - ✅ **插件市场页**（决策 82）：`web/src/pages/PluginMarketplace.tsx`
     （路由 `/plugins`）。补上两个此前**从未被前端调用**的后端入口：
     `POST /v1/marketplace/import` 与
     `GET /v1/plugins/{name}/compatibility`（PiG 版本 × edge 版本矩阵）。
     7 个用例，10 条变异全部被抓。顺带统一了 `pluginimport.Report` 的
     JSON 字段命名（此前与内嵌的 `LoadWarning` 两种约定并存）。
   - ✅ **节点上「实际装了什么」的清单面**（决策 83）：新增
     `GET /v1/plugins/nodes/{edgeID}/installed` + 页面第三张卡片。管道本来
     就是通的（`NodeFleet.Installed` 已实现且有测试，节点侧
     `MethodPluginList` 处理器完整），缺的只是 HTTP 面与调用方。顺带把
     `NodeFleet` 里四处重复的「没有隧道」字符串收成 `ErrNoTunnel` 哨兵，
     让「控制面够不着」与「这台不应答」在 HTTP 层分得开（503 vs 502）。
   - ℹ️ 存量技能市场是另一回事：`/settings/marketplace` 已于 2026-05-19
     退役并折进 `/skills?tab=install`，它服务的是 skill pack，不是 PiG
     package，不计入上面这条。

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
    （诊断侧的对应登记见 §4.25.4：`redis.hot_keys` 与
    `kafka.rebalance_history` 两条已作为未裁决项写进
    `pluginmanifest.DiagnosisGaps`，`host.top_cpu_procs` 属于下面 D 的改名类）
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

22. ✅ **导入器的覆盖面收口了，包括一个不能复制的东西（决策 88，§4.26）**——
    上一版 `pluginimport` 手写了 7 个资源类，而 PiG 声明 8 个：`themes` 与
    `agent-environments` 被**静默丢弃**，报告读起来和「本来就没带」一样。
    现在名单是 `core/domain.PackageResources`（住 `core`，因为权威在 PiG 而
    唯一能 import PiG 的模块是 `core/pig`，两边都要读），由
    `core/pig/pigcontract` 对着 PiG 的 `Kind` 常量逐类核对，`pluginimport`
    从它展开。源 `package.json` 改为**读而不复制**——PiG 的 `pi` / `pig` 块
    会抑制约定发现，复制它会把这层抑制一起带到节点上，让一个「声明 1 个、
    目录里 9 个」的容器在转换后仍然只服务 1 个；不复制则按约定发现，得到的是
    可见的超集，并作为一条 Decision 交给 reviewer。撞名目录（`commands` 与
    `prompts`）从静默挑一个改成 `resource_directory_collides` 警告。

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
  当前真实结果**分两条轴**（决策 87 / §4.25）：**诊断 16/20，补救 0/20，
  联合 0/20**。四个只读包（`opskeeper-sre-readonly`
  的 host/alert/topology、`opskeeper-sre-observability` 的
  database/source/observability、`opskeeper-sre-middleware` 的
  pg/redis/k8s/kafka/rabbitmq/mq/git-artifact、`opskeeper-sre-repair`
  的 recovery）盖住了 20 个 case 里 16 个的**诊断半边**；补救半边一个都没盖住，
  因为 20 个 case 每个都同时命名诊断与补救，而节点包按设计只出 L0/L1。
  缺口逐条给出方法名，联合 0/20 由
  `TestNoShippedCaseIsReportedAsCovered...` 钉住（决策 69 / 80）。
  **诊断轴是真正的构建闸门**：`make eval-coverage` 跑
  `--fail-on-unrecorded-diagnose-gap`，只对**未登记**的诊断缺口失败；剩下的
  4 个缺口逐条登记在 `pluginmanifest.DiagnosisGaps` 并附理由，登记表两个
  方向都有守卫（新增缺口红、过期条目也红）。拆轴当天它就查出一个真实缺陷
  ——`k8s.describe_pod` 被误划 L2 而进不了只读包（§4.25.3）。
  两个口径要分清：这个闸门量的是**插件包能提供什么**（按方法名精确匹配），
  补救动作**经审批路径能否派发**是另一条轴——`opskeeper-eval vocabulary` 的
  loop-action 可执行性，以及 `RegistryInvoker` 在 approved phase 上的实际接线
  （决策 80 查实：八个家族全部接线，写工具唯一调用点是 approved phase）。
  家族声明之外还剩
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

- ~~**控制面的 turn 要不要从 `pigagent.Kernel` 换到 `pigcoding.Session`**~~
  **决策 75 曾判为「维持 `Kernel`」（选项 C）；决策 84 推翻了它，理由见
  §4.22。** 两笔账在 HEAD 上都仍然成立，但**推论都不成立**：上限不需要
  `MaxTurns` 字段（`ToolCallHookResult.Terminate` 就在策略闸门那个面板上），
  行 id 时序不需要注入 `SessionManager`（`TurnEndEvent` 每轮一次、顺序由
  事件流保证）。同时查实**调用方钩子是被追加而非替换**，策略闸门在 SDK
  路径上原样存活——这是整件事成立的前提。**换的方向已定（用户明确要求
  「彻底改成 pig 风格」），内核本身尚未换**，剩下的三步列在 §4.22 第六节。
  以下是决策 75 的原始记录，保留作为该结论为何曾经合理的证据： 决策 72 把它挂起来是因为两个缺口，
  这一轮把其中一个的理由更正了、另一个坐实了：
  - **账二的理由错了**。`agent.AgentOptions.OnMessagePersist` 是个**字段**
    （`agent/agent.go:650`），不是构造后的 setter——`pigagent.Kernel` 正在用它分配
    行 id 并回填 SSE 帧（`core/pig/pigagent/kernel.go:181`）。真正的缺口只是
    `coding.SessionStartOptions` 没有这个字段，而 `coding.NewSession` 用自己的
    持久化占住了接线点。**能力在，面板没开。**
  - **选项 A 不可实现**。`type SessionManager = icodingagent.Session`
    （`coding/session_manager.go:12`）是**类型别名到具体结构体**，不是接口，我们
    注入不进去。所以「行 id 改由 entry id 承载」除非 fork PiG，否则没有路。
  - **选项 B 因此没有动机**。`OnMessagePersist` 可用，行 id 时序不必退化成
    「turn 结束后反推」。
  - **账一随之作废**。`MaxTurns` 确实不在 `SessionStartOptions` 里，但 Kernel
    一直带着它，`defaultMaxIterations = 30` 与 persona 上限都在。
  换句话说：换过去要同时丢掉 turn 上限和行 id 时序，而这两样都是 B 阶段闸门
  「SSE 帧 golden 逐帧一致」要验的东西。`pigcoding` 留在设置与模型目录
  （今天 `llmpig` 就是这么用的）。详见 §4.13。
- **`pig-cmd` 是集成还是 fork**（决策 72）。`piglets/company` 已经是计划 §二
  那张图的可运行实现，且作为 piglet **不 import 任何 PiG Go 包**——正好是
  OpsKeeper 评估第三方时最看重的那条性质。默认选择是**当外部进程集成**：与节点
  agent 同级，走已有 tunnel / RPC 面，OpsKeeper 只做运维语义（profile、审批、
  审计）那部分宿主职责。fork 的唯一理由是它的 roster 与预算语义要变成 OpsKeeper
  的 profile 与爆炸半径——在那之前是过早优化。

- ~~**机械式模块迁移只剩最后一块**~~ **已完成（决策 63）**：`internal/manager`
  + `internal/iam` → `core/manager`，`internal/` 已删除，13 个模块目录
  303 包全绿。`harness` 在 `core/harness`（决策 37），节点面在 `core/edge`
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

- **PiG 0.x 双重不稳定**：缓解是三件事叠在一起——`core/pig` 模块级收口（一个
  PiG 变更只落在这一个模块）、`core/pig/pigcontract` 契约套件（形状由
  `contract.go` 在编译期钉住，语义由 `contract_test.go` 在测试期钉住，上游
  改名或改值都会在这里红，而不是变成一个空帧）、以及**固定 tag 而不是本地
  checkout**（决策 65：6 个 go.mod 只 `require v0.3.0`，`make
  module-standalone-check` 在 `GOWORK=off` 下逐模块 build + test，所以"这次构建
  用的到底是哪份 PiG"永远有一个可执行的答案）。
- **插件安全是最大风险面**（高权限 bash + 第三方扩展）：缓解靠宿主强制三件套——
  能力注入式凭据、tool 白名单、宿主侧审计与审批。插件 **never** 获得审批放行权。
- **NodeFleet 连接规模**：每 edge 常驻 RPC 流，需要连接池上限、心跳重连、风暴抑制
  （沿用现有 tunnel 机制）。
- **RPC 延迟**：诊断类工具多一跳 stdio，需对交互式会话做流式背压
  （PiG 的 `EventCh` + `EventDone` 已支持背压丢弃）。

**假设**

- PiG 继续跟踪 Pi 0.87.1；插件契约以 Pi 为准，PiG 特有能力（fused/cellpack）
  只用于官方插件。
- **PiG 的 `mcp` 包类型当前只是声明，不是运行时**；OpsKeeper 侧的 MCP 运行时
  是自建的（`mcpclient` + `biz/mcp` + `tools.MCPTool`，决策 85）。命名规则由
  `core/ports.ComposeMCPToolName` 单点定义并被 `core/pig/pigmcp` 共用，
  两侧名字不一致过一次，那是本文件里唯一一个已被测试钉死的教训。
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
