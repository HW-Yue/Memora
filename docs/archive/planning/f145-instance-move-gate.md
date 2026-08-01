# F145 Instance Move 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

普通用户通过一条 `memora move` 命令，将完整 Instance 安全搬到另一个绝对目录或设备，
并保持搬迁前的 daemon 运行状态。

## RED

- source、backup、target 必须是三个绝对、规范化、互不包含的路径；backup 和 target 必须不存在。
- source daemon 运行时先停止，再调用 F143 Create 与 F144 Restore，验证后才启动 target。
- source daemon 原本停止时，搬迁完成后 target 也保持停止。
- stop 之后 backup/restore/start-target 任一步失败，都尽力重新启动 source，并返回组合错误。
- source 永不删除或修改；收据显式记录 `source_retained`、backup hash、目标路径和 daemon 状态。
- CLI 只接受显式 `--backup`、`--target`，并输出可机读 JSON 收据。

## 边界

F145 不自动删除源目录、不修改用户配置、不负责后台服务注册；launchd 属于 F148。用户确认目标
稳定后可自行归档源目录，Memora 不代替用户执行不可恢复删除。

## 完成门

目标成功、原本停止、backup/restore fault、target start fault/source restart、CLI 参数与收据、race
和全仓 CI 全绿后合入。下一项 F146。

## 完成证据

运行/停止状态保持、backup 失败后的 source restart、target start 失败后的 source restart、
路径隔离、源保留和 CLI JSON 收据测试均通过；race 与全仓 CI 全绿。下一项 F146。
