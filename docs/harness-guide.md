# Harness 评测指南

> **面向**：opskeeper 开发者、平台 SRE、CI 维护者
> **目的**：用 golden case 评测 Agent 决策质量，用 judge 模型打分，用 leaderboard 跟踪回归
> **关联**：
> - ADR：[docs/superpowers/decisions/2026-07-13-harness-judge-models.md](superpowers/decisions/2026-07-13-harness-judge-models.md)
> - 集成指南：[docs/integration-guide.md](integration-guide.md)
> - 运维手册：[docs/operations-manual.md](operations-manual.md)

---

## 一、概念

| 概念 | 说明 |
|---|---|
| **golden case** | 标准化的事故场景（YAML 描述），含注入 / 期望 / rubric |
| **fault-injector** | 在隔离环境注入故障（PG 长事务 / Redis 大 key / K8s pod OOM / 主机磁盘满）|
| **judge** | LLM 评分模型（默认 Claude Sonnet 4 + GPT-4o 双模型取均值）|
| **leaderboard** | 评分历史 + 回归基线 + 排名 |
| **regression baseline** | 历史评分基线，新评分对比基线判断是否下降 |

## 二、CLI 速查（`cmd/opskeeper-eval`）

```bash
opskeeper-eval --help
opskeeper-eval list-cases                           # 列出全部 golden case
opskeeper-eval list-cases --severity P0             # 按严重度
opskeeper-eval run --case pg/long-running-tx        # 跑单个 case
opskeeper-eval run --suite middleware-baseline      # 跑一组
opskeeper-eval run --suite full --env staging       # 全量回归
opskeeper-eval inject --case k8s/pod-oom --env staging  # 仅注入不评分
opskeeper-eval judge --case pg/long-running-tx --response agent-response.json  # 外部 judge
opskeeper-eval leaderboard                          # 查看排行榜
opskeeper-eval leaderboard --since 30d              # 近 30 天
opskeeper-eval plugin-coverage                      # golden case 的能力期望 vs 插件包能力
opskeeper-eval plugin-coverage --fail-on-gap         # CI：有结构性缺口即非零退出
opskeeper-eval plugin-coverage --filter host/ --json # 只看主机类，机器可读
opskeeper-eval vocabulary                           # golden case 的能力期望 vs 本构建全部能力注册表
opskeeper-eval vocabulary --fail-on-gap              # CI：有结构上无法满足的 case 即非零退出
opskeeper-eval vocabulary --json                    # 机器可读
opskeeper-eval vocabulary --kind-map kinds.json     # 校验 kind 映射文件是否陈旧

# 闭环最后一环：生产契约 → judge 可评分的响应
opskeeper-eval project --contract rc.json --kind-map kinds.json --out resp.json
opskeeper-eval project --contract rc.json --kind-map kinds.json --bare --out resp.json
opskeeper-eval judge --case pg/lock-waits --response resp.json   # 直接评分
```

> `judge` 会先做能力检查：本构建无法产出的 case **直接拒绝打分**，并指名缺哪个
> 符号。确实要打分（例如为了看部分得分曲线）时加 `--allow-unservable`，结果
> 里的 `unservable_case` 字段会一直跟着这个分数。

---

## 二.5、能力覆盖闸门（plugin-coverage）

**问题**：golden case 的期望写成 `<family>.<method>`（`pg.lock_waits`、
`host.host_load`），而舰队的能力来自插件包。两套词汇之前没有人做过连接，
于是**一个结构上不可能通过的 case 会永远打 0 分**，而 leaderboard 把它
显示成"Agent 不行"——这是个会让所有人查错方向的假象。

**加入**：`internal/pkg/pluginmanifest/coverage.go` 建立连接表，
`opskeeper-eval plugin-coverage` 打印结果，`--fail-on-gap` 让 CI 卡住。

**关键性质**：

- **映射是声明的，不是猜的**。`toolCapabilities` 的每一条都是从注册该工具的
  extension 里读出来的，不是从工具名前缀推出来的——按前缀推会把
  `list_database_sources` 猜对、`query_change_events` 猜错。
- **每个缺口都要有解释**，而不是只报一个数字。归到两个命名清单之一：
  - `MiddlewareFamilies`（`pg` / `redis` / `k8s` / `mq` / `kafka` / `rabbitmq`）
    —— 控制面 adapter，**不是**插件包。清单的词汇来自 adapter 实现
    （`internal/middleware/adapter/<pkg>/<pkg>.go` 注册 `"<pkg>.method"`），
    不是 case 所在目录名：mq 目录下的 case 写的是 `kafka.*` / `rabbitmq.*`，
    按目录名建表会把它们全报成"包不存在"。
  - `NonPackageFamilies`（`git-artifact`）—— 控制面关联器，根本不是工具族。
- **两个方向的漂移都是红色**：
  - 己方包有工具没有能力族 → `TestEveryShippedToolHasACapabilityFamily`
    （否则该工具对应的 case 会静默掉分）。
  - 表里有条目没有对应工具 → `TestTheCapabilityTableHasNoDeadEntries`
    （改名后遗留的死键在 review 时是误导）。
  - case 里出现既没被打包、也没被任何清单解释的族 →
    `TestEveryCaseFamilyIsEitherPackagedOrNamedAsADeliberateGap`。

**当前真实结果：2/20 全绿。** host 两例由只读包覆盖；其余 18 例的缺口
**全部**落在控制面 adapter 上。这是仓库的真实状态，不是回归——把它打印
出来正是这个闸门存在的意义。

---

## 二.6、结构可满足性闸门（vocabulary）

**问题**：`plugin-coverage` 回答的是"**插件包**能不能服务这个期望"。在那之前
还有一层更靠前的问题——"**这个系统**能不能产出这个期望"。两套词表写在不同
地方，从没有人做过连接，于是**一个任何注册表都没有对应工具的 case 会永远打
0 分**，而 leaderboard 把它显示成"Agent 不行"。

**加入**：`core/harness/vocabulary`（纯计算，不依赖实现）+ `opskeeper-eval
vocabulary`。四个能力来源按"谁拥有这个能力"的顺序被咨询：

| 来源 | 覆盖形态 | 读法 |
|---|---|---|
| `middleware-adapter` | **精确符号** | 真实调用 6 个 adapter 的 `RegisterTools` 进一个空 registry，再 `ListTools("")`。**不扫源码** |
| `plugin:<name>` | **能力族** | `p.Capabilities()`。包声明的是"我覆盖 host 这个族"，不是逐个方法名 |
| `loop-investigator` | **精确符号** | `investigatorreal.RemediationActions`（闭环修复规划器实际会提出的动作） |
| `loop-root-cause-kinds` | 单独报告 | 从 `investigatedOutputSchema` **派生**的 enum，不并入并集 |

**关键性质**：

- **精确匹配优先于族匹配**。`plugin-coverage` 已经论证过按族连接是刻意的
  （逐方法名匹配会在包改名的瞬间产生假阴性）；这里再进一步——全构建范围内
  只要有任何一个注册表按字面名注册了该符号，就报那个注册表，因为它才是能被
  精确指过去的子系统。
- **adapter 工具表是"跑出来的"，不是"扫出来的"**。抓字符串字面量是第二个
  解析器，它对什么算工具名有自己的看法，并且会静默漏掉任何它没有建模的注册
  路径——而那正是能力闸门要防的失效。
- **loop 的动作表有 AST 漂移测试**（`TestTheDeclaredVocabularyMatchesTheCode`），
  双向比对：代码能产出但没声明 → 红；声明了但没代码路径能产出 → 红。
- **根因 enum 是派生的**，直接解析模型实际被约束的那份 JSON schema，所以
  不存在漂移可能；restate 一份就会变成"带测试形状的注释"。

**`judge` 的拒绝**。可满足性不达标时 `opskeeper-eval judge` **直接拒绝打分**，
错误信息指名缺哪个符号。理由和 plugin-coverage 一样：一个 0 分在产物里会被读
成对 agent 的判决，而它其实是对语料库/能力表的判决。`--allow-unservable`
可以强行打分，代价是产物里永久带上 `unservable_case` 字段。

**当前真实结果：9/20 可满足。** 缺口具体且小——14 个符号：

- 根因 3 个：`k8s.top_pods`、`pg.replication_status`、`git-artifact.LinkK8sImage`
- 修复 11 个：`k8s.uncordon`、`k8s.drain`、`k8s.resize_pvc`、`k8s.cleanup_logs`、
  `kafka.restart_broker`、`kafka.scale_consumer`、`kafka.repartition`、
  `rabbitmq.scale_consumer`、`redis.kill_client`、`redis.scan_and_delete`、
  `redis.scan_and_redistribute`

注意 `redis.kill_client`（语料）vs `redis.client_kill`（adapter 注册）：**这是
改名未对齐，不是能力缺失**——两种读法差一个字，而闸门按精确匹配处理，所以它
是缺口。这条差异要由人来裁决，代码不应该替人猜。

**当前真实结果：平台 9/20，闭环 0/20。** 第二个数字才是决定 judge 会怎么做的
那个——见下一节。

> **一个被抓住的错误**：这个闸门的第一版只拿 corpus 去比
> `investigatorreal.RemediationActions`，报出 **0/20**。那个数字是错的——
> 语料的 57 个符号里有 36 个由真实 middleware adapter 注册。拿单个子系统去
> 比整张能力表，得到的是"全都不满足"，而一个"全都不满足"的闸门和一个正常
> 工作的闸门**长得一模一样**：都打印数字、都非零退出。因此
> `TestACaseWhoseSymbolsTheAdaptersRegisterIsReportedServable` 把方向钉死。

---

## 三、Golden Case 编写规范

### 3.1 目录结构

```
core/harness/cases/<resource>/<case-name>/case.yaml
```

例：

```
core/harness/cases/host/disk-full/case.yaml
core/harness/cases/pg/long-running-tx/case.yaml
core/harness/cases/redis/big-key/case.yaml
```

### 3.2 YAML 字段规范

```yaml
id: pg/long-running-tx                   # 必填，格式 <resource>/<case-name>
description: 模拟 PG 长事务阻塞          # 必填，一句话
severity: P0                             # 必填，P0/P1/P2/P3
tags: [pg, lock, performance]            # 可选，用于筛选

prerequisites:                           # 可选，前置条件列表
  - pg.test_db_seeded
  - pg.test_user_with_privilege

inject:                                  # 必填，注入步骤
  - type: pg.open_long_tx                # 注入器类型
    duration: 300s                       # 持续时间
    params:                              # 注入参数
      sql: "BEGIN; SELECT pg_sleep(60);"
      sessions: 10

expect:                                  # 必填，期望行为
  time_to_detect: 30                     # 秒，期望 Agent 多快发现
  time_to_remediate: 120                 # 秒，期望 Agent 多快修复
  root_cause_lines:                      # 期望根因工具
    - pg.pg_stat_activity
    - pg.pg_locks
    - pg.top_queries
  remediation_options:                   # 期望修复方案（多选一即可）
    - pg.kill_session
    - pg.cancel_query

rubric:                                  # 必填，评分维度
  rca_accuracy: 0.85                     # 根因准确度阈值
  time_to_remediate: 120                 # 修复时长阈值
  no_collateral_damage: true             # 是否要求零副作用

metadata:                                # 可选，元信息
  owner: "@opskeeper-oncall"
  created_at: "2026-07-13"
  references:
    - "https://wiki.opskeeper.io/runbook/pg-long-tx"
```

### 3.3 完整示例：PG 长事务 case

```yaml
id: pg/long-running-tx
description: 模拟 PG 长事务导致锁等待和性能下降
severity: P0
tags: [pg, lock, performance, long-tx]
prerequisites:
  - pg.test_db_with_orders_table
  - pg.test_user_with_kill_privilege

inject:
  - type: pg.open_long_tx
    duration: 600s
    params:
      sql: "BEGIN; LOCK TABLE orders IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120); COMMIT;"
      tx_id: tx-12345

expect:
  time_to_detect: 30
  time_to_remediate: 120
  root_cause_lines:
    - pg.pg_stat_activity
    - pg.pg_locks
    - pg.top_queries
  remediation_options:
    - pg.kill_session
    - pg.cancel_query

rubric:
  rca_accuracy: 0.85
  time_to_remediate: 120
  no_collateral_damage: true

metadata:
  owner: "@opskeeper-oncall"
  created_at: "2026-07-13"
  references:
    - "https://wiki.postgresql.org/wiki/Lock_Monitoring"
```

### 3.4 JSON Schema 校验

`core/harness/schema/case.schema.json` 是权威 schema。新增 case 自动校验：

```bash
opskeeper-eval validate --case pg/long-running-tx
# → Validation passed
```

校验失败的常见原因：
- 缺 `id` / `description` / `severity` / `inject` / `expect` / `rubric`
- `time_to_detect` > `time_to_remediate`（逻辑错误）
- `inject.type` 未在 fault-injector 注册表中
- `rubric.*` 超出合理范围（`rca_accuracy` 应在 [0, 1]）

---

## 四、故障注入器（fault-injector）

### 4.1 内置注入器

| 类型 | 适用资源 | 关键参数 |
|---|---|---|
| `host.fill_disk` | host | path, target_percent, duration |
| `host.cpu_spike` | host | cpu_percent, duration, processes |
| `pg.open_long_tx` | postgres | sql, duration |
| `pg.lock_table` | postgres | table, lock_mode, duration |
| `pg.create_bloat` | postgres | table, bloat_factor |
| `redis.big_key` | redis | key, size_mb |
| `redis.slow_cmd` | redis | cmd, sleep_ms |
| `mq.backlog` | rabbitmq / kafka | queue/topic, message_count |
| `k8s.pod_oom` | k8s | namespace, deployment, memory_limit |
| `k8s.node_notready` | k8s | node_name, duration |

### 4.2 环境限制

- **staging / dev**：默认允许
- **prod**：必须 `--confirm-prod` + 双人审批
- **注入时间窗**：默认 5 分钟（`--max-duration` 可调，但不超过 10 分钟）

### 4.3 自动清理

注入器在 `duration` 到期后自动回滚（kill session / release lock / delete key）。若回滚失败，强制告警并人工介入。

---

## 五、Judge 模型

### 5.1 默认配置：双模型取均值

| 模型 | 用途 | 备注 |
|---|---|---|
| Claude Sonnet 4 | 主评分 | 中文 / 代码 / 推理强 |
| GPT-4o | 副评分 | 通用推理 / 多语言 |

### 5.2 评分维度

每个 case 由 judge 在以下 5 维度独立打分（0-1）：

1. **rca_accuracy** — 根因工具是否用对
2. **time_to_detect** — 检测时长（vs rubric 阈值）
3. **time_to_remediate** — 修复时长（vs rubric 阈值）
4. **collateral_damage** — 副作用（kill 错 session 等）
5. **rubric_compliance** — 与 case 定义的一致性

### 5.3 一致性校验

两模型评分差异 > 0.2 时标记为 `Flagged`，进入人工 rubric 复评队列。一致率目标：

```
一致率（差异 < 0.1）>= 80%
```

详见 [ADR：judge 模型选型](superpowers/decisions/2026-07-13-harness-judge-models.md)。

### 5.4 缓存 + 增量

- **缓存**：相同 `(case_id, agent_response_hash)` 复用评分结果
- **增量**：PR 修改的 case 跑全量，其余 case 复用上次结果
- **限速**：每分钟最多 60 次 judge 调用

### 5.5 评分降级

若双模型一致率持续 < 70%（持续 4 周），降级到单模型（仅 Claude Sonnet 4）。详见 ADR "回滚条件"。

---

## 六、Leaderboard 与回归基线

### 6.1 Leaderboard 查看

```bash
opskeeper-eval leaderboard
```

输出示例：

```
Harness Leaderboard — 最近 30 天
┌──────────────────────────────┬─────────┬────────┬──────────┐
│ Case                         │ Score   │ Δ vs   │ Status   │
│                              │ (avg)   │ base   │          │
├──────────────────────────────┼─────────┼────────┼──────────┤
│ pg/long-running-tx           │ 0.92    │ +0.02  │ ✅ pass   │
│ redis/big-key                │ 0.88    │ -0.03  │ ⚠ warn  │
│ k8s/pod-oom                  │ 0.85    │ -0.08  │ ❌ fail   │
│ host/disk-full               │ 0.94    │ +0.01  │ ✅ pass   │
└──────────────────────────────┴─────────┴────────┴──────────┘
Overall: 0.90 (baseline 0.91, Δ -0.01)
```

### 6.2 回归基线规则

| 评分下降幅度 | 行为 |
|---|---|
| < 5% | 静默（log only） |
| 5%-15% | **告警**（Slack / 钉钉） |
| > 15% | **CI 阻断**（merge 拒绝） |

### 6.3 基线更新

```bash
# 把当前评分设为新基线（每月一次）
opskeeper-eval leaderboard --lock-baseline

# 查看历史基线
opskeeper-eval leaderboard --baselines
```

---

## 七、CI 集成

### 7.1 GitHub Actions 示例

```yaml
# .github/workflows/harness.yml
name: Harness Eval
on:
  pull_request:
    paths:
      - 'internal/manager/**'
      - 'core/harness/**'
      - 'cmd/opskeeper-eval/**'

jobs:
  eval:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - name: Build opskeeper-eval
        run: go build -o opskeeper-eval ./cmd/opskeeper-eval
      - name: Run harness suite
        env:
          OPSKEEPER_LLM_ANTHROPIC_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          OPSKEEPER_LLM_OPENAI_KEY: ${{ secrets.OPENAI_API_KEY }}
        run: |
          ./opskeeper-eval run --suite pr-baseline --env staging --report eval-report.json
      - name: Check regression
        run: |
          ./opskeeper-eval leaderboard --check-regression --report eval-report.json
      - name: Upload report
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: harness-report
          path: eval-report.json
```

### 7.2 REST API（CI 集成）

```bash
# 异步触发评测
curl -X POST https://ops.example.com/api/v1/harness/runs \
  -H "Authorization: Bearer $JWT" \
  -H "Content-Type: application/json" \
  -d '{
    "suite": "middleware-baseline",
    "env": "staging",
    "webhook_url": "https://ci.example.com/callback"
  }'
# → {"run_id": "hr-abc123"}

# 查询进度
curl https://ops.example.com/api/v1/harness/runs/hr-abc123 \
  -H "Authorization: Bearer $JWT"
```

详见 [docs/api/harness.md](api/harness.md)。

---

## 八、生产保护

### 8.1 Prod 环境注入拦截

```bash
# 拒绝：prod 环境 + 未确认
$ opskeeper-eval inject --case k8s/pod-oom --env prod
ERROR: prod environment requires --confirm-prod and 2-person approval

# 通过：显式确认 + 审批人
$ opskeeper-eval inject --case k8s/pod-oom --env prod --confirm-prod \
    --approver "@alice" --approver "@bob"
✅ approved, injecting in 30s
```

### 8.2 时间窗限制

```bash
# 默认 5 分钟
$ opskeeper-eval inject --case pg/lock-table --env staging
duration=300s

# 调整上限（最大 600s = 10 分钟）
$ opskeeper-eval inject --case pg/lock-table --env staging --max-duration 600
duration=600s

# 超过限制被拒
$ opskeeper-eval inject --case pg/lock-table --env staging --max-duration 1200
ERROR: max-duration cannot exceed 600s
```

### 8.3 审计

所有 inject / run / judge 操作必审计：

```bash
logcli query '{app="opskeeper-eval"} |= "inject"' --since=24h
```

---

## 九、Case 库扩展

### 9.1 贡献流程

1. 在 `core/harness/cases/<resource>/<new-case>/case.yaml` 写新 case
2. `opskeeper-eval validate --case <new-case>` 校验 schema
3. 在 staging 跑一次：`opskeeper-eval run --case <new-case> --env staging`
4. PR review + 合并
5. 纳入下月回归基线

### 9.2 案例库覆盖目标（v1.0）

| 资源 | 当前 | 目标 | 缺口 |
|---|---|---|---|
| PG | 6 | 20 | 14（真空闲事务 / 慢查询 / 复制延迟 / vacuum stuck / 索引膨胀 / autovacuum 失效 / etc.）|
| Redis | 4 | 12 | 8 |
| MQ | 4 | 10 | 6 |
| K8s | 4 | 12 | 8 |
| Host | 2 | 6 | 4 |
| **总计** | **20** | **60** | **40** |

每 case 估时 2-3 天（含开发 + review + 跑测）。

---

## 十、相关文档

- ADR：[docs/superpowers/decisions/2026-07-13-harness-judge-models.md](superpowers/decisions/2026-07-13-harness-judge-models.md)
- Spec：[openspec/specs/harness-eval-platform/spec.md](../openspec/specs/harness-eval-platform/spec.md)
- 集成指南：[docs/integration-guide.md](integration-guide.md)
- 运维手册：[docs/operations-manual.md](operations-manual.md)
- API 文档：[docs/api/harness.md](api/harness.md)


---

## 二.7、闭环投影（project）：让 judge 评的是系统真产出的东西

**问题**：judge 评的是 `judge.AgentResponse`，生产写的是 `RootCauseJSON`，两者之间
**什么都没有**。于是历史上每一个分数都来自手写的响应文件，golden 语料从来没有被
真正考核过——它只被一份人手写的东西考核过。

**三套词表在这里相遇，只有两套能对上**：

| 来源 | 形状 | 能否直接映射 |
|---|---|---|
| `remediation_options[].action` | `pg.terminate_long_tx` | ✅ 与 case 同构 |
| `root_cause_object.kind` | `pg_lock`（闭集 enum） | ❌ 另一套 namespace |
| `evidence_chain[].tool` | `query_promql`（裸名、跨族） | ⚠️ 只能作为 tool call 供 LLM judge 推理，不参与精确匹配 |

**根因那一列是刻意留空的。** 投影包 `core/harness/projection` 接受一个
`Resolver`，由调用方通过 `--kind-map` 提供：

```bash
opskeeper-eval project --contract rc.json \
  --kind-map docs/kind-map.example.json \
  --detected-at 2026-10-01T09:59:19Z \
  --investigated-at 2026-10-01T10:00:00Z \
  --recovered-at 2026-10-01T10:01:28Z \
  --bare --out resp.json
```

- **kind 映射是数据，不是代码分支**。"pg_lock 和 pg.lock_waits 是同一个发现"是一次
  关于**语义**的判断，只有懂这个领域的人能做；写成代码分支会让这个判断永远隐形。
  写成 JSON，它可以被 review、diff、签字。
- **没有映射就拒绝出响应**（除非 `--allow-unmapped-root-cause`）。输出一个
  `root_cause_matched` 为空的响应，judge 会打 0 分并读成"这次诊断什么都没找到"——
  那是一次凭空捏造的判决。
- **`--bare` 输出裸响应**，因为 `judge --response` 已经用严格解码读
  `judge.AgentResponse`；再套一层外壳等于让 judge 为同一个类型学第二套 schema。
- **映射文件会被校验**：`vocabulary --kind-map` 会检查每一条映射指向的符号是否真的
  有人产出。映射是数据，工具改名之后它会静默指向一个不存在的名字，然后每一次
  经过它的运行都在 `remediation_quality` 上打 0 而**任何地方都不报错**。仓库里的
  `docs/kind-map.example.json` 由一条测试守着（`TestTheShippedExampleKindMapHasNoStaleEntries`），
  所以它是被维护的产物而不是会腐烂的文档。

**端到端实测**（真实契约，`pg_lock` → 映射到 `pg/lock-waits` 的期望根因）：

```
$ opskeeper-eval project --contract rc.json --kind-map kinds.json --bare --out resp.json
project: kind=pg_lock root_cause=[pg.lock_waits pg.active_sessions]
         remediations=[pg.terminate_long_tx pg.kill_backend] tool_calls=2 → resp.json

$ opskeeper-eval judge --case pg/lock-waits --response resp.json
  rca_accuracy         1     ← kind 映射生效
  remediation_quality  0     ← 闭环提的是 pg.kill_backend，case 期望 pg.kill_session
  time_efficiency      1     ← 41s/60s、88s/120s
  overall              0.70
```

那个 0 是**这一整轮最有价值的输出**：它把"闭环提不出语料期望的修复动作"这件一直
看不见的事，变成了一个可以指着数字讨论的事实。

**防锈**：`projection.Doc` 是 `loop.RootCauseJSON` 的手写镜像（评测面不得依赖控制面
实现，这是 `scripts/modulecheck` 的模块规则）。镜像会烂——契约加字段，两边都还能编译，
投影悄悄少投一层。所以有两条双向测试：真实契约过线格式进镜像（**严格解码**，未知字段
即失败），镜像写出的东西再读回控制面类型。变异验证：给 `loop.RootCauseObject` 加一个
字段 → `TestTheMirrorStillMatchesTheContract` 精确报错。
