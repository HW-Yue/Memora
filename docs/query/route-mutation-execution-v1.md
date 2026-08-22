# Route Mutation Execution v1

状态：F130 已完成。

> **目标形态已改。** 本文的执行把 membership 变更当作独立的一类操作来落库。
> [写入形态](../product/write-model.md)取代了这一层：**叶子直接挂 RowID**，
> 没有独立的 membership 关系，挂载变更成为叶子自身的改动。
> 原子性要求（拆分/合并一次事务提交）不变。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。
> membership 的职责拆解、新归宿与分阶段迁移见
> [叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## MSQL 与批准

```sql
APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes;
```

`:plan` 必须是未经修改的 `memora.route-mutation-plan/v1`。statement input 必须同时
携带 `memora.authorization/v2`、Database scope，以及 `memora.approval/v1`：action
固定为 `APPLY_ROUTE_MUTATION`，`subject_sha256` 等于 plan hash 去掉 `sha256:` 前缀后的
64 位十六进制值。无批准、批准不匹配、跨 Table 或 legacy backend 都拒绝执行。

## 提交前重验

引擎在一个 native authority write window 内重新读取并逐项比较：

- 每个 Node guard 的稳定 ID、Table scope 与 revision；
- 每个 guarded parent 的完整 direct child ID 集合；
- 每个 guarded leaf 的完整 `0..1` RowID/revision locator 集合；
- plan identity、plan hash、base snapshot hash、action shape 与影响计数。

任何差异返回稳定冲突且零写入。branch 子节点被重挂时，计划 guard 覆盖整棵移动
子树；执行器确定性重算所有受影响 path，不允许未 guard 的节点被连带修改。

## 原子发布与覆盖证明

一个 native transaction 同时追加新 Route、重挂/重算 path revision、删除 tombstone、
membership tombstone/attach revision 与一个 Committed Change envelope。执行器在提交前
证明所有 action 都被物化且删除节点不再拥有 live child；事务 staging 或 commit 失败
时不发布部分对象。

提交前还会按最终状态验证 `Leaf → active Row` 基数。原子 move 可以先 tombstone 旧
occupant 再写入新 occupant；最终仍有两个不同 Row 时返回 `constraint_violation` 且零写入。

成功返回 `memora.route-mutation-receipt/v1`，包含 plan ID/hash、operation、change
sequence、各类覆盖计数与 `verified=true`。Receipt 是本次提交结果，不允许 Agent 据此
跳过新的 revision/snapshot guard；旧计划冲突后必须重新读取并生成。

## 不做

- 不修改 Row 正文、History、Schema 或关系；
- 不由引擎猜语义分组、名称或 purpose；
- 不支持跨 Table/Database 移动；
- 不为 stale plan 自动 rebase，也不接受 ad hoc action 拼接。

## 关联

- [Route Mutation Plan v1](./route-mutation-plan-v1.md)
- [Committed Change Read v1](./change-read-v1.md)
- [Exact Object Write Lock v1](../storage/exact-object-write-lock-v1.md)
