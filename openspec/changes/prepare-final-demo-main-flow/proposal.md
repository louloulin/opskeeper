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
