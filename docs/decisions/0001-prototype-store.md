# ADR-0001：SQLite 原型 Store

状态：Accepted，F04。只适用于原型阶段。

## 背景

Memora 必须先验证 Codex/Claude Skill 的自动维护体验，再投入完整 Page、MVCC、Undo、Redo 和 Binlog 内核。如果第一阶段同时实现全部物理存储，产品风险会被数据库工程周期掩盖。

原型仍需具备真实事务、进程重启持久化、并发读取、Unicode 和无 CGO macOS 制品，不能用只在测试中存在的内存 Map 冒充可用产品。

## 决策

原型后端使用 `modernc.org/sqlite`，固定依赖版本并通过 `database/sql` 调用。选择它是因为当前版本提供 CGO-free SQLite 实现，并已在本仓库验证可交叉编译 darwin/arm64 与 darwin/amd64。

SQLite 只能存在于 `internal/store/sqlite`。上层依赖 Memora 自己的窄 `Store`/`Tx` 契约：

```text
Begin(ReadOnly | ReadWrite)
→ Get / Put / Delete
→ Commit / Rollback
```

业务层、MSQL Parser、Data Dictionary、Router 和索引模块禁止导入 SQLite driver、拼接 SQLite SQL 或依赖 SQLite rowid/schema。原型表只是 Store 适配器的私有实现。

## 强制测试

同一 `storetest` contract 必须覆盖：

- commit 后关闭重开仍可读取；
- rollback 后写入不可见；
- read-only 事务拒绝修改；
- Unicode bucket/key/value；
- 多个并发 reader；
- delete 与 not-found 语义；
- `CGO_ENABLED=0` 的 macOS 双架构编译。

未来原生 Store 必须复用并通过同一契约测试，再增加 Page、MVCC、恢复和故障注入测试。

## 结果与边界

收益：可以尽早发布真实可持久化的 Skill-first 垂直切片，且 macOS 用户不需要 C 编译环境。

代价：原型磁盘文件不是 Memora 最终格式；SQLite 内部事务和 WAL 不能作为最终 Redo/Binlog 规格的证明。

进入原生内核前必须具备版本化逻辑 snapshot。迁移流程先导出 Catalog、Row、revision、关系和配置，再导入原生 Store；派生索引删除后重建。迁移验证成功前不得删除原型文件。

## 替换条件

只有 AI-native benchmark 达到发布门槛，才执行原生内核 Phase。若产品门槛未通过，继续优化 Skill、Schema 和检索质量，不因已经使用 SQLite 而提前扩写或永久固化其接口。
