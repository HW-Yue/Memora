# B8 · `daemon.Inspect` 是读操作却删 PID 文件

严重度：medium。状态：**待做**（顾问判断：单开一块；只动行为，不动它的判断逻辑）。

## 症状

`Inspect(dataDir)` 先试拿实例锁。**拿得到**说明没有守护进程在跑，于是它顺手
`os.Remove(pidFile)` 再返回 `Running=false`；拿不到锁才去读 PID、报 `Running=true`；
锁打不开或 PID 读不出来则报错。

删掉的那个文件在**当时**确实是上一轮遗留的（守护进程是在持锁期间写 PID 的），所以这不是
数据损坏。但它让一个「读」产生磁盘副作用：`Inspect` 被 `memora doctor`、`status`、
以及**每条命令的 `ensureDaemon`** 调用——检查状态的路径不该写盘，清理应当由明确的生命周期
操作（`Acquire` / `Stop` / `destroy`）承担。副作用还让 `Inspect` 在只读挂载或权限受限的实例
目录上表现不一致（`_ = os.Remove(...)` 静默失败）。

## 证据

- `internal/daemon/lifecycle.go:110`：`_ = os.Remove(paths.PIDFile)`，位于「抢到锁」分支内。
- 调用方：`internal/cli`（`ensureDaemon`、`doctor`、`status`）、`internal/daemon/manager.go`。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B7 的第 1 条不变量（原先混在「补测试」里）。

## 已定的方向（顾问，2026-09-26）

**改成纯读**：`Inspect` 只回答「在不在跑 / PID 是多少」，不写盘。遗留 PID 文件的清理交给
明确的生命周期操作：`Acquire`（接管实例时）或 `Stop`/`destroy`（结束实例时）。

## 验收

- `Inspect` 前后实例目录字节不变（含「没在跑但 PID 文件还在」这一态）；
- 旧的遗留文件仍会被清掉——由 `Acquire` 负责，并有一条测试钉住；
- 「拿不到锁 → 读 PID → Running=true」「读不出 PID → 报错」这两条语义**不许变**，
  它们是 B6 fail-closed 的依据（`instance destroy` 依赖「报错 = 问不出来」）。
