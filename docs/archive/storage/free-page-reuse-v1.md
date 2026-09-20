# Free Page Reuse v1

状态：F152 已实现；由冻结目录 churn 门实测触发。

## 触发证据

320 个 Database 的 Catalog B+ Tree 删除末尾 10% 并加入等量新 Database。实现前物理
Page 从 9 增至 11，浪费 22.22%，超过冻结的 20% 门。实现后同一 workload 保持 9 Page。

## 分配顺序

B+ Tree Mutation Planner 需要新 Page 时严格按以下顺序选择：

1. 本计划先前 rebalance 后已退休的最小 Page ID，标记为 `recycled`；
2. Runtime 从已提交 `free` Page 扫描得到的最小 Page ID，标记为 `reused`；
3. 没有可复用 Page 时才推进 allocator high-water，标记为 `allocated`。

同一计划 recycled Page 开始和结束都是可达 B+ Tree Page，不写中间 free image。
durable reused Page 使用 full-page image 覆盖原 free Page，必须带原 Page LSN 作为冲突前置。

## 持久化与恢复

- retirement 仍在原事务写入带 WAL LSN 的 `TypeFree` full-page image；
- Runtime 只在 WAL recovery 完成后扫描 `[FirstDataPageID, NextPageID)`，重建内存 free set；
- reusable Page 必须同 space/generation、ID 合法、类型为 free 且 LSN 非零；
- commit publish 成功后才从 free set 删除 reused、加入 retired；失败不改变集合；
- 重启不依赖新的 control/WAL 格式，free Page image 就是持久化真相。

## 故障边界

错误类型、错误 generation、重复/乱序 ID、未产生 Page change、不是 Runtime free set 成员
或 expected LSN 冲突都在 publish 前失败。WAL 已 durable 但 Buffer publish 失败仍沿用
Runtime poison + reopen recovery，不增加旁路修复。

## 关联

- [B+ Tree Mutation Plan v1](./btree-mutation-plan-v1.md)
- [Tree Commit Preparation v1](./tree-commit-preparation-v1.md)
- [Tree Metadata Recovery v1](./tree-metadata-recovery-v1.md)
