# 原生极简存储格式

状态：F52–F63 已实现；typed objects、事务发布、fsync 与崩溃尾恢复已闭环。

## 当前唯一目标

把一个带稳定 ID 和类型的 Record 写进 Memora 自有文件，关闭文件，重新打开，
再按同一 ID 取出完全相同的内容。

F52 不证明数据库已经可用，只证明最底层字节闭环真实成立。闭环通过前不实现
事务、rollback、崩溃恢复、MVCC、Undo/Redo、Binlog、Page、B+ Tree 或 Buffer
Pool。

## 磁盘位置

```text
<instance>/
├── instance.meta
├── system/system.memora
├── databases/db_<stable-id>/database.memora
└── tmp/
```

F52 只在测试临时目录创建一个 `database.memora`。它不读取、修改或迁移现有
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
F62 已增加事务帧，F63 已完成 fsync 与崩溃尾恢复。F64 开始让 Row、History、
Relation 和 Route membership 使用同一事务原子发布。

后续版本可以改变物理 format version，但必须提供明确迁移；不能为了避免升级而把
F52 重新膨胀成完整内核。

## F52 验收

- 空文件 create/open；
- ASCII、中文、空 payload 与边界长度 Put/Get；
- close/reopen 后 byte-for-byte 相同；
- 多个不同 ID 可读取，重复 ID 被拒绝；
- magic/version/length/CRC 损坏得到稳定错误；
- fuzz decode 不 panic、不越界、不无限分配。

## 关联

- [F52 开工门](../planning/f52-native-format-gate.md)
- [ADR-0003](../decisions/0003-native-minimal-store-first.md)
- [逻辑类型与字段预算](../data/logical-types.md)
