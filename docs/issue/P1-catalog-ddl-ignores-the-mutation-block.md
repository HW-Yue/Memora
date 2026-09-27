# P1 · Catalog DDL 静默忽略 `mutation` 块

严重度：medium（宿主以为自己记了 actor/reason，其实没有）。状态：**待做**。

## 症状

`CREATE DATABASE` / `ALTER DATABASE …` 这类 Catalog DDL **不是** `mutationStatement`
（`internal/msql/executor/batch.go` 的清单里没有 `Create`/`Alter`），所以照同形的
`ALTER ROUTE … SET PURPOSE` 抄一个 `mutation` 块会被**静默忽略**，不是报错。

编写 Skill 的人会以为自己留了 actor / `reason` / `max_affected_rows` 回执，实际上没有。

## 证据

- `internal/msql/executor/batch.go` 的 `mutationKind` / `mutationStatement` 清单不含 Catalog DDL。
- `docs/decisions.md` 2026-09-25 条目末尾自己写着「**待定（下一条候选，未做）**」。
- 与 Skill 的 `write.md` 里"照抄 ALTER ROUTE 的 mutation 块"形成对照。

## 已定的修法

让不受支持的语句**显式拒绝** `mutation` 块，并配回归测试。

这条语句本身只是把既有行为暴露出来；修法影响所有 Catalog DDL，是与 ALTER DATABASE 描述字段
**无关的独立变更**，所以当时没有顺手做。
