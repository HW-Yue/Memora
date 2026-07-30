# 原生极简存储格式

状态：讨论稿。原生极简底座优先已确认；append-only 布局、字段编号与精确二进制
golden 须在 F52 开工前确认并冻结。

## 目标

先得到一个 Memora 自己可读写、可校验、可恢复的最小持久化核心。它不是 SQLite
包装，也不先实现 B+ Tree 数据库。AI 仍只操作 MSQL 和逻辑对象。

## 磁盘位置

```text
<instance>/
├── instance.meta
├── system/
│   └── system.memora
├── databases/
│   └── db_<stable-id>/
│       └── database.memora
└── tmp/
```

- `instance.meta`：只保存启动所需身份与 format version；
- `system.memora`：Database catalog、授权、审计和 Instance 级配置；
- `database.memora`：一个逻辑 Database 的全部权威逻辑记录与 Table Router；
- `tmp/`：迁移、校验和 compaction 的可丢弃临时输出。

第一阶段不创建 `redo/`、`undo/`、`binlog/`、per-table Tablespace 或独立索引
generation 文件。需要时由后续格式版本增加，不能预留一堆空架构冒充实现。

## 文件结构

每个 `.memora` 文件由固定 Header 和只追加 Transaction Frame 组成：

```text
File Header
Transaction BEGIN
  Logical Record Frame...
Transaction COMMIT
Transaction BEGIN
  ...
Transaction COMMIT
```

Header v1 固定 64 字节：

```text
magic[8] | format_version u32 | file_kind u16 | header_size u16
flags u32 | created_at i64 | file_uuid[16] | scope_uuid[16] | crc32 u32
```

整数使用 little-endian。magic、版本、类型、长度或 CRC 不符时拒绝打开，不能
猜测、静默升级或覆盖。

Frame Header v1 固定 32 字节：

```text
frame_length u32 | frame_type u16 | flags u16
transaction_id u64 | commit_sequence u64
payload_crc32 u32 | header_crc32 u32
```

`frame_length` 包含 Header 与 payload。未知 frame type 按版本兼容规则跳过或拒绝，
不能当成已知记录解码。

## 逻辑 Record

payload 使用 Memora 自己的 typed field 编码，不保存 SQL、Go struct dump 或
SQLite row。每条 Record 具有：

```text
object_kind | record_schema_version | stable_object_id | fields[]
```

每个 field 使用稳定数字 `field_id + logical_type + flags + length + bytes`；字段按
ID 排序，Text 为 UTF-8。首批 object kind：

- Database/Table/Column catalog；
- Row revision 与逻辑 tombstone；
- Relation revision；
- Table Route node 与完整 Row membership；
- Database 配置与 commit metadata；
- audit event（仅 `system.memora`）。

逻辑类型复用现有 NULL、Bool、Integer、Time、Text、Bytes、Stable ID 和列表契约。

## 提交与恢复

- 单 writer 为一个事务连续追加 BEGIN、Record、COMMIT；
- COMMIT 保存本事务 frame 数、范围和事务摘要；
- COMMIT 完整写入并 `fsync` 后事务才成功；
- 启动时顺序扫描，只发布校验完整且有 COMMIT 的事务；
- 崩溃留下的半个 frame 或无 COMMIT 尾部不可见，可在持有排他锁后截断；
- 已提交区间出现 CRC、sequence 或引用错误时报告 corrupt，不带病启动。

早期版本在打开时重建有界内存目录：stable ID → 最新 Record offset、Route child、
leaf membership 与反向 membership。它是确定性执行缓存，不是 AI 语义判断，也不
进入模型上下文。

## 演化边界

append-only 文件增长后，compaction 写入新的完整 `.memora` 文件，校验后原子
替换；长期 History 不能被错误丢弃。只有实测证明启动扫描、随机读取或并发成为
瓶颈，才引入 checkpoint、Page、B+ Tree、Buffer Pool 或独立日志。

无论物理结构怎样升级，MSQL、稳定 ID、revision、Table Router 和 logical
snapshot 不能改变。

## F52 验收

- Header/Frame/typed field 的 golden bytes；
- 空文件、单事务、多事务和 Unicode reopen；
- 每个字节截断点、CRC 损坏、未知版本和非法长度；
- 未提交尾部不可见，已提交事务不丢失；
- 相同逻辑输入得到确定性 payload；
- fuzz decode 不 panic、不越界、不无限分配。

## 关联

- [ADR-0003：原生极简 Store 优先](../decisions/0003-native-minimal-store-first.md)
- [逻辑类型与字段预算](../data/logical-types.md)
- [Logical Snapshot v1](./logical-snapshot-v1.md)
