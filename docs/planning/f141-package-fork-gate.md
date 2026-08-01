# F141 Package Fork 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

已安装只读 Database 可通过 hash-bound L2 计划派生为身份独立、可写的新 Database；原安装库
保持只读且不变化，fork 保留完整 Row/History/Relation 语义并记录 merge base provenance。

## RED

- Plan 绑定源 package/snapshot、目标 Database ID/name 与确定性 remap hash，且不写 Store。
- Apply 重验源与 approval；目标冲突、stale source 或 plan 改写零写入拒绝。
- Database/Table/Column/Relation 全局身份确定性 remap；Row ID 与 revision/history 保持。
- Column-ID keyed values 与 Relation endpoints 全部同步 remap，reopen 后可读可写。
- 源 Database 的只读位与 authority 不变化。

## 完成证据

身份/value/relation remap、源隔离、fork 可写与 race 通过；全仓 CI 全绿。下一项 F142。
