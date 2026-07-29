# MSQL SELECT Planner v1

状态：F15d 已实现。

## 绑定边界

F15d SELECT 必须：

- 使用显式 `database.table`，不依赖隐式 current database；
- projection 只包含一段 Column/system identifier，或单独的 `*`；
- 携带可求值为 1–1000 的 `LIMIT`；
- WHERE 字段在扫描前全部绑定，空 Table 也不能掩盖未知字段或参数类型错误。

Column current name 和 alias 都绑定到稳定 `column_id`；输出只使用当前名称。`*` 按固定顺序返回 `row_id/revision/row_state/schema_version`，再按 Catalog Column 顺序返回业务字段。

## 参数与注入边界

named/positional 参数由 [MSQL 参数与表达式 v1](./msql-expressions.md) 绑定到 AST occurrence。参数内容不拼接为 SQL，也不传给 SQLite；带引号、分号、注释和关键字的值只能参与类型化比较。

## 读取计划

`WHERE row_id = <constant-or-parameter>` 以及包含它的 AND 条件走稳定 Row ID 精确读取，不受 Table 扫描窗口影响。Row 不存在时 SELECT 返回空结果，不把正常未命中升级为 statement error。

其他 predicate 按稳定插入顺序扫描当前 live Row。单次原型扫描窗口为 1000：

- 达到 SQL `LIMIT` 正常返回，`truncated = false`；
- 窗口耗尽但仍有 Row、且结果未达到 LIMIT 时返回已有结果并令 `truncated = true`；
- 后续 cursor/分页协议在完整查询链路中补齐，不能把不完整扫描冒充完整结果。

## 输出

Planner 返回 Result Envelope 可直接采用的 Column metadata 和 Row maps。系统整数保持 revision/schema version，Timestamp 已规范化 UTC，业务值不做字符串插值或再解析。

## 关联

- [MSQL 参数与表达式 v1](./msql-expressions.md)
- [Row Store v1](../data/row-store-v1.md)
- [MSQL Result Envelope v1](./result-envelope.md)
