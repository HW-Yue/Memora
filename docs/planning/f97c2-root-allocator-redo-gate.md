# F97c2 Root/Allocator Redo 开工与完成门

状态：已批准并开工；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

versioned Tree control Page 可随 committed `root`/`allocator` WAL 事务严格、幂等恢复，
并以普通 Page 先写、control root-last 的顺序发布。

## 产品门

- 用户故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 依赖：F81–F97c1 已完成；
- 规格：[Root/Allocator Redo v1](../storage/root-allocator-redo-v1.md)；
- 明确不做：生成 Tree mutation、运行时 durable commit、业务索引、MVCC 和 Page 复用。

结论：PASS。该 Feature 只解释并恢复已提交 metadata redo；F97d 才负责产生 WAL 和
在线发布。

## RED Matrix

- 当前 committed `root`/`allocator` 返回 `ErrUnsupportedRedo`；
- bootstrap control durable → root Page → committed control、root grow/shrink、
  allocator advance、retired Page；
- bootstrap Write/Sync、普通 Page、control-last Write/Sync 各 fault 后重复恢复收敛；
- reopen、checkpoint 后 `RecoverSegmentSet`、重复恢复与更高 generation 跳过；
- unknown version、bad identity/generation/root/high-water、乱序/重复 metadata；
- allocator 缺 Page init、retired 缺 free Page、root 指向 retired/越过 high-water；
- 任一 validation/corruption 整个事务零写入；
- codec golden、seed corruption、targeted/full race。

## 完成门

- RED 因 `ErrUnsupportedRedo` 失败，随后最小实现转绿；
- `go test -count=20 ./internal/store/wal`；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- `gofmt`、`git diff --check` 与 `./scripts/ci.sh`；
- 完成证据、计划状态和独立原子 commit 同步。
