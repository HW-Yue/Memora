# Tree Metadata Recovery v1

状态：F97c3 已实现并验收，PASS；依赖 F97c1/F97c2。

## 唯一结果

已提交 `root`/`allocator` redo 可严格、幂等恢复到
[Tree Control v1](./tree-control-v1.md)，普通 Page 先写，control Page 最后发布。

## Validation

- 每个 Tree transaction 必须恰有一个最后出现的 root redo；
- allocator redo 可选，但若存在必须位于 root 前且 generation 与 root 一致；
- `[expected_next, next)` 每个 Page 都必须有更早的 B+ Tree `page-init`；
- 每个 retired Page 必须在同一事务被写成 `free` Page，不立即复用；
- root 必须小于 final allocator high-water，且不能属于 retired；
- validation 失败时事务零 Page 写入。

## Recovery 顺序

首次 recovery 若 Page 1 不存在，先写入 bootstrap control 并 Sync，保证真实 Page
Manager 可连续分配 Page 2；随后写普通 Page 并 Sync，再覆盖 committed control 并再次
Sync。普通 Page 与 control 之间的持久化屏障是 root-last 的必要条件，不能只依赖写入
调用顺序。

exact generation/state/LSN 可跳过；磁盘 generation 更高时旧 metadata redo 幂等跳过；
generation 缺口、相同 generation 不同状态或坏 control 一律 corruption。I/O 失败不
报告事务恢复完成，重复执行必须收敛。

## 明确不做

F97c3 不生成 B+ Tree mutation、不定义在线 WAL→Page→root commit API、不接业务
key space、不复用 free Page。运行时 durable commit 属于 F97d。
