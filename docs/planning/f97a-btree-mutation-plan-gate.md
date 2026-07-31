# F97a B+ Tree Mutation Plan 开工与完成门

状态：完成，PASS；F97b 尚未 Review 或授权。

## 产品门

- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 唯一结果：任意深度 byte-key upsert/delete 生成零共享写入的确定性私有 Page 计划；
- 用户价值：为后续 durable commit 提供可整体提交或丢弃的物理 write set，事务失败不再
  依赖撤销已经污染的共享 Page；
- 标准 MSQL 旅程：沿用 F97 Review 中的 guarded UPDATE/COMMIT/reopen/SELECT；本项只
  验收该旅程的 B+ Tree 私有 mutation 计算，不切换 Executor；
- 作用域：物理 B+ Tree Page 与私有 allocator high-water；不改变 Row revision、
  History、Route、Schema、Result Envelope 或 AI 上下文预算；
- 依赖：F90–F96 已完成且当前 `go test ./internal/store/...` 通过；
- 契约：[B+ Tree Mutation Plan v1](../storage/btree-mutation-plan-v1.md)；
- 架构：复用既有 Page/Node codec 与纯 mutation，不引入新后端、Provider、Vector、
  SQLite fallback 或 Agent 旁路；
- 明确不做：WAL、Buffer Pool、root/control Page、文件 I/O、业务 key、MVCC 与锁；
- 开工前结论：PASS。

## RED 入口

已在新增 `internal/store/btree/plan_test.go` 和占位 API 后运行：

```text
go test ./internal/store/btree -run TestMutationPlanner
```

首个 RED 编译成功，并由占位 API 返回稳定 `ErrMutationPlanNotImplemented`；失败原因是
Planner 能力缺失，不是 undefined symbol、坏 fixture 或随机顺序。占位错误未保留在
最终实现中。

## RED matrix

- `TestMutationPlannerUpsertSingleLeaf`：insert/replace、after-image、expected LSN；
- `TestMutationPlannerUpsertPropagatesSplitToNewRoot`：leaf/internal 递归 split、连续 ID；
- `TestMutationPlannerDeleteRepairsAndShrinksRoot`：right-first、left fallback、级联与 retire；
- `TestMutationPlannerMultipleOperationsUseOverlayAtomically`：同事务多 mutation、单次失败
  回到调用前私有状态、整体可丢弃；
- `TestMutationPlannerRejectsCorruptionAtomically`：space/generation/level/cycle/boundary/
  leaf-link/duplicate child、Reader fault、depth、ID overflow；
- `TestMutationPlannerMatchesSeededReferenceModel`：固定 seed 交错操作，每步排序 map、
  leaf chain、separator、可达/allocated/retired 对拍；
- `TestMutationPlannerDeepCopiesAndIsDeterministic`：输入、Reader Page、Plan 输出零 alias，
  相同输入 byte-for-byte 相同计划。

## GREEN 与 REFACTOR 边界

- GREEN 只新增 Planner、局部 path/overlay/allocator helper 和必要测试；
- 复用 F93–F96，不复制第二套 node codec、split 或 rebalance 算法；
- 每个新错误分支都有 atomicity/boundary case；
- GREEN 后才整理 path frame、overlay clone 与 invariant checker；
- 不为 F97b–F97d 预埋 WAL/control Page API。

## 完成门

```text
go test -count=20 ./internal/store/btree
go test -race ./internal/store/btree
go test ./...
go test -race ./...
go vet ./...
./scripts/ci.sh
```

还必须保存 reference-model seed，证明多层随机状态序列、错误原子性、确定性与深复制；
完成文档、计划状态和实际 commit 同步后才能标记 PASS 并 Review F97b。

## 完成证据

- 固定 reference-model seed：`9701`，240 步交错 upsert/delete，每步校验完整排序 map、
  reachable Page、leaf chain 和 exact lookup；
- 覆盖 leaf/internal 递归 split、root grow、right-first/left fallback、级联 merge、root
  shrink、transient allocation、Reader fault、corruption、overflow、深复制与确定性；
- `go test -count=20 ./internal/store/btree`：PASS；
- package/full `go test` 与 `-race`、`go vet ./...`、`./scripts/ci.sh`：PASS；
- CI 的 format、unit、race、integration、e2e、darwin arm64/amd64 cross-build：PASS。

## 授权

- F97a 规格 Review：PASS；
- 用户执行授权：已取得并完成；
- F97b–F97d 不在本次授权范围内。
