# Phase D：先闭环，再增强正确性

目标：每一步都能写进去并取出来，避免先堆大量底层模块、最后才发现无法接通。

## 已有状态

F44–F50 的发行与机械测试已有实现。F51 因 Vector/cosine 和非逐层查询路径撤销。
SQLite 仍是当前运行后端，只作为后续迁移来源；在原生闭环和迁移验证前不删除。

## F52 原生 Record Put/Get（已完成）

目标故事：`US-ENGINE`、`US-DEVELOPER`。

闭环：创建 `database.memora` → Put 一个 Record → Close → Reopen → 按 stable ID
Get → payload 完全一致。

先测：golden bytes、Unicode、空 payload、多 ID、重复 ID、损坏 Header/长度/CRC、
未知版本和 fuzz decode。

开发：实现 32-byte File Header、24-byte Record Header、append、open scan、
ID → offset 和 Get。只支持单 writer；不做事务与恢复。

提交：`feat(F52): read and write native records`

## F53 Catalog 与 Row Put/Get

目标故事：`US-INSERT`、`US-READ`、`US-DEVELOPER`。

闭环：创建最小 Database/Table Catalog → 编码真实 Row → 写入 → reopen → 按
RowID 解码 → Schema、字段值和 ID 一致。

先测：现有逻辑类型、Column ID、NULL、中文、字段预算、未知字段和 Schema version。

开发：在 F52 payload 上实现 typed Catalog/Row codec；不接事务、History、Relation
或 Router。

提交：`feat(F53): round-trip native catalog and rows`

## F54 最小 MSQL 垂直闭环

目标故事：`US-INSERT`、`US-READ`、`US-COLD`。

闭环：MSQL 建库/建表 → INSERT → 关闭并重开原生仓库 → `SELECT by row_id`。

先测：CLI/执行器只走 MSQL；最终 Row/revision 与 F53 codec 一致；结果不泄漏文件
offset、Record Header 或 SQLite 概念。

开发：接入最窄 Catalog/Row repository。v0 只支持单进程、autocommit 单语句；
明确不承诺多对象原子性和崩溃安全。

提交：`feat(F54): run insert and select on native files`

## F55 接宽当前逻辑能力

目标故事：`US-UPDATE`、`US-DELETE`、`US-SPLIT`、`US-SCHEMA`。

依次增加 Update/Delete、History、Relation、Table Route node/membership。每增加
一种对象都必须先完成 `write → reopen → read`，不能一次迁移所有模块。

提交：按对象拆成不超过约 400 行生产代码的独立 Feature，不强行塞进一个 commit。

## F56 事务与恢复

只有 F52–F55 闭环通过后开始。

先测：多 Record 原子提交、rollback、fsync 边界、每个截断点、崩溃尾部、已提交
损坏、重放幂等和 reopen。

开发：在已跑通的 Record 格式上增加 transaction ID、BEGIN/COMMIT、commit
sequence 和 recovery；仍不自动引入 MVCC/Undo/Redo。

提交：`feat(F56): add native transactions and recovery`

## F57 SQLite 迁移与默认切换

先测：真实 `prototype.sqlite`/`security.sqlite` fixture → logical snapshot → 原生
导入 → canonical hash → MSQL 回读；中断或失败保留原文件。

开发：新 Instance 默认 `.memora`，旧实例只经显式计划、备份和校验后切换。

提交：`feat(F57): migrate SQLite stores to native files`

## F58 删除 SQLite

删除 driver、`internal/store/sqlite`、主程序 `.sqlite` 文件名、SQLite benchmark
执行器和测试耦合。若需长期读取旧格式，将兼容器隔离为主 binary 外的独立工具。

提交：`refactor(F58): retire SQLite storage`

## F59 Table 级语义树

在原生记录上接通每 Table 独立 root、逐层 MSQL、RowID leaf 和 split/merge
membership；删除 Database Router 与 MATCH/Vector fallback 主路径。

提交：`feat(F59): navigate table semantic trees`

## F60 新 AI-native 产品门

按 `US-*` 重做真实宿主旅程，固定每一步 MSQL、Route Frame、最终原生数据和用户
输出；禁止 Row/chunk Vector 充当事实权威和全库 prompt 扫描。Route-only 候选
预测器必须有界、可审计，并在失败时回退普通 Router。

## F61+ 证据驱动优化

只有容量、启动、随机读取、并发或空间数据证明需要时，才规划 checkpoint、
compaction、Page/Buffer Pool、B+ Tree、MVCC、Undo/Redo 和 Binlog。

## Phase D 退出条件

- F52、F53、F54 各自都有独立 write/read 闭环；
- 事务/恢复建立在已接通的数据格式上，而不是孤立演示；
- 迁移后新 Instance 只产生 Memora 文件，SQLite 被安全移出主程序；
- MSQL CRUD、History、Relation 和 Table Router 在原生底座通过。
