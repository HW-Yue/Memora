# Row Store v1

状态：F15a/F15b 已实现持久 CRUD 与 revision；MSQL 执行由 F15c 补齐。

> **目标形态已改。** 本文的 RowID 是创建时分配的全局 `row_<uuid>`
> （`IDSource.Next()` 不接受表参数）。[写入形态](../product/write-model.md)取代了这点：
> **RowID 按表递增**——每张表（含 history 表）各自维护自己的递增序列，
> 不从全局共用一个计数。同时 Row 要带一个 `history_id` 字段指向本行最新一条
> history 记录。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。

## 逻辑身份

每条 Row 创建一次稳定 `row_<uuid>`，并保存：

```text
row_id, database_id, table_id, schema_version,
revision, row_state, values, created_at, updated_at
```

创建时 `revision = 1`、`row_state = live`。Row 不保存可变 Database、Table 或 Column 名称；业务值以稳定 `column_id → value` 编码。读取时按当前 Catalog 投影名称，因此 rename 不复制 Row，也不改变身份。

## Revision mutation

UPDATE 和 DELETE 都必须同时携带：

- `expected_schema_version`：防止按过期 Schema 解释字段；
- `expected_revision`：防止覆盖 Row 的更新版本。

任一不匹配返回 `revision_conflict`，重新读取前不得强制覆盖。成功 UPDATE 保持 `row_id/created_at`，写入当前 Schema version、令 revision 加一并更新 `updated_at`。同一 expected revision 的并发更新只有一个能够成功。

DELETE 是逻辑删除：保留当前字段，把 `row_state` 改为 `deleted` 并令 revision 加一。普通 Get/List 不返回 tombstone；内部显式读取可用于恢复、History 和后续事务链路。再次 UPDATE/DELETE 已删除 Row 返回 `not_found`，F15 不执行物理清除。

F15e 已把这些操作接入统一 MSQL Executor，结构化 mutation options 和影响预算见 [MSQL Mutation Executor v1](../query/msql-mutation.md)。

F16a 增加 transaction-scoped Row API：同一个 scope 内的 Catalog 绑定、Get/List 与 CRUD 共用一个 read-write Store transaction，并可统一 Commit/Rollback。原有 Service CRUD 是该 scope 的 autocommit 包装，不能维护另一套 validation 或 revision 逻辑。

F17a 起每次 Row mutation 还在同一 transaction 内追加 [History Store v1](./history-store-v1.md)。当前 Row 保存最近一次 `commit_sequence`；同一显式事务修改多个 Row 时 sequence 相同，而每个 Row 的 revision 独立递增。

F17b 的补偿恢复复用相同 transaction scope 和 expected schema/revision guard。它可以从 tombstone 恢复 live snapshot，但结果永远是新的 COMPENSATE revision，不会把当前 revision 倒退。

## Schema 与值校验

每次写入必须携带 `expected_schema_version`。Row Service 在同一个 Store transaction 中读取 Catalog 和写 Row；版本不一致返回 `revision_conflict`。

Insert：

- 拒绝未知 Column 和同一 Column 的 current name/alias 重复输入；
- 缺失 nullable Column 自动保存为 NULL；
- 缺失 `NOT NULL` Column 返回 `constraint_violation`；
- 所有值经过 [逻辑类型与字段预算 v1](./logical-types.md)；
- 任一字段失败时不写 Row，也不更新 Table Row index。

## 原型持久化

Row 和每表稳定 Row ID index 都只通过 `Store`/`Tx` 读写。SQLite bucket、SQL、rowid 和 schema 不进入 Row 层。

JSON 只是原型 Store value 的版本内编码。读取使用 JSON number 保留整数精度，再按当前 Column 类型规范化；Timestamp 重新规范化为 UTC。关闭 Store 后重开必须得到相同 ID、revision 和值。

## 读取边界

- `Get` 只返回 live Row，`GetIncludingDeleted` 是内部显式 tombstone 读取；
- `List` 按稳定插入顺序返回 live Row；
- 单次内部 List/Page limit 必须在 1–1000，并显式报告是否仍有更多 live Row；F15d Planner 据此返回截断状态；
- 返回字段使用当前 Column 名，不回显废弃 alias。

## 关联

- [语义记录模型](./semantic-records.md)
- [Catalog v1](./catalog-v1.md)
- [逻辑类型与字段预算 v1](./logical-types.md)
- [SQLite 原型 Store](../decisions/0001-prototype-store.md)
