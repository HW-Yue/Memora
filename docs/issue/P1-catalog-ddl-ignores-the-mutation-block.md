# P1 · Catalog DDL 静默忽略 `mutation` 块

严重度：medium（宿主以为自己记了 actor/reason，其实没有）。状态：**已关**（2026-09-26 修复）。

## 结案

- 规则落在**一处**：`refusesMutationBlock(statement)` = `!mutationStatement(statement)`，在
  `Engine.Execute` 顶部执行——Catalog DDL 与 `SELECT` 都在那里早返回，所以那里是唯一必经点；
  `mutationBlockEmpty` 用与零值比较，将来给 `MutationOptions` 加字段也覆盖得到。
- batch 自己处理的 `BEGIN`/`COMMIT`/`ROLLBACK` 到不了 `Execute`，在循环里同样拒绝。
- 报 `validation_error` 并把话说全：「这块会被丢掉，删掉它」。不静默丢，也不假装支持。
- **范围说明**：取「所有非 mutation 语句」而不是只管 Catalog DDL——同一句谎对读也成立，
  而且已核实自家路径干净（Skill 的读示例不带块、CLI 排干读前显式清空、全仓测试无依赖）。
- **更大的替代仍待定**：让 Catalog DDL 真记 provenance（纳入 mutation 分类／审计面）是独立变更，
  decisions.md 2026-09-25 已判为不在本块，本次仍不做。
- 证据：`18866cf3`（merge `a97cdace`）；Skill 句从 "ignored rather than honoured" 改成 refused，
  六处同步并已发布（发布仓库 `7aec1b7`）。
- **顾问不可用**：本轮 Codex 子 agent 两次 `product-error`，上述范围判断由我自己下。

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
