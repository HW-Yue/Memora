# F97b2 WAL Recovery Open 开工与完成门

状态：完成，PASS；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

`OpenSegmentSet` 以最高有效 durable frontier 为唯一 authority：严格验证其前缀，
持久移除其后的 crash tail，并返回可继续 Commit、Roll 和 Checkpoint 的 writer。

## 产品门

- 用户故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 用户结果：确认提交不丢失，未确认尾部不发布，重开无需人工清理 WAL；
- 依赖：F83–F86c、F97a、F97b1 已完成；
- 影响范围：WAL Segment Set repairing open；
- 明确不做：Page/root/allocator redo、Buffer Pool、B+ Tree、业务 key 和 MVCC。

结论：PASS。该 Feature 只有“按既有 frontier 收敛 WAL 并恢复 writer”一个结果，
不新增 durable authority，也不接 Page 恢复。

## 冻结协议

1. 先验证目录项、retained manifest、frontier 双槽及截至 frontier Segment 的连续
   identity；未知目录项、Segment 缺号或 authority 缺失直接报 corruption。
2. frontier 以内按精确 byte boundary 严格验证 Record CRC/LSN、事务 count/digest、
   transaction ID 唯一性及 checkpoint 序列。
3. authority 校验全部通过前禁止 truncate、remove 或 sync；前缀损坏失败后所有文件
   byte-for-byte 不变。
4. frontier Segment 超长时截到精确 offset 并 Sync；其后的连续 Segment 按 ID
   从高到低删除，随后 Sync 目录。
5. frontier 后内容不作为证据，不解析其 Record 或 Header；完整 commit、partial
   record 和未完整创建的新 Segment 都按 tail 丢弃。
6. truncate、file Sync、remove 或 directory Sync 失败返回
   `ErrRecoveryRequired`；重试以同一 frontier 继续并最终收敛。
7. 修复完成后按 authority 状态建立 writer；重复重开不再修改文件。

## RED Matrix

| RED | 当前失败 |
| --- | --- |
| partial change/commit tail 自动截断 | `OpenSegmentSet` 返回 corruption/poisoned |
| 完整但未发布 commit 被丢弃 | open 返回 `ErrPoisoned` |
| roll publish 前的新 Segment 被删除 | open 返回 `ErrPoisoned` |
| frontier 前 bit flip/truncate 零修复拒绝 | 需证明 corruption 失败无任何写入 |
| truncate/file Sync/remove/dir Sync 可重试 | open 没有 repairing I/O 或稳定错误 |
| retained/reclaim 后 tail 收敛 | open 不能越过 frontier 后 Segment |
| 修复后 Commit/Roll/Checkpoint 可写 | open 无法返回 writer |

## 完成证据

- targeted WAL tests 与 F97b1 全部回归；
- 每个 repairing I/O fault point 注入失败并重复 reopen；
- subprocess crash-tail fixture；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- `gofmt` 与 `git diff --check`；
- 规格、计划和完成状态同步后形成一个原子 commit。

实际结果：

- complete/uncommitted、partial header、partial payload、完整未发布 commit 均被截到
  frontier，已确认事务保持且 writer 可继续 Commit、Roll、Checkpoint；
- frontier 前 bit flip、truncate、commit digest、Record LSN 损坏均返回
  `ErrCorrupt`，带 speculative tail 时仍证明所有文件 byte-for-byte 不变；
- truncate、file Sync、descending remove、directory Sync 故障均返回
  `ErrRecoveryRequired`，重复 open 收敛并恢复可写；
- 未发布 Segment 从高到低删除；Segment 缺号零修复拒绝；retained/reclaim layout 与
  subprocess crash tail 均 PASS；
- `go test -count=20 ./internal/store/wal`：PASS；
- `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS。
