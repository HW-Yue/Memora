# MSQL History v1

状态：F17c 已实现。

## AS OF

历史 snapshot 通过 SELECT 的 table 后缀读取：

```sql
SELECT title, revision, commit_sequence
FROM work.notes AS OF REVISION :revision
WHERE row_id = :row_id
LIMIT 1;

SELECT *
FROM work.notes AS OF COMMIT_SEQUENCE :sequence
WHERE row_id = :row_id
LIMIT 1;
```

v1 必须包含可静态提取的精确 `row_id = literal|parameter` predicate。History Store 没有做全表时间索引，因此不允许把 AS OF 降级成无界历史扫描。revision/commit sequence 必须是正 INTEGER；目标不可见或不存在返回 `not_found`。

snapshot 的 system fields 保留历史 `revision`、`commit_sequence`、`schema_version` 和 `row_state`。业务 values 通过稳定 Column ID 按当前 Catalog 名称投影；rename 不改写历史。

## SHOW HISTORY

```sql
SHOW HISTORY FROM work.notes FOR ROW :row_id LIMIT 20;
```

Row ID 必须满足 `RELATION_ID`，LIMIT 为 1–1000。结果按最新 revision 优先，列固定为：

```text
row_id, revision, commit_sequence, schema_version, operation
row_state, actor, source, reason, updated_at
```

SHOW 不回传完整 values；需要内容时再用 AS OF 精确读取。超过 LIMIT 时 statement 和顶层 envelope 标记 `truncated = true`。

## RESTORE

```sql
RESTORE work.notes ROW :row_id TO REVISION :target_revision;
```

RESTORE 是写语句，request 必须提供：

- `expected_schema_version`；
- 当前 Row 的 `expected_revision`；
- 1–1000 的 `max_affected_rows`；
- 建议显式提供 `actor/source/reason` provenance。

目标 snapshot 重新按当前 Schema 校验，然后以新的 `COMPENSATE` revision 提交。它可以从 tombstone 恢复 live Row，但不能降低 current revision、覆盖 history 或绕过 guard。

RESTORE 参与普通 Batch 原子性：显式事务中后续写失败时，当前 Row、COMPENSATE history 和 commit sequence 一并回滚；对应 result 标记 `rolled_back`。

## 参数与安全

AS OF、SHOW HISTORY 和 RESTORE 的 row ID、revision、sequence、LIMIT 都是 AST expression，只接受不引用 Row field 的 literal/parameter。参数不会重新进入 Lexer，provenance 也不会拼接到 SQL。

## 关联

- [History Store v1](../data/history-store-v1.md)
- [MSQL Batch 与事务边界 v1](./msql-batch-transactions.md)
- [MSQL Result Envelope v1](./result-envelope.md)
