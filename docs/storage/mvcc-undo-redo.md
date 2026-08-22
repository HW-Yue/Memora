# MVCC、Undo Log、Redo Log 与 Binlog 边界

状态：F103–F104 已完成 Row snapshot 与精确对象 Lock Manager；F107 接入 Page
Store writer，物理 Undo 继续后置。见
[ADR-0004](../decisions/0004-fast-row-directory-minimal-mvcc.md)和
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

> **目标形态已改，本文两处结论作废。**
> [写入形态](../product/write-model.md)取代了它们：
>
> 1. **日志分工与命名**。本文标题里的 "Binlog" 指的是 Change Log。新形态里这是
>    **三份不同的日志**：change log 管事务回滚（undo）、redolog 管
>    `prepare`/`commit` 崩溃原子性、**binlog 是唯一恢复依据**。
>    下文凡称 "Binlog" 之处，按 change log 理解；
> 2. **history 的去向**。下文"该记录类型将被删除，`SHOW HISTORY` 改由版本链取数据 +
>    Change Log 取归属拼出"**不再是目标**。新形态是 **history 独立成表**——每张业务表
>    配一张 history 表，键 `(row_id, 序号)`，读一行的完整历史一次范围扫。
>
> 本文的 MVCC、snapshot 水位、事务与版本标识几节仍然有效，也仍如实描述当前代码。
> 上述两处**不能作为新开发的设计依据**。

## 先分清三个东西

这三样都在描述"这行数据怎么变成现在这样"，但粒度和用途完全不同。
文档长期没把它们分开讲，是设计被反复误解的一个来源。

| 概念 | 是什么 | 粒度 | 谁用 |
| --- | --- | --- | --- |
| **snapshot** | MVCC read view，一个 commit sequence 水位 | 每次读 | 引擎内部事务可见性；`Authority.Capture()` → `versions.HighWater()` |
| **版本链／旧版本** | 这一行过去长什么样，是**数据** | 每个版本 | `AS OF`、`SHOW HISTORY`、MVCC 回溯 |
| **Change Log** | 谁改的、为什么改，是**归属** | 每个**事务** | `SHOW CHANGES`、审计、同步 |

`snapshot` **不是**"数据库快照文件"，是事务可见性水位。

`ObjectKindHistory` 记录是**第二份归属拷贝**：它按行记 actor／source／reason，
而 Change Log 的 `change.Metadata` 已经按事务记了同一批字段，
`change.Entry` 里甚至有 `HistoryLocator` 直接指向它
（`internal/change/model.go:70`）。一个事务碰 50 行只有一条 envelope，
把归属抄到 50 个版本上才是真重复。

> 这一段的**诊断**成立（当前确实是两份归属拷贝），但它开的**药方已作废**。
> 曾经的计划是删掉该记录类型、让 `SHOW HISTORY` 由「版本链取数据 + Change Log 取归属」
> 拼出——`48ef5b6` 已按此加了 `Row.ChangeSequence` 外键。
> [写入形态](../product/write-model.md)给的是另一个答案：**history 独立成表**，
> 归属就存在 history 表里，一行的全部变更按 `(row_id, 序号)` 范围扫。
> `Row.ChangeSequence` 在新形态下何去何从待定。
> 现状见[存储层总览](./README.md)。

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
- 多对象写按稳定 key 排序并执行非等待 batch try-lock；冲突立即返回 retryable
  `write_conflict`，等待队列与死锁检测只有 F158 证据成立后才进入；
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
