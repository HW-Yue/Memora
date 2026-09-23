# MSQL Route Read v1

状态：F111 已实现；F182a 增加有界 Route aliases 读取列；SHOW 增加 database_id/table_id scope 字段；
2026-09-23 `SHOW ROUTES` 的叶子增加 `row_id` / `row_revision` 两个可空列。

## 目的

Admin 与 Agent 使用同一条 MSQL 链路逐层读取 Table Router：point node、children page、
single locator leaf。Route 只负责导航，任何读取都不得夹带 Row 正文、答案、embedding
或物理 Store 信息。

## 分层语法

```sql
DESCRIBE ROUTE :route_id;
SHOW ROUTES FROM TABLE work.notes AT ROOT;
SHOW ROUTES UNDER :parent_id;
OPEN ROUTE :leaf_id LIMIT :limit;
```

`SHOW ROUTES` **没有** `LIMIT` 也没有 `CURSOR`：它返回该节点的**整层**孩子。层的体量由
`route_policy.branch_fanout`（默认 12，上限 100）唯一决定，所以读侧不需要页，也没有"还有下一页"
这种状态——`LIMIT`/`CURSOR` 与 `query_budgets.route_children` 已于 2026-09-22 一起撤掉，写了会被
语法拒绝并说明原因。`OPEN ROUTE` 保留 `LIMIT`（locator 集合基数 `0..1`）与 cursor 语法。

- `DESCRIBE` 是一个有界 point read，返回 node 元数据、非 null `aliases`、`database_id/table_id` scope 与
  按需 synopsis，不返回 children；
- `SHOW` 返回一层 child node、非 null `aliases` 与 `database_id/table_id` scope，默认不返回
  synopsis；scope 字段让并发多库导航时每条 Route Frame 都能归并到唯一库表，不再依赖
  `source` 回显；alias 固定最多 8 项且合计最多 512 UTF-8 bytes，不破坏逐层上下文上限；
  **叶子还带 `row_id` 与 `row_revision`**（见下节）；
- `OPEN` 只接受 leaf，只返回零个或一个 `database_id/table_id/row_id/revision` locator；
- 业务字段和正文只能由后续 `SELECT ... WHERE row_id = ... LIMIT ...` 回表。

`OPEN` 的 `LIMIT` 必填，Canonical Skill 固定使用 1；cursor 语法为兼容保留，但合法
Leaf 不会产生 next cursor。

## 叶子在列表里就带出它挂的行

`SHOW ROUTES` 的每行增加两个**可空**列：

| 列 | 类型 | 语义 |
|---|---|---|
| `row_id` | ID，可空 | 这个叶子挂的那个活跃 Row 的稳定行号。root / branch 恒为 `null`；叶子没挂活跃行时也是 `null`（不是空串——空串会被当成一个行号） |
| `row_revision` | INTEGER，可空 | **那个 Row 的 revision**，即事实的版本 |

**`row_revision` 与同一行上的 `revision` 不是一回事，别混：**

- `revision` 是**路由节点自己的**版本。节点被改名、改 `purpose`、重新挂载时它会动；
  叶子底下的事实被编辑时它**不动**。它是 `ALTER ROUTE` / `ROUTE MUTATION` 的乐观并发凭据。
- `row_revision` 是**那个 Row 的**版本。事实被 `UPDATE` 时它会动；节点被改名时它**不动**。
  它是 `UPDATE … WHERE row_id = :row` 的乐观并发凭据。

把一个当另一个用，结果是在一个谁都没碰过的对象上拿到 `revision_conflict`。

这两列能存在，是因为**写路径保证"一个活跃行只挂在一个叶子上"**
（`internal/sqlstore/invariant.go`，doctor 用 `orphan_rows` / `multi_leaf_rows` /
`mismatched_mounts` 计数）：叶子 → 行是一对一的，所以列表说得出来。它不是一个新的事实来源——
解析用的就是 `OPEN ROUTE` 那段逻辑（只认 `row_state = 'live'`），所以列表与 `OPEN ROUTE`
不可能给出不同的答案；叶子指着一个已经不活跃的行（doctor 的 `mismatched_mounts`）时，两边
一致地报"没有行"，而不是让便宜的那条路去报一个读不回来的行号。

代价上这是**每层一条语句**，不是每叶子一条：一层的体量由 `route_policy.branch_fanout` 担保，
且同层节点属于同一张表，所以是一次按 id 集合的查表，不是扫表。

`OPEN ROUTE` **保留**：它仍然是"只开一个叶子"的读法，别的读取方在用，也是上面那条一致性的
对照面。只是**逐层走树不必再为每个叶子发一条**——实测最宽那一趟 50 条语句里有 35 条是它。

## List page

`OPEN` 使用 `memora.list-page/v1`，返回 version、limit、输入 cursor、snapshot、truncated
和可选 next cursor；其 visible locator 集合基数是 `0..1`。`SHOW ROUTES` 不再返回 page：
整层就是答案，没有 cursor、没有 `next_cursor`，`truncated` 因此恒为 false（而不是一个可以被
追问"是不是还有"的把手）。

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
- [中间 Route Synopsis](../archive/query/route-synopsis.md)
- [MSQL Metadata Read v1](../archive/query/metadata-read-v1.md)
- [F111 开工与完成门](../archive/planning/f111-route-read-protocol-gate.md)
