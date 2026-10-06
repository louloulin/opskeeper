# Harness 评测平台：CLI 面（不是 REST）

> **范围**：golden case 执行、fault 注入、judge 评分、三诊断轴、leaderboard 回归、能力词表。
> **真实形态**：`cmd/opskeeper-eval` 一个二进制 + `core/harness` 九个库包。**本平台没有 HTTP 服务。**
> **本文描述的是代码当前实际行为**，含「未交付」一节（文末）。曾经有一份 REST 文档描述 13 个端点，**那 13 个端点从未存在**（见文末）。
> **形状守卫**：`make apidoc-check`（`scripts/apidoc`）保证本文不会声称一个不存在的端点或子命令；本文的每个子命令都在 `cmd/opskeeper-eval/main.go` 的 `case` 里被逐一核对。

---

## 一、为什么是 CLI

评测平台跑的是**注入真实故障、驱动真实 agent、再打分**这件事。它的调用者是 CI 与人，
不是浏览器。它需要的是可复现的命令行、机器可读的 JSON 输出、以及能在没有 HTTP 客户端的
地方跑（节点侧、离线机器）。**曾经按 REST 写的那份文档描述的是一个不存在的服务**——
`docs/api/` 下曾经有十三行 `GET /api/v1/harness/...`，仓库里没有任何一处注册它们。

---

## 二、二进制

```bash
go run ./cmd/opskeeper-eval <subcommand> [flags]
opskeeper-eval --version
opskeeper-eval <subcommand> --help     # 每个子命令的 flag 列表
```

退出码：`0` 成功；`1` 命令返回错误（错误信息在 stderr）；`2` 未知子命令。

---

## 三、子命令

### 3.1 `run` — 执行 case 或 suite

| flag | 作用 |
|---|---|
| `-case` | 单个 case id，例如 `pg/long-running-tx` |
| `-suite` | suite 名 |
| `-env` | 执行环境（默认 staging） |
| `-judge-model` | 评分模型 |
| `-concurrency` | 并发数 |
| `-output` / `-report-dir` | 输出文件 / 报告目录 |

```bash
opskeeper-eval run --case pg/long-running-tx --env staging
opskeeper-eval run --suite middleware-baseline --concurrency 4
```

### 3.2 `inject` — 手动触发故障注入

| flag | 作用 |
|---|---|
| `--case` | case id |
| `--cases-dir` | golden case 目录（默认 `core/harness/cases`） |
| `--target` | 目标（`ns=test deploy=order-svc` 形式，空格分隔的 `key=value`） |
| `--env` | 目标环境，默认 `staging` |
| `--confirm-prod` | 允许对 `prod` 注入。**没有它就注入不了生产**，这是一个必须显式写出来的开关 |
| `--dry-run` | 读真实的 case、走真实的路由，列出这个 case 会注入什么；不注入 |

```bash
# 列出 k8s/pod-oom 会注入什么（这一步现在真的能跑通）
opskeeper-eval inject --case k8s/pod-oom --dry-run

# 真注入
opskeeper-eval inject --case k8s/pod-oom --target ns=test deploy=order-svc
```

**未交付：六个注入器全部是骨架。** `core/harness/injector` 下的
pg / redis / host / k8s / rabbitmq / kafka 都不碰任何真实系统——没有 pgx 连接、
没有 redis 客户端、没有 kubectl、没有 stress-ng。它们通过
`CheckAvailable` 自报不可用并说明缺什么，`inject` 因此以非零退出，
把每一步没执行的原因逐条打出来。**不会打印任何"注入成功"。**

一个认不出的类型报 `ErrUnsupportedType` 而不是"不可用"：那是接线问题，
与当前环境无关，报成不可用会把人引去查环境。

### 3.3 `judge` — 对已有响应重跑评分

| flag | 作用 |
|---|---|
| `-case` / `-cases-dir` | 单个 case / 语料目录 |
| `-response` | `judge.AgentResponse` 的 JSON（用 `project` 生成） |
| `-judge` | `heuristic` 或 `llm` |
| `-provider` / `-model` | LLM judge 的 provider 与模型 |
| `-out` | 输出 |
| `-plugins-dir` | 插件包目录（能力可服务性检查用） |
| `-allow-unservable` | 允许对本构建无法服务的 case 打分 |

```bash
opskeeper-eval judge --case pg/long-running-tx --response agent-response.json
opskeeper-eval judge --case pg/long-running-tx --response agent-response.json \
    --judge llm --provider anthropic
```

**`allow-unservable` 是一道诚实的开关**：一个 case 需要的工具本构建没有时，它的分数不是
agent 的成绩。不给这个开关就直接判不合格，等于把平台的失败算成 agent 的失败。

### 3.4 `run-loop` — loop 模式闭环

| flag | 作用 |
|---|---|
| `-case` / `-cases-dir` / `-env` | 同 `run` |
| `-execution-mode` | `dry-run` 或 `real-agentteams` |
| `-incident-id` / `-trace-id` | 真实模式的标识 |
| `-hitl-evidence` / `-mcp-evidence` / `-fixture-before-evidence` / `-fixture-after-evidence` | 六类证据文件 |
| `-judge` / `-judge-model` / `-judge-provider` | 评分路径 |

```bash
opskeeper-eval run-loop --case host/cpu-spike --execution-mode=real-agentteams \
  --incident-id host-cpu-spike-real --trace-id <32 hex> \
  --postmortem-evidence pm.json --judge llm
```

### 3.5 `leaderboard` — 排行榜与回归基线

| flag | 作用 |
|---|---|
| `--dir` | LoopResult JSON 目录（默认 `harness/result/loop`） |
| `--out-dir` | Markdown 报告输出目录 |
| `--threshold` | `recovery_pass_rate` 门槛，低于则 NOT QUALIFIED |
| `--baseline-file` | 回归基线文件（默认 `harness/result/baseline.json`） |
| `--lock-baseline` | 把当前分数写成本次基线 |
| `--baselines` | 打印基线表 |
| `--check-regression` | 对照基线检查回归；有 block 时非零退出 |
| `--fail-on-warn` | `--check-regression` 下 warn 也非零退出 |

### 3.6 `list-cases` — 列出语料

| flag | 作用 |
|---|---|
| `-cases-dir` | 语料目录（默认 `core/harness/cases`） |
| `-filter` | 按 id 片段过滤 |

```bash
opskeeper-eval list-cases --filter pg
```

### 3.7 `plugin-coverage` — case 能力期望 vs 插件包能力

| flag | 作用 |
|---|---|
| `-cases-dir` / `-plugins-dir` / `-filter` | 输入 |
| `-json` | 机器可读输出 |
| `-fail-on-gap` | 有缺口即非零退出 |
| `-fail-on-unrecorded-diagnose-gap` | 未登记的诊断缺口也非零退出 |

### 3.8 `vocabulary` — case 能力期望 vs 本构建真实词表

| flag | 作用 |
|---|---|
| `-cases-dir` / `-plugins-dir` / `-filter` / `-kind-map` | 输入 |
| `-json` / `-fail-on-gap` | 输出与门控 |

```bash
opskeeper-eval vocabulary
```

### 3.9 `project` — 把生产的 RootCauseJSON 投影成评分响应

| flag | 作用 |
|---|---|
| `-contract` | 生产的 RootCauseJSON 契约文件 |
| `-kind-map` | 故障类型映射 |
| `-out` | 输出 `judge.AgentResponse` |
| `-bare` | 只输出响应，不做打分 |
| `-allow-unmapped-root-cause` | 允许契约里出现映射表没有的根因类型 |
| `-detected-at` / `-investigated-at` / `-recovered-at` | 三个时间戳 |

```bash
opskeeper-eval project --contract rc.json --kind-map kinds.json --out resp.json
```

### 3.10 `axes` — 三个诊断轴的声明面

| flag | 作用 |
|---|---|
| `-cases-dir` / `-filter` | 输入 |
| `-json` | 机器可读输出 |
| `-fail-on-unmeasured-axis` | 有 case 三轴中任一无法测量即非零退出 |

```bash
opskeeper-eval axes --fail-on-unmeasured-axis
```

派生规则在 `core/harness/axes`（一份实现，两条评分路径共用），打分在
`core/harness/judge`，存储与对比在 `core/harness/leaderboard`。

---

## 四、CI 里的三道闸门

```bash
make eval-coverage      # plugin-coverage：哪些 case 没有插件能服务
make eval-vocabulary    # vocabulary：哪些 case 连结构上都无法满足
make eval-axes          # axes：哪些 case 没声明三轴
```

三者都是 `eval-gates` 的组成部分，且都在 CI 每次 push 跑到。**它们的输出不是分数，是缺口**：
一个 case 在这里红了，说明平台还不能公平地评它，而不是 agent 答错了。

---

## 五、未交付

以下内容在旧版本文里出现过，**代码从未实现**：

| 项 | 状态 | 缺什么 |
|---|---|---|
| `GET /api/v1/harness/cases`（列 case） | ❌ 从未实现 | 无此端点；用 `list-cases` |
| `POST /api/v1/harness/cases/validate` | ❌ 从未实现 | 无此端点；`schema.NewLoader` 在进程内校验 |
| `GET/POST /api/v1/harness/runs`、`/runs/{run_id}` | ❌ 从未实现 | 无运行记录服务；结果落文件与 leaderboard |
| `POST /api/v1/harness/inject`、`/inject/{inject_id}/stop` | ❌ 从未实现 | 注入是 `inject` 子命令 |
| `GET /api/v1/harness/leaderboard`、`POST .../lock`、`POST .../check-regression` | ❌ 从未实现 | 排行榜是 `leaderboard` 子命令 |
| `GET /api/v1/harness/judge/models`、`POST .../judge`、`GET .../judge/consistency` | ❌ 从未实现 | 评分是 `judge` 子命令 |
| 响应信封 `{code, message, data}` | ❌ 不适用 | CLI 输出是文本或 `--json` 的结构 |

**「双模型 judge 一致性」（judge/consistency）是一个真想法，但不是已交付的端点**：现在能做的
是同一响应分别用 `heuristic` 与 `llm` 打两次并人工比对，命令层面没有内建的一致性判定。

---

## 六、相关

- CLI：`cmd/opskeeper-eval/`
- 库：`core/harness/{schema,injector,judge,axes,leaderboard,projection,vocabulary,runner}`
- 闸门：`make eval-gates`
- 形状守卫：`scripts/apidoc`- 闸门：`make apidoc-check` —— 本文件里每条 `METHOD /path` 必须**被源码里真实的路由注册服务**（决策 269 起要求「注册过」，而不只是「树里出现过这个字符串」）
