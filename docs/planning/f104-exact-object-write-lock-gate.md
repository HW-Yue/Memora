# F104 Exact-Object Write Lock 开工与完成门

状态：已完成；产品门与完成门 PASS。

## 产品门

- 目标故事：`US-UPDATE`、`US-SCHEMA`、`US-CONFLICT`、`US-ENGINE`；并发 writer
  不覆盖同一 Row/Schema/Route，AI 收到稳定冲突后重读或重试。
- 标准旅程：transaction A 锁目标并 stage → B 锁同目标得到 `write_conflict` →
  A commit/rollback 释放 → B 重读后成功；不要求用户处理锁。
- 作用范围：Instance 内易失 logical write lock；不改变 Row/Route/Schema 内容和历史。
- 上下文：冲突只返回 kind 与目标 ID，不增加 Route Frame 或模型调用。
- 永久边界：锁不是 revision、WAL LSN、Page latch 或语义冲突 authority。
- 架构选择：稳定 key 排序的 non-waiting batch try-lock；等待/死锁后置 F158。
- 用户执行授权：F81–F109 持续授权已记录。
- 唯一主要结果：transaction guard 对精确逻辑对象提供排他、原子、可释放的写锁。
- 明确不做：Page Store mutation 接线、range/gap lock、等待、公平队列、死锁检测、
  跨进程锁与持久化。
- 开工前结论：PASS。

## RED matrix

| Case | 当前缺口 | 期望 |
| --- | --- | --- |
| same object | 无 logical lock manager | 一方持有时另一方稳定冲突 |
| batch atomicity | 多对象无统一取得边界 | 任一冲突则本批零新增锁 |
| canonical order | 输入顺序可能改变结果 | 对拍排序模型，blocked key 稳定 |
| reentrant/lifetime | 无 transaction guard | 同 guard no-op，terminal release 全部 |
| independent/readers | 全局 mutex 会过度串行 | 不同 key 独立，reader 无需调用 |
| cancellation | 取消可能留下锁 | acquisition 前取消零泄漏 |
| concurrency/race | 相反顺序可能等待成环 | fail-fast、无死锁、race clean |

首个 RED：两个 guard 对同一 Row key 的第二次 TryAcquire 当前没有可用实现，无法返回
结构化 `write_conflict` 并在第一个 guard Release 后成功重试。

## 完成门

- Row/Schema/Route key codec、边界与 collision corpus；
- same-key、independent-key、batch rollback、reentrant、owner reuse、terminal release；
- 相反输入顺序的可控并发与 reference model、`-race`；
- result code、文档、targeted repetition、全仓 unit/vet/race/CI；
- 完成后结论：PASS。三类 key codec、整批原子性、稳定 blocked key、guard 生命周期、
  取消/上限、100 轮相反顺序并发与 5000 步 reference model 已覆盖；targeted
  repetition、全仓 unit/vet/race/CI 均通过。
