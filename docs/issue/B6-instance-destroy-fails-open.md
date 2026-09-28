# B6 · `instance destroy` 必须 fail-closed

严重度：medium。状态：**已关**（2026-09-26 修复）。

## 结案

只做 fail-closed，没有铺开租约体系（`AcquireMaintenance` 仍是未完成设计）：
Inspect 报错 → 直接拒绝并报原因，目录不动；明确 `Running=true` 才先 `daemon.Stop`；
只有明确的「没在跑」才走到 `RemoveAll`。

两条证据（都在旧实现上先跑红）：
1. `TestInstanceDestroyRefusesWhenItCannotTellWhetherTheDaemonIsRunning` —— 用 `daemon.Acquire`
   真的握住实例锁、删掉 PID 文件，让 `Inspect` 在「锁被持有但读不出 PID」时报错；
   旧实现打印 `removed Memora instance …`（租约还握着就删了）。
2. `TestInstanceDestroyRefusesWhenInspectionCannotRunAtAll` —— 在锁文件位置放一个目录，
   让锁根本打不开。**这条第一版是假的**：它把整个 `system/` 换成文件，`instance.Read`
   更早就因 ENOTDIR 失败，于是修与不修都会「通过」；改成只毁锁文件本身之后才是真 RED。

**顾问（做完一块之后的咨询）指出的缺口正是第 2 条**：原先只覆盖了一种 Inspect 错误形状。
已补上，并顺带发现第一版覆盖不成立。

**未做**：租约（`AcquireMaintenance`）——按审计指示不铺开。

## 症状

`instance destroy` 把 `Inspect` 的错误当成"没在运行"，照样删实例目录；而 `RemoveAll` 不持租约 ——
可能在别人正写的时候把目录删掉。

## 证据

- `internal/cli/instance.go:59-66`：`inspectErr == nil && state.Running` 不成立也照样 `RemoveAll`。
- `AcquireMaintenance` 全仓零调用 → 租约是未完成设计。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B6。

## 已定的修法

**只做 fail-closed**：Inspect 出错就**拒绝删除**并报出原因；确认在跑就拒绝（或先停）。

不要顺手铺开租约体系 —— 那需要另开 RFC，别让一条 medium 拖出基础设施工程。
