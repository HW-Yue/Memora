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
- [AI-native 可演化配置](./product/adaptive-configuration.md) — 默认值进入数据库；冻结、迁移和 AI 优化边界留到最后阶段确定。
- [可安装的独立语义数据库](./product/installable-database-package.md) — 单库打包、安装、直接问答和全局跨库入口。
- [质量模型与验收](./product/quality-model.md) — 写入、检索、修改、上下文和接管的效果指标。
- [语义记录模型](./data/semantic-records.md) — AI 自定义表和字段，记录是短小完整的知识模块。
- [自描述 Data Dictionary](./data/self-describing-data-dictionary.md) — 让陌生 Agent 在有界输出内理解库、表、字段和边界。
- [Catalog v1](./data/catalog-v1.md) — F13 的稳定对象身份、名称解析、语义必填项和 Schema version 传播规则。
- [逻辑类型与字段预算 v1](./data/logical-types.md) — F14 的 NULL、整数、布尔、时间、文本、关系 ID 和禁止截断规则。
- [Row Store v1](./data/row-store-v1.md) — F15 的稳定 Row 身份、Column ID 编码、Schema/revision 校验与逻辑删除契约。
- [History Store v1](./data/history-store-v1.md) — F17 的追加式语义 revision、commit sequence、provenance 与补偿边界。
- [Relationship Store v1](./data/relationship-store-v1.md) — F18 的结构化关系、revision、双向索引与完整性边界。
- [Agent Inverted Index v1](./data/agent-index-v1.md) — F19 的完整词项快照、posting、revision 失效与 24/64 预算。
- [Mechanical Inverted Index v1](./data/mechanical-index-v1.md) — F20 的中英 tokenizer、N-gram、空间预算、来源隔离与重建。
- [Pending Reindex v1](./data/pending-reindex-v1.md) — F24 的语义索引立即失效、durable queue、lease、重试与 revision guard。
- [资料吸收](./data/assimilation.md) — 外部资料临时读取，吸收后不保存原文。

## Agent 与查询

- [AI 自主权与约束](./agent/autonomy.md) — 自主建模、风险等级和引擎不变量。
- [可选内置 Agent Runtime](./agent/embedded-agent-runtime.md) — v0 后候选；第一版由 Codex/Claude Code 按 Skill 调用统一 MSQL 核心。
- [索引发现 Sub-agent](./agent/index-discovery-subagent.md) — 逐层导航并融合倒排，只返回数据项定位，主 Agent 再用 SQL 取数。
- [数据库查询 Sub-agent](./agent/database-query-subagent.md) — 旧首选方案；保留为宿主侧兼容方式。
- [数据库 Mutation Agent](./agent/database-mutation-agent.md) — Skill 写入职责、维护选择和收据设计。
- [MSQL](./query/msql.md) — 标准化发现、查询、修改和事务语言。
- [MSQL Lexer v0](./query/msql-lexer.md) — F10 已冻结的 Token、Unicode source span、注释、参数与稳定词法错误。
- [MSQL Parser Core v1](./query/msql-parser.md) — F11 已冻结的单语句 AST、表达式优先级、参数与精确 Parser 错误。
- [MSQL Batch 与事务边界 v1](./query/msql-batch-transactions.md) — F12/F16 的可恢复多语句解析、原子事务执行和跨 request IPC session。
- [MSQL Catalog DDL v1](./query/catalog-ddl.md) — F13c 的自描述建模、发现、rename 和限定名 Binder 契约。
- [MSQL 参数与表达式 v1](./query/msql-expressions.md) — F15c 的无插值参数绑定、整数/布尔表达式和稳定求值错误。
- [MSQL SELECT Planner v1](./query/msql-select.md) — F15d 的限定表/字段绑定、强制 LIMIT、精确 Row ID 与截断边界。
- [MSQL Mutation Executor v1](./query/msql-mutation.md) — F15e 的参数化 CRUD、expected revision、影响行数预算与零插值边界。
- [MSQL History v1](./query/msql-history.md) — F17c 的 AS OF、SHOW HISTORY、RESTORE 补偿与事务回滚语义。
- [MSQL Relationships v1](./query/msql-relationships.md) — F18c 的参数化关系创建、双向发现、逻辑删除与 Batch 语义。
- [MATCH Fusion v1](./query/match-fusion-v1.md) — F21 的双通道独立归一化、0.8/0.2 权重、稳定排序与 locator-only 输出。
- [Router Tree v1](./query/router-tree-v1.md) — F22 的稳定节点、多叉路径、多叶 membership、容量和遍历 cursor。
- [Index Discovery v1](./query/index-discovery-v1.md) — F23 的 Route、倒排、关系确定性融合、预算与 locator-only 输出。
- [MSQL Result Envelope v1](./query/result-envelope.md) — 单条/批量统一结果、稳定错误码、warning 与截断兼容规则。
- [Agent 语义目录索引（Router）](./query/semantic-routing.md) — 多层多叉语义树逐层找到叶子数据项 ID，再与倒排和关系候选融合。
- [无向量检索质量链路](./query/retrieval-quality.md) — 从逐层发现、候选融合、返回定位到主 Agent SQL 回表的完整流程。
- [上下文生命周期](./query/context-lifecycle.md) — 当前重点：索引缓存、污染、失效和平台限制。
- [Query Workspace 与缓存边界](./query/working-set-cache.md) — 区分 Agent 临时状态、物理 Page 缓存和查询结果缓存。

## 存储

- [存储引擎术语](./storage/terminology.md) — 与 MySQL/InnoDB 对齐的标准命名和 Memora 独有概念。
- [Buffer Pool](./storage/buffer-pool.md) — daemon 中缓存文件 Page，最近访问的 Page 按 LRU 或近似算法保留与淘汰。
- [MVCC、Undo Log 与 Redo Log](./storage/mvcc-undo-redo.md) — 版本、并发、回溯和恢复。
- [Binlog 与多设备同步基础](./storage/binlog-and-sync.md) — 已提交逻辑变更流，为增量同步和时间点恢复保留基础。
- [物理与检索索引](./storage/indexing.md) — B+ Tree、倒排索引及聚簇/非聚簇方向。
- [Index Generation Manifest v1](./storage/generation-manifest-v1.md) — F25 的三类独立 generation、原子发布、query pin 与 GC。
- [Logical Snapshot v1](./storage/logical-snapshot-v1.md) — F26 的后端无关逻辑备份、兼容迁移、未知字段和确定性哈希。
- [Tablespace、Page 与 Record 布局](./storage/tablespace-page-record-layout.md) — Data File、Extent、Page、混合字段和 Schema 演化。
- [Instance、Database 与 Table](./storage/instance-database-table.md) — 一个本地实例承载多个逻辑数据库。
- [macOS Instance 数据目录](./storage/macos-instance-directory.md) — 默认 datadir、缓存/日志边界和自定义路径规则。
- [Database 物理目录](./storage/database-file-layout.md) — 每库 data/history、每表 Tablespace 和独立索引 generation。

## 导出与调研

- [Obsidian Wiki 导出](./export/obsidian-wiki.md) — 数据库快照到 Markdown/Wikilink。
- [市场调研入口](./research/competitors.md) — Agent 记忆、AI 数据库、直接竞品与市场空白。
- [Agent 记忆与个人知识产品](./research/market-memory-systems.md) — Mem0、Letta、Graphiti、Basic Memory、Memvid 等。
- [AI 数据库与检索基础设施](./research/market-databases.md) — seekdb、Memoria、HelixDB、LanceDB、Chroma、Qdrant 等。
- [市场空白与定位](./research/market-positioning.md) — 可借鉴能力、核心风险与可能壁垒。

## 计划

- [开发与验证路线](./planning/roadmap.md) — 先验证 AI-native 体验，再进入完整存储内核。
- [TDD 开发总计划](./planning/tdd-development-plan.md) — 按独立 feature branch/commit 推进，测试先行并设置阶段质量门。
- [Phase A 退出验收](./planning/phase-a-exit-evidence.md) — 干净 datadir 下 CLI、daemon、并发 MSQL parse、重启和全量门禁证据。

## 开发

- [测试约定](./development/testing.md) — Unit、integration、e2e、race、隔离目录和确定性 Testkit。
- [进程配置与宿主边界](./development/process-configuration.md) — 非秘密启动配置的优先级，以及不接收 Codex/Claude 模型密钥的边界。
- [本地 IPC 协议](./development/ipc-protocol.md) — 长度前缀 JSON、协议版本、并发请求和连接级 Session 生命周期。
- [ADR-0001：SQLite 原型 Store](./decisions/0001-prototype-store.md) — 用可替换 CGO-free 后端先验证产品，原生内核通过质量门后再替换。

## 文档规模约束

- 一个文件只回答一个主要问题。
- 目标不超过约 150 行；超过后优先按子问题拆分。
- 不保存聊天逐字稿。
- 设计例子只保留能解释协议的最小片段。
