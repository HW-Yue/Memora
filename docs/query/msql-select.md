# MSQL SELECT Planner v1

状态：F15d 已实现；F112 已补充 point Row detail metadata；每条 Row 返回 `route_paths`。

> **目标形态已改——但 `route_paths` 的行为不变，只是换数据来源。**
> 本文说它「由 `row_id → memberships` 反向索引直接解析」。
> [写入形态](../product/write-model.md)去掉了独立的 membership 关系，
> 这个反查改由一棵专门的**反向索引树**（`row_id → leaf_ids`）承担。
> 返回内容、字段名和语义都不变。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码。
> 迁移设计见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

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

每条返回的 Row 都携带 `route_paths`：该 Row 当前挂载的全部 leaf 的完整语义索引路径
（如 `/架构/存储`）。它由 `row_id → memberships` 反向索引直接解析，不依赖逐层 Route
导航或 `OPEN ROUTE`；Row 未挂载任何 leaf 时为空数组。路径用于导航归并与语义定位，
不替代 `row_detail` 或 Row 正文。

包含精确 RowID predicate 的 SELECT 还返回 `memora.row-detail/v1`。业务 Column metadata
携带稳定 Column ID、purpose 与显式 semantic role；`SELECT *` 按 Catalog Column 顺序
保留全部动态字段。title role 缺失时只声明 `row_id_revision` fallback，禁止猜列名。

完整契约见 [MSQL Row Detail Read v1](./row-detail-read-v1.md)。

## 关联

- [MSQL 参数与表达式 v1](./msql-expressions.md)
- [Row Store v1](../data/row-store-v1.md)
- [MSQL Result Envelope v1](./result-envelope.md)
