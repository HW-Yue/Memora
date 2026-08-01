# ADR-0007：Router 权威，候选预测器可组合

状态：Accepted，2026-08-01；F162/F163 资源门已执行，CPU exact 保持默认，
Apple Accelerate/HNSW 延后。取代“禁止任何 Vector/Embedding 路径”的绝对边界。

## 背景

Table Router 的单层约 16 个短语义节点，上下文本身不大；真实成本主要来自
Database、Table 和每层 Route 工具返回后触发的多次模型续推。只保留严格逐步导航
可以保证路径显式，却没有利用廉价候选信号减少模型调用。

旧 F21/F23 曾让相似度融合成为查询主路径，并把候选当作答案来源，因此被撤销。
这不应扩大为禁止所有可回退的 Route 位置预测。

## 决定

Memora 提供由 Canonical Skill 通过 MSQL 显式组合的检索武器库：

- 紧凑 Database/Table Catalog；
- 倒排词项对 Database、Table、Route 的位置聚合；
- 查询向量与 AI 维护的 Route 节点向量的候选匹配；
- 带 revision 的当前 Route Frame；
- Table Router 的正常逐层导航和最终 RowID SQL 回表。

预测器只返回带来源的候选位置。AI 仍读取明确 Route 节点、选择路径并通过 SQL
回表；预测错误只导致丢弃预取结果和正常回退，不能改变可见事实或正确性。

## Vector 边界

- 只允许对 Route name、aliases、purpose、ancestor path 和边界说明生成向量；
- 禁止持久化 Row 正文、文档 chunk、图片或事实的向量副本；
- Vector 不能直接返回正文、生成答案、自动排除零命中 Table 或成为权威相关性；
- Route vector 是可删除、可重建的派生 generation，绑定 model/version、维度、
  Route revision 和来源文本 digest；
- 缺少模型、generation 失效或候选不足时，普通 Router 必须保持完整可用；
- 不同 embedding space 的向量禁止混合比较。

## 实现顺序

先冻结与算法无关的候选 MSQL/envelope，再实现 CPU 精确扫描 reference backend。
当前不实现 HNSW，也不让调用方在生产 SQL 中依赖物理算法名称。

CPU exact 只负责向量匹配，不负责把文本变成向量。文本编码仍需要同一 embedding
space 的 encoder，但不要求外部在线服务：可由本地小模型、宿主 adapter 或云端
Provider 实现。引擎只接收版本化向量和模型身份，不内置供应商或生成式 LLM 调用。

Apple Accelerate 和 HNSW 都属于证据触发的可替换后端。只有真实 Route 规模下的
`p95`、CPU/内存压力和 Recall 证明 reference backend 越过冻结门槛，才各自拆出
独立 Feature。

F162/F163 在 Apple M4 上分别测量 4,368 与 17,472 条、384 维 Route：最大 p95 为
2.434 ms 与 9.957 ms，内存均未越冻结门，因此不实现 Accelerate/HNSW。结果是当前
机器与规模下的延后证据，不是永久禁止；复测入口记录在按证据触发门文档中。

## 开发顺序

F97d3–F109 继续完成当前存储闭环。Route Predictor 在真实 Host Contract 和冻结的
Route Benchmark Corpus 之后、真实 Runner 之前逐项实现，避免先看结果再修改题库。

## 后果

- Memora 仍由 AI 创建和读取显式语义树，不退化为传统向量知识库；
- 可以用额外 CPU、内存和少量误预测 token 换取更少模型调用；
- embedding 生成、模型分发、隐私和 Mac 加速仍需独立 Review；
- 本地 encoder 可以随可选模型包离线分发，但不能让模型缺失破坏 Router 基线；
- 旧无向量 Benchmark 只保留为 Router-only 基线，不再是最高产品禁令。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [语义路由投机预取](../query/speculative-route-prefetch.md)
- [Route Predictor Feature 计划](../planning/route-predictor-feature-plan.md)
