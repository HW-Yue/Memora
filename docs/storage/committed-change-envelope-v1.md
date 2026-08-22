# Committed Change Envelope v1

状态：F109 已完成并验收；2026-08-01 冻结格式与提交边界。

> **目标形态已改。** 本文冻结的 envelope 就是[写入形态](../product/write-model.md)
> 三份日志里的 **change log**，它的职责被收窄为**事务回滚的 undo 依据**，
> **不参与崩溃恢复重建**——那是 binlog 的唯一职责。
> 另外 envelope 目前与 Row/Table 等正文混装在同一个 `database.memora` 记录流里，
> 新形态要求 change log 与 binlog 是两份独立的日志。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。

## 唯一结果

每次经默认语义 mutation service 成功提交的逻辑事务，都在同一个 native 原子事务中
写入一个完整、不可变的 change envelope。rollback、校验失败和 crash tail 不产生可见
envelope，split/merge 等跨对象事务也只产生一个 envelope。

F109 建立 durable event source；F113 已增加独立 Page cursor index 与 MSQL/Admin
读取协议。不得把 F109 的内部 `ListAfter` 扫描暴露成产品查询 fallback。

## Envelope

`memora.committed-change/v1` 包含：

- `transaction_id`：由 canonical envelope checksum 派生的稳定 128-bit 标识；
- `commit_sequence`：Committed Change Log 自己的全局递增 cursor；
- `committed_at`、`actor`、`source`、`reason` 和可选 Source Receipt ID；
- 按字典序去重的 Database scope；
- 一到 4096 个确定排序的 change entry；
- 覆盖 cursor、时间、metadata、scope 和全部 entry 的 SHA-256 checksum。

Change Log cursor 与 Row/Relation 的 snapshot `commit_sequence` 是两个协议：前者排序
逻辑事务，后者保持既有 AS OF 可见性。Row entry 用稳定 History locator 关联正文，
因此 Catalog/Route commit 不会改变既有 Row snapshot sequence。

## Entry

支持 Database、Table、Column、Row、Relation、Route node、Route membership 和
Configuration。每项只保存稳定 object scope、operation、before/after revision、Schema
version、History locator 与有界 related object IDs；不复制 Row 正文、prompt、模型推理、
Page offset 或 Buffer Pool 状态。

operation 为 `INSERT`、`UPDATE`、`DELETE`、`COMPENSATE`、`SPLIT`、`MERGE` 或
`RESTORE`。Entry、metadata、identity 和 encoded envelope 都有硬预算；非 canonical、
重复、错 scope、错 revision、未知字段、checksum tamper 或超预算记录均视为 corruption。

## 提交协议

```text
Authority operation gate
  → 分配 change cursor
  → 在 native Transaction 中 stage 全部业务正文/History/结构变化
  → stage 一个 ObjectKindCommittedChange envelope
  → native records fsync
  → native COMMIT + fsync
  → F107 Page index publication；失败则 poison/reopen reconcile
```

Envelope 不是第二份 Binlog 文件，也不与 native body 做两阶段提交。Page Index 当前是
业务查询 authority；Change envelope 是 immutable body source。F113 的派生 Page cursor
只保存 sequence/transaction/checksum locator，仍由 envelope body 决定事件内容。

## 覆盖与边界

默认服务已覆盖 Catalog DDL、Row insert/update/delete/restore、Route membership、
Relation、Route node、Configuration 和跨对象 split/merge。物理 Repository fixture、
engine bootstrap、迁移和 logical snapshot import 不伪装成用户事务，也不补造历史事件。
F109 启用前已有的逻辑历史不回填 synthetic envelope。

Change record 进入 F105 source inventory，所以 F108 replacement/reopen 会保留并重验它；
logical snapshot 暂不搬运观察时间线。Page change index 与 MSQL cursor 已由 F113 完成；
保留/清理、Admin timeline 页面、replication 和 PITR 分别留给后续 Feature。

## 完成证据

- canonical codec、tamper、duplicate、budget 和 corrupt record；
- business body + envelope commit/rollback 原子性及截断 COMMIT crash-tail reopen；
- Catalog/Route/Row+membership/Relation/Configuration 连续九事务 reference journey；
- cross-object mutation 一个 envelope，失败零 envelope；
- 16 个并发 writer 在 Authority gate 下得到无重无漏 cursor，`-race` 全绿；
- 带 change record 的 F108 replacement 与 authority reopen 全绿。
- `scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、e2e 和 cross-build 全绿。
