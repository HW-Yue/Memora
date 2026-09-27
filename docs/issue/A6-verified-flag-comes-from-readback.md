# A6 · 回执的 `verified` 必须来自提交后的读回

严重度：high。状态：**待做**。

## 症状

两个 L2 变更回执**无条件**报 `verified: true`，而 `schemachangeplan.VerificationCode` 全仓没有任何赋值 ——
即"已验证"是一个常量，不是一次验证。

## 证据

- `internal/sqlstore/route_plan.go:36-39`、`internal/sqlstore/schema_plan.go:25-28`：都在 `db.update`
  **之前**写死 `Verified: true`。
- `internal/schemachangeplan/model.go:131`：`VerificationCode` 从不赋值。
- CLI 退出码跟着 `receipt.Verified` 走（`internal/cli/cli.go:359-361`），所以现在退出码也没有信息量。
- 来源：[audit-2026-09-23.md](./audit-2026-09-23.md) A6。

## 已定的修法（别重新推导）

`db.update` **提交之后**，用一次只读 view 读回并比对**计划自己声明的目标**（不要重写 applier 的逻辑，
那正是 B4 的教训）：

- SCHEMA：逐条 `plan.Actions` 校验后置条件 —— `ADD/RENAME/ALTER` 的 `After.ColumnID` 必须是活列且形状等于
  `After`；`DROP` 的 `Before.ColumnID` 必须不再是活列。
- ROUTE：`plan.Creates` 的节点必须存在且 name/kind/purpose/parent 与计划一致；`plan.Deletes` 的节点必须不可见。
- `VerificationCode` 填**读回形状的摘要**（必须非常量；或删掉该字段，二选一并在 `docs/decisions.md` 写理由）。
- 不一致时：`Verified=false` 并带可诊断信息（回执与错误一起给，"只置 false 不吭声"不可接受）。
- executor 要把新字段透传，CLI 退出码继续跟 `receipt.Verified`。

## 验收

- RED：端到端断言 `VerificationCode != ""`（改之前恒空）。
- 对"读回校验函数"直接喂一个不一致的状态做单测（提交后无法插进"提交与校验之间"的篡改，除非加测试缝——
  加缝要在报告里说明）。

## 备注

做过两次尝试：派 Claude Code 子 agent 均 `invalid-result` 崩掉；随后自己开工判断需要更多上下文才安全。
