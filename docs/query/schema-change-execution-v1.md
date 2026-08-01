# Schema Change Execution v1

状态：F132 已完成。

## MSQL 与 approval

```sql
APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes;
```

`:plan` 必须是 F131 原样返回、canonical hash 未变且 `status=review_required` 的
`memora.schema-change-plan/v1`。`memora.approval/v1` 必须同时满足：

- `action=APPLY_SCHEMA_CHANGE`；
- `subject_sha256` 等于 plan hash 去掉 `sha256:` 后的 64 位十六进制；
- `confirmed=true`；
- Authorization 仍覆盖 MSQL 绑定的 Database name 或稳定 ID。

`blocked`、未知字段、非 canonical 顺序、action/impact/guard/hash 篡改、越权和 legacy
backend 都在写入前拒绝。

## 执行时重验

native Catalog authority 在一个写窗口内重读持久化 snapshot，逐项比较：

- Database/Table ID 与 revision；
- 完整 Column ID/name/alias/type/nullability/purpose/role/revision surface；
- 对 F131 做过 Row 扫描的计划，重新枚举最多 1000 条完整 live Row，并要求 ID/revision
  集合与 `row_guards` 完全相等。

因此 planning 后的 Row 新增、删除、更新，以及任何 Catalog 变化都会使计划 stale。
ADD/RENAME/ALTER/DROP 在内存中一次物化，Table 与 Database 各只升一个 revision，再以
一个 Catalog generation 和一个 committed Change envelope 发布。DROP 的 Change entry
使用 DELETE；历史 Row body 中的旧 Column ID/value 不做物理擦除。

## Receipt 与补偿

成功返回 `memora.schema-change-receipt/v1`，包含 plan ID/hash、Change sequence、目标
Database/Table revision、action 数与 post-commit verification。只有
`status=committed && verified=true` 是完整成功。

无论 validation、approval、revalidation 或 publish 在提交前失败，都不产生部分 Schema
或 Change Log。可逆计划的 receipt 可附带 `compensation_proposal`；它只是反向
`memora.schema-change-proposal/v1`，必须重新 PLAN、重新检查 live Row、重新审批后才能
APPLY。包含 DROP 的计划不返回自动补偿，避免把逻辑 History 保留误称为无损恢复。

## 关联

- [Schema Change Plan v1](./schema-change-plan-v1.md)
- [Committed Change Envelope v1](../storage/committed-change-envelope-v1.md)
- [Skill Schema 生命周期 v1](../agent/skill-schema-lifecycle-v1.md)
