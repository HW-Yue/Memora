# ADR-0011：存储引擎只做数据库，其余一切在它上面建表

状态：Accepted，2026-09-13；2026-09-20 明确基座为 **SQLite + sqlite-vec**。
取代 [ADR-0004](../archive/decisions/0004-fast-row-directory-minimal-mvcc.md)
与 [MVCC、Undo 与 Redo 边界](../archive/storage/mvcc-undo-redo.md)。
语义树的表形态见 [Route 配套表](../product/route-companion-table.md)。

## 背景

自研引擎时期，除了「表」还长着一批专用结构：版本树、按表 history 树、
objects 树、change 树、全文树。每一种都有自己的 codec 和发布路径。
每加一个产品概念就要新造一种存储；版本树还同时兼着产品历史和事务快照读。

## 决定

### 1. 两层，边界只有「表」

```text
产品层   history、语义树、变更日志、链接、配置……全部是普通 SQLite 表
         Agent 的每个操作 = 内部生成的一个事务，对这些表做 SQL 读写
───────────────────────────────────────────────
SQLite    表、索引、事务、WAL、恢复；sqlite-vec 提供 vec0
          不知道什么是 history，也不知道什么是 Route
```

引擎对所有表一视同仁。某张表是数据表、history 表还是语义表，
是产品层记在 Catalog 上的标记，不据此改变 SQLite 行为。

### 2. 不做 MVCC

- 写事务串行执行；
- 读只看最新已提交状态，没有快照、没有 read view、没有 undo log；
- 未提交的写对任何读都不可见——提交点是 SQLite `COMMIT`；
- 分页读跨越多次请求时，可能看到页与页之间发生的提交。按 RowID 游标翻页，
  不重复不遗漏已存在的行，但不承诺整批读是同一时刻的切面。

理由：Memora 是单用户个人库，并发写极少；快照隔离的代价
远大于它在这里带来的价值。

### 3. history 是数据表的配套普通表

- 只有数据表配 history 表；语义表、history 表自己不配；
- Agent 改数据 → 一个事务内：改数据表，追加 history 表；
- `SHOW HISTORY`、`AS OF` 是对 history 表的查询；
- history 只记**原地修改**；拆分、合并、删除改变身份，走主表里的废弃 + 接替，
  见[数据行的生命周期](../product/row-lifecycle-successor.md)。

### 4. 语义树是配套普通表

Agent 改语义树 → 内部生成参数化 SQL，在一个事务内改语义配套表
（以及需要时业务行上的 `route_leaf_ids`）。细节见
[Route 配套表](../product/route-companion-table.md)。

### 5. 其余结构

| 结构 | 归属 |
|---|---|
| 链接 | 数据行上的 `links` 字段，见[双向链接](../product/row-links.md) |
| 变更日志 | 普通表 `mem_changes`，与数据、history 同一事务写 |
| 配置 | 普通表 |
| Catalog | 普通表；表的角色标记是它的字段 |
| 词法索引 | 普通表 `mem_postings` |
| 向量索引 | sqlite-vec vec0 虚拟表 `mem_vectors` |

对 Agent 可见哪些表（只有数据表），由产品层按 Catalog 标记过滤。

## 后果

- 新产品概念只需「建表 + 写 SQL」；
- 没有 Memora 自研页格式、WAL、B+ 树或恢复重放；
- 失去多语句读一致性；分页读不承诺同一时刻切面。

## 实施

内核在 `internal/sqlstore`。尚未完成的产品项见
[执行计划](../planning/execution-plan.md)。
