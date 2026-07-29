# macOS Instance 数据目录

状态：第一阶段平台、默认根路径和 Instance 顶层目录已确认；各目录内部文件继续逐项设计。

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

实现使用 macOS/Go 的标准用户目录 API 解析 Application Support，不能在代码中拼接固定用户名或依赖当前工作目录。

## 为什么不照搬 MySQL 的绝对路径

MySQL 官方 macOS DMG 默认使用 `/usr/local/mysql/data`，由系统级 LaunchDaemon 和 `_mysql` 用户运行。这适合需要管理员安装的多用户 Server。

Memora 的物理层次参考 MySQL，但部署身份不同：它是个人用户数据，默认不应要求 sudo、不应由其他系统用户拥有，也不应与程序二进制安装目录耦合。因此采用 Apple 为用户级应用持久数据规定的 Application Support。

## 其他 macOS 目录

```text
~/Library/Caches/Memora/   可随时重建的缓存
~/Library/Logs/Memora/     daemon 和诊断日志
```

数据库 Page、Redo、Undo、Binlog、Data Dictionary、Router 和倒排索引都不能放入 Caches。模型凭据进入 macOS Keychain，不进入 datadir。

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

## MySQL 参考边界

Instance 内继续参考 MySQL 的结构原则：一个 datadir、集中式事务日志、每个逻辑 Database 的独立子目录。文件扩展名、Page 格式和恢复协议由 Memora 自己定义，不兼容 `.ibd`。

## 尚未确认

- `instance.meta` 的编码、校验和原子更新协议；
- `system/` 的内部文件名；
- launchd 使用 LaunchAgent 还是其他用户级启动方式；
- Unix socket、PID 和锁文件位置；
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
