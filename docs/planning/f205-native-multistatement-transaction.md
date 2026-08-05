# F205：native 多 statement 原子事务

状态：2026-08-05 已完成。

## 唯一主要结果

让 production daemon/native backend 在同一 MSQL Session 中执行
`BEGIN → 多条 L1 数据语句 → COMMIT/ROLLBACK`，并以一个 native store 事务提交，使正式资料吸收面
能够原子写入多个语义 claim。

## 冻结边界

- 显式事务只新增 F195 已允许的 `INSERT`、`UPDATE`、`DELETE`、`RESTORE`、`RELATE` 和
  `UNRELATE`；Catalog、Route 结构、配置、RESHAPE 和 ASSIMILATION 控制语句不进入本事务。
- Agent 仍只调用 `protocol/msql.ExecuteMSQL`；事务工厂由 daemon 组合根注入，Agent 不导入
  native Row、Page 或 Store。
- 一个事务共用一个 Row/Relation commit sequence，并生成一个 committed-change envelope；全部
  Row、History、Relation、Route Membership 和 change 记录同生共灭。
- 为保持 immutable revision 与同一 envelope 的确定性，F205 要求一个事务最多触碰同一 Row 一次；
  后续若需要同一事务内多次改写，另立 Feature 补充 staged revision 模型。
- 同一事务内提供 staged Row/Relation 的 read-your-writes；首个写失败会回滚整个事务并进入既有
  aborted 状态，直到 `COMMIT` 或 `ROLLBACK` 清理。
- Session 关闭、IPC 断开或 daemon 关闭都会回滚未提交事务；不允许把客户端断线解释成提交。
- F205 不改变 native 文件格式，不加入 DDL 事务，也不开放未经 F195 Review 的多 claim 旁路。

## RED 与故障矩阵

首个 RED：

```text
go test ./internal/daemon -run TestNativeMSQLTransactionCommitsTwoRowsAtomically
```

当前 production native Rows 不实现旧执行器写死的 `*row.Transaction`，因此 `BEGIN` 返回
`unsupported`。这证明缺少的是 native transaction boundary，不是 Parser、Binder 或 fixture。

完成证据：

| 证据 | 预期 |
| --- | --- |
| IPC 多 INSERT + COMMIT | 两行同时可见且共用 commit sequence |
| 显式 ROLLBACK / disconnect | staged Row、History、Membership、change 均不可见 |
| 第二条写入失败 | 第一条结果标记 rolled back，重开仍无半提交 |
| native commit 尾部截断 | reopen 忽略未提交事务，不出现部分 Row |
| Page publication fault | fail closed；reopen 后 body 与 Page 收敛到全有或全无 |
| 并发 Session | 写入串行化且无 sequence 冲突，`go test -race` 通过 |
| reference model | commit/rollback/失败序列的可见 Row 集合与模型一致 |

## 完成门

目标 package、真实 daemon/IPC、native file reopen/fault、并发/reference-model、全量测试、全量
`-race`、`go vet` 和格式检查均通过。关键证据为 `internal/daemon/native_transaction_test.go` 的
IPC/断线/重开/并发/reference-model 测试，以及 `internal/pagestoremigration/f205_native_transaction_test.go`
的多 Row Page publication fault→poison→reopen 收敛测试；native store 的 partial transaction recovery
继续由既有 `internal/store/native/transaction_test.go` 覆盖。

## 关联

- [正式 MSQL 吸收提交面](./f195-msql-assimilation-surface.md)
- [F204 之后的开发计划](./post-f204-development-plan.md)
- [小 Feature TDD 与合入协议](./feature-tdd-protocol.md)
