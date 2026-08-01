# F52 原生 Put/Get 开工门

状态：开工前 PASS；完成后 PASS。

## Feature

`F52 native record put/get`

目标用户故事：`US-ENGINE`、`US-DEVELOPER`。

用户结果：开发者能证明 Memora 自己的文件确实保存了 Record；后续功能不再建立
在一份尚未写入、尚未读取过的纸面格式上。

## 当前闭环

```text
Create database.memora
→ Put(kind, stable_id, payload)
→ Close
→ Open
→ Get(kind, stable_id)
→ payload byte-for-byte 相同
```

F52 不新增 MSQL。它只验证物理底座；F54 必须立即用下面的产品闭环证明可以接上：

```sql
CREATE DATABASE project_memora PURPOSE :purpose;
CREATE TABLE project_memora.decisions (...);
INSERT INTO project_memora.decisions (...) VALUES (...);
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
```

## 明确不做

- 不做 BEGIN/COMMIT、rollback 或多 Record 原子性；
- 不做 fsync durability、崩溃尾部恢复或自动 repair；
- 不做 Update/Delete/History/Relation/Router；
- 不接 daemon、不切换默认 Store、不迁移/删除 SQLite；
- 不做 Page、B+ Tree、Buffer Pool、MVCC、Undo/Redo、Binlog；
- 不使用 Embedding、Vector/cosine 或隐藏 MSQL 旁路。

## 实现与错误边界

- 32-byte File Header、24-byte Record Header；
- 单 writer，append one Record；
- Open 扫描并建立 ID → offset；
- Get 校验 kind、ID、长度和 payload CRC；
- 重复 ID、半条 Record、未知版本和损坏只报错，不恢复；
- 所有长度在分配前做硬上限检查。

## 计划证据

- 先写失败的 close/reopen Put/Get 测试；
- golden bytes 与 deterministic encoding；
- Unicode、空值、多 ID、重复 ID；
- magic/version/length/CRC 错误矩阵；
- fuzz decode；
- Feature 实现不导入 SQLite driver。

## 实际完成证据

- `internal/store/native` 实现 File/Record Header、Create/Open/Put/Get/Close；
- 中文、空 payload、多 ID、重复 ID 和 close/reopen byte equality 已通过；
- Header/Record golden bytes 已通过；
- 每个截断 prefix、CRC、未知版本、非法 identity/长度已通过；
- decoder fuzz seed、unit、race、integration、e2e 与 cross-build 已通过；
- 实现只使用 Go 标准库，未导入 SQLite。

完成后结论：`PASS`。这只证明 F52 字节闭环，不代表事务、恢复、真实 Row 或
AI-native 产品已经通过。
