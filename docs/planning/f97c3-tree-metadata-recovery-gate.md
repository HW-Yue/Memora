# F97c3 Tree Metadata Recovery 开工与完成门

状态：已完成，PASS；授权来自 2026-07-31 后续 Feature 持续执行指令。

## 唯一结果

committed root/allocator redo 可按 bootstrap → Page → control-last 顺序严格、幂等恢复。

## 产品门

- 用户故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 依赖：F81–F97c2 已完成；
- 规格：[Tree Metadata Recovery v1](../storage/tree-metadata-recovery-v1.md)；
- 明确不做：生成 mutation、在线 durable commit、业务 key、MVCC 和 Page 复用。

结论：PASS。F97c3 只把已提交 redo 应用到 Page/Tree control；F97d 才产生 WAL 并在线发布。

## RED Matrix

- 当前 committed `root`/`allocator` 返回 `ErrUnsupportedRedo`；
- bootstrap control durable → root Page → committed control、root grow/shrink、
  allocator advance、retired Page；
- bootstrap Write/Sync、普通 Page、control-last Write/Sync 各 fault 后重复恢复收敛；
- reopen、checkpoint 后 `RecoverSegmentSet`、重复恢复与更高 generation 跳过；
- bad control identity/generation/root/high-water、乱序/重复 metadata；
- allocator 缺 Page init、retired 缺 free Page、root 指向 retired/越过 high-water；
- 任一 validation/corruption 整个事务零写入；
- targeted/full race 与真实 Page Manager reopen。

## 完成门

- `go test -count=20 ./internal/store/wal -run 'TestRecover.*(Root|Tree)'`；
- `go test ./...`、`go test -race ./...`、`go vet ./...`；
- `gofmt`、`git diff --check` 与 `./scripts/ci.sh`；
- 完成证据、计划状态和独立原子 commit 同步。

## 完成证据

- bootstrap、grow/shrink、allocator advance、retired Page、重复恢复与更高 generation
  均通过；
- bootstrap Write/Sync、普通 Page Write、Page 预发布 Sync、control-last Write/Sync
  的逐点故障注入均在重试后收敛；
- validation/corruption 覆盖坏 control、世代/identity/high-water、metadata 顺序、
  缺 Page init/free 与 root 越界，失败保持零写入；
- 真实 Page Manager + Segment Set 通过 checkpoint、close、reopen 后恢复；
- `go test -count=20 ./internal/store/wal -run 'TestRecover.*(Root|Tree)'`、全仓
  `go test`、全仓 `-race`、`go vet`、format、`git diff --check` 与
  `./scripts/ci.sh` 全部 PASS。
