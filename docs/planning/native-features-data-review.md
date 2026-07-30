# F53–F61：真实数据与 MSQL 闭环

状态：已批准；每项都不承诺事务、掉电安全或并发。

实现进度：F53a–F56 已完成；后续项仍按本文件门禁推进。

## F53a Typed Payload Round-trip

- 故事：`US-ENGINE`；
- 写入：稳定版本的 typed fields，不使用 JSON 或 Go struct dump；
- 读取：经原生文件 close/reopen 后恢复 TEXT、整数、布尔和文本列表；
- 测试：中文、负数、截断拒绝；
- 完成：typed payload 的独立物理闭环。

## F53b Catalog Record Round-trip

- 故事：`US-SCHEMA`、`US-ENGINE`；
- 写入：Database、Table、Column 各自使用稳定 object kind 与 typed fields；
- 读取：reopen 后按 ID 重建父子关系、名称、用途、Column 类型和 Schema version；
- 测试：中英名称、alias、nullable、TEXT budget、未知字段、确定性 bytes；
- 不做：Row、Catalog UPDATE、MSQL、JSON/Go struct dump；
- 完成：一个真实 Catalog fixture byte-for-byte/field-for-field round-trip；依赖 F53a。

## F54 Row Record Round-trip

- 故事：`US-INSERT`、`US-READ`；依赖 F53；
- 写入：RowID、DatabaseID、TableID、Schema version、revision=1、state、ColumnID values；
- 读取：reopen 后按 RowID 解码，并用 F53 Catalog 校验类型与 Column 归属；
- 测试：NULL、INTEGER、BOOLEAN、TIMESTAMP、TEXT、RELATION_ID、中文与超限拒绝；
- 不做：UPDATE、DELETE、History、MSQL；
- 完成：真实 Row 与 Catalog 一起重开后完全等价。

## F55 Latest Revision 与 Tombstone

- 故事：`US-UPDATE`、`US-DELETE`、`US-ENGINE`；
- 写入：同一 object ID 允许严格递增 revision；逻辑删除写 tombstone；
- 读取：Get latest、Get exact revision、默认隐藏 tombstone；
- 测试：revision 1→2、陈旧 revision 拒绝、重开后 latest、删除后不可见；
- 不做：跨对象原子性、History、compaction；
- 完成：一个 Row revise/delete 的物理闭环。

## F56 Native Catalog MSQL

- 拆分：F56a Catalog schema revision、F56b typed service、F56c MSQL/Schema evolution；
- 故事：`US-COLD`、`US-SCHEMA`；
- 旅程：`CREATE DATABASE/TABLE → close/reopen → SHOW DATABASES/TABLES → DESCRIBE`；
- 接口：业务层依赖 typed Catalog repository，不依赖 offset 或文件名；
- 测试：稳定 envelope、Binder、rename/alias、重启后 Schema version；
- 不做：Row INSERT、事务、切换 daemon 默认 Store；
- 临时限制：仅隔离 fixture；I/O 中断可报失败并丢弃测试文件，不宣称原子回滚；
- 完成：Catalog 的第一个 MSQL 垂直闭环。

## F57 Native INSERT/SELECT

- 故事：`US-INSERT`、`US-READ`；
- 旅程：F56 建模 → `INSERT` → close/reopen → `SELECT ... WHERE row_id=? LIMIT 1`；
- 测试：参数绑定、类型/预算错误、RowID、revision、投影和不存在 Row；
- 不做：UPDATE、DELETE、History、Router、事务；
- 临时限制：仅隔离 fixture，不进入用户默认 datadir；
- 完成：用户数据首次通过正式 MSQL 写入并从原生文件回表。

## F58 Native UPDATE/DELETE

- 故事：`US-UPDATE`、`US-DELETE`；依赖 F55/F57；
- 旅程：SELECT revision → UPDATE/DELETE with expected revision → reopen → SELECT；
- 测试：精确目标、陈旧写拒绝、tombstone、未误伤其他 Row；
- 不做：History、关系和 Route 同步、事务；
- 完成：单 Row 修改闭环，明确仍可能在跨对象更新时部分成功。

## F59 Native History

- 故事：`US-CORRECT`、`US-UPDATE`；
- 旅程：INSERT/UPDATE → reopen → `SHOW HISTORY` 得到所有 revision；
- 测试：顺序、actor/source/reason、deleted revision、exact revision lookup；
- 不做：RESTORE、事务 Undo、物理 Purge；
- 完成：History append/read 闭环，不把它冒充事务恢复。

## F60 Native Relation

- 故事：`US-INSERT`、`US-READ`、`US-SPLIT`；
- 旅程：`RELATE → reopen → SHOW RELATIONS` 正反向均可定位 RowID；
- 测试：跨表/跨库 Policy、重复边、逻辑删除、悬空引用拒绝；
- 不做：图评分、多跳自动扩展、向量；
- 完成：关系 Record 和双向内存目录闭环。

## F61 Table Router 物理闭环

- 故事：`US-COLD`、`US-READ`、`US-DBA`；
- 旅程：为 Table 建 root/branch/leaf、挂 RowID → reopen → SHOW/UNDER/OPEN；
- 测试：每 Table 独立 root、cursor、fan-out、multi-membership、反向 membership；
- 不做：AI 自动选路、Skill 改造、split/merge 原子性、MATCH fallback；
- 完成：Table Router 数据结构已真实落盘并能逐层读取。
