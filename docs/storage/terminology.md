# 存储引擎术语

状态：命名规范已确认；物理布局仍需原型验证。

## 原则

与 MySQL/InnoDB 概念相同的结构使用相同名称。语义不相同的 Memora 能力保留独立名称，不能只为熟悉感硬套术语。

## 采用的通用术语

| 术语 | 在 Memora 中的含义 |
| --- | --- |
| Instance | 一个本地 Memora 运行和存储实例；替代存储层的 Vault |
| Database | Agent 面向的逻辑数据库；SQL 中可与 Schema 视为同义入口 |
| Table / Column / Row | 逻辑表、字段和数据行 |
| Data Dictionary | Database、Table、Column、Index 和版本定义；替代 Schema Catalog |
| Tablespace | Page 的逻辑地址空间，可由一个或多个 Data File 承载 |
| Data File | Tablespace 的物理文件；达到策略上限后可以滚动创建 |
| Page | 固定大小的 I/O、校验和缓存单位 |
| Extent | 一组连续 Page，作为批量空间分配单位 |
| Segment | 为某个索引或存储结构分配的 Page/Extent 集合，不是文件 |
| Record | Row 某个 revision 的物理编码 |
| Buffer Pool | 有容量上限的 Page 内存缓存；替代 Page Cache 作为正式名称 |
| Undo Log | 回滚未提交修改并支持旧版本读取的信息 |
| Redo Log | 遵守 WAL 顺序的崩溃恢复日志；正式名称不再写成 WAL 文件 |
| Binlog | 已提交逻辑事务的有序变更日志，用于设备同步、订阅和时间点恢复 |
| LSN | Redo Log 的字节位置/顺序，不与 commit sequence 混用 |
| Transaction ID | 当前 Instance 内一次事务的身份，用于锁、Undo 和事务状态 |
| Global Transaction ID | 已提交事务跨设备不变的来源身份，用于同步幂等、位点和防回环 |

`Vault` 只在“Obsidian Vault”语境使用，避免同时表示 Memora 存储实例。

## Memora 独有术语

以下概念没有 MySQL 等价物，继续使用 Memora 名称：

- MSQL；
- Semantic Record；
- Router、Route Frame；
- Context Pack；
- Query Agent / Mutation Agent；
- object revision；
- Buffer Pool / Buffer Frame / Dirty Page / Page Table。

`transaction ID`、`global transaction ID`、`commit sequence`、`revision`、Redo `LSN` 和 Binlog position/event ID 表示不同维度，不能因为都递增就共用字段。

## 避免的歧义

- 不把固定大小物理文件称为 Segment，应称 Data File；
- 倒排索引的不可变批次称 `Posting Run`，避免与 Tablespace Segment 冲突；
- 逻辑 Row 不暴露 Page、Slot 或物理 Record 地址；
- Object/Row Directory 是 Memora 候选结构，若最终不符合聚簇索引定义，不得冒称 Clustered Index；
- Write-Ahead Logging 描述 Redo Log 的写入约束，不作为另一套日志名称。

## 参考

- MySQL 8.4 File Space Management：https://dev.mysql.com/doc/refman/8.4/en/innodb-file-space.html
- MySQL 8.4 InnoDB Limits：https://dev.mysql.com/doc/refman/8.4/en/innodb-limits.html

## 关联

- [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
