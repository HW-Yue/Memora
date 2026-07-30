# Index Discovery v1

状态：历史实现，产品方向已撤销。full path、query_terms、MATCH 与关系融合不再
是查询主路径；目标见[语义树检索质量链路](./retrieval-quality.md)。

## 输入与边界

Query Agent 提交 `database_id`、零到多个绝对 `route_paths`、原始 `query`、完整 `query_terms` 和 1–1000 的 `limit`。请求至少包含 Route path 或 query。

Planner 只返回：

```text
database_id + table_id + row_id + revision
+ score + route/search/relation signal
```

任何索引摘要和 Row 正文都不进入结果。主 Agent 必须再用参数化 SELECT 回表。

## 执行

一次一致 snapshot 中依次完成：

1. 从 Database Router `/` 冷启动，逐段按 current name/alias 验证候选 path；
2. 到达 leaf 后取得 locator；
3. 无论 Route 是否命中或耗尽预算，都执行 Database 级 Agent/机械 MATCH；
4. 从融合前的高分候选做一跳、双向关系扩展；
5. 按 stable Table/Row ID 打破同分并应用 LIMIT。

Route 选错不能屏蔽全局倒排；关系只能补充当前有效 Row locator，不能带回关系描述或正文。

## 启动权重与预算

确定性总分为：

```text
0.6 × route_score + 1.0 × search_score + 0.3 × relation_score
```

三个 signal 均为 0–1；同一 Row 的多路信号相加。启动预算为 32 个 Route node、8 个关系 seed、每 seed 12 个邻居。Router leaf 自身最多读取 100 个 locator。

Route 或关系预算耗尽、底层 MATCH/leaf 截断、或最终 LIMIT 截断时返回 `truncated=true`；预算导致时同时返回 `budget_exhausted=true`。Planner 不通过无限加深或提高 limit 隐藏耗尽。

## 错误与一致性

- 非绝对 path、空请求和非法 limit 返回 validation error；
- 三路对同一 Row 返回不同 revision 视为索引损坏；
- 跨 Database、缺失 Table/Row ID 或 revision 的 locator 视为内部错误；
- Row transaction source 在同一 Store snapshot 读取 Router、两路倒排和关系；
- 已删除 Row 不能经关系扩展重新出现。

## 关联

- [Router Tree v1](./router-tree-v1.md)
- [MATCH Fusion v1](./match-fusion-v1.md)
- [MSQL Relationships v1](./msql-relationships.md)
