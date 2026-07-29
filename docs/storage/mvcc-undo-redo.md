# MVCC、Undo Log 与 Redo Log

状态：方向已形成；具体恢复算法和隔离级别待原型。

## 四种版本

- format version：磁盘编码兼容性；
- schema version：Table、Column 和约束；
- commit sequence：Instance 内单调提交顺序；
- object revision：单个逻辑 Row 的语义修订次数。

这些版本不能共用一个字段，Redo Log 的 LSN 也不能代替 commit sequence。

## MVCC 候选

- 每次修改创建不可变 Record revision；
- Row Directory 指向最新 Record；
- Record 保存 begin/end commit sequence；
- 默认候选为 Snapshot Isolation；
- 同一 Row 并发写使用 first-committer-wins；
- Agent 更新携带 expected revision；
- AI 推理在事务外完成，提交使用短事务。

## Undo Log、Redo Log 与历史

- 历史 revision：给 Agent 查询“谁为什么改了什么”；
- Undo Log：回滚未提交事务，并为 MVCC 旧版本读取提供必要信息；
- Redo Log：遵守 write-ahead logging，崩溃后重做已经承诺但尚未落盘的修改。

Page 在对应 Redo Log 持久化前不能刷盘。已提交修改的业务撤销应创建补偿 revision，而不是删除历史。

## 并发错误

版本冲突返回当前 revision、commit sequence 和恢复建议。数据库不能替 AI 静默合并语义冲突。

## 未决问题

- Snapshot Isolation 是否足够，何时需要 Serializable？
- 使用 in-place + Redo Log、Copy-on-Write，还是混合结构？
- Undo Log 使用 before image、inverse operation 还是 Record 版本链？
- 多 CLI 进程的 commit sequence 和锁如何协调？
- compaction 如何确定最老活跃快照？
- Data Dictionary 的 MVCC 如何参与 SQL parse/bind/execute？

## 关联

- [存储引擎术语](./terminology.md)
- [AI 自主权与约束](../agent/autonomy.md)
- [物理与检索索引](./indexing.md)
