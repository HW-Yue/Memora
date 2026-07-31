# F97d3 Durable Tree Runtime 开工与完成门

状态：已完成，PASS；授权来自 2026-08-01“执行完全部讨论 Feature”的持续指令。

## 产品门

- 故事：`US-ENGINE`、`US-RECOVER`、`US-CORRECT`；
- 唯一结果：单 writer 把 Tree plan 变成 WAL-durable、Buffer 原子可见的 commit；
- 规格：[Durable Tree Runtime v1](../storage/durable-tree-runtime-v1.md)；
- 依赖：F97d1/F97d2 已完成；
- 明确不做：Catalog/Row key、F98 索引、checkpoint、snapshot、对象锁；
- 开工前结论：PASS。

## RED Matrix

- 首次 Open 持久化 bootstrap control，失败不返回 Runtime；
- Commit 顺序固定为 prepare/preflight → WAL durable → batch publish；
- WAL 返回的 record LSN 正确写入 changed/new/free/control Page，control 最后；
- invalid plan、duplicate transaction 与 preflight conflict 零 WAL/零 Buffer 变化；
- WAL outcome unknown 和 poisoned WAL 使 Runtime poison；
- WAL 已 durable 后 batch capacity/conflict/fault 使 Runtime poison；
- poison 后拒绝继续 Commit，旧 state 不冒充已提交 state；
- crash 在 WAL durable 后、Buffer publish/flush 前，reopen recovery 得到同一 root/
  allocator/revision，并可继续下一次 Commit；
- 64 个并发同 base plan 确定性串行：一个成功，其余作为 stale plan 拒绝，WAL 不交错；
- `Read` 在 publish barrier 下只见完整旧/新 batch；
- flush fault 保持 dirty 并可重试，不错误 poison Runtime。

## 完成门

- 先保留缺少 Runtime 能力导致的明确 RED，再实现最小 GREEN；
- targeted `-count=20`、treecommit/wal/buffer race；
- 全仓 test/race/vet、format、`git diff --check` 与 `./scripts/ci.sh`；
- corruption、outcome unknown、reopen、fault injection 与 reference state 全有证据；
- 文档与完成证据同步，独立 commit 合入 `main` 后才进入 F98。

## 完成证据

- 首次 Open 将 bootstrap control 写入并 Sync；Sync fault 不返回 Runtime，重试收敛；
- changed/new/free/control 使用 WAL 实际分配的 record LSN，Buffer publish 保持
  control-last；
- invalid/stale plan 与 new-page collision 零 WAL，duplicate transaction 不 poison；
- outcome unknown、poisoned WAL 和 durable 后 publish failure 均 poison，后续 commit
  稳定拒绝；
- WAL durable 后未 flush 的进程重开由 recovery 恢复相同 root/allocator/revision，
  并能继续下一 revision；
- 64 个并发同 base plan 只有一个成功，其余 stale，race 下无 WAL 交错；
- flush write fault 保留 dirty，retry 后清空且 Runtime 可继续 commit；
- targeted `-count=20`、treecommit/wal/buffer race、全仓 test/race/vet 与
  `./scripts/ci.sh` 全部 PASS。

完成后结论：PASS。
