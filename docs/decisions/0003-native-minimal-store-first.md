# ADR-0003：原生极简 Store 优先

状态：Accepted，2026-07-30。先完成 Put/Get，再接真实 Row 与 MSQL；事务、恢复和
SQLite 迁移均后置。

## 决策

下一阶段先实现 Memora 自己的最小持久化底座，再把现有 Catalog、Row、Relation、
History 和 Table Router 接上去。SQLite 只作为迁移来源临时保留，不再扩展、不再
作为默认长期后端；迁移验证完成后删除实现、依赖、文件名和测试耦合。

第一闭环只解决：

- 数据放在哪里、由哪些文件承载；
- File/Record Header 和 payload 怎样编码；
- 一个 Record 怎样 Put、close/reopen 和按稳定 ID Get；
- 损坏怎样明确报错，而不是假装恢复。

真实 Catalog/Row、MSQL 接入、事务/恢复、SQLite 迁移按独立闭环逐步增加。
B+ Tree、固定 Page、Buffer Pool、MVCC、Undo/Redo、Binlog 和 compaction 更晚，
以证据决定是否加入。

## 为什么改变顺序

产品体验尚需重做，但继续依赖 SQLite 会让 Memora 的真实数据边界、文件格式、
恢复语义和迁移成本越来越晚暴露。先做极简原生底座可以让后续语义树、MSQL 和
AI 用户旅程建立在最终可控的持久化契约上。

这不改变产品职责：AI 决定语义结构；F52 只保证写入的字节能正确读回。

## 退出条件

只有同时满足以下条件，才删除 SQLite：

1. 原生 Store 先通过 Put/Get、CRC 和 reopen，再单独通过事务/恢复测试；
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
