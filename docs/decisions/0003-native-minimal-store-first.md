# ADR-0003：原生极简 Store 优先

状态：Accepted（底座优先顺序），2026-07-30。取代 ADR-0001 中“先过 AI 质量门、
再做原生内核”的顺序；append-only 精确格式仍需通过 F52 开工门确认。

## 决策

下一阶段先实现 Memora 自己的最小持久化底座，再把现有 Catalog、Row、Relation、
History 和 Table Router 接上去。SQLite 只作为迁移来源临时保留，不再扩展、不再
作为默认长期后端；迁移验证完成后删除实现、依赖、文件名和测试耦合。

第一版原生底座不同时实现完整数据库教科书栈。它只解决：

- 数据放在哪里、由哪些文件承载；
- 文件头、事务帧和逻辑 Record 怎样编码；
- 原子提交、校验、重启扫描和损坏拒绝；
- 稳定 ID、revision、Table Router 和历史怎样持久化；
- 现有逻辑层怎样迁移并证明结果等价。

B+ Tree、固定 Page、Buffer Pool、MVCC、Undo/Redo、Binlog、复杂 compaction
全部后置，以真实容量、并发和延迟证据决定是否加入。不能为了“像传统数据库”而
先把它们写进核心格式。

## 为什么改变顺序

产品体验尚需重做，但继续依赖 SQLite 会让 Memora 的真实数据边界、文件格式、
恢复语义和迁移成本越来越晚暴露。先做极简原生底座可以让后续语义树、MSQL 和
AI 用户旅程建立在最终可控的持久化契约上。

这不改变产品职责：AI 决定语义结构；原生 Store 只保证字节、事务和恢复正确性。

## 退出条件

只有同时满足以下条件，才删除 SQLite：

1. 原生 Store 通过共享 contract、崩溃尾部、CRC 和 reopen 测试；
2. 逻辑 snapshot 从 SQLite 导入原生格式后 canonical hash 相同；
3. 当前 MSQL CRUD、History、Relation 和 Router 旅程在原生后端通过；
4. 原型文件保留到迁移成功并完成回读验证；
5. 默认 daemon 已切换原生 Store，仓库不再需要 SQLite driver。

## 非决策

Unix socket/IPC 是否删除是独立运行时决策。“删除 SQLite”不能被扩大解释为
自动删除 daemon IPC；在用户确认前不改变该边界。

## 关联

- [原生极简存储格式](../storage/native-minimal-store.md)
- [ADR-0001：SQLite 原型 Store](./0001-prototype-store.md)
- [Phase D 计划](../planning/tdd-phase-d-release-kernel.md)
