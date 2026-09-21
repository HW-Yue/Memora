# 执行计划

状态：2026-09-20。现役唯一工作队列。

工作项用题目。`F1`–`F228` 只在 `docs/archive/`。分支 `feature/<short-name>`。
每项按 [TDD](./feature-tdd-protocol.md) 独立 Review、授权、验收。

## 现在有什么

SQLite 一个文件：Catalog、数据表、history、语义配套、`mem_changes`、配置。
查询主路：`SHOW ROUTES` → `OPEN ROUTE` → `SELECT`。
写入：`INSERT` / `UPDATE` / `DELETE` / `SPLIT` / `MERGE`，叶子挂 RowID。
接入：CLI、MCP、只读 Admin、Skill。

## 还要写什么

产品形态见 [写入](../product/write-model.md) 与 [查询](../product/query-model.md)。

### 落地基座（先于下一件）

候选顺序见[决策日志](../decisions.md)（待授权）：修 `ci.yml` 的 `CGO_ENABLED` →
分支上见一次真实绿 CI → squash 一个 baseline 提交落 `main`、分支留 tag →
补 `catalog`／`row`／`instance` 最小回归。

### 行必须可导航（落地基座之后）

[规格](./row-navigable.md)。现在 `exec` 直连 INSERT 不带叶子也能提交。
先只读报告，再在 `sqlstore` 提交路径拦截。不做召回、不做行链接。

### 行链接读写

字段已在行上；MSQL 还没有。见 [行链接](../product/row-links.md)。

### 归档式删除 + history 谱系（新规格，待排期）

[行删除](../product/row-delete-archive.md) 与 [history 谱系](../product/history-lineage.md)。
未决先定：入向链接怎么办、history 指针字段的位置与 `successor_ids` 的关系。
挂载 1:1 已在 [写入形态](../product/write-model.md) §1.3 定案。

### 召回架构

关键词与向量只出路径、事实回表。方案未定，不写代码。

### 查询 Skill 编排

四条路怎么单走或组合。排在召回入口之后。

Route / Schema Mutation Plan 本轮不改。
