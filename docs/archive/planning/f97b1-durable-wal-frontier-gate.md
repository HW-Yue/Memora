# F97b1 Durable WAL Frontier 开工与完成门

状态：完成，PASS；后续 F97b2 也已完成。

## 产品门

- 唯一结果：Segment Set 用双槽 control 持久发布可信 durable byte boundary；
- 用户价值：重开能区分已确认 WAL 与碰巧留在文件中的 speculative tail；
- 目标故事：`US-RECOVER`、`US-ENGINE`、`US-DEVELOPER`；
- 依赖：F83–F86c、F97a 已完成；
- 语义：validation/写前错误确定未提交；WAL/control I/O 开始后的错误返回
  `ErrOutcomeUnknown` 并 poison，重开按最高有效 frontier 判定；
- 格式：[Durable WAL Frontier v1](../../storage/wal-durable-frontier-v1.md)；
- 明确不做：截尾/删除 speculative WAL、Page/root/allocator redo、B+ Tree、Buffer Pool；
- F97b2 不属于本 Feature 的实现范围；F97b1 开工前结论：PASS。

## RED 入口

先新增可编译占位 API 与 `internal/store/wal/frontier_test.go`，运行：

```text
go test ./internal/store/wal -run TestDurableFrontier
```

首个 RED 编译成功并由占位 API 返回稳定 `ErrFrontierNotImplemented`，证明缺少持久
frontier；占位错误未保留在最终实现中。

## RED matrix

- create golden：两个 4 KiB control、generation 1、Segment 1 Header boundary；
- Commit：WAL Sync → inactive slot write/Sync → success，reopen frontier 与 receipt 一致；
- Checkpoint/Roll：frontier 单调覆盖 checkpoint Record 与新 active Header；
- validation/create failure 不推进 frontier，保持可写；
- WAL partial write/Sync、slot partial/short write/Sync 返回 outcome unknown 并 poison；
- slot bit flip、unknown version、错 size/identity/LSN、同 generation 冲突与双槽损坏；
- frontier 前/后 file size、Segment ID 与 retained manifest/reclaim 组合；
- response-loss subprocess 与固定 fault seed；并发 Commit/Roll/Checkpoint 保持单调；
- control 输入输出深复制、closed 状态、全量 race。

## GREEN 与完成门

- 只新增 control codec、双槽 publish/open 与三种 durability 事件接线；
- 不实现 truncate/remove/repair，不解释 root/allocator payload；
- 每个新 I/O 分支补 fault/atomicity 测试，GREEN 后再整理 open/publish helper；
- `go test -count=20 ./internal/store/wal`、package/full race、`go vet ./...`、
  `./scripts/ci.sh` 全部 PASS；
- 文档、状态与独立 commit 同步后才 Review F97b2。

## 完成证据

- create golden、两个 4 KiB/`0600` slot、generation 交替与 reopen：PASS；
- Commit、Checkpoint、Roll 均按 WAL Sync → frontier write/Sync → success 推进；
- WAL Sync、slot write/partial/short write/Sync、checkpoint/roll publish fault 均返回
  `ErrOutcomeUnknown` 并 poison；重开由有效 generation 决定最终结果；
- frontier 之后的完整 commit 或新 Segment 返回 `ErrPoisoned`，未实现截尾；
- 双槽损坏、unknown version、短文件、generation 冲突、LSN/Segment/transaction identity
  错误均拒绝；固定 corruption seed `9711` 共 128 次；
- subprocess 在成功 Commit 后不 Close 直接退出，reopen 保留 frontier 与事务：PASS；
- `go test -count=20 ./internal/store/wal`、package/full race、全量 test、vet：PASS；
- `./scripts/ci.sh` 的 format、unit、race、integration、e2e、双架构 cross-build：PASS。

## 授权

- outcome-unknown 语义与 F97b1/F97b2 拆分：已确认；
- F97b1：已授权并完成；
- 后续 Feature 按 2026-07-31 持续执行授权逐项 Review、实现和验收。
