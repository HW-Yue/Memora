# 执行计划

状态：**2026-09-20。当前唯一的工作队列。**

每项仍须按 [TDD 协议](./feature-tdd-protocol.md) 独立 Review、授权、实现、验收。
持久化相关项测 reopen / 中断 / 错误注入；SQLite 自己的页格式与崩溃恢复不测。

## 现在代码里有什么

- **存储**：`internal/sqlstore`，一个实例一个 SQLite 文件。Catalog、数据表、
  history、语义配套表、`mem_changes`、配置。写串行，读看最后一次提交。
- **查询主路**：`SHOW ROUTES` 逐层走到叶子，`OPEN ROUTE` 取 RowID，`SELECT` 回表。
- **写入**：`INSERT` / `UPDATE` / `DELETE` / `SPLIT` / `MERGE`，叶子挂 RowID。
- **结构**：Catalog DDL、Route DDL、Route Mutation Plan、Schema Change Plan。
- **接入**：CLI（`query` / `exec` / `mutate` / `schema` / `doctor`）、MCP、
  只读 Admin、Skill。

## 产品形态（要实现）

查询一共四条路，事实一律 `SELECT` 回表，见 [查询形态](../product/query-model.md)：

| 路 | 职责 |
| --- | --- |
| 语义索引 | Agent 主路：逐层 `SHOW ROUTES`（**已实现**） |
| 关键词召回 | 一次拿到命中的语义路径，不给分数或正文 |
| 向量召回 | 同上；允许 Route / Row 语义向量（[ADR-0012](../decisions/0012-row-vector-leaf-path.md)） |
| jev | Skill 层可选：把一层 child 交给 `Choice`，内核仍是 `SHOW ROUTES` |

行与行之间只通过数据行上的 `links` 字段相连，见 [行链接](../product/row-links.md)。
字段已在行上；读写语句还没写。

## 队列

### 召回架构

关键词与向量两条路的存储、维护和 MSQL 入口。约束已经冻住：只出路径、事实回表。
方案未定前不写代码。

### F224：Row 必须可导航

[规格](./f224-mandatory-row-route.md)。零叶子的 Row 没有路径可返回。本轮不实现。

### 行链接读写

`links` 字段的双向写入、读取与懒更新，见 [行链接](../product/row-links.md)。

### 查询 Skill 编排

四条路怎么单走或组合。排在召回入口之后。jev 见
[jev 选择器](../query/jev-branch-selection.md)。

Route / Schema Mutation Plan 与写入侧 fan-out 本轮不改。
