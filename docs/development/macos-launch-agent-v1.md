# macOS LaunchAgent v1

状态：F148 已实现。

## 用户入口

```text
memora service install [--data-dir /absolute/instance]
memora service status  [--data-dir /absolute/instance]
memora service uninstall [--data-dir /absolute/instance]
```

三个命令只操作当前用户的 `gui/<uid>` domain，不要求 root。每个规范化 data dir 通过
SHA-256 前缀得到独立 `io.memora.daemon.<digest>` label，因此一个用户可注册多个 Instance。

## plist

文件位于 `~/Library/LaunchAgents/<label>.plist`，唯一程序是当前绝对 `memora` 路径，参数固定：

```text
memora daemon run --data-dir /absolute/instance
```

不经过 shell、不继承命令字符串。`KeepAlive.SuccessfulExit=false` 使异常退出被重启，而正常
SIGTERM 退出不被反复拉起；ProcessType 是 Background，默认 10 秒 throttle，umask 为 077。
stdout/stderr 分别进入 `~/Library/Logs/Memora/<label>.*.log`。

## 安装与恢复

- install 先验证 executable regular/executable 与 Instance metadata。
- plist 以 staging、file fsync、atomic rename、directory fsync 发布，固定 0644；目录固定 0700。
- 使用现代 `launchctl bootout/bootstrap`。bootstrap 失败时恢复旧 plist；旧服务存在时尝试重载旧定义。
- status 只依赖 `launchctl print` 的退出状态，不解析 Apple 明确声明为非 API 的文本格式。
- uninstall 先 bootout 再移除 regular plist；即使二进制或 Instance 已移动/删除仍可清理。
- 现有 plist 或目标目录是 symlink/非 regular 时拒绝。

## 边界

这是 legacy LaunchAgent 文件集成，不是 root LaunchDaemon，也不宣称是带 GUI bundle 的
SMAppService 登录项。外接盘在登录时不可用会导致 daemon 非零退出并受 launchd throttle；数据
不会回退到默认 Instance。
