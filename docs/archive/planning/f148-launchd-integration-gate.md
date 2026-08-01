# F148 launchd Integration 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

macOS 当前用户可用 `memora service install|status|uninstall` 将一个 Instance daemon 注册为
GUI session LaunchAgent，登录后启动、异常退出后重启、正常停止后不反复拉起。

## RED

- Label 由规范化 data dir 的 SHA-256 派生；同一 Instance 稳定、不同 Instance 隔离。
- plist 的 Program/arguments 全为绝对值且不经过 shell；只执行 `daemon run --data-dir`。
- 使用 `gui/<uid>`、`bootstrap`/`bootout`/`print`，不使用 legacy load/unload，不要求 root。
- plist staging + fsync + atomic rename；拒绝 symlink，权限固定，bootstrap 失败恢复旧文件。
- `KeepAlive.SuccessfulExit=false`、Background、private umask、独立 stdout/stderr 路径。
- install 校验 executable 与 Instance；uninstall 即使 executable/Instance 已丢失仍可清理。
- CLI 输出稳定 JSON receipt；所有 launchctl 调用均可注入，不在测试中修改真实用户会话。

## 边界

F148 只支持 macOS LaunchAgent，不做 root LaunchDaemon、SMAppService GUI 登录项或其他平台服务。
release 安装器和签名属于 F149/F150。

## 完成门

deterministic plist、multi-instance、install/reinstall rollback、status、uninstall、symlink/fault、CLI、
race 与全仓 CI 全绿后合入。下一项 F149。

## 完成证据

deterministic plist/label、现代 gui domain 命令、权限、bootstrap 新安装清理、reinstall 旧文件
恢复、已丢失 executable/Instance 的卸载、known-not-loaded status、symlink、CLI 与 race/全仓 CI
均通过。下一项 F149。
