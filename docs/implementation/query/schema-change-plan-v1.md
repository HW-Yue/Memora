# Schema Change Plan v1

状态：F131 已完成。

## MSQL 入口

```sql
PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal;
```

`:proposal` 是严格的 `memora.schema-change-proposal/v1`，包含 proposal ID、actor、
source event、reason、expected Table revision，以及 1–16 个显式变化。MSQL 限定名
绑定 Database/Table scope；proposal 不能自行扩大权限。

## 变化词汇

- `ADD_COLUMN`：携带完整 name/type/nullability/purpose/semantic role definition；
- `RENAME_COLUMN`：携带稳定 Column ID、expected revision 与新名称；
- `ALTER_COLUMN`：携带稳定 ID/revision 与保留当前名称的完整目标 definition；
- `DROP_COLUMN`：携带稳定 ID/revision，计划标为 destructive/non-reversible。

一个 proposal 对同一现有 Column 最多一个 action。当前 Column snapshot 最多 256 项，
最终 Schema 必须至少保留一个 Column，所有 current name/alias 不冲突，
`title`/`summary` role 各最多一个。新增
Column ID 由 Table ID、proposal ID 与 change ID 确定性派生。

## Snapshot 与兼容性

计划绑定 Database/Table revision、完整 Column surface 与 base Schema hash。纯 rename、
可空新增及只改 purpose/role 不扫描 Row。以下变化读取最多 1000 条完整 live Row 快照：

- 新增 NOT NULL Column；
- type、TEXT 长度或 nullability 变化；
- DROP Column 的 populated-value impact。

每条扫描 Row 的 ID/revision 进入 guard 与 Row snapshot hash。扫描截断、重复/无效 Row
或读取失败时拒绝生成计划。值校验复用正式 logical type validator，不保存或返回值本身。

## 状态与边界

兼容时返回 `memora.schema-change-plan/v1`、`status=review_required`、影响计数与 plan
hash。不兼容时仍返回确定计划，但状态为 `blocked`，仅列出最多 32 个 RowID-only blocker
并保留不兼容总数。blocked 不能执行，也不自动生成 conversion/backfill 值。

F131 的 PLAN 始终只读：不修改 Catalog、Row、History 或 Change Log。F132 通过独立的
hash-bound APPLY 入口负责 approval、重验、原子执行与收据；Agent 不得把 plan actions
翻译为普通 DDL。详见 [Schema Change Execution v1](./schema-change-execution-v1.md)。

## 与旧协议的关系

`memora.schema-plan/v1` 是 F32 的宿主 Ensure/rename runner，保留兼容；新 Column/constraint
演化以本协议为正式 engine-level 计划边界，不共享版本或 hash。

## 关联

- [Catalog DDL v1](./catalog-ddl.md)
- [逻辑类型与字段预算 v1](../data/logical-types.md)
- [Skill Schema 生命周期 v1](../agent/skill-schema-lifecycle-v1.md)
