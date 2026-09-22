# Memora 文档入口

当前有效设计只在这里。历史在 [`archive/`](./archive/README.md)，日常不要读。

持久化基座是 **SQLite**。Page、WAL、B+ Tree、自研恢复都不是本项目对象。

## 产品

这三份冲突时以它们为准：

1. [写入形态](./product/write-model.md) — 数据表 + history + 语义配套；叶子挂 RowID。
2. [查询形态](./product/query-model.md) — 四条路；事实一律 `SELECT` 回表。
3. [架构原则](./product/architecture-principles.md)

边界：[宪章](./product/ai-native-product-charter.md) ·
[产品边界](./product/ai-native-boundary.md) ·
[契约](./product/ai-native-contract.md)

配套：[语义表](./product/route-companion-table.md) ·
[行生命周期](./product/row-lifecycle-successor.md) ·
[行删除](./product/row-delete-archive.md) ·
[history 谱系](./product/history-lineage.md) ·
[行链接](./product/row-links.md) ·
[配置](./product/adaptive-configuration.md)

## 工作

- [项目计划](./planning/project-plan.md) — 七个里程碑、判据与顺序
- [M7 召回详细计划](./planning/m7-recall-plan.md) — 七个 Feature、两次闭环、风险
- [执行计划](./planning/execution-plan.md) — 唯一队列。先落地基座（CI → baseline → 核心回归），
  再[行必须可导航](./planning/row-navigable.md)
- [Admin 显示槽位](./planning/admin-display-slots.md) — 文档居中且只渲染一次
- [Admin 语义画布的性能](./planning/admin-canvas-performance.md) — 卡顿的四个来源与这次的修法
- [Admin 语义画布的手势](./planning/admin-canvas-gestures.md) — 画布手势只有一层，而且必须能自己结束
- [Admin 语义画布的连线](./planning/admin-canvas-connections.md) — 布局吃真实卡高，锚点交给 port
- [引擎拥有形状](./planning/engine-owned-shape.md) — 讨论稿：列归引擎，命名/描述/位置/正文归 agent
- [向量 rekey](./planning/vector-rekey.md) — 卸下 TOFU 身份，再让排干重新上锁
- [无上下文 subagent 循环实测（2026-09-22）](./development/dogfood-2026-09-22.md) — 五轮摩擦与修复
- [TDD](./planning/feature-tdd-protocol.md) · [产品门](./planning/feature-product-gate.md)
- [决策日志](./decisions.md) — 运行中的判断；规范级结论进 ADR

## 现行内核

- [存储](./storage/README.md)
- [MSQL](./query/msql.md) · [语义 Router](./query/semantic-routing.md)
- [Agent 与引擎的分界](./query/agent-engine-boundary.md) — 谁发指令、谁展开
- [INSERT 隐式建路径](./query/implicit-route-path-v1.md) — 用路径而不是 leaf id 挂载
- [检索四条路](./query/retrieval-routes-jev.md) · [jev 逐层选择](./query/jev-branch-selection.md) · [jev 走树](./query/jev-tree-v1.md)

## ADR

[0011 一切建表](./decisions/0011-pure-storage-engine-tables-everything.md) ·
[0014 行形状归引擎](./decisions/0014-the-engine-gives-the-row-shape.md) ·
[0015 召回可说位置出处](./decisions/0015-recall-exposes-position-provenance.md) ·
[0013 两路召回按名次融合](./decisions/0013-recall-fusion-by-rank.md) ·
[0012 Row 向量给叶子路径](./decisions/0012-row-vector-leaf-path.md) ·
[0008 关键词倒排](./decisions/0008-full-content-inverted-index.md) ·
[0007 预测器可组合](./decisions/0007-route-predictor-arsenal.md)（向量边界被 0012 修订）·
[0009 薄 Agent Loop](./decisions/0009-memora-owned-agent-loop.md) ·
[0002 延后内置 Agent](./decisions/0002-defer-embedded-agent.md)

0001、0003–0006 在 [`archive/decisions/`](./archive/decisions/)。

## 使用

[Skill](./agent/canonical-skill-v1.md) ·
[Skill 写入](./agent/skill-write-v1.md) ·
[MCP](./agent/mcp-adapter-v1.md) ·
[CLI](./development/cli-database-workflow.md) ·
[测试](./development/testing.md)
