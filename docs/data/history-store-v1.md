# History Store v1

状态：F17a 追加式存储已实现；F17b 将接入 AS OF、SHOW HISTORY 和补偿撤销。

## 目的与边界

History Store 保存已提交 Row 的长期语义 revision，用于解释“谁、从哪里、为什么改了什么”。它是权威数据，不是事务 Undo，也不参与 Undo Purge。原型通过 `Store`/`Tx` 持久化 `memora.history/v1` 逻辑记录，不暴露 SQLite 表或文件编码。

每条记录保存修改后的完整 Row snapshot：

- 稳定 `database_id + table_id + row_id`；
- `schema_version + revision + commit_sequence`；
- `INSERT | UPDATE | DELETE | COMPENSATE` operation；
- `row_state`、按稳定 Column ID 编码的完整 values；
- `actor + source + reason` provenance；
- Row created/updated 时间和 history recorded 时间。

完整 snapshot 让 AS OF 和补偿不依赖字段 diff 链。Table/Column rename 不改写旧记录；F17b 投影时再通过当前或指定 Schema 解释稳定 ID。

## 原子提交顺序

一个 Row transaction 第一次写入时，在同一个 Store transaction 内递增 Instance 级 `commit_sequence`。该 transaction 的全部 Row history 共用此 sequence：

```text
reserve commit sequence
→ write current Row
→ append immutable history record
→ update per-Row revision index
→ Commit
```

任何一步失败或显式 Rollback 都撤销当前 Row、History、revision index 和 sequence 分配，不留下孤立历史或空洞。下一个已提交事务可以复用被回滚的候选 sequence。

不同 Row 的 object revision 独立递增；同一事务可产生多个相同 commit sequence 的 history record。两者不能互相替代。

## 兼容与 provenance

F17 之前的原型 Row 可能没有 `commit_sequence` 或早期 history。读取仍允许 `commit_sequence = 0`；首次后续修改从当前 Row revision 继续追加，不伪造缺失的旧历史。逻辑 snapshot 迁移会显式标记该历史缺口。

直接 Row API 未提供 provenance 时使用明确的技术默认值 `system:direct-api / direct-api / row mutation`，避免空字段。MSQL mutation 可以通过结构化 options 传入 actor、source 和 reason；参数值不得拼入 provenance 或 SQL source。

## F17b 读取边界

待接入：

- 按 revision 和 commit sequence 的 AS OF；
- SHOW HISTORY 的有界、结构化 provenance；
- 从任意历史 snapshot 创建新的 COMPENSATE revision；
- 删除后的恢复和当前 Schema 校验。

补偿只能新增 revision，不能删除或覆盖既有 history。

## 关联

- [Row Store v1](./row-store-v1.md)
- [MSQL Mutation Executor v1](../query/msql-mutation.md)
- [MVCC、Undo Log 与 Redo Log](../storage/mvcc-undo-redo.md)
