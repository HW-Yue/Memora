# F84 WAL Durable Transaction 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：事务只在 change + commit 全部写入且 WAL Sync 成功后返回成功；
- 依赖：F83 WAL Record Stream 已完成；
- 架构：单 writer、连续事务、commit digest、失败后 poisoned，见
  [WAL Durable Transaction v1](../storage/wal-durable-transaction-v1.md)；
- 明确不做：Page apply、reader publish、recovery、checkpoint、Group Commit；
- AI/MSQL/Route 不接触物理 WAL，无 Vector/Provider/SQLite；
- 用户执行授权：2026-07-31；
- 开工前结论：PASS。

## RED

`go test ./internal/store/wal` 必须因 transaction writer 明确未实现而失败：

- commit→Sync→receipt 与 reopen scan；
- Sync/write fault 不得返回成功，durable LSN 不得虚假前进；
- duplicate/invalid/interleaved transaction 拒绝；
- 未提交尾部忽略，commit digest/count/first LSN 篡改拒绝；
- concurrent callers 仍生成连续、不交错事务。

实际 RED：全部目标因 `WAL durable transaction is not implemented` 失败，未使用
编译失败或坏 fixture。

## 完成证据

- targeted、`go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh`：format、vet、unit、race、integration、e2e、cross-build 全 PASS；
- 两条 change 与 commit 的三个 write fault point、Sync failure：全部不返回成功，
  durable LSN 不前进，Segment 进入 poisoned；
- commit/reopen/scan receipt、重开 duplicate ID、32 concurrent callers：PASS；
- invalid input、未提交尾部、interleaving、count/first LSN/digest 篡改：全部按契约
  忽略或拒绝；
- 未包含 F85 redo apply、recovery、Page 修改或 checkpoint。

完成后结论：PASS。
