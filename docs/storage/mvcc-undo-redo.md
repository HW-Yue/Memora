# MVCC、Undo Log、Redo Log 与 Binlog 边界

状态：事务标识和隔离级别方向已确认；具体恢复算法待原型。

## 事务与版本标识

- transaction ID：标识当前 Instance 内的一次事务，用于活跃事务、锁、Undo 和提交状态；
- global transaction ID：标识一次已提交事务的原始设备与序号，用于 Binlog 同步、幂等和防回环；
- format version：磁盘编码兼容性；
- schema version：Table、Column 和约束；
- commit sequence：Instance 内单调提交顺序；
- object revision：单个逻辑 Row 的语义修订次数。

这些标识不能共用一个字段。transaction ID 不能单独表达 MVCC 可见性顺序，global transaction ID 不能代替本地 transaction ID，Redo Log 的 LSN 也不能代替 commit sequence。

## MVCC

- 事务创建时分配本地 transaction ID，提交或回滚后进入终态；
- 主数据结构保存最新 Record，Record 系统头携带最近修改事务和 Undo roll pointer；
- UPDATE/DELETE 先写 Undo record，再修改当前 Record；
- 旧快照沿 Undo version chain 重建其可见版本；
- 没有活跃快照和恢复需求再引用旧 Undo 时，Purge 才能回收；
- 默认隔离级别为 `REPEATABLE READ`，并支持 `READ COMMITTED`；
- 同一 Row 并发写使用 first-committer-wins；
- Agent 更新携带 expected revision；
- AI 推理在事务外完成，提交使用短事务。

`REPEATABLE READ` 的一致性读复用事务快照；`READ COMMITTED` 的每次一致性读获取新快照。普通一致性读不加行锁，`SELECT ... FOR SHARE` / `FOR UPDATE` 属于锁定读并读取当前可见版本。范围锁、gap lock 与 next-key lock 的防幻读边界参考 InnoDB。

隔离级别使用 MySQL 风格 MSQL 设置，首版至少支持：

```sql
SET TRANSACTION ISOLATION LEVEL REPEATABLE READ;
SET TRANSACTION ISOLATION LEVEL READ COMMITTED;
```

## Undo Log、Redo Log 与历史

- 历史 revision：给 Agent 查询“谁为什么改了什么”；
- Undo Log：回滚未提交事务，并为 MVCC 旧版本读取提供必要信息；
- Redo Log：遵守 write-ahead logging，崩溃后重做已经承诺但尚未落盘的修改。

事务 Undo 会被 Purge，因此不能承担长期语义历史。每次已提交语义修改另外写入目标 Database 的共享追加式 History Store，并按 `table_id + row_id + revision + commit_sequence` 建立定位；业务撤销创建新的补偿 revision，而不是删除历史。Page 在对应 Redo Log 持久化前不能刷盘。

Binlog 另外记录已提交事务的逻辑变更，为多设备同步和时间点恢复服务。它不能代替本机 Redo 崩溃恢复，也不能直接复用物理 Redo 格式。Redo 与 Binlog 使用 MySQL 风格内部两阶段提交和 Group Commit，保证本地事务与可同步事件一致，详见 [Binlog 与多设备同步基础](./binlog-and-sync.md)。

## 并发错误

数据库只处理可机械判定的并发与约束冲突。版本冲突返回当前 revision、commit sequence 和恢复建议；唯一键、外键、类型和锁错误返回稳定错误码。内容之间是否存在语义矛盾不由引擎判断，也不进入 MVCC 状态；Skill 负责展示相关 Row 并根据用户指示重新提交 SQL。

## 未决问题

- 是否以及何时增加 `READ UNCOMMITTED` 和 `SERIALIZABLE`？
- gap/next-key lock 的内部结构与死锁检测策略；
- 使用 in-place + Redo Log、Copy-on-Write，还是混合结构？
- Undo record 的字段级 before image、roll pointer 和 segment/page 编码；
- 多 CLI 进程的 commit sequence 和锁如何协调？
- compaction 如何确定最老活跃快照？
- Data Dictionary 的 MVCC 如何参与 SQL parse/bind/execute？
- Group Commit 的刷盘批次、顺序和故障注入怎样实现？

## 关联

- [存储引擎术语](./terminology.md)
- [AI 自主权与约束](../agent/autonomy.md)
- [物理与检索索引](./indexing.md)
