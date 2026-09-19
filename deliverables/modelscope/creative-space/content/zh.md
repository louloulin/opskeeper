# OpsKeeper · Agent 原生运维工作台

## 场景与挑战

告警风暴、多系统割裂和人工执行风险拖慢根因定位，事故证据也难沉淀复用。

## 解决方案与价值

OpsKeeper 以插件方式接入 AgentTeams，由 Manager 协调诊断、预演、修复与验证角色；正式恢复必须经过人工审批，操作对象、候选方案和执行凭据逐项核对。

## 安全边界

本创空间只做公开展示，不运行特权运维环境，不保存凭据，也不代理 OpsKeeper、Matrix 或 AgentTeams。所有在线入口均在新窗口打开并保留原有认证。

## 能力表述

修复预演在独立 preview-pg 中执行固定负载重放，对比结果一致性、查询延迟和写入影响。通过预演只代表可进入人工审批，不代表自动执行；失败候选会被拒绝。当前不声明 PolarDB HA，也不声明复制原实例活动会话。

## 架构与展示边界

OpsKeeper 的运行链路覆盖告警接入、根因诊断、修复预演、人工审批、执行验证和知识沉淀。本创空间仅复现产品叙事与公开展示证据：应用启动时读取本地中文内容与八份公开安全素材，不连接 Manager、PostgreSQL、Matrix 或 AgentTeams，也不提供任何服务端写入 API。

## 证据导览

- **总览**：`opskeeper-overview.gif` 呈现端到端产品旅程。
- **监控与根因**：`monitor.png` 与 `rca-session.png` 展示告警上下文和诊断会话。
- **编排与拓扑**：`workflow-editor.png` 与 `topology-map.png` 展示流程编排和系统关系。
- **审批与档案**：`write-gate.png`、`artifacts.png` 与 `knowledge-vault.png` 展示人工审批边界、执行产物和知识库。

## 快速开始

```bash
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
python app.py
```

启动后访问 `http://127.0.0.1:7860`。创空间为只读展示，不需要 OpsKeeper 凭据、数据库连接或内部服务地址。

## 入口卡片

下方卡片覆盖 GitHub 源码、产品官网、OpsKeeper 服务、AgentTeams Rooms、AgentTeams Dashboard 和路演全流程控制台。外部入口均在新窗口打开，不在页面内 iframe 特权系统。
