# B1 · 叶子间搬迁要像别的写一样落账

严重度：medium。状态：**已关**（2026-09-26 修复）。

## 结案

搬迁改走正常行写路径，计划自己的 provenance 一路传到 history 与变更日志。

- 调用点把 apply 路径已有的 `change.Metadata`（plan 的 actor/source/reason）交给
  `moveMembership`；新增 `writeMetadata`（`metadataFrom` 的反向）供历史与变更条目使用。
- **审计前提的一处更正**：`MembershipMove.Revision` 并非完全的死字段 —— planner
  在 `build.go` 里从叶子 locator 填它，并用它发现同一 Row 在两个来源叶上 revision 不一致。
  顾问判断：**保留并标注 informational**，不提升为执行期校验。挡住过期计划的仍是整份
  leaf locator 集合。
- 改前 `TestMergingLeavesAccountsForTheMovedRows` 报「revision stayed at 1」；改后 revision
  前进、history +1 且带计划 provenance、变更日志有该行 update 条目、召回单元挪到新叶、
  回执的 `membership_revisions=1` 有据可依。
- 证据：`eaa48223`（merge 见 git log）；文档 `route-mutation-execution-v1.md` 原写
  「不修改 Row 正文、History…」，已订正为「搬迁是对行的写」。

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
