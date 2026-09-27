# B6 · `instance destroy` 必须 fail-closed

严重度：medium。状态：**待做**。

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
