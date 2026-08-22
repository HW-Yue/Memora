# F224：Row 必须可导航（写入时强制语义索引）

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
沿用 [F223](./f223-route-branch-fanout-limit.md) 的处理形状：越界不是警告，是写入失败。

> **目标形态已改，本候选需按新形态重写后再评估。** 本文建立在
> membership 之上（「Row 有没有 live membership」是它的判据）。
> [写入形态](../product/write-model.md)去掉了独立的 membership 关系——
> 叶子直接挂 RowID，判据变成"有没有叶子指向这个 Row"，靠反向索引树回答。
> **问题本身依然成立**（零 Route 归属的孤儿 Row 要在写入时挡住），
> 变的是判据的取法。迁移设计见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## 问题

一条 Row 可以在**没有任何 Route 归属**的情况下写入成功，无报错、无警告。
这样的 Row 占着存储、出现在 Change Log 和历史里，但语义导航永远到不了它。

产品契约是「AI 逐层导航到唯一 RowID，再用 SQL 回表读取事实」，而
Catalog、字面位置和 Route-only Vector「只能预测导航候选，不能成为事实来源」。
因此**无 Route 的 Row 等于静默的数据丢失**：写进去了，语义上不存在。

## 四层都不拦

| 层 | 位置 | 现状 |
| --- | --- | --- |
| Wire | `protocol/msql/protocol.go:83` | `route_leaf_ids` 为 `omitempty`，完全可选 |
| skillwrite policy | `internal/skillwrite/policy.go:124` | 只拒绝 `nil`；`validateSnapshot`（`:160`）只校验**上界**，空数组 `[]string{}` 通过 |
| `msql.execute` 直连 | `internal/daemon/execute.go:409` | **没有任何 policy**——而这正是 Canonical Skill 唯一的入口 |
| 引擎 | `internal/nativerow/service.go:165` | 空切片 → 空 memberships → 循环不执行 → 直接提交 |

`internal/msql/executor/mutation.go:512` 的 `validateMutationOptions` 校验
schema version、`max_affected_rows` 和 expected revision，**从不看 `RouteLeafIDs`**。

`internal/semantichealth/model.go:29` 已有 `KindUnroutedRow`，但那是事后扫描。
F223 自己批评过这个模式：「`semantichealth` 在 12 个 child 时报 `route_capacity`，
但只是事后报告」。孤儿 Row 仍停在这一档。

## 唯一主要结果

任何提交后处于 live 状态的 Row，必须至少有一个活跃 Route Leaf 归属。
会产生零归属 live Row 的写入一律失败，返回结构化信封与可执行出路。

## 不变量的正确形式

约束的是**提交后的状态**，不是请求字段，否则会误伤合法用法：

- **INSERT**：结果 Row 必须 ≥ 1 个 membership。缺失或空数组均失败；
- **UPDATE**：`RouteLeafIDs == nil` 现在表示「保留既有归属」
  （`internal/row/history.go:212`、`internal/nativerow/service.go:633`），
  这个语义**必须保留**。只有当本次更新会把归属清空时才失败；
- **DELETE / tombstone**：已删除的 Row 没有 live 归属，**豁免**；
- **SPLIT / MERGE**：每个 live 目标各自满足上述不变量，沿用既有
  `TargetRouteLeafIDs` 校验。

## 执行点

在**引擎层**统一执行，与 F223 一致（「三条写入路径统一执行」）。
`internal/nativerow` 是所有路径的唯一汇合点；只在 skillwrite 加校验挡不住
`msql.execute` 直连。

skillwrite 的 `validateSnapshot` 同时修掉空数组漏洞，作为提前失败的第二道，
但**不作为唯一保障**。

## 失败信封

沿用 F223 `BranchOverflowError` 的形状：明确、可执行、由 Agent 选。

```text
Row would be committed without a semantic index: <table> requires at least one
Route leaf membership; either attach an existing leaf or create one first
  {"kind":"attach_existing_leaf","statement":"<INSERT ... with route_leaf_ids>"}
  {"kind":"create_leaf_then_write","statement":"CREATE ROUTE UNDER :parent ..."}
```

稳定错误码使用 `result.CodeConstraint`。

## 存量数据

已存在的无 Route Row **不被追溯清理**，也不阻塞 reopen——写入门只约束新写入。
它们继续由 `semantichealth` 的 `unrouted_row` 报告，由 Agent 决定挂载还是删除。
本 Feature 不实现自动挂载。

## 明确不做

- 不引擎自动挑选 Leaf：挂到哪里是语义判断，引擎不替 Agent 决定；
- 不追溯迁移存量无 Route Row；
- 不放宽 F223 的 fan-out 上限来给新 Leaf 让路——两个门相互独立，
  同时越界就同时报，Agent 自己权衡先重构还是先加宽；
- 不改 `route_leaf_ids` 的 wire 形状（保持 `omitempty`，由引擎判定结果状态）。

## RED 与完成门

- RED 先证明：不带 `route_leaf_ids` 的 INSERT 当前提交成功且 Row 零归属；
  空数组 `[]string{}` 同样通过 skillwrite 校验；
- INSERT 缺失、空数组、全部指向已删除 Leaf 三种情形均失败，且不留下部分写入；
- UPDATE 不带 `route_leaf_ids` 仍保留既有归属并成功（防止回归）；
- UPDATE 显式传空数组导致归属清空时失败；
- DELETE 与 tombstone 不受影响；
- SPLIT/MERGE 每个 live 目标独立满足不变量；
- `msql.execute` 直连路径与 skillwrite plan 路径**都**被拦（证明执行点在引擎）；
- 失败信封含两条出路与稳定错误码；事务回滚后无残留 membership 与 posting；
- 存量无 Route Row 的数据库仍可 reopen、可读、可被 `semantichealth` 报告；
- 目标测试、`-race`、Agent import allowlist 与完整 CI 全绿。

## 关联

- [执行计划](./execution-plan.md)
- [F223 Route Branch Fan-out 硬上限](./f223-route-branch-fanout-limit.md)
- [语义 Router](../query/semantic-routing.md)
- [AI-native 产品契约](../product/ai-native-contract.md)
