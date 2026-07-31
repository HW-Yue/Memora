# F97b WAL Recovery Open 拆分 Review

状态：REVISE；发现 durable frontier 前置缺口，待用户确认语义与拆分，未授权实现。

## 产品门

- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 原目标：active Segment 的 crash tail 自动收敛到最后 durable 边界并恢复可写；
- 用户结果：已确认提交不丢失，未确认尾部不发布，重开不需要人工删除 WAL；
- 作用域：WAL Segment Set 的 durable authority 与 repairing open；
- 明确不做：Page/root/allocator redo、B+ Tree、Buffer Pool、业务 key、MVCC；
- 依赖：F83–F86c、F97a 已完成。

## 阻断证据

当前 Segment 只把 `durableLSN` 保存在内存。重开会把文件中所有完整 Record 都设为
durable；`OpenSegmentSet` 遇到完整 uncommitted tail 返回 `ErrPoisoned`，遇到 partial
Record 直接返回 `ErrCorrupt`。

仅凭 WAL 字节无法区分：

| 磁盘尾部 | 可能来源 | 直接接受 | 直接截断 |
| --- | --- | --- | --- |
| 完整 change + commit | fsync 成功但响应丢失；或 fsync 未确认但页缓存仍保留 | 可能发布未确认事务 | 可能丢已提交事务 |
| commit/change CRC 或长度损坏 | 未提交 crash tail；或已提交区域损坏 | 必须拒绝 | 无 authority 时可能掩盖已提交损坏 |

因此“严格校验 committed prefix、只截 durable frontier 之后的字节”需要独立的持久化
frontier。当前格式没有该证据，原 F97b 不能直接进入 RED。

## 必需语义修正

- validation 或写入前错误：确定未提交；
- WAL/control I/O 开始后的错误：返回 `commit outcome unknown / recovery required`，不宣称
  rollback；
- 只有 WAL Sync 与 frontier publish 都成功才返回 commit success；
- 重开以最高有效 frontier 为 authority：frontier 内完整提交必须保留，之后字节必须丢弃；
- “调用方收到错误”不能等同“事务一定 abort”，因为 fsync/响应失败存在不可判定窗口。

这取代 F97 总门中“所有失败事务永不发布”的过强表述；准确边界应是“未到 commit
decision 的事务不发布；decision I/O 错误返回 outcome unknown，由 recovery 决定”。

## 建议拆分

| Feature | 唯一主要结果 | 独立 RED | 明确不做 |
| --- | --- | --- | --- |
| F97b1 Durable WAL Frontier | 每次成功的 Commit/Checkpoint/Roll 都发布双槽 durable byte boundary | WAL 已 Sync 但 control 未发布时仍被普通 reopen 当作 durable | 截尾、Page redo |
| F97b2 Repairing Open | 严格校验 frontier 内字节，持久截断/删除 frontier 后 tail，并恢复 writer | partial/uncommitted active tail 仍使 open 失败 | frontier 写入、root redo |

候选协议见 [Durable WAL Frontier v1](../storage/wal-durable-frontier-v1.md)与
[WAL Recovery Open v1](../storage/wal-recovery-open-v1.md)。两个 Feature 分别 Review、
授权、实现、验收和合入；F97c 继续等待二者完成。

## 决定门

- 原 F97b：REVISE，不实现；
- 推荐：批准 outcome-unknown 语义及 F97b1/F97b2 拆分；
- 当前授权：仅完成本 Review，未取得任一实现授权。
