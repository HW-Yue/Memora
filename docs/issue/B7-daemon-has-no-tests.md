# B7 · 给 `internal/daemon` 补测试

严重度：medium。状态：**待做**。

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
