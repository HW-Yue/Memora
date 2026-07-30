# Skill Schema 生命周期 v1

状态：F32 已实现并冻结。

## 目标

宿主 Agent 可以为新语义领域创建或复用 Database/Table，也可以在明确
影响范围内 rename，而不能绕过 Catalog、直接接触物理文件或提交任意 DDL。

入口是 `memora schema --plan <JSON>`，计划版本为
`memora.schema-plan/v1`，收据版本为 `memora.schema-receipt/v1`。

## Ensure

Ensure Plan 必须包含：

- Database 的 `name/purpose/scope`；
- Table 的 `name/purpose/row_semantics`；
- 至少一个带 `name/type/purpose` 的 Column；
- actor、source event、reason 和 authorized databases。

Runner 先 `SHOW DATABASES`，再 `SHOW TABLES`。复用只依据提议名称、计划显式
给出的同义词、当前名称和 Catalog aliases 的大小写无关精确匹配；不进行模糊
语义猜测。没有匹配时才生成受限 `CREATE DATABASE/TABLE` MSQL。所有读写仍
经过统一 daemon/MSQL/Catalog 链路。

## Migration

Migration Plan 必须声明 Database 当前 schema version、每个对象的预期
version，以及 `max_affected_objects`。v1 只接受：

- `RENAME_TABLE`；
- `RENAME_COLUMN`。

Preview 返回对象数、固定为零的 Row 影响、反向步骤和确定性 SHA-256 影响
hash。超出上限、版本过期、越权或不可逆 action 在 DDL 前失败。

Catalog DDL 当前是 autocommit，所以多个 rename 不伪装成原子事务。Runner
在每步前读取 revision；中途失败时，以相反顺序执行已完成步骤的补偿 rename，
随后 `DESCRIBE` 验证当前名称。收据区分 `applied`、`rolled_back` 和
`rollback_failed`；即使回滚成功，原迁移仍以原稳定错误码失败。

Rename 保持稳定对象 ID，并按 Catalog 规则保留 alias、递增对象和上层 Schema
version。涉及删除、类型变化、隐私缩小或大范围迁移时，宿主必须请求用户，
不属于 v1 自动执行范围。

## 关联

- [Canonical Skill v1](./canonical-skill-v1.md)
- [MSQL Catalog DDL v1](../query/catalog-ddl.md)
- [Catalog v1](../data/catalog-v1.md)
