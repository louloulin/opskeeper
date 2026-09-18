# Comet Design Handoff

- Change: prepare-final-demo-main-flow
- Phase: design
- Mode: compact
- Context hash: e67cb343ee45b851e51ff93afebd3673eba90c6eb34916015fa1366aacd55870

Generated-by: comet-handoff.sh

OpenSpec remains the canonical capability spec. This handoff is a deterministic, source-traceable context pack, not an agent-authored summary.

## openspec/changes/prepare-final-demo-main-flow/proposal.md

- Source: openspec/changes/prepare-final-demo-main-flow/proposal.md
- Lines: 1-43
- SHA256: 10eaeb8d65fe6c23ec96e1ed8ffb5a9bc4eaa2d24f7e675250c101a855a859c6

```md
# Proposal: prepare-final-demo-main-flow

## Why

决赛路演需要一条可重复、可观测、可回看的主线：以 PostgreSQL 应用连接池耗尽为唯一主案例，通过 `home.yueming.xin` 轻量业务页面放大故障体感，完整走通 业务受影响→告警→诊断→预演→审批→修复→验证→关闭→复盘 闭环。

评委建议一要求"集中打磨这一主线"并统一演示材料；建议二要求"修复预演+对比+控制面可靠"但明确不需要 PolarDB HA。当前 preview-pg 已有数据层对比（索引/数据修复），需要补强连接池运行态对比和 home 页面业务体感层。

**当前阻塞**：`agentteams-worker-opskeeper-investigator` 刚因 QwenPaw API timeout 退出，诊断环节无法执行。

## What

1. **恢复 investigator worker** 并确认 7 个 RCA 角色全部在线
2. **实现 home 控制台一键注入**：home 调用 Manager 受控场景 API，由 Manager 编排 pool-fixture、生成 incident_id 并关联告警
3. **在 benyue-lumos-ops 房间执行真实 PG 池耗尽 E2E**：home 页面体感→告警→诊断→审批→修复→验证→关闭
4. **构建三层对比框架**（不用 PolarDB HA 满足评委建议二）：
   - 层1 业务体感：home.yueming.xin 故障前后对比面板
   - 层2 运行态重放：preview-pg + pool-fixture 修复前后指标对比
   - 层3 数据回归：修复前后 checksum 一致性校验
   - 预演必须绑定 replay_profile_id，负载配置不一致则标记 NOT COMPARABLE
   - 结构化预演存储、HITL 门禁与 Archive readback 复用 `repair-preview-readback`，本变更只负责路演编排与验收
5. **验证控制面可靠性增强**：隔离部署、状态持久化、Manager 重启后事故进度留存、执行幂等与目标指纹校验
6. **版本一致性 readback**：Manager / Worker / Dashboard / plugin-manager
7. **演示剧本** `FINAL_DEMO_SCRIPT.md`

## Scope

- ✅ 阿里云公网 demo 环境恢复与 E2E
- ✅ benyue-lumos-ops 房间全流程验证
- ✅ home.yueming.xin 业务体感层与故障前后对比面板
- ✅ preview-pg 运行态重放 + 数据回归校验
- ✅ 归档回看 + 版本 readback
- ✅ 演示剧本
- ❌ 不做 PolarDB HA（诚实标注"单机 Docker 增强 + 生产演进路径"）
- ❌ 不重构 AgentTeams / Dashboard 核心代码
- ❌ 不新增 CPU / 锁等待等第二案例（保留为答辩备选）

## Non-goals

- 不替换 AgentTeams 或 Element 本身
- 不引入 Frontier LLM 路由层
- 不修复 Grafana / Frontier 的 degraded 状态（demo 不依赖）
- 不宣称 preview-pg 等同于生产活动会话（诚实边界：受控负载重放）
```

## openspec/changes/prepare-final-demo-main-flow/design.md

- Source: openspec/changes/prepare-final-demo-main-flow/design.md
- Lines: 1-255
- SHA256: cd7a24ed821e66a624e8648b876d738549b1d3bb036b5efe760563aa533310de

[TRUNCATED]

```md
# Design: prepare-final-demo-main-flow

## 演示叙事（核心）

评委打开 `home.yueming.xin` 看到一个正常运行的轻量业务页面（订单/查询），
并在同一页面点击“注入连接池故障”。该控制台动作调用 OpsKeeper Manager 的
演示场景 API，由 Manager 编排 pool-fixture 并生成 incident_id。
随后订单、库存、审计等数据库依赖区域请求变慢 / 部分失败，而静态导航与页面壳保持
可用，让评委从**业务体感**发现问题。演示者切换到监控页确认连接池 4/4、等待队列
与应用错误率上升；此时 Prometheus 告警已自动进入 OpsKeeper。观众再切到 Element
房间查看 Manager 主持诊断、预演与修复建议。人工审批后执行修复，先在监控页确认
连接池容量与占用恢复，再回到 home 重新查询订单 / 库存 / 审计数据，确认业务恢复。

**关键**：home.yueming.xin 不只是展示页，它是"故障体感放大器"——
让评委看到连接池耗尽如何影响真实业务，而不是只看 PG 内部指标。

## 高层架构

```
┌─────────────────────────────────────────────────────────────┐
│  路演入口                                                     │
│  home.yueming.xin（业务页面 + 路演控制台）                       │
│  正常：请求 <100ms，页面响应流畅                                 │
│  故障：请求超时，页面卡顿/报错；控制台只发起受控演示场景            │
└──────────────────────┬──────────────────────────────────────┘
                       │ 一键注入（Manager API）
                       ▼
            ┌─────────────────────┐
            │ OpsKeeper Manager    │
            │ 创建 incident + 编排 │
            └─────────┬───────────┘
                      │ 受控场景请求
                      ▼
            ┌─────────────────────┐
            │ opskeeper-pool-     │
            │ fixture（受控负载）   │
            │ 饱和连接池            │
            └─────────┬───────────┘
                      │
        ┌─────────────┼─────────────┐
        ▼             ▼             ▼
  ┌──────────┐ ┌────────────┐ ┌──────────┐
  │ app log  │ │ Prometheus │ │ pool     │
  │ (慢/失败) │ │ 指标        │ │ metrics  │
  └────┬─────┘ └─────┬──────┘ └────┬─────┘
       └─────────────┼─────────────┘
                     ▼
            ┌─────────────────┐
            │ OpsKeeper 告警    │
            │ pool_exhausted  │
            └────────┬────────┘
                     │
                     ▼
  ┌────────────────────────────────────────────────────┐
  │ RCA 全流程（Element #benyue-lumos-ops + Dashboard） │
  │ 告警 → 诊断 → 审批 → 修复 → 验证 → 关闭               │
  └────────┬───────────────────────────────────────────┘
           │
           ▼
  ┌─────────────────┐     ┌──────────────────┐
  │ preview-pg      │     │ home.yueming.xin │
  │ 修复预演 + 对比   │     │ 业务恢复确认       │
  └─────────────────┘     └──────────────────┘
```

### home 控制台的注入边界

- home **不直接连接 pool-fixture，也不持有数据库或负载凭据**；它只调用 Manager 的
  `/demo/scenarios/pg-pool-exhausted/start` 类受控 API。
- Manager 负责场景白名单、目标指纹、影响面、持续时间、幂等键和 incident_id 生成；
  重复点击返回同一 incident，不重复注入。
- 注入成功后，Manager 发送 `agentteams.workflow` 事件并让 home 进入进度看板。
- Prometheus 告警仍然独立触发；告警与控制台动作通过同一 alert_fingerprint /
  incident_id 关联，避免 home 绕过监控链路。
- 路演者不需要在 Element 中复制“故障注入”提示词；房间提示词仅保留为回调失败时的
  演示兜底，并必须走同一个 Manager 入口。

## 评委建议二的满足方案（不用 PolarDB HA）

结构化预演记录、PASS/FAIL 门禁和 Archive readback 以 `repair-preview-readback`
```

Full source: openspec/changes/prepare-final-demo-main-flow/design.md

## openspec/changes/prepare-final-demo-main-flow/tasks.md

- Source: openspec/changes/prepare-final-demo-main-flow/tasks.md
- Lines: 1-49
- SHA256: e6151813860286001be43b9e5bd6d877281243339ddaf8dd2c19c27485bcdfff

```md
# Tasks: prepare-final-demo-main-flow

## P0 — 恢复与 E2E

- [ ] 1. 重启 `agentteams-worker-opskeeper-investigator`，排查 QwenPaw API TimeoutError 根因，确认 7 个 RCA Worker 全部 Up
- [ ] 2. 确认 `opskeeper-pool-fixture` / `opskeeper-pool-metrics` / `opskeeper-preview-pg` / `opskeeper-hitil-monitor` 容器健康
- [ ] 3. 实现 home 控制台一键注入：home 调用 Manager 受控场景 API，Manager 编排 pool-fixture、生成 incident_id、关联告警并保证幂等
- [ ] 4. 在 `benyue-lumos-ops` 房间验证一次 PG 池耗尽 E2E，完整走通 告警→诊断→预演→审批→修复→验证→关闭
- [ ] 5. 验证 Element 与 Dashboard 同步显示任务进展（agentteams.workflow 消息）
- [ ] 6. 验证本次 E2E 事故在归档页 evidence_complete=true，可反查全部 7 类事件与 trace_id
- [ ] 7. 验证演示节奏：home 数据库区域降级→监控确认 4/4 高负荷→rooms 展示 Manager 主持→人工审批→监控确认恢复→home 查询恢复
- [ ] 8. 验证 home 的订单/库存/审计卡片来自真实查询接口：故障时超时或显示错误态，页面壳与非数据库区域不整页不可用；修复后无需刷新或仅需一次刷新即恢复

## P0 — 版本一致性

- [ ] 9. 回读公网版本矩阵并记录：Manager / Worker plugin / Dashboard plugin / plugin-manager / pool-fixture / preview-pg
- [ ] 10. 确认 `home.yueming.xin` / `teams.yueming.xin` / `rooms.yueming.xin` / `opskeeper.yueming.xin` 四个域名全部 HTTP 200

## P0 — 三层对比框架（不用 PolarDB HA 满足建议二）

- [ ] 11. **层1 业务体感对比**：在 `home.yueming.xin` 增加"故障前后对比面板"（请求延迟 / 成功率 / 连接池占用 / 页面状态），修复后持续采集 ≥30s
- [ ] 12. **层2 运行态重放对比**：在 `preview-pg` 上用 pool-fixture 重演连接池耗尽，采集修复前 baseline（active=4/cap=4, latency=500ms, error=40%）与修复后 candidate（active=0/cap=8, latency<10ms, error=0%）
- [ ] 13. **层3 数据回归校验**：修复前后各跑一次 checksum，确认连接池修复不引入数据问题
- [ ] 14. 在 preview-pg 对比表新增"连接池修复"行，与现有"索引修复 / 数据修复"并列展示三类候选
- [ ] 15. 为预演结果增加 `replay_profile_id` / 负载配置摘要；配置不一致时显示 `NOT COMPARABLE`，不得进入审批结论
- [ ] 16. 复用 `repair-preview-readback` 的结构化结果与 Archive API；路演在审批前只展示紧凑 baseline/A/B 决策卡，完整对比表在事故关闭后回看
- [ ] 17. 演示单机可靠性增强：待审批或待验证状态下重启 Manager，事故与进度留存；重试修复时通过执行 ID + 目标指纹避免重复执行

## P1 — 路演页面

- [ ] 18. 确认 `home.yueming.xin` 主路演页面能展示 PG 池定位/审批/修复全流程（评委建议一）
- [ ] 19. 在 Dashboard Runtime 页验证持续观测窗口（修复后 ≥30s 连接池/请求指标）
- [ ] 20. 在 Dashboard 归档页验证同类历史事故反查（相似 incident 可点击跳转）
- [ ] 21. 在 Archive / preview-pg 页面验证"修复方案一通过一拒绝"：pool resize 通过 + aggressive kill 拒绝，Authority 以 Archive 为准、preview-pg 为深链

## P1 — 演示剧本

- [ ] 22. 输出 `FINAL_DEMO_SCRIPT.md`：按评委建议一编排的手工演示步骤
  - 开场：home.yueming.xin 正常页面 + 运维协作成本痛点（≤30s）
  - 触发：home 控制台一键注入 → Manager 编排 pool-fixture → home 页面变慢（≤30s）
  - 主线：告警 → 诊断 → 预演对比 → 审批 → 转折（错误目标拒绝）→ 修复 → 验证 → 关闭（≤3min）
  - 收尾：归档回看 + 版本展示 + preview-pg 对比表（≤30s）
- [ ] 23. 标注剧本中每步对应的应用入口（home / teams / rooms / preview）与预期指标

## P2 — 答辩备选

- [ ] 24. 确认 CPU 飙高 / 锁等待案例可一键切换（作为答辩 Q&A 备选，不进主线）
- [ ] 25. 更新 PPT 中旧描述（恢复验证/事故状态/版本信息），与决赛实际演示对齐
- [ ] 26. 在 PPT 附录页说明"不用 PolarDB HA 的边界"：单机 Docker 增强 + 生产演进路径
```

## openspec/changes/prepare-final-demo-main-flow/specs/final-demo-reliability/spec.md

- Source: openspec/changes/prepare-final-demo-main-flow/specs/final-demo-reliability/spec.md
- Lines: 1-60
- SHA256: fd19891e07da972d800dd9facfae48e3b53f9cb6ae82cc72b73605aaf4f881f3

```md
## ADDED Requirements

### Requirement: Initiate scenarios through the Manager control plane
The home demo console SHALL start a controlled PostgreSQL pool-exhaustion scenario through an OpsKeeper Manager API rather than directly operating the pool fixture or storing database credentials.

#### Scenario: One-click injection is requested
- **WHEN** the presenter clicks the injection action on `home.yueming.xin`
- **THEN** Manager validates the scenario, target fingerprint, duration, and blast radius
- **AND** Manager creates or returns the idempotent incident before starting the fixture

#### Scenario: Injection is repeated
- **WHEN** the same scenario is requested again while it is active
- **THEN** Manager returns the existing incident without creating a duplicate injection or incident

### Requirement: Produce comparable remediation evidence
The final demo SHALL separate business incident observation from controlled remediation rehearsal, and every candidate comparison SHALL be bound to a replay profile containing the workload model, concurrency, duration, SQL distribution, timeout, random seed, target version, and candidate version.

#### Scenario: Candidate comparison is comparable
- **WHEN** a preview candidate reports baseline and candidate metrics
- **THEN** both runs reference the same `replay_profile_id`
- **AND** the comparison may be used in the approval evidence

#### Scenario: Replay profiles differ
- **WHEN** baseline and candidate runs do not share the same replay profile
- **THEN** the comparison is marked `NOT COMPARABLE`
- **AND** it cannot be presented as approval evidence

### Requirement: Show business impact and recovery across demo surfaces
The final demo SHALL make database-dependent home queries visibly fail or degrade during injection while preserving the page shell, and SHALL use monitoring and the collaboration room to explain diagnosis and remediation before the same home queries are rechecked after recovery.

#### Scenario: Injection impacts database-backed widgets
- **WHEN** the pool-exhaustion scenario is active
- **THEN** order, inventory, and audit query areas show a bounded error, timeout, or degraded state
- **AND** static page navigation remains usable without a full-page failure

#### Scenario: Recovery is confirmed
- **WHEN** the approved remediation finishes
- **THEN** monitoring shows pool capacity and utilization recovering
- **AND** the home database queries return normal data without bypassing the same application connection path

### Requirement: Demonstrate single-host control-plane resilience
OpsKeeper SHALL keep incident alerting, diagnosis evidence, candidate rehearsal, approval state, execution state, and verification results persisted by incident ID while running the public demo without database high availability.

#### Scenario: Manager restarts while approval is pending
- **WHEN** Manager is restarted while an incident is waiting for approval or verification
- **THEN** the incident and its progress remain visible after restart
- **AND** no in-flight remediation is silently duplicated

#### Scenario: Remediation is retried
- **WHEN** a remediation request is retried after restart
- **THEN** OpsKeeper validates its execution ID and target fingerprint first
- **AND** it rejects execution when the target fingerprint does not match the approved target

### Requirement: Present honest reliability boundaries
The demo SHALL describe the current deployment as isolated single-host Docker reliability hardening and SHALL NOT claim automatic failover, cross-availability-zone recovery, or equivalence to production database high availability.

#### Scenario: Reliability boundary is displayed
- **WHEN** the preview page or presentation explains control-plane reliability
- **THEN** it identifies isolation, persistence, backup, restart retention, and idempotency as implemented mechanisms
- **AND** it names managed PostgreSQL or PolarDB high availability as the production evolution path rather than a current capability
```

