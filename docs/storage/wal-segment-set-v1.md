# WAL Segment Set v1

状态：F86a 已完成；冻结 checkpoint 之前的多 Segment 顺序边界。

> **已实现，但未接线。** `SegmentSet.Roll`／`PublishCheckpoint`／`LatestCheckpoint`／
> `Reclaim` 在生产代码里**零调用方**——生产对 `wal` 包只用 `OpenSegmentSet`／
> `CreateSegmentSet`／`RecoverSegmentSet`。因此 WAL 实际上**从不滚段、从不
> checkpoint、从不回收，无界增长**。
> 本文描述的协议正确且有测试覆盖，但**不描述运行时行为**。
> 判据与建议动作见[架构审计](../development/architecture-audit-2026-08.md) §1.1，
> 风险已登记在[已知风险](../development/known-risks.md)。

## 目录与身份

一个 Segment Set 使用独占目录，v1 文件名固定为：

```text
segment-00000000000000000001.wal
segment-00000000000000000002.wal
```

- Segment ID 从 1 开始连续递增；
- 第一段使用调用方给定的 start LSN；
- 下一段 start LSN 等于上一段 Next LSN，其 64-byte Header 继续占用全局 LSN；
- reopen 按文件名排序并逐一校验 Header identity、start LSN、Record/transaction；
- 缺号、重复、未知目录项、错 Header 或损坏 Segment 都拒绝打开，不猜测顺序。

## 写入与 Roll

Segment Set 只有一个 active Segment 和一个单 writer：

- `Commit` 复用 F84 change/commit/Sync，transaction ID 在整个 Set 内不得重复；
- `Roll` 只允许在 active Segment 至少有一个 durable commit 且没有未提交尾部时执行；
- Roll 先确认 active durable，再以连续 ID/start LSN 创建并同步新 Segment；
- 新 Segment 创建失败时 active Segment 保持可写，不发布半成品 active 状态；
- transaction 永不跨 Segment，显式 Roll 不按大小自动触发。

跨 Segment scan 按 Segment ID 和 WAL 顺序返回 F84 已验证事务。Set Close 关闭全部
Segment；closed 后 Commit/Roll/scan 返回稳定错误。

F86a 不写 checkpoint、不选择 recovery 起点、不删除 Segment，也不实现大小阈值、
后台 rolling、Group Commit、Page flush 或 Buffer Pool。
