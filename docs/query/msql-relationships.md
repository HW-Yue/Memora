# MSQL Relationships v1

状态：F18c 已冻结结构化关系的创建、双向读取和逻辑删除语法。

## 创建

```sql
RELATE work.notes ROW :source
TO work.tasks ROW :target
TYPE :relation_type
DESCRIPTION :description;
```

source/target 表必须使用 `database.table` 限定名，Row ID、type 和 description 只接受字面量或参数，不能引用 Row Column。`DESCRIPTION` 可省略。

`RELATE` 是单关系 mutation：

- `max_affected_rows` 必须为 1–1000；
- 不需要 `expected_schema_version`，因为它不修改业务 Row；
- 两个 endpoint 都必须是当前 live Row；
- 跨库由 Relation Policy 决定；
- 成功结果返回 `affected_rows=1`、relation revision 和 commit sequence。

所有文本均由参数绑定传值，不做 SQL 字符串插值。

## 双向读取

```sql
SHOW RELATIONS FROM work.notes
FOR ROW :row_id
DIRECTION OUTGOING
LIMIT :limit;
```

方向只能是 `OUTGOING` 或 `INCOMING`，LIMIT 为 1–1000。结果只包含关系及稳定 endpoint 定位：

```text
relation_id, relation_type,
source_database_id, source_table_id, source_row_id,
target_database_id, target_table_id, target_row_id,
description, revision, commit_sequence, relation_state
```

该结果用于图发现；Agent 需要业务内容时必须再用普通 `SELECT` 按 Row ID 回表。

## 删除

```sql
UNRELATE :relation_id;
```

`UNRELATE` 是逻辑删除，request mutation options 必须携带 `expected_revision` 和有效 `max_affected_rows`。成功后 revision 递增，历史不改写。

## 事务

`RELATE` 和 `UNRELATE` 与 INSERT/UPDATE/DELETE 使用同一个 BatchSession 事务。显式事务中的任一关系写失败会回滚整个事务；解析失败也按 mutation 失败处理。跨 request 的 BEGIN/COMMIT/ROLLBACK 行为不变。

`RELATE`、`UNRELATE` 是保留关键字；`type`、`description`、`direction` 和 `relations` 仍可作为普通业务标识符，避免破坏既有 Schema。

## 关联

- [Relationship Store v1](../data/relationship-store-v1.md)
- [MSQL Batch 与事务边界 v1](./msql-batch-transactions.md)
- [MSQL 参数与表达式 v1](./msql-expressions.md)
- [MSQL Result Envelope v1](./result-envelope.md)
