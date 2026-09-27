# B1 · 叶子间搬迁要像别的写一样落账

严重度：medium。状态：**待做**。

## 症状

`APPLY ROUTE MUTATION` 执行叶子间搬迁时绕过全部记账：不推进 revision / `updated_at` /
`commit_sequence`，不写 history，也不写行的 `mem_changes` —— **挂载变更在审计面上是隐形的**。
计划里的 `MembershipMove.Revision` 是一个从无校验的死字段。

## 证据

- `internal/sqlstore/route_plan.go:211-249` 的 `moveMembership` 是裸 `UPDATE ... SET route_leaf_ids`，
  对照正常写路径（`rows.go:284` 起）缺 `advance` → `writeRow` → `appendHistory` → `rowChange`。
- 执行期有叶子反向校验兜底，所以不是"旧计划能通过"，而是"变更不留痕"。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B1。

## 已定的修法

改走正常行写的路径（`advance` → `writeRow` → `appendHistory` → `rowChange`），让
`receipt.MembershipRevisions` 真的有对应。

**`MembershipMove.Revision` 是死字段**：删掉它，或标注为 informational —— **不要**给它补一个没人写的校验。

## 验收

- RED：搬迁一次后断言 revision 递增、`SHOW HISTORY` 多一条、变更日志里有该行的条目。
