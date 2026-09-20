# macOS 上跑 daemon

现役入口是 CLI 的 `daemon` 子命令。

```text
memora daemon start  [--data-dir /absolute/instance]
memora daemon status [--data-dir /absolute/instance]
memora daemon stop   [--data-dir /absolute/instance]
memora daemon run    [--data-dir /absolute/instance]
```

`start` 把 daemon 放到后台；`run` 在前台占住进程。一个用户可以给不同 `--data-dir` 开多个实例。登录时自动拉起属于后续独立 Feature，不在当前内核里。
