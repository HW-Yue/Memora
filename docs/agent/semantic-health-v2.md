# Semantic Health v2

状态：F128 已完成。

> **目标形态已改，本文的四类 membership 问题项里有三类将结构性消失。**
> [写入形态](../product/write-model.md)让**叶子直接挂 RowID**，不再有独立的
> membership 关系，于是：
>
> - `stale_membership`（locator revision 与当前 Row 不同）——叶子上不存 revision，
>   无从过期；
> - `invalid_membership_scope`（locator 的 Database/Table 与 leaf 不一致）——
>   叶子自己就带这两个字段，不存在第二份可以对不上；
> - `multi_row_leaf`（一个叶子挂了多行）——一个字段放不下两个 RowID。
>
> 三者从"要扫、要报告、要人工修"变成**不可能发生**。
> `unrouted_row`（live Row 没有 live 挂载）与 `orphan_membership`（指向不存在的 Row）
> 保留，改由反向索引树解析。
> **这是对外可见的能力减少**，实施时须在发布说明中点名。
>
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。职责拆解与分阶段迁移见
> [叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## 只读快照

`memora maintain --report` 返回 `memora.semantic-health/v2`。扫描读取当前 Catalog、每表
最多 1,000 个 live Row、全部 live Route node，并检查全局最多 10,000 个 leaf locator。
输出只含 ID、对象名、计数、风险与动作，不含 Row values、历史正文或模型判断。

## 确定性 issue

- `route_capacity`：root/branch 的 live child ≥12；
- `multi_row_leaf`：历史或损坏状态让一个 Leaf 出现多个活跃 Row locator；报告携带
  Leaf ID 与受影响 RowID，供 AI DBA 做单调减少的语义重塑；
- `ambiguous_siblings`：同 parent 的规范化 name/alias 相交；
- `invalid_route_structure`：Table root 数不为 1、parent 缺失/跨 Table、kind/层级不合法；
- `unrouted_row`：完成扫描的 Table 中 live Row 没有 live membership；
- `orphan_membership`：完成 Row 扫描后 locator 指向不存在的 Row；
- `stale_membership`：locator revision 与当前 Row revision 不同；
- `invalid_membership_scope`：locator Database/Table 与 leaf 不一致；
- v1 保留 `duplicate_row`、`synonymous_columns`、`stale_description`。

同义 sibling 只是“需要复核的歧义”，不是自动 merge 结论。所有 issue 都是
`review_required`、`auto_fix=false`；F128 没有语义自动修复。

F129 起，AI 可在读取受影响 Route/locator 后，通过只读 MSQL 生成绑定当前 guard/hash
的局部 split/merge/move 计划；F169 后 Leaf 不再以容量 split。健康报告本身仍不能成为
分组答案或执行授权。

## 截断与误报边界

Row page 截断时，不对该 Table 生成 `orphan_membership` 或 `unrouted_row`；locator 总预算
耗尽后，不生成依赖完整 membership 集合的 `unrouted_row`。已扫描对象内部可以继续报告
scope/revision 等正证据问题。报告 `truncated=true`，不得把零 issue 解读为健康证明。

## 关联

- [语义数据库健康维护 v1（历史）](../archive/design/semantic-health-v1.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [Route Mutation Plan v1](../query/route-mutation-plan-v1.md)
