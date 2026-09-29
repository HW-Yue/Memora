# B7 · 给 `internal/daemon` 补测试

严重度：medium。状态：**已关**（2026-09-26：三条不变量逐条核对，缺的那条补上）。

## 结案

审计列的三条，逐条核对的结果不同：

1. **断连回滚 —— 此前零证据，已补**（`TestADisconnectRollsBackTheOpenTransaction`）：
   真 daemon + 真实例；连接 A 建 schema、`BEGIN`、`INSERT`，并在事务内断言自己看得见这条写；
   不 COMMIT 关连接；连接 B 用自己限定 5 秒预算的写证明**写锁已经放开**，最后断言唯一那行是
   断连之后写的。刻意不拿「另一个连接看不到那行」当证据——未提交的行本来就不对其他会话可见。
   **变异检验**：把 `internal/ipc/server.go` 的 `SessionClosed` 调用换掉，测试立刻红并报出
   「the abandoned transaction still holds the write lock」（5.4s，不再等 10 秒超时）；还原后绿。
2. **版本偏移「先执行后拒绝」—— 已覆盖，无需新增**：服务端
   `TestDaemonRefusesSkewedEngineProtocolBeforeExecuting`（被拒的请求带着 CREATE DATABASE，
   拒后库里什么都没有）+ 客户端 `ensureDaemon` 拒绝/接受两条（`internal/cli/daemon_protocol_test.go`）。
3. **`Inspect` 是读操作却删 PID 文件 —— 拆成 [B8](./B8-inspect-deletes-the-pid-file.md)**。
   顾问判断：那要改的是**行为**（让读保持只读，清理交给明确的生命周期操作），与「补测试」
   不是一个主要结果，不该混进本块。

**之后每条修复顺手往这个包加测试**（A4、P5 已经各加过一批）。

## 症状

`internal/daemon` 约 920 行、零测试，且全仓没有任何 `_test.go` 引用它（集成层也没测）。
A4 / A5 / B6 三条修复的证据都缺在这一个包上。

## 证据

- 全仓无引用：`grep -rl 'internal/daemon' --include='*_test.go'` 为空。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B7。

## 已定的修法

A4 已给它种下第一批测试（进程内起真 daemon + 真 socket + 真 SQLite 的辅助已经有了）。
剩下三条不变量：

1. Inspect 是读操作却删 PID（`internal/daemon/lifecycle.go:110`）。
2. 版本偏移"先执行后拒绝"（A5 已让服务端拒绝，但 client / `ensureDaemon` 那两条路径要有 e2e）。
3. 断连回滚（`server.go:66-73` → `service.go:99-108` → `batch.go:276-291`）。

**之后每条修复顺手往这里加测试，不指望一次补完。**
