# F85 Crash Recovery 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：重启只把完整已提交的 Page redo 幂等写回 Page Store；
- 依赖：F81 Page Codec、F82 Page File Manager、F83 Stream、F84 Transaction 完成；
- 格式：见 [Crash Recovery v1](../../storage/crash-recovery-v1.md)；
- 明确不做：checkpoint/回收、Buffer Pool、reader publish、B+ Tree、root/allocator；
- torn Page 只能由 page-init/FPI 修复，delta 不猜旧内容；
- 用户执行授权：2026-07-31；
- 开工前结论：PASS。

## RED

`go test ./internal/store/wal` 必须因 crash recovery 未实现而失败：

- committed page-init/delta → Sync → close/reopen 后恢复；
- 未提交尾部不应用，重复恢复不改坏 Page；
- Page LSN 跳过已应用或更新的 redo；
- checksum 损坏由 FPI 修复，缺少 FPI 的 delta 拒绝；
- invalid delta/image、missing space、root/allocator 在写入前拒绝；
- Page Write 每个故障点与 Sync fault 不报告成功，重试可收敛；
- WAL digest/corruption 仍在任何 Page write 前拒绝。

实际 RED：全部目标因 `WAL crash recovery is not implemented` 失败，未使用编译失败
或坏 fixture。

## 完成证据

- targeted 连续 10 次、`go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh`：format、vet、unit、race、integration、e2e、cross-build 全 PASS；
- real Page Manager 的 init/delta、Sync、close/reopen/read 与 FPI torn Page repair：PASS；
- 未提交尾部忽略，重复恢复和较新 Page LSN 跳过：PASS；
- 两个 Page Write fault point、Sync fault、部分恢复重试：确定性收敛；
- invalid image/delta、missing space、unsupported redo、bad commit、WAL bit flip/truncate
  都在 Page write 前拒绝；
- 未包含 F86 checkpoint、WAL 回收、F87 Buffer Pool 或 root/allocator 格式。

完成后结论：PASS。
