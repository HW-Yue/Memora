# P4 · Table 的 `purpose` / `row_semantics` 焊死在建表那一刻

严重度：medium-high（同「库描述改不动」是同一类缺口）。状态：**待做**（2026-09-26 由用户新边界带出）。

## 症状

用户 2026-09-26 定下边界：**已有库里的任何东西都要允许改**。按这条核对，数据库描述那一块
已经补上（`ALTER DATABASE … SET PURPOSE/SCOPE/ANTI SCOPE`），列的 `purpose` 也能通过
`ALTER_COLUMN` 改；但 **Table 的 `purpose` 与 `row_semantics` 没有 amend 路径** ——
两个建表时**必填**的字段，写下去就只能靠重建表来改。

## 证据

- 解析器：`internal/msql/parser/parser.go:835` 的 `SET` 分支条件是
  `alter.Object == "DATABASE" && parser.matchWord("SET")`；`ALTER TABLE` 这条只认
  `ADD COLUMN`（`:808`）、`RENAME COLUMN`（`:817`）、`RENAME`（`:833`），落到别的动作报
  `ADD COLUMN or RENAME`（`:855`）。
- 结构变更计划只到列一级：`internal/schemachangeplan/` 的动作词汇是
  `ADD_COLUMN` / `RENAME_COLUMN` / `ALTER_COLUMN` / `DROP_COLUMN`
  （[schema-change-plan-v1](../implementation/query/schema-change-plan-v1.md) §变化词汇），
  `Plan` / `PlannedAction` 里没有表级定义项。
- 两个字段是建表必填：`internal/skillschema/policy.go:90`（缺 `Purpose` 或 `RowSemantics` 即拒）。
- 全仓唯一写 `Table.Purpose` 的地方是**拼 `CREATE TABLE` 语句**：`internal/skillschema/runner.go:295`
  （`" PURPOSE " + literal(table.Purpose) + " ROW SEMANTICS " + literal(table.RowSemantics)`）。
- 来源：用户 2026-09-26 的边界 + `docs/planning/engine-owned-shape.md`「待定」。

## 候选修法（**先定设计，别直接写**）

1. 补 `ALTER TABLE <name> SET PURPOSE '…' ROW SEMANTICS '…'`，与库级 `SET` 同形：部分写、
   字面量、L2、`mutation` 块仍不支持（与 Catalog DDL 一致，见 [P1](./P1-catalog-ddl-ignores-the-mutation-block.md)）。
2. 或给结构变更计划加一个表级动作，让它进同一个「计划—校验—应用—读回」通道（更重，但和
   列形状共用一套证据）。

推荐 1，理由是**与库级 amend 对称**：同一个「给写入权就要给修订权」的判断已经在库描述那里做过一次。

## 设计依赖（先解决，再动手）

`docs/planning/engine-owned-shape.md` 的「待定」里还有一条没定：**`row_semantics` 是撤掉、
引擎写常数、还是原样留给 agent**。如果它被撤掉，这条只剩 `purpose` 需要 amend 路径。别在
那条待定没定之前实现 `ROW SEMANTICS` 的 amend。
