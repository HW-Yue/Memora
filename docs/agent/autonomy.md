# AI 自主权与约束

状态：分层原则已形成；默认 Policy 尚未确定。

## 四层治理

1. 引擎不变量：事务、版本、权限、引用和系统字段，AI 不能绕过；
2. 个人 Policy：决定某个数据库允许 AI 自动执行哪些操作；
3. Schema/Ontology 治理：允许演化，但需要描述、查重和历史；
4. Agent 自主区：在前三层范围内自主建库、建表和维护数据。

## 风险等级

- L0：SHOW、DESCRIBE、SELECT、EXPLAIN，自动执行；
- L1：局部、可逆、同库写入，通常自动执行；
- L2：Schema、批量修改、跨库关系，先生成影响计划；
- L3：PURGE、清历史、降低隐私、强制覆盖，必须显式授权。

## 标准修改流程

```text
Discover → Plan → Validate → Execute → Verify → Commit/Rollback
```

重要修改携带：

- expected revision；
- Schema version；
- reason；
- actor 和 source；
- 最大影响行数；
- 可访问数据库范围。

## Schema 自主性

AI 创建类型前需要发现已有表和字段。发现同义表或字段时，AI 直接生成 merge/rename/migration 计划；引擎在事务中迁移数据、关系、索引和别名，并保留可回滚历史。引擎不能替 AI 决定业务语义。

每次创建或演化都必须同时维护 purpose、scope、anti-scope、Row 语义、别名和示例。只创建名称和类型、不解释用途的 Schema 不算有效提交。

## 未决问题

- L1/L2 的默认边界；
- AI 创建新数据库是否需要用户确认；
- Schema 相似性在没有向量时如何判断；
- 多 Agent 同时演化 Schema 时如何合并；
- Policy 由用户自然语言生成后如何验证没有越权。

## 关联

- [MSQL](../query/msql.md)
- [MVCC、Undo Log 与 Redo Log](../storage/mvcc-undo-redo.md)
- [自描述 Data Dictionary](../data/self-describing-data-dictionary.md)
