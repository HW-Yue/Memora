# 历史设计归档

这里保留早期调研、完整推演、被取代设计以及已经结束的 Feature 计划和完成证据。

- [早期竞品调研与概念设计](./AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)
- [早期 AI 自主治理与 MVCC 设计](./AI_NATIVE_AUTONOMY_AND_MVCC_2026-07-29.md)
- [早期 MSQL、语义路由与上下文协议](./MSQL_SEMANTIC_ROUTING_AND_CONTEXT_2026-07-29.md)
- [早期 Wiki 导出设计](./WIKI_EXPORT_DESIGN_2026-07-29.md)
- [F00–F163 历史计划与完成证据](./planning/README.md)
- [被取代的设计与实现规格](./design/README.md)
- [被取代的存储规格](./storage/) — 含
  [聚簇行存储 v1](./storage/clustered-row-storage-v1.md)：2026-08-22 前的存储设计终点，
  已被[写入形态](../product/write-model.md)取代（history 从"顺物理指针链回溯"
  改为"独立成表、按 `(row_id, 序号)` 范围扫"）；
- [F42 AI-native v1 冻结题库](./benchmarks/ai-native-v1.json)

归档用途：

- 追溯一个结论是怎样形成的；
- 找回尚未迁移的候选思路；
- 比较新旧方案。

归档不是当前规格。日常讨论和实现不要整篇读取这些文件。
