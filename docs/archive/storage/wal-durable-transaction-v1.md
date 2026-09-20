# WAL Durable Transaction v1

状态：F84 已完成；冻结单 writer 的 WAL transaction durability。

## Transaction

一个事务由 1–N 条连续 change Record 和最后一条 `commit` Record 组成：

```text
change(tx=7) → change(tx=7) → commit(tx=7) → fsync → success receipt
```

- transaction ID 必须非零，且同一 Segment 内不能重复提交；
- change Type 只允许 page-init、page-delta、full-page-image、root、allocator；
- 调用方不能预填 LSN、transaction ID 或 commit/checkpoint Record；
- 同一事务的 Record 不得与其他事务交错；
- `Append` 成功但 `Sync` 失败时绝不返回成功，Segment 进入 poisoned 状态；
- poisoned Segment 拒绝继续 append/commit，必须关闭并由后续恢复流程处理。

## Commit Payload

Commit Payload 固定 56 bytes：

```text
magic[4] = "MTXN"
version u16 = 1
size u16 = 56
record_count u32
reserved u32 = 0
first_lsn u64
sha256(change Record exact bytes)[32]
```

digest 覆盖每条 change Record 的完整编码字节，包含其 LSN 和 CRC32C，不包含 commit
Record。扫描时 count、first LSN、transaction ID、连续性和 digest 必须全部匹配。

## API

- `NewTransactionWriter(segment)` 扫描已有 commit，建立已使用 transaction ID 集合；
- `Commit(id, records)` 在 Segment 单锁内追加 change、commit 并调用一次 Sync；
- 只有 Sync 成功才返回 Receipt；
- Receipt 包含 transaction ID、first/commit/durable LSN、change count 和 digest；
- `ScanCommitted` 只返回完整且 digest 正确的事务；
- 尾部完整但没有 commit 的 change Records 不视为已提交，也不返回给恢复层。

F84 不把 WAL 变化应用到 Page，不发布 reader view，不实现 checkpoint、redo recovery、
Group Commit、segment rolling 或 Change Log。
