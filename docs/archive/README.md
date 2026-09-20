# 历史设计归档

这里保留早期调研、完整推演、被取代设计以及已经结束的 Feature 计划和完成证据。

- [早期竞品调研与概念设计](./AI_NATIVE_PERSONAL_DATABASE_RESEARCH_2026-07-29.md)
- [早期 AI 自主治理与 MVCC 设计](./AI_NATIVE_AUTONOMY_AND_MVCC_2026-07-29.md)
- [早期 MSQL、语义路由与上下文协议](./MSQL_SEMANTIC_ROUTING_AND_CONTEXT_2026-07-29.md)
- [早期 Wiki 导出设计](./WIKI_EXPORT_DESIGN_2026-07-29.md)
- [F00–F163 历史计划与完成证据](./planning/README.md)
- [被取代的设计与实现规格](./design/README.md)
- [被取代的存储规格](./storage/) — 自研 Page、Buffer Pool、WAL/三份日志、B+ Tree、
  COW generation、MVCC、Instance 物理格式等 63+ 份，2026-09-20 整批移入。
  当前形态见[存储层](../storage/README.md)；
- [被取代的数据/查询专文](./data/relationship-store-v1.md)与
  [Route 向量 generation](./query/route-vector-generation-v1.md)；
- [2026-08 执行计划](./planning/execution-plan-2026-08.md) — 引擎侧 E 阶段队列；
- [F42 AI-native v1 冻结题库](./benchmarks/ai-native-v1.json)

归档用途：

- 追溯一个结论是怎样形成的；
- 找回尚未迁移的候选思路；
- 比较新旧方案。

归档不是当前规格。日常讨论和实现不要整篇读取这些文件。
