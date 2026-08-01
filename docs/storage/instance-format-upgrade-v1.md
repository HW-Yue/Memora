# Instance Format 升级与回滚 v1

状态：F49 已实现；v1→v2 迁移、备份和 repair 边界已冻结。

## 兼容状态

当前 binary 的 Instance format 为 v2，最低可升级版本为 v1。读取
`instance.meta` 时必须区分：

- `current`：format v2，可启动 daemon；
- `upgrade_required`：format v1，只能 plan/upgrade/repair；
- `newer_format`：format 大于 v2，拒绝打开，不能尝试降级；
- `corrupt`：magic、长度、Page Size、UUID 或 checksum 无效。

较新 format 不是损坏。普通 `init`、daemon start/run 与数据操作都不能静默
迁移、覆盖或降级 Instance。

## v1→v2

v2 保持 44 字节 `instance.meta` 编码、Instance UUID、创建时间和 Page Size
不变，只把 format version 提升为 2，并新增：

```text
system/format-state.json
```

该文件记录版本化 migration ID、from/to version、Instance ID、备份点和完成时间。
它只记录从 v1 迁移到 v2 的来源和恢复点；全新创建的 v2 Instance 不要求该文件，
也不承载业务 Row。

命令分两步：

```bash
memora upgrade --plan [--data-dir ...]
memora upgrade --apply --yes [--data-dir ...]
```

Plan 是只读 JSON，不创建目录。Apply 要求 daemon 已停止并持有同一 daemon
排他锁；未带 `--yes`、版本不相邻、Instance 已是当前版本或来自更新 binary
均失败。

## 备份与 journal

修改前在 Instance 同级的 `<name>.memora-backups/` 下原子发布一个 `0700`
备份点。备份包含除 `tmp/`、daemon PID/lock 和迁移 journal 外的全部有界普通
文件，并用 `memora.instance-backup/v1` manifest 记录相对路径、mode、size 与
SHA-256。符号链接、特殊文件、路径逃逸、文件数或总大小超预算都会阻断升级。

随后原子写入：

```text
system/format-migration.json
```

journal 绑定 Instance ID、from/to version 和备份绝对路径。先发布 journal，
再替换 `instance.meta`，再发布 `format-state.json`，最后删除 journal。journal
存在即表示迁移未完成；`instance.Read` 和 daemon 必须拒绝启动。

## Repair

中断后运行：

```bash
memora doctor repair --yes [--backup /absolute/backup] [--data-dir ...]
```

未指定 backup 时只接受当前 journal 绑定的备份。Repair 要求 daemon 停止、
显式确认、备份 manifest/hash 全部有效且 Instance ID 一致。它从备份逐文件
原子恢复，清除备份后新增的持久文件，删除 v2 format state 与 journal，再验证
v1 状态；可以安全重试。对已完成迁移发起显式 backup rollback 时，Repair 会先
发布同样的 journal，崩溃后仍只能续用已绑定备份。
Repair 不接受任意目录、不修补损坏 backup、不删除 backup，也不恢复其他
Instance。

F49 不支持原地 downgrade。需要回退 binary 时，必须先用该 binary 创建的
兼容备份执行 repair；成功前旧 binary 与原 datadir 的组合保持拒绝状态。

## 关联

- [macOS Instance 数据目录](./macos-instance-directory.md)
- [测试约定](../development/testing.md)
- [历史 Phase D](../archive/planning/tdd-phase-d-release-kernel.md)
