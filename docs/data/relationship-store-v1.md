# Relationship Store v1

状态：F18a 已冻结持久化记录、revision 和双向索引；Row 引用完整性与 MSQL 接入由 F18b 完成。

## 系统记录

关系由统一的系统 Relationship Store 保存，不要求 AI 为每个业务库设计关系表。每条记录包含：

```text
relation_id
source(database_id, table_id, row_id)
relation_type
target(database_id, table_id, row_id)
description
revision, commit_sequence, state
created_at, updated_at
```

`relation_id` 是稳定的 `rel_...` 标识。source 和 target 使用稳定对象 ID，不依赖可变名称。关系记录和索引格式均有独立版本信封。

AI 定义 `depends_on`、`contradicts`、`part_of` 等 type 的业务语义；引擎只验证字段预算、对象完整性、事务和版本链，不推断 type 的含义。type 为 1–128 个字符，description 上限为 1200 个字符，禁止静默截断。

## Revision 与删除

创建从 revision 1 开始。删除是逻辑删除：当前记录变为 `deleted`，revision 递增，并追加不可变历史版本。修改必须携带 expected revision，冲突返回稳定错误。

关系的当前记录、历史版本、正向索引和反向索引必须由调用方在同一个 Store 事务中提交。回滚不能留下记录或孤立索引。

## 双向发现

引擎维护两种派生定位：

- source endpoint → outgoing relation IDs；
- target endpoint → incoming relation IDs。

索引只用于定位；读取仍回表验证当前记录，并过滤已删除版本。索引中的稳定 ID 可留待后续 compaction 回收。

图允许自环和循环关系。引擎不能把循环当成一致性错误，也不能替 AI 判断领域图是否合理。

## F18b 边界

F18a 的 transaction-scoped Store 接受稳定 endpoint，不单独判断 Row 是否存在。F18b 在 Row transaction 内完成：

- source/target 当前 Row 存在性；
- Row 删除时同事务失效全部入边和出边；
- 跨表与跨库 Policy；
- MSQL 参数绑定和 Batch 回滚。

## 关联

- [语义记录模型](./semantic-records.md)
- [Row Store v1](./row-store-v1.md)
- [History Store v1](./history-store-v1.md)
- [物理与检索索引](../storage/indexing.md)
