# Checkpoint Publish v1

状态：F86b 已完成；冻结 Page durability barrier 后的恢复起点。

> **已实现，但未接线。** `SegmentSet.Roll`／`PublishCheckpoint`／`LatestCheckpoint`／
> `Reclaim` 在生产代码里**零调用方**——生产对 `wal` 包只用 `OpenSegmentSet`／
> `CreateSegmentSet`／`RecoverSegmentSet`。因此 WAL 实际上**从不滚段、从不
> checkpoint、从不回收，无界增长**。
> 本文描述的协议正确且有测试覆盖，但**不描述运行时行为**。
> 判据与建议动作见[架构审计](../development/architecture-audit-2026-08.md) §1.1，
> 风险已登记在[已知风险](../development/known-risks.md)。

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
