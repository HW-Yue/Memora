# macOS Instance 数据目录

状态：默认路径、顶层目录、`instance.meta` v2、v1 升级和 daemon 运行文件已实现。

## 平台范围

第一阶段只支持 macOS，Memora 作为当前用户的本地 daemon 运行，不安装成需要 root 权限和专用系统账号的多用户数据库服务。

## 默认位置

非沙盒版本的持久数据根目录为：

```text
~/Library/Application Support/Memora/
```

默认 Instance datadir 为：

```text
~/Library/Application Support/Memora/instances/default/
```

实现使用 Go 的用户主目录 API 取得当前用户目录，再解析 macOS Library 位置；不能拼接固定用户名或依赖当前工作目录。

## 为什么不照搬 MySQL 的绝对路径

MySQL 官方 macOS DMG 默认使用 `/usr/local/mysql/data`，由系统级 LaunchDaemon 和 `_mysql` 用户运行。这适合需要管理员安装的多用户 Server。

Memora 的物理层次参考 MySQL，但部署身份不同：它是个人用户数据，默认不应要求 sudo、不应由其他系统用户拥有，也不应与程序二进制安装目录耦合。因此采用 Apple 为用户级应用持久数据规定的 Application Support。

## 其他 macOS 目录

```text
~/Library/Caches/Memora/   可随时重建的缓存
~/Library/Logs/Memora/     daemon 和诊断日志
```

数据库 Page、Redo、Undo、Binlog、Data Dictionary、Router 和倒排索引都不能放入 Caches。Skill-first v0 不接收模型凭据；未来若引入自带 Provider，凭据只能进入系统 Keychain，不能进入 datadir。

## 路径覆盖

测试、独立 Instance 和高级用户可以显式指定 datadir，例如候选入口：

```text
memora init --data-dir /absolute/path
```

覆盖路径必须是绝对路径，并经过所有权、权限、可写性、文件系统能力和“未嵌套在另一个 Instance 中”的校验。项目当前目录、Git 仓库、iCloud Drive、Dropbox 等同步目录不能成为无提示默认值。

## Instance 顶层目录

默认 Instance 采用固定职责边界：

```text
default/
├── instance.meta   Instance 身份和启动所需最小元数据
├── system/         Data Dictionary、系统表和 Instance 级内部数据
├── databases/      每个逻辑 Database 的独立子目录
├── redo/           集中式 Redo Log
├── undo/           集中式 Undo tablespace
├── binlog/         Row-based Binlog、索引和位点元数据
└── tmp/            重建、排序、compaction 等可丢弃中间文件
```

这些目录由 daemon 创建和管理，用户与 Agent 不直接编辑。删除 `tmp/` 只能损失未完成的可重试后台工作；其他目录均属于数据库持久状态，不能当缓存清理。

`databases/` 下使用稳定 `database_id`，每库再分权威 data/history 与可重建 index generation。完整布局见 [Database 物理目录](./database-file-layout.md)。

## instance.meta v2

`instance.meta` v2 延续固定 44 字节的 bootstrap 编码，只保存启动不变量：

```text
magic[8] | format_version u32 | page_size u32
created_at_unix_nano i64 | instance_uuid[16] | crc32 u32
```

整数使用 little-endian；v1 默认 Page Size 为 16 KiB。文件权限为 `0600`，Instance 和固定子目录为 `0700`。

初始化先在同目录写临时文件并 `fsync`，再用不覆盖既有目标的 hard-link 原子发布，最后同步目录。两个进程同时首次初始化时只有一个 UUID 成为正式身份，另一方读取胜出的完整文件。重复 `memora init` 保持原身份；长度、magic、Page Size 或 CRC 错误都拒绝启动，绝不自动覆盖。v1 返回明确的 upgrade-required 状态，高于 v2 的格式返回 newer-format，不能都误报成损坏；迁移与回滚见 [Instance Format 升级与回滚 v1](./instance-format-upgrade-v1.md)。

## daemon 运行文件

F07 使用：

```text
system/daemon.lock
system/daemon.pid
system/security.sqlite
```

`daemon.lock` 的非阻塞排他 `flock` 是“一个 Instance 只有一个 writer daemon”的真相源；PID 文件只用于状态展示和发送 SIGTERM。锁释放但 PID 遗留时，`status/start` 将其视为 stale 并清理。daemon 启动前必须读取并校验 `instance.meta`，不能在未初始化或 metadata 损坏的目录中运行。

F46 起，`security.sqlite` 独立保存只含元数据和 payload hash 的 daemon 审计事件。它不进入逻辑 Database snapshot、Wiki 或 Database Package，避免审计记录随业务库导出；`memora doctor` 会严格验证其版本与记录完整性。

Unix socket 不放入 datadir，避免自定义深层路径超过 macOS `AF_UNIX` 上限。它位于仅当前用户可访问的临时运行目录，文件名由规范化 datadir 稳定派生；具体协议与清理规则见 [本地 IPC 协议](../development/ipc-protocol.md)。

## MySQL 参考边界

Instance 内继续参考 MySQL 的结构原则：一个 datadir、集中式事务日志、每个逻辑 Database 的独立子目录。文件扩展名、Page 格式和恢复协议由 Memora 自己定义，不兼容 `.ibd`。

## 尚未确认

- 除运行文件与 `security.sqlite` 外，`system/` 后续内部文件名；
- launchd 使用 LaunchAgent 还是其他用户级启动方式；
- 备份、快照和导出包默认输出位置；
- 自定义 datadir 位于外接盘或网络文件系统时的支持边界。

## 参考

- [Apple Application Support Directory](https://developer.apple.com/documentation/foundation/url/applicationsupportdirectory)
- [MySQL macOS Native Package Layout](https://dev.mysql.com/doc/refman/8.4/en/macos-installation-pkg.html)
- [MySQL macOS Launch Daemon](https://dev.mysql.com/doc/refman/8.4/en/macos-installation-launchd.html)

## 关联

- [Instance、Database 与 Table](./instance-database-table.md)
- [Database 物理目录](./database-file-layout.md)
- [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)
