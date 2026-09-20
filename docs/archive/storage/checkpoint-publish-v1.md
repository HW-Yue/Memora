# Checkpoint Publish v1

状态：F86b 已完成；冻结 Page durability barrier 后的恢复起点。

> **已接线（2026-08-25）。** `SegmentSet.Roll`／`PublishCheckpoint`／
> `Reclaim` 由 `pagestoremigration` 的 `maintainRedoLog` 在每次成功写入之后调用：
> 活跃段超过 `walSegmentRollBytes`（4 MiB）就滚段、发 checkpoint、回收旧段。
> 生产 barrier 见 `generation.go` 的 `redoBarrier`。
> 因此本文描述的协议**现在也描述运行时行为**。
> 缘由与分阶段见[共享循环 redo log](./shared-circular-redo-v1.md)。

## 发布顺序

Checkpoint 只由 Segment Set 单 writer 发布：

```text
latest durable transaction LSN
→ FlushThrough(recovery_lsn)
→ append checkpoint Record
→ WAL Sync
→ publish Checkpoint receipt
```

`FlushThrough` 成功证明所有 `page_lsn < recovery_lsn` 的 Page 已安全落盘。barrier
失败时不能写 checkpoint；checkpoint Record write/Sync 失败时不能发布新恢复起点，
active Segment 进入 poisoned。

没有比上一 checkpoint 更新的 committed transaction 时拒绝重复发布。Checkpoint
可以写入刚 Roll 出的空 active Segment，因为 recovery LSN 可以位于上一 Segment。

## Payload 与 Receipt

Checkpoint Record 使用 `transaction_id/space_id/page_id = 0`，Payload 固定 32 bytes：

```text
magic[4] = "MCHK"
version u16 = 1
size u16 = 32
checkpoint_id u64
recovery_lsn u64
covered_segment_id u64
```

checkpoint ID 从 1 连续增长。recovery LSN 是最后一个被 barrier 覆盖的 transaction
exclusive durable LSN；covered Segment 是该 transaction 所在 Segment。Receipt
另含 checkpoint Record LSN 和 checkpoint Sync 后的 durable LSN。

## Recovery

reopen 必须校验所有 checkpoint 的 ID、LSN、Segment identity 和单调性。存在
checkpoint 时，Segment Set recovery 只重放 `transaction durable_lsn >
recovery_lsn` 的事务；没有 checkpoint 时从第一段开始。F85 的 Page LSN 幂等与 FPI
规则不变。

F86b 不删除或截断任何 Segment，不实现 F86c reclaim、自动 checkpoint 周期、
Buffer Pool cleaner 或后台 rolling。
