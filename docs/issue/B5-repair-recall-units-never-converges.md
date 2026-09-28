# B5 · `REPAIR RECALL UNITS` 永不收敛

严重度：medium。状态：**已关**（2026-09-26 修复，真实 CI 见下）。

## 结案

取审计给的第一个选项（**明确 blocked**），不取「算进 remaining」：这类行重复多少次都不会好，
把它们混进 `remaining` 只会让调用方一直重试。

- `RecallReceipt` 加 `Blocked`；`repairRecallUnits` 对「不持有恰好一个叶子」的活行计 `Blocked`
  并 **跳过** `syncRecallUnit` —— 原来那一步会删掉它仅有的单元，等于让关键词召回丢一个活行。
- `REPAIR RECALL UNITS` 输出多一列 `blocked`，并在 `blocked > 0` 时给一条
  `recall_units_blocked` 通知（新登记的稳定码），说明要修的是挂载。
- RED→GREEN：原实现上报 `{Rebuilt:1 Dropped:0 Remaining:0}`；改后 `blocked=1`、
  第二轮与第一轮完全一致、单元保留、`RECALL` 仍命中、语句层带通知。
- 证据：`a644f672`（merge `1fceff90`）；文档 `docs/implementation/query/msql.md` 与 Skill
  `references/recall-and-vectors.md` 同步，六处一致。

## 症状

对"live 行但叶子数 ≠ 1"这类行，`REPAIR RECALL UNITS` 每轮都报 `rebuilt=1 / remaining=0`，
`doctor.broken_recall_units` 不降 —— 第一轮和第二轮没有区别，看起来修好了其实没动。

## 证据

- `internal/sqlstore/recall.go:311-314` 无条件 `unitNeedsRebuild = true`；
  `syncRecallUnit`（`recall.go:81-83`）对这类行只删不建；
  应用循环 `recall.go:287-296` 仍 `rebuilt++` 且 `remaining` 不涨。
- 与 `recall_repair_test.go:72-79`"收敛后的一轮必须什么都不做"的断言直接冲突。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) B5。

## 已定的修法

这类行**不是"已重建"**，而是**被挡住**：

- 要么计入一个新的 `blocked`（回执里能看出是哪条行、为什么），
- 要么算成 `remaining` 递减的"无法修复"并如实报。

关键是**第二轮必须与第一轮不同**（收敛或明确 blocked），不能每轮都 `rebuilt=1 / remaining=0`。
