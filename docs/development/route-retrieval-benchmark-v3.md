# Route Retrieval Benchmark v3

状态：F124 Corpus、F124a Frame 与 F124b Lexical 已冻结；F124c–F124e 完成后才运行 F125。

## 核心问题

1. 每层 fanout、树深和兄弟歧义怎样影响真实模型逐层选择？
2. 倒排、CPU Vector 和组合候选能否减少模型调用而不降低 RowID 成功率？
3. 投机预取节省的调用是否大于误预测 token、embedding 和 CPU 成本？
4. 不同 host/model 的共同安全 fanout 和候选预算是多少？

## 固定实验臂

| Arm | 发现方式 | 事实读取 |
| --- | --- | --- |
| A | Router-only，严格逐层 | RowID SQL 回表 |
| B | Catalog + Lexical locations，再读 Router | RowID SQL 回表 |
| C | Catalog + CPU exact Vector Route candidates，再读 Router | RowID SQL 回表 |
| D | Lexical + Vector 有界并集，再读 Router | RowID SQL 回表 |
| E | D + 少量候选 Table 根 Route 投机预取 | RowID SQL 回表 |

所有 arm 共享同一权威 Table Router。Vector 只覆盖 Route semantic surface，不覆盖
Row、正文或 chunk；候选结果不能直接进入最终答案。

## Corpus 冻结

F124 在任何 predictor 实现前冻结：

- Database/Table/Route snapshot、ground-truth path 和 RowID；
- fanout 4/8/12/16/24/32，depth 1/2/3/4/6；
- 明显分离、相关主题、边界重叠、同义改写和负例；
- 中文、英文、混合术语及 seeded candidate shuffle；
- 最大回退次数、Context 预算和停止条件。

不得根据 Vector 或 Lexical 的首次结果修改题目、aliases 或正确路径。

## 公平性

- 相同 host/model、Canonical Skill 基线、授权 scope 和最终 SQL；
- 相同 Route revision、embedding model/version/dimension 和生成文本 digest；
- Vector arm 只用 CPU 精确扫描，不使用 HNSW、ANN 或 GPU；
- 各 arm 分别记录冷启动与有效 Route Frame 的 warm follow-up；
- Provider Key 不进入 Memora，未知模型身份标记 `INCOMPLETE`。

## 指标

- `level_top1_accuracy`、`exact_path_success`、`rowid_success`；
- `predictor_recall@k`、`prefetch_hit_rate`、`fallback_calls`；
- 模型调用数、总输入/输出 token、`mispredict_tokens` 和费用；
- query embedding、CPU scan、MSQL、模型各段延迟及端到端 `p50/p95`；
- vector generation 大小、重建时间和峰值内存；
- 无关 locator、错误正文读取、truncation 和权限拒绝。

报告必须按 arm、fanout、depth、难度、语言和 host/model 分桶，公开原始计数、失败
样本、suite digest 和置信区间，不能只发布总平均分。

## 正确性门

- predictor miss 或 generation 缺失必须回退 Router；
- 任一 arm 的最终事实只来自 snapshot 一致的 SQL Row；
- 候选不能扩大授权 scope 或自动排除零命中 Table；
- 相比 Router-only，优化 arm 若降低冻结的 RowID 成功率则不能成为默认；
- Benchmark 只提供证据，不自动改 Router、Skill 或索引配置。

## 后续决定

F126 先选择默认 Discovery profile 和共同安全预算。只有 CPU exact scan 的真实 `p95`
或资源成本越过预先冻结门槛，才 Review Apple Accelerate；仍不满足时才 Review HNSW。

## 关联

- [Route Predictor Feature 计划](../planning/route-predictor-feature-plan.md)
- [AI-native 质量模型](../product/quality-model.md)
- [无向量 Route Benchmark v2](./no-vector-route-benchmark-v2.md)
