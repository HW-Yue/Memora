# Phase A 退出验收

状态：已通过。

## 已验证链路

集成测试在全新临时目录构建真实 `memora` binary，并完成：

```text
init
→ daemon start
→ status / ping
→ version --json
→ memora parse（成功 AST golden）
→ memora parse（精确错误 golden）
→ 8 个并发 parse 客户端
→ daemon stop
→ daemon restart
→ 再次 parse
```

所有目录均位于 `t.TempDir()`，不读取或修改真实用户 datadir。测试结束通过 CLI stop 清理 daemon，并由 Go testing 生命周期清理文件。

## Parse 诊断入口

`memora parse [--data-dir path] '<MSQL>'` 通过正式 Unix socket 调用 daemon 的 `msql.parse` 方法。成功返回 `memora.msql.ast/v1` Batch，失败返回稳定 lexer/parser code、消息、statement index 和位置。

该命令只验证统一 Lexer/Parser/AST 链路，不执行语句，不是绕过 MSQL 的数据接口。F13 起的 Catalog、Binder 和 Executor 必须继续挂在同一 daemon 请求链路后。

## 质量门

Phase A 当前满足：

- CLI、instance、daemon 和 IPC 可在干净环境启动；
- frame、并发 request、session lifecycle 和取消行为有 unit/race 覆盖；
- MSQL Lexer、单语句 Parser、Batch 和事务边界有 golden/fuzz 覆盖；
- GitHub 与本地使用同一个 `scripts/ci.sh`；
- format、vet、unit、race、integration、e2e 和 macOS 双架构 cross-build 全绿。

## 下一阶段

Phase B 从 F13 Data Dictionary 开始。SQLite 继续只位于 Store 适配层后，Catalog 和 MSQL 契约不得暴露后端表结构。

## 关联

- [TDD 开发总计划](./tdd-development-plan.md)
- [MSQL Parser Core v1](../query/msql-parser.md)
- [MSQL Batch 与事务边界 v1](../query/msql-batch-transactions.md)
- [本地 IPC 协议](../development/ipc-protocol.md)
