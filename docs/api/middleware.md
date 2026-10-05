# 中间件 Adapter：工具面（不是 REST）

> **范围**：PG / Redis / RabbitMQ / Kafka / K8s / Host / Git 七类适配器向 Agent 暴露的**工具**面，共 101 个工具、8 个族。
> **实现**：`core/manager/middleware/{registry,adapter,toolset}`，各适配器在 `core/manager/middleware/adapter/*`。
> **本文与代码的关系**：本文描述的是**代码当前实际行为**，含「未交付」一节（文末）。
> **形状守卫**：`core/manager/middleware/adapter/docsurface_test.go` 把本文写的族名与工具总数对着**活注册表**校验；`make apidoc-check` 保证本文不会声称一个没人注册的端点。

---

## 一、这里没有 REST API

中间件适配器**不是 HTTP 服务**。它们实现 `adapter.MiddlewareAdapter`，通过
`registry.Registry` 把工具注册进进程内的工具表，再由 `toolset` 转成 Agent 可调用的工具定义，
经 `basetool.BaseTool` 到达 `pig` 内核。**Agent 调用它们的方式是工具调用，不是 HTTP 请求**，
所以本文件没有端点表——曾经有过一份端点表，那是**从未实现的契约**（见文末）。

在 Agent 侧一次真实调用的形状：

```json
{
  "name": "pg.lock_waits",
  "arguments": { "database": "orders" }
}
```

---

## 二、注册与工具表

| 导出 | 作用 |
|---|---|
| `toolset.Registry()` | 用**空实例**注册全部适配器，返回一张工具表（可离线使用：能力检查、工具面生成、文档校验都用它） |
| `toolset.Tools(reg)` | 全部工具 |
| `toolset.ReadTools(reg)` | 只读工具（L0/L1） |
| `toolset.PackagedReadTools(reg)` | 打进插件包的只读工具 |
| `toolset.FamilyNames()` | 族名（工具名的点号前缀） |
| `toolset.ParseFamily(tool)` | 工具名 → 族 |
| `toolset.IsRead(level)` | 某风险等级是否属于只读 |
| `toolset.RequiredArgs(args)` | 从参数表里取必填参数 |

每个工具在 `registry.Tool` 上带四样东西，**四样都是承重的**：

- `Name` — `<族>.<方法>`，例如 `pg.lock_waits`；
- `Description` — 给模型读的说明；
- `RiskLevel` — 风险等级（见 §四）；
- `ParamsSchema` — 给模型看的完整 JSON Schema；`ArgsSchema` 是给调度方看的
  「参数名 → 类型」，类型带 `!` 表示必填。

**两套 schema 都在，是因为它们回答不同的问题**：`ArgsSchema` 回答「哪些参数不能被凭空编出来」
（pid、role、表名），`ParamsSchema` 回答「怎么向模型描述这个工具」。一个工具若两者都写，
必须保持一致——工具面生成器的测试会断言 `ParamsSchema` 是合法 JSON。

---

## 三、族与工具数

| 族 | 工具数 | 覆盖 |
|---|---|---|
| `pg` | 23 | 会话、锁等待、长事务、慢日志、膨胀、复制、索引使用、VACUUM |
| `k8s` | 24 | Pod/节点/PVC 读写、驱逐与排水、日志清理、滚动重启与回滚、扩缩容 |
| `redis` | 19 | 大键/热键、碎片率、内存、慢日志、客户端、配置读写、故障转移 |
| `host` | 11 | 负载、内存、磁盘、进程、服务状态与重启、旧日志清理 |
| `git` | 8 | 仓库清单、代码搜索、diff、blame、提交历史、运行时符号反查 |
| `mq` | 6 | Broker 状态、队列深度、消费者滞后、消息重放、排空 |
| `kafka` | 5 | 主题列表、消费滞后、分区倾斜、再分配 |
| `rabbitmq` | 5 | 集群信息、队列、消费者、清空队列 |
| **合计** | **101** | |

族名与总数由 `docsurface_test.go` 对着 `toolset.Registry()` 校验。**本文不逐条列出 101 个工具**：
逐条抄一遍就是第二份清单，而这份清单会漂——守卫能守住族与总数，守不住一百行散文。

要看当下真实的工具面：

```bash
go run ./cmd/opskeeper-eval vocabulary   # 能力词表，读的是活注册表
```

---

## 四、风险等级

`adapter.RiskLevel` 是适配器实现者给的映射表（`OpRiskLevel` 接口），**双重门控**：cmdpolicy 与
Casbin 各判一次。

| 等级 | 含义 | 门控 |
|---|---|---|
| `L0` | 只读 | 直接执行 |
| `L1` | 诊断读取 | 直接执行 |
| `L2` | 软写（如 `pg.analyze_table`） | Casbin 单层审批 |
| `L3` | 硬写（如 `pg.kill_session`） | cmdpolicy + Casbin |
| `L4` | 破坏性（如 `redis.flushdb`） | 双人审批 |

`toolset.IsRead(level)` 只把 L0/L1 算作只读，**「只读工具面」因此是一句可执行的判定，而不是
一句形容词**。

---

## 五、租户、凭据、审计

- **租户**：工具的 `Handler` 签名是 `(ctx, args)`，租户从 `context` 读
  （`tenantctx`），不作为参数由模型提供——**模型无法指定租户**，这是隔离的第一道闸。
- **凭据**：适配器实例在装配根按目标构造（`cmd/opskeeper/loop_adapters.go`），
  **凭据不进工具参数、不进提示词**；`make edge-credential-check` 守住节点侧读不到任何云厂商凭据。
- **审计**：调用经宿主统一的审计咽喉（`core/manager/biz/audit`，HMAC 链），
  插件与适配器**没有写审计的权限**，只能被记录。

---

## 六、装配根与能力检查是三份清单

「有哪些适配器」这个事实在仓库里有三份：`toolset.Registry()`（空实例表）、
`cmd/opskeeper/loop_adapters.go`（带真实依赖的装配）、`cmd/opskeeper-eval/vocabulary.go`
（能力检查用）。**三份目前一致，但没有守卫保证它们一致**——这是本文件记下的下一项，
不是已关的账。加一个新适配器而只改其中一份，今天不会红。

---

## 七、未交付

以下内容在旧版本文里出现过，**代码从未实现**。文档已删除对应描述：

| 项 | 状态 | 说明 |
|---|---|---|
| `POST /api/v1/middleware`（创建资源） | ❌ 从未实现 | 没有这个端点，也没有资源 CRUD 的 REST 面 |
| `GET /api/v1/middleware`、`GET /api/v1/middleware/{id}` | ❌ 从未实现 | 同上 |
| `POST /api/v1/middleware/{id}/diagnose/pg`、`/diagnose/redis` | ❌ 从未实现 | 诊断是**工具**（`pg.*` / `redis.*`），不是端点 |
| `POST /api/v1/middleware/{id}/execute/pg` | ❌ 从未实现 | 执行是工具调用，且必经审批链 |
| `POST /api/v1/middleware/approvals/{ticket_id}/decide` | ❌ 从未实现 | 审批走既有 HITL 通道，不在中间件名下 |
| `POST /api/v1/middleware/{id}/webhooks` | ❌ 从未实现 | 无此端点 |
| 响应信封 `{code, message, data}` | ❌ 不适用 | 工具返回是工具的返回值，不是那个信封 |
| 独立业务错误码（4000/4003/…） | ❌ 不适用 | 工具错误是 `error`，由 Agent 侧转成工具结果 |

---

## 八、相关

- 实现：`core/manager/middleware/{registry,adapter,toolset}`、`core/manager/middleware/adapter/*`
- 形状守卫：`core/manager/middleware/adapter/docsurface_test.go`
- 能力闸门：`make eval-vocabulary`（对着活注册表跑）
- 节点侧工具面：`core/pig/extensions/opskeeper-sre-*`（插件形态的同一批能力）
