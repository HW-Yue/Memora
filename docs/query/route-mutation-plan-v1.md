# Route Mutation Plan v1

状态：F129 计划生成与 F130 审批执行均已完成。

## MSQL 入口

```sql
PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal;
```

`:proposal` 是严格的 `memora.route-mutation-proposal/v1` 对象，包含 proposal ID、
`SPLIT|MERGE|MOVE`、actor、source event、reason、source Route ID、目标定义及显式
child/Row 分组。MSQL 绑定 Table scope；proposal 不得自行扩大 Database/Table 权限。

## AI 与引擎边界

AI 负责命名目标节点、说明 purpose/synopsis，并明确每个 child Route 或 locator Row
应去哪个目标。引擎不根据文本、向量或相似度猜分组，只验证：

- source、parent、child 与 locator 都属于已绑定 Table 且仍为当前 revision；
- SPLIT 完整且不重不漏地覆盖 source 的 direct children 或 locator Rows；
- MERGE 只合并同 parent、同 kind 的至少两个非 root sibling；
- MOVE 不把节点移入自身子树，目标 parent 不是 leaf；
- 结果没有 sibling name 冲突，fan-out ≤12、leaf locator ≤100；
- 扫描有 cursor、数量和总预算上限；任何截断都拒绝生成计划。

## 计划

成功返回 `memora.route-mutation-plan/v1`，包含绑定 scope、provenance、base snapshot
hash、Node/revision guards、create/reparent/membership/delete actions、影响计数及 plan hash。
新 Route ID 由 Table、proposal ID 与 target key 确定性派生；同一快照和 proposal 的
字节输出一致。

计划状态固定为 `review_required`。生成计划不修改 Route、membership、Row、History
或 Change Log。执行协议见 [Route Mutation Execution v1](./route-mutation-execution-v1.md)。

## 操作形状

- `SPLIT`：一个 branch/leaf source、至少两个新 target；branch 使用 `child_route_ids`，
  leaf 使用 `row_ids`，两者不能混用；source 最后删除。
- `MERGE`：至少两个同 kind sibling source、一个新 target；全部 direct contents 自动
  汇入 target，source 最后删除。
- `MOVE`：一个非 root source 和一个不同的 target parent；保留 source ID 和子树。

## 关联

- [Semantic Health v2](../agent/semantic-health-v2.md)
- [Route Read v1](./route-read-v1.md)
- [Router Tree v1（历史）](../archive/design/router-tree-v1.md)
