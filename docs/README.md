# Memora 文档入口

先看你问的是哪一个问题：

| 问题 | 去哪 |
|---|---|
| **现在坏了什么 / 还欠什么？** | [`issue/`](./issue/README.md) —— 唯一活账。一个未解决的问题一个文件，`README` 是状态表 |
| **现在到底是怎么实现的？** | [`implementation/`](./implementation/README.md) —— 现行内核、数据、Agent 面、对外面、已交付设计 |
| **还要做什么？** | [`planning/`](./planning/README.md) —— 队列与未完成的设计 |

## 方向与契约（冲突时以它们为准）

1. [写入形态](./product/write-model.md) — 数据表 + history + 语义配套；叶子挂 RowID
2. [查询形态](./product/query-model.md) — 四条路；事实一律 `SELECT` 回表
3. [架构原则](./product/architecture-principles.md)

边界：[宪章](./product/ai-native-product-charter.md) ·
[产品边界](./product/ai-native-boundary.md) ·
[契约](./product/ai-native-contract.md)

## 另外两层

- **规范级结论**：[`decisions/`](./decisions/)（ADR）；运行中的判断在 [`decisions.md`](./decisions.md)。
- **带日期的证据**：[`development/`](./development/)（dogfood 轮次、验收报告）——它们是证据，不是账；
  其中没做完的结论必须在 [`issue/`](./issue/README.md) 里有一条。
- **市场调研**：[`research/`](./research/)。
- **历史**：[`archive/`](./archive/README.md)，只用于追溯，日常不要读。

## ADR 索引

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

## 基座

持久化基座是 **SQLite**。Page、WAL、B+ Tree、自研恢复都不是本项目对象（[ADR-0011](./decisions/0011-pure-storage-engine-tables-everything.md)）。
