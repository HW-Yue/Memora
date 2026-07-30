# F52 原生文件格式开工门

状态：待用户确认格式方向后 PASS；未开始实现。

## Feature

`F52 define minimal native file format`

目标用户故事：`US-ENGINE`、`US-RECOVER`、`US-DEVELOPER`。

用户结果：Memora 的持久数据不再由 SQLite 格式定义；损坏、版本不兼容和崩溃尾部
可以确定性识别，后续服务层可以在不改变 MSQL 的情况下迁到自有文件。

## 标准旅程

F52 本身只提供 codec，不新增 MSQL。F55 接通后必须保持以下旅程：

```sql
CREATE DATABASE project_memora PURPOSE :purpose;
CREATE TABLE project_memora.decisions (...);
INSERT INTO project_memora.decisions (...) VALUES (...);
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
-- restart native store
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
```

两次 SELECT 的逻辑 Row、revision 和 stable ID 必须一致；MSQL 结果不能出现 offset、
frame、文件名或 SQLite 概念。

## 数据与恢复影响

- 新格式：64-byte File Header、32-byte Frame Header、typed logical fields；
- 事务：append BEGIN/Record/COMMIT，完整 COMMIT + fsync 才成功；
- 恢复：忽略未提交尾部，已提交区损坏则拒绝打开；
- 上下文：不影响 AI Route Frame；codec 不进入模型上下文；
- 回滚：F52 不读取或修改现有用户数据，只生成隔离测试文件。

## 永久边界审计

- 不使用 SQLite、Embedding、Vector/cosine；
- 不实现 SQL、Router 或物理 Page 旁路；
- 不把 Go struct 内存布局直接落盘；
- 不提前实现 B+ Tree、MVCC、Undo/Redo、Binlog；
- decoder 对长度和内存分配设置硬上限。

## 架构披露

已确认：先做自有极简底座，再接现有系统，迁移验证后删除旧原型。

待确认：

1. 是否接受“每库一个 append-only `database.memora`，启动扫描重建内存定位”的
   最小方案；
2. 用户所说要删除的“socket”究竟是 SQLite，还是 Unix socket/IPC。

开工前结论：`PENDING USER CONFIRMATION`。

## 完成证据要求

golden bytes、truncate/CRC/unknown-version matrix、fuzz、deterministic encoding、
reopen 和未提交尾部测试全部通过后，才可把完成后结论标为 PASS。
