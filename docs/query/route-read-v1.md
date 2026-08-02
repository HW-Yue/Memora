# MSQL Route Read v1

状态：F111 已实现并冻结。

## 目的

Admin 与 Agent 使用同一条 MSQL 链路逐层读取 Table Router：point node、children page、
single locator leaf。Route 只负责导航，任何读取都不得夹带 Row 正文、答案、embedding
或物理 Store 信息。

## 分层语法

```sql
DESCRIBE ROUTE :route_id;
SHOW ROUTES FROM TABLE work.notes AT ROOT [CURSOR :cursor] LIMIT :limit;
SHOW ROUTES UNDER :parent_id [CURSOR :cursor] LIMIT :limit;
OPEN ROUTE :leaf_id [CURSOR :cursor] LIMIT :limit;
```

- `DESCRIBE` 是一个有界 point read，返回 node 元数据、`database_id/table_id` scope 与
  按需 synopsis，不返回 children；scope 字段供 stable-ID 深链路验证，不加入逐层
  `SHOW` 的紧凑 Route Frame；
- `SHOW` 返回一层 child node，默认不返回 synopsis；
- `OPEN` 只接受 leaf，只返回零个或一个 `database_id/table_id/row_id/revision` locator；
- 业务字段和正文只能由后续 `SELECT ... WHERE row_id = ... LIMIT ...` 回表。

`LIMIT` 必填；Canonical Skill 对 `OPEN` 固定使用 1。cursor 语法为兼容保留，但合法
Leaf 不会产生 next cursor。

## List page

`SHOW` 与 `OPEN` 复用 `memora.list-page/v1`，每次都返回 version、limit、输入 cursor、
snapshot、truncated 和可选 next cursor。`OPEN` 的 visible locator 集合基数是 `0..1`。

cursor 绑定读取类型、稳定 parent/leaf scope、snapshot 与下一 offset，并使用 canonical
encoding 和 checksum。损坏、非 canonical、跨 scope、越界 cursor 返回
`validation_error`；两页之间 Route 或 membership 变化返回 `revision_conflict`，不能
静默混合导航状态。

历史实例若存在一个 Leaf 多个活跃 Row，`OPEN` 返回 `constraint_violation`，不得把它
分页成候选桶继续查询；AI DBA 必须先完成语义 reshape。

尚未创建 Table Router root 是合法空状态：`SHOW ROUTES FROM TABLE ... AT ROOT` 返回
带确定性 snapshot 的空 list page，而不是 `not_found` 或 `internal_error`。不存在或已
删除的 point Route 返回稳定 `not_found`。

## 类型边界

- 对 root/branch 执行 `OPEN` 返回 `constraint_violation`；
- 对 leaf 执行 `SHOW ROUTES UNDER` 返回 `constraint_violation`，调用方应改用 `OPEN`；
- point node、children 与 locator 都先执行 database authorization；
- cursor 不是授权凭据，不允许扩大 Database/Table scope。

## 边界

- 不读取 Row detail/history，它属于 F112；
- 不读取 committed changes 或 trace，它们属于 F113/F114；
- 不做 predictor、vector 或 HNSW；它们只能在后续作为可回退候选来源。

## 关联

- [Agent 语义目录索引](./semantic-routing.md)
- [中间 Route Synopsis](./route-synopsis.md)
- [MSQL Metadata Read v1](./metadata-read-v1.md)
- [F111 开工与完成门](../archive/planning/f111-route-read-protocol-gate.md)
