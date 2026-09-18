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
