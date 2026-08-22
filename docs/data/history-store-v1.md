# History Store v1

状态：F17a–F17c 追加、Row API 和 MSQL 历史链路已实现。

> **目标形态已改。** 本文描述的"History 是一种系统对象、每个 Row 版本一条独立记录"
> 已被[写入形态](../product/write-model.md)取代：**history 独立成表**——每张业务表配
> 一张 history 表，同样是一棵 B+ 树，键为 `(row_id, 序号)`，读一行的完整历史是一次
> **范围扫**；业务行带 `history_id` 直接跳到最近一次改动。
> 本文仍如实描述**当前代码**（`ObjectKindHistory` 扁平记录），在实现改完之前可以照它
> 读代码，但**不能作为新开发的设计依据**。

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

## 读取与补偿

Row Service 和 transaction scope 提供：

- `AsOfRevision` 精确读取指定 object revision；
- `AsOfCommit` 读取不晚于指定 commit sequence 的最近 revision；
- `HistoryPage` 按最新 revision 优先返回 1–1000 条并报告 `has_more`；
- `Restore` 从指定 revision 创建新的 `COMPENSATE` revision。

AS OF snapshot 按稳定 Column ID 通过当前 Catalog 投影，因此 rename 后使用新名称但不搬迁历史。Restore 同时校验当前 expected schema/revision，并把旧 snapshot 重新按当前 Schema 验证；新增的非 nullable Column 缺值等不兼容情况必须失败。

补偿可以恢复逻辑删除 Row，但只能新增 revision，不能删除或覆盖既有 history。`AS OF`、`SHOW HISTORY` 和 `RESTORE` 的语法与有界结果见 [MSQL History v1](../query/msql-history.md)。

## 关联

- [Row Store v1](./row-store-v1.md)
- [MSQL Mutation Executor v1](../query/msql-mutation.md)
- [MVCC、Undo Log 与 Redo Log](../storage/mvcc-undo-redo.md)
