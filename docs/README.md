# Memora 文档入口

当前有效设计只在这里。历史规格在 [`archive/`](./archive/README.md)，
日常不要整篇读取。

持久化基座是 **SQLite**。Page、WAL、B+ Tree、自研恢复都不是本项目对象。

## 最高产品参考规范

任何设计与实现与这三份冲突时，以这三份为准：

1. **[写入形态](./product/write-model.md)** — 数据表 + history 表 + 语义配套表，
   一次写入一个 SQLite 事务；叶子直接挂 RowID。
2. **[查询形态](./product/query-model.md)** — 四条路：语义索引（Agent 主路）、
   关键词召回、向量召回、Skill 层 jev；召回只给语义路径，事实一律 SQL 回表。
3. **[架构原则](./product/architecture-principles.md)** — 能用一张表就别造复杂逻辑。

宪章与边界：[产品宪章](./product/ai-native-product-charter.md)、
[产品边界](./product/ai-native-boundary.md)、
[产品契约](./product/ai-native-contract.md)。

## 派发工作

- [执行计划](./planning/execution-plan.md) — **当前唯一工作队列**
- [过程稿](./plan.md) — 按块核实后再收成执行计划
- [TDD 协议](./planning/feature-tdd-protocol.md) · [Feature 产品门](./planning/feature-product-gate.md)
- [决策日志](./decisions.md) — 运行中的判断记录；规范级结论仍进下方 ADR

## 现行内核与规格

- [存储层](./storage/README.md) — SQLite 普通表
- [检索路线](./query/retrieval-routes-jev.md) · [jev 选择器](./query/jev-branch-selection.md)
- [MSQL](./query/msql.md) · [语义 Router](./query/semantic-routing.md)
- [Route 配套表](./product/route-companion-table.md) ·
  [行生命周期](./product/row-lifecycle-successor.md) ·
  [行链接](./product/row-links.md)
- [可演化配置](./product/adaptive-configuration.md) ·
  [Fan-out 硬上限](./planning/route-branch-fanout-limit.md)

现役不变量：[一叶一行](./planning/single-row-route-leaf.md)。
排队：[行必须可导航](./planning/mandatory-row-route.md)。

## 决策（Accepted）

- [ADR-0007 预测器可组合](./decisions/0007-route-predictor-arsenal.md)（向量边界被 0012 修订）
- [ADR-0008 全内容倒排](./decisions/0008-full-content-inverted-index.md)
- [ADR-0009 薄 Agent Loop](./decisions/0009-memora-owned-agent-loop.md)
- [ADR-0011 一切建表](./decisions/0011-pure-storage-engine-tables-everything.md)
- [ADR-0012 Row 向量直给叶子路径](./decisions/0012-row-vector-leaf-path.md)
- [ADR-0002 延后内置 Agent](./decisions/0002-defer-embedded-agent.md)

被取代的 ADR-0001、0003–0006 在 [`archive/decisions/`](./archive/decisions/)。

## Agent 与使用

- [Canonical Skill](./agent/canonical-skill-v1.md) ·
  [Skill 写入](./agent/skill-write-v1.md) ·
  [Schema 生命周期](./agent/skill-schema-lifecycle-v1.md)
- [MCP Adapter](./agent/mcp-adapter-v1.md) ·
  [Agent 的 MSQL 边界](./agent/agent-msql-dependency-injection.md)
- [CLI](./development/cli-database-workflow.md) ·
  [Go SDK](./development/go-sdk-v1.md) ·
  [IPC](./development/ipc-protocol.md) ·
  [LaunchAgent](./development/macos-launch-agent-v1.md)

数据词典：[语义记录](./data/semantic-records.md)、
[Catalog](./data/catalog-v1.md)、
[逻辑类型](./data/logical-types.md)、
[自描述字典](./data/self-describing-data-dictionary.md)。
