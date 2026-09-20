# Exact-Object Write Lock v1

状态：F104 已完成；logical write-lock 契约已冻结。

## 唯一结果

一个 Instance 内的 writer 在修改明确逻辑对象前取得 transaction-scoped 排他锁；
同一对象同时只能由一个 transaction guard 持有，普通 MVCC reader 不取锁。

F104 交付可供 Page Store writer 接线的内存 Lock Manager，不改变 MSQL、持久格式、
commit sequence 或 WAL。F107 切换写 authority 时，autocommit guard 持有到语句终态，
显式事务 guard 持有到 commit/rollback。

## 精确对象 Key

- Row：`database_id + table_id + row_id`；
- Schema：`database_id + schema_object_id`，Table 演化使用 Table ID，Database 内创建
  可锁 Database schema namespace ID；
- Route：`database_id + route_node_id`，子节点结构修改锁明确 parent/node。

组件非空、无首尾空白且最多 2048 UTF-8 bytes。kind 与 length-prefixed 组件形成
collision-free binary key；批量请求按该 key 排序、去重，所以输入顺序不改变冲突对象。
单批最多 1000 个输入 key。这不是字符串拼接、范围锁、gap lock 或 Page latch。

## Guard 与冲突

- nonzero transaction owner 先 `Begin` 一个唯一 guard；owner 尚活跃时不能复用；
- `TryAcquire` 是非等待、整批原子操作；无冲突时一次取得全部新 key；
- 同 guard 重入已有 key 是 no-op；后续批次冲突时保留该 guard 先前已持有的锁，但不
  泄漏本批尚未持有的 key；mutation plan 应尽量一次提交完整 key 集合；
- 冲突返回稳定 `write_conflict` 与第一个 canonical blocked key，不暴露对方 owner；
- `Release` 幂等释放 guard 的全部 key 并封闭 guard；释放后 contender 可立即重试；
- context 在 acquisition 前取消时不取得任何新锁。

采用 fail-fast 是本地单 writer、短事务的安全默认值：不创建等待环，也不需要超时、
等待队列或死锁检测。真实旅程证明 fail-fast 不够时才进入 F158。

## 边界

- 锁状态不写 WAL、snapshot、Database package 或 system prompt；daemon crash 后清空；
- expected revision 仍负责检测已提交陈旧写；write lock 只阻止活跃 writer 重叠；
- 不保护未知范围、SQL predicate、正文内容或语义相似度；
- F104 不把旧 SQLite/Frame writer 改成新 authority，也不提前实现 F107 写接线。

## 完成证据

- Row/Schema/Route binary golden、UTF-8/长度边界与 collision corpus；
- same-key conflict、不同 kind/object 独立、重入/去重、owner reuse 与幂等 Release；
- 冲突批次零新增锁且不丢 guard 旧锁，canonical blocked key 不受输入顺序影响；
- cancellation 与 1000-key 硬界限不泄漏；
- 相反 batch 顺序并发 100 轮无等待环，5000 步固定 seed 与 reference model 一致；
- `write_conflict` 已注册为 retryable statement error；全仓 unit/vet/race/CI 通过。

## 关联

- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
- [Snapshot Visibility v1](./snapshot-visibility-v1.md)
- [ADR-0004](../decisions/0004-fast-row-directory-minimal-mvcc.md)
