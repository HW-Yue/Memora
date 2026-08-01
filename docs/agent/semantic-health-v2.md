# Semantic Health v2

状态：F128 已完成。

## 只读快照

`memora maintain --report` 返回 `memora.semantic-health/v2`。扫描读取当前 Catalog、每表
最多 1,000 个 live Row、全部 live Route node，并以 cursor 读取全局最多 10,000 个 leaf
locator。输出只含 ID、对象名、计数、风险与动作，不含 Row values、历史正文或模型判断。

## 确定性 issue

- `route_capacity`：root/branch 的 live child ≥12，或 leaf locator ≥100；
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
的局部 split/merge/move 计划；健康报告本身仍不能成为分组答案或执行授权。

## 截断与误报边界

Row page 截断时，不对该 Table 生成 `orphan_membership` 或 `unrouted_row`；locator 总预算
耗尽后，不生成依赖完整 membership 集合的 `unrouted_row`。已扫描对象内部可以继续报告
scope/revision 等正证据问题。报告 `truncated=true`，不得把零 issue 解读为健康证明。

## 关联

- [语义数据库健康维护 v1](./semantic-health-v1.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [Route Mutation Plan v1](../query/route-mutation-plan-v1.md)
