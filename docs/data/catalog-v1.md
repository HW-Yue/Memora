# Catalog v1

状态：F13 已实现 Database、Table、Column 和 MSQL Catalog Binder。

## 持久契约

Catalog 逻辑格式版本是 `memora.catalog/v1`。原型把完整逻辑 snapshot 作为一个 Store value 原子提交；上层不依赖 SQLite 表、rowid 或 SQL。F26 可以直接以该逻辑模型为迁移输入，而不是复制原型数据库文件。

每个对象拥有带类型前缀的稳定 ID：

```text
db_<uuid>
tbl_<uuid>
col_<uuid>
```

rename 只修改当前名称，不修改 ID。旧名称进入 alias，当前名称和 alias 都可继续解析。

Table 同时保存所属稳定 `database_id`。读取旧 F13 snapshot 时由父 Database 确定性补齐；Row Store 因而不需要把可变 Database 名称写进 Row。

## 名称规则

- 名称展示时保留原始 Unicode 和大小写；
- 查重与解析使用去除首尾空白后的 Unicode 小写形式；
- 当前名称和历史 alias 共用同一个冲突域；
- Database 在 Instance 内唯一，Table 在所属 Database 内唯一，Column 在所属 Table 内唯一；
- `SHOW` 结果按规范化名称确定性排序。

Column 当前名称和 rename 目标不能占用 `row_id`、`database_id`、`table_id`、`schema_version`、`revision`、`row_state`、`created_at`、`updated_at` 等系统信封字段。

F13 不做模糊同义合并。用途相近但名称不同的对象仍需由 Agent 提出显式 merge/rename 方案。

## 自描述最低要求

- Database 创建必须提供 `purpose` 和 `scope`；
- Table 创建必须提供 `purpose` 和 `row_semantics`；
- Column 创建必须提供逻辑类型和 `purpose`；
- `anti_scope` 和 Table `scope` 可选，但存在时随 Catalog 持久化。

Table 可在创建时原子携带初始 Column，也可随后逐个增加 Column。任一初始 Column 无效或重名时，整个 Table 都不会写入。F13b 读取 F13a 未包含 `columns` 字段的 snapshot 时将其规范化为空数组，保持同一 `memora.catalog/v1` 逻辑格式兼容。

F14 后 Column 的 `type` 保存规范名称，文本字段另存当前 `max_characters`。旧 snapshot 中的 `TEXT(n)` 在读取时规范化为 `type = TEXT` 和 `max_characters = n`；类型集合与值验证见 [逻辑类型与字段预算 v1](./logical-types.md)。

缺失必填语义返回 `validation_error`，名称或 alias 冲突返回 `already_exists`，解析不到对象返回 `not_found`。

## Schema version

- 新对象从 `schema_version = 1` 开始；
- Table 创建、rename 或 Column 结构变化都会增加所属 Database 版本；
- Column 增加或 rename 会增加所属 Table 版本；
- 对象 rename 增加该对象自身版本；
- 纯读取和同名 no-op 不增加版本。

时间使用 UTC；测试可以注入 Clock 和 ID source。关闭并重新打开 Store 后，稳定 ID、alias、描述和版本必须完全保留。

## 关联

- [自描述 Data Dictionary](./self-describing-data-dictionary.md)
- [SQLite 原型 Store](../decisions/0001-prototype-store.md)
- [历史 Phase B TDD 计划](../archive/planning/tdd-phase-b-database.md)
- [MSQL Catalog DDL v1](../query/catalog-ddl.md)
