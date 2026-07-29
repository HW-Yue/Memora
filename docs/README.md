# Memora 文档入口

这里默认只保存当前有效设计。旧讨论过程位于 [`archive/`](./archive/README.md)，除非追溯决策，否则不要读取。

## 阅读规则

- 先打开本页，只选择与当前问题直接相关的一个主题文件。
- 不要一次读取整个 `docs/`。
- 每个主题文件只记录结论、理由、边界和未决问题。
- 新结论应修改对应主题，不复制到多个综合文档。
- 跨主题关系使用链接，不复制正文。

## 状态总览

- [已形成的方向](./planning/confirmed-directions.md)
- [尚未确认的问题](./planning/open-questions.md)
- [仍未解决的痛点](./planning/unresolved-pain-points.md)

## 产品与数据

- [AI-native 产品边界](./product/ai-native-boundary.md) — AI 负责语义建模，引擎负责物理正确性。
- [AI-native 产品契约](./product/ai-native-contract.md) — 什么才算真正由 AI 自主管理的数据库。
- [可安装的独立语义数据库](./product/installable-database-package.md) — 单库打包、安装、直接问答和全局跨库入口。
- [质量模型与验收](./product/quality-model.md) — 写入、检索、修改、上下文和接管的效果指标。
- [语义记录模型](./data/semantic-records.md) — AI 自定义表和字段，记录是短小完整的知识模块。
- [自描述 Data Dictionary](./data/self-describing-data-dictionary.md) — 让陌生 Agent 在有界输出内理解库、表、字段和边界。
- [资料吸收](./data/assimilation.md) — 外部资料临时读取，吸收后不保存原文。

## Agent 与查询

- [AI 自主权与约束](./agent/autonomy.md) — 自主建模、风险等级和引擎不变量。
- [内置 Agent Runtime](./agent/embedded-agent-runtime.md) — 自带模型循环，内外调用统一进入同一 MSQL 执行核心。
- [数据库查询 Sub-agent](./agent/database-query-subagent.md) — 旧首选方案；保留为宿主侧兼容方式。
- [数据库 Mutation Agent](./agent/database-mutation-agent.md) — 写入职责和收据设计，后续并入内置 Runtime 的能力配置。
- [MSQL](./query/msql.md) — 标准化发现、查询、修改和事务语言。
- [语义路由](./query/semantic-routing.md) — 短索引、倒排召回与关系扩展。
- [无向量检索质量链路](./query/retrieval-quality.md) — 从意图扩展、候选融合到有界 Context Pack 的完整流程。
- [上下文生命周期](./query/context-lifecycle.md) — 当前重点：索引缓存、污染、失效和平台限制。
- [工作集与 LRU 缓存](./query/working-set-cache.md) — 跨聊天复用热路径，减少重复发现和工具调用。

## 存储

- [存储引擎术语](./storage/terminology.md) — 与 MySQL/InnoDB 对齐的标准命名和 Memora 独有概念。
- [MVCC、Undo Log 与 Redo Log](./storage/mvcc-undo-redo.md) — 版本、并发、回溯和恢复。
- [物理与检索索引](./storage/indexing.md) — B+ Tree、倒排索引及聚簇/非聚簇方向。
- [Tablespace、Page 与 Record 布局](./storage/tablespace-page-record-layout.md) — Data File、Extent、Page、混合字段和 Schema 演化。
- [Instance、Database 与 Table](./storage/instance-database-table.md) — 一个本地实例承载多个逻辑数据库。

## 导出与调研

- [Obsidian Wiki 导出](./export/obsidian-wiki.md) — 数据库快照到 Markdown/Wikilink。
- [市场调研入口](./research/competitors.md) — Agent 记忆、AI 数据库、直接竞品与市场空白。
- [Agent 记忆与个人知识产品](./research/market-memory-systems.md) — Mem0、Letta、Graphiti、Basic Memory、Memvid 等。
- [AI 数据库与检索基础设施](./research/market-databases.md) — seekdb、Memoria、HelixDB、LanceDB、Chroma、Qdrant 等。
- [市场空白与定位](./research/market-positioning.md) — 可借鉴能力、核心风险与可能壁垒。

## 计划

- [开发与验证路线](./planning/roadmap.md) — 先验证 AI-native 体验，再进入完整存储内核。

## 文档规模约束

- 一个文件只回答一个主要问题。
- 目标不超过约 150 行；超过后优先按子问题拆分。
- 不保存聊天逐字稿。
- 设计例子只保留能解释协议的最小片段。
