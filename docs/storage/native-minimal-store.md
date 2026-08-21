# 原生极简存储格式

状态：F52–F65 已实现；typed objects、事务发布、fsync、崩溃尾恢复和跨对象
reshape 已闭环。

> **这个文件的角色正在改变。** 它现在是通用记录存储：13 种 object kind 全塞在同一个
> 平坦 append-only 文件里，靠一张常驻内存的 `records` map 做唯一的物理索引——
> 而每个版本是一条独立记录，于是那张 map 随"写过多少次"增长、永不释放。
> [聚簇行存储 v1](./clustered-row-storage-v1.md) 把每一类对象的**当前**状态移进树，
> 本文件收敛为**版本区**：只装被指针链引用的历史版本。
> 链上的记录永远不按名字查，所以版本区不需要索引，那张 map 随之消失。
> 现状见[存储层总览](./README.md)第 7 节。

## 当前唯一目标

把一个带稳定 ID 和类型的 Record 写进 Memora 自有文件，关闭文件，重新打开，
再按同一 ID 取出完全相同的内容。

F52 不证明数据库已经可用，只证明最底层字节闭环真实成立。闭环通过前不实现
事务、rollback、崩溃恢复、MVCC、Undo/Redo、Binlog、Page、B+ Tree 或 Buffer
Pool。

## 磁盘位置

F52 起草时设想的是每 Database 一个文件（`databases/db_<stable-id>/database.memora`）。
**实现没有走这条路，而且不会走了**：所有 Database 共用一套物理文件，理由与评估见
[F226](../planning/f226-per-database-fault-isolation.md)。下面是实测的当前布局
（两个 Database 与一个 Database 的文件集合完全相同）：

```text
<instance>/
├── instance.meta
├── system/{auxiliary,security}.memora, daemon.lock
├── databases/
│   ├── database.memora            # 所有 Database 的权威 Record
│   ├── page-authority-v1.json
│   ├── page-index-v1/             # 四棵派生树，全 Instance 共用
│   │   ├── {catalog,current,versions,fulltext}.pages
│   │   ├── {catalog,current,versions,fulltext}.wal/
│   │   └── manifest.json
│   └── change-index-v1/           # 单一全局 commit sequence
│       ├── changes.pages
│       └── changes.wal/
└── {tmp,binlog,redo,undo}/
```

单套文件是**有意选择**，不是待办：最热的读路径（Catalog Atlas 与
`SHOW LEXICAL LOCATIONS FROM ALL TABLES`）本来就跨 Database，拆分会把它变成
常态 fan-out。故障隔离改在逻辑层做，见 F226 Stage 1。

F52 当时只在测试临时目录创建一个 `database.memora`，不读取、修改或迁移现有
SQLite 文件，也不切换 daemon 默认后端。

## Bootstrap v0 文件

文件包含一个 Header、独立 Record，以及由 BEGIN/COMMIT 包围的事务 Record：

```text
File Header
Record Frame
Transaction BEGIN
Record Frame ...
Transaction COMMIT(digest)
Record Frame
...
```

File Header v0 固定 32 字节：

```text
magic[8] | format_version u16 | header_size u16
file_kind u16 | flags u16 | file_uuid[16]
```

Record Header v0 固定 24 字节：

```text
record_length u32 | object_kind u16 | flags u16
record_schema_version u32 | id_length u32
payload_length u32 | payload_crc32 u32
```

Header 后依次写 stable ID bytes 和 payload bytes。整数使用 little-endian，ID 和
Text 使用 UTF-8。`record_length = 24 + id_length + payload_length`。

## 最小 API

```text
Create(path, file_kind) → File
Open(path) → File
Put(object_kind, schema_version, stable_id, payload)
Get(object_kind, stable_id) → payload
Close()
Begin() → Transaction[Put, Commit, Rollback]
```

- v0 只允许单进程、单 writer；
- `Put` 每次追加一个完整 Record Frame；
- v0 同一 `object_kind + stable_id` 重复写入返回错误，不定义 update；
- `Open` 顺序扫描完整 Record，建立 ID → file offset 内存表；
- `Get` 按 offset 读取并校验 kind、ID、长度和 payload CRC；
- `Close` 只负责关闭文件；F52 不承诺掉电 durability。
- F62 在 COMMIT 后一次发布事务内全部 Record；缺少 COMMIT 的完整事务重开后不可见；
- F63 在数据帧和 COMMIT 间设置 fsync 边界；重开会截断未提交事务或半写尾部；
- 已完成 Record 的 CRC 或已完成 COMMIT 的 transaction digest 损坏仍拒绝打开。

## 错误边界

- magic、版本、长度、ID、CRC 或重复 ID 不合法时返回稳定错误；
- 文件尾存在半条 Record 或未提交事务时恢复到最后完整发布边界并截断；
- decoder 必须先校验长度上限，再分配内存；
- 不识别的 format version 拒绝打开；
- 这些是读取正确性检查，不是崩溃恢复。

## 后续闭环

F53a–F61 已完成 Catalog、Row、MSQL CRUD、History、Relation 与 Table Router；
F62 已增加事务帧，F63 已完成 fsync 与崩溃尾恢复。F64 已让 Row、History、
Relation 和 Route membership 可由同一 Mutation Plan 原子发布。
F65 进一步让 split/merge 的多个 Row、History、Relation、Route 和 membership
在同一事务内发布，并用 `superseded` 保留来源历史。
F66 增加 logical snapshot 与 native typed authority 之间的原子导入/确定性导出，
作为旧后端迁移桥；snapshot metadata 不是运行时真相源。
F68 已将 daemon 默认 authority 切换为 `database.memora`；旧 SQLite authority
只作为保留源和显式备份存在，切换前必须通过 logical snapshot hash 回读验证。
F109 新增 `ObjectKindCommittedChange=12`；change envelope 与同一逻辑事务的 immutable
body 共用 transaction COMMIT，rollback 或 crash tail 均不可见。

后续版本可以改变物理 format version，但必须提供明确迁移；不能为了避免升级而把
F52 重新膨胀成完整内核。

## 当前 RowID 读取现实

MSQL Executor 已把精确 `WHERE row_id = :id` 识别为专用 Get，不扫描业务 Row。
文件层也会在重开时建立 `record_id → payload offset` 内存 Map，并用 `ReadAt`
读取选中的 Record。

但 Get 前的 `DescribeTable` 目前会重读并组装全部 Catalog；逻辑 Row Repository
为找到最新 revision，也会列出并排序全部 Row record ID，再匹配 `row_id` 与
`row_id@revision`。因此当前精确 RowID Get 随 Catalog 与 Row revision 总数增长，
还不是最终的 B+ Tree point-get 路径。

下一步目标是 16 KiB Page 上的持久化 B+ Tree、单实例 Buffer Pool 与 Redo WAL：
Catalog、当前 Row、Row version 和 Table 顺序拥有已提交 root，内存 Map 只作缓存。
它不改变 MSQL、RowID、History 或 Route。见
[ADR-0005](../decisions/0005-btree-mandatory-primary-index.md)和
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

## F52 验收

- 空文件 create/open；
- ASCII、中文、空 payload 与边界长度 Put/Get；
- close/reopen 后 byte-for-byte 相同；
- 多个不同 ID 可读取，重复 ID 被拒绝；
- magic/version/length/CRC 损坏得到稳定错误；
- fuzz decode 不 panic、不越界、不无限分配。

## 关联

- [F52 开工门](../archive/planning/f52-native-format-gate.md)
- [ADR-0003](../decisions/0003-native-minimal-store-first.md)
- [逻辑类型与字段预算](../data/logical-types.md)
