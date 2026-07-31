# MVCC、Undo Log、Redo Log 与 Binlog 边界

状态：F103 已完成 Row snapshot visibility；F104 接入精确对象写锁，物理 Undo
继续后置。见
[ADR-0004](../decisions/0004-fast-row-directory-minimal-mvcc.md)和
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

## 事务与版本标识

- transaction ID：标识当前 Instance 内的一次事务，用于活跃事务、锁、Undo 和提交状态；
- global transaction ID：标识一次已提交事务的原始设备与序号，用于 Binlog 同步、幂等和防回环；
- format version：磁盘编码兼容性；
- schema version：Table、Column 和约束；
- commit sequence：Instance 内单调提交顺序；
- object revision：单个逻辑 Row 的语义修订次数。

这些标识不能共用一个字段。transaction ID 不能单独表达 MVCC 可见性顺序，global transaction ID 不能代替本地 transaction ID，Redo Log 的 LSN 也不能代替 commit sequence。

## MVCC

- daemon 串行发布写事务，第一阶段不支持多个物理 writer 并行提交；
- autocommit 语句在开始时固定 committed snapshot；
- 显式事务固定开始时的 snapshot，并读取自己的暂存写；
- 写入前取得 Row、Table/Schema、Route 等明确对象的排他写锁；
- autocommit 在语句终态释放锁，显式事务在 commit/rollback 后释放；
- 多对象写按稳定 key 排序；首选非等待 try-lock，冲突策略待 F104 Review；
- Row revision 先写入事务缓冲，只有完整 WAL COMMIT 后进入 B+ Tree committed view；
- reader 依据 snapshot commit sequence 选择可见 immutable revision；
- rollback 丢弃未发布记录，不需要用物理 Undo 恢复 in-place Page；
- expected revision 继续处理 Agent 的陈旧语义写；
- AI 推理在事务外完成，提交保持短小。

普通 reader 依靠 MVCC，不取得写锁，也不被未提交 writer 阻塞。这个模型提供接近
`READ COMMITTED` autocommit 与 repeatable snapshot transaction 的必要行为，
但第一 Feature 不开放隔离级别矩阵，也不承诺 `SELECT ... FOR UPDATE`、
gap/next-key lock、范围锁、锁等待或死锁检测。

## Undo Log、Redo Log 与历史

- 历史 revision：给 Agent 查询“谁为什么改了什么”；
- Undo Log：未来允许未提交 Page steal、in-place Row body 或多 writer 时再引入；
- Redo Log：F83–F89 必做的 Page WAL 与刷盘顺序，保证 committed B+ Tree/Page 崩溃恢复。

当前 append-only Frame 仍是迁移前实现。F84 后的新 Page mutation 先形成私有 write set，
Redo COMMIT fsync 后发布；未提交写不进入共享 Page，因此首版 rollback 可丢弃
staged writes。未来事务 Undo 会被 Purge，仍不能承担永久 History；业务撤销继续
创建补偿 revision。

F109 Committed Change Log（Binlog）记录已提交事务的逻辑变化，第一用途是 Admin
展示数据、Schema 与语义索引变化。change envelope 作为业务 Page Record，与
相关 Page 变更进入同一 WAL transaction，因此首版不需要独立 Redo/Binlog 两阶段
提交。未来同步与 PITR 可以复用该变化流，但必须单独 Review，详见
[Committed Change Log 与未来同步](./binlog-and-sync.md)。

## 并发错误

数据库只处理可机械判定的并发与约束冲突。版本冲突返回当前 revision、commit sequence 和恢复建议；唯一键、外键、类型和锁错误返回稳定错误码。内容之间是否存在语义矛盾不由引擎判断，也不进入 MVCC 状态；Skill 负责展示相关 Row 并根据用户指示重新提交 SQL。

## 未决问题

- 是否以及何时增加可配置隔离级别？
- 是否有真实需求增加锁等待、范围锁或死锁检测；
- Undo record 的字段级 before image、roll pointer 和 segment/page 编码；
- 多 CLI 进程的 commit sequence 和锁如何协调？
- compaction 如何确定最老活跃快照？
- Data Dictionary 的 MVCC 如何参与 SQL parse/bind/execute？
- 何时由真实提交吞吐证明需要 Group Commit？

## 关联

- [存储引擎术语](./terminology.md)
- [AI 自主权与约束](../agent/autonomy.md)
- [物理与检索索引](./indexing.md)
