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

- [AI-native 产品宪章](./product/ai-native-product-charter.md) — 最终产品形态、AI 标准操作流程、用户故事和永久边界。
- [AI-native 产品边界](./product/ai-native-boundary.md) — AI 负责语义建模，引擎负责物理正确性。
- [AI-native 产品契约](./product/ai-native-contract.md) — 什么才算真正由 AI 自主管理的数据库。
- [AI-native 可演化配置](./product/adaptive-configuration.md) — 默认值进入数据库；冻结、迁移和 AI 优化边界留到最后阶段确定。
- [可安装的独立语义数据库](./product/installable-database-package.md) — 单库打包、安装、直接问答和全局跨库入口。
- [Database Package v1](./product/database-package-v1.md) — F44 的确定性单库包、完整性、只读打开、可信安装与冲突边界。
- [质量模型与验收](./product/quality-model.md) — 写入、检索、修改、上下文和接管的效果指标。
- [语义记录模型](./data/semantic-records.md) — AI 自定义表和字段，记录是短小完整的知识模块。
- [自描述 Data Dictionary](./data/self-describing-data-dictionary.md) — 让陌生 Agent 在有界输出内理解库、表、字段和边界。
- [Catalog v1](./data/catalog-v1.md) — F13 的稳定对象身份、名称解析、语义必填项和 Schema version 传播规则。
- [逻辑类型与字段预算 v1](./data/logical-types.md) — F14 的 NULL、整数、布尔、时间、文本、关系 ID 和禁止截断规则。
- [Row Store v1](./data/row-store-v1.md) — F15 的稳定 Row 身份、Column ID 编码、Schema/revision 校验与逻辑删除契约。
- [History Store v1](./data/history-store-v1.md) — F17 的追加式语义 revision、commit sequence、provenance 与补偿边界。
- [Relationship Store v1](./data/relationship-store-v1.md) — F18 的结构化关系、revision、双向索引与完整性边界。
- [Agent Inverted Index v1](./data/agent-index-v1.md) — F19 历史实现；不再作为语义查询主路径。
- [Mechanical Inverted Index v1](./data/mechanical-index-v1.md) — F20 历史字面索引实现；保留与否待架构对账。
- [Pending Reindex v1](./data/pending-reindex-v1.md) — F24 的语义索引立即失效、durable queue、lease、重试与 revision guard。
- [资料吸收](./data/assimilation.md) — 外部资料临时读取，吸收后不保存原文。

## Agent 与查询

- [AI 自主权与约束](./agent/autonomy.md) — 自主建模、风险等级和引擎不变量。
- [Canonical Skill v1](./agent/canonical-skill-v1.md) — F28 的唯一宿主规则源、版本 lint、冲突边界和上下文预算。
- [宿主模型与 CC Switch 兼容边界](./agent/host-provider-compatibility.md) — Provider 由宿主管理，兼容自定义 OpenAI/Anthropic 协议地址且密钥不进入 Memora。
- [Skill 查询流程 v1](./agent/skill-query-v1.md) — F30 历史实现与 Table 级逐层 Route 目标状态机。
- [Skill 写入流程 v1](./agent/skill-write-v1.md) — F31 的七种语义决策、Mutation Plan、Policy、短事务和紧凑收据。
- [Skill Schema 生命周期 v1](./agent/skill-schema-lifecycle-v1.md) — F32 的同义 Schema 复用、受限 DDL、影响预览和补偿回滚。
- [Conversation Delta 交接 v1](./agent/conversation-delta-v1.md) — F33 的显式事件、幂等去重、checkpoint 和缺失上下文处理。
- [Skill 语义冲突交互 v1](./agent/skill-conflict-v1.md) — F34 的并列来源/revision 差异、用户决议和 Mutation Plan 绑定。
- [资料清单与覆盖 v1](./agent/assimilation-coverage-v1.md) — F35 的临时 inventory、窗口去重、未读范围和中断恢复。
- [资料独立复核与提交 v1](./agent/assimilation-review-v1.md) — F36 的隔离复核、语义提交门禁、来源锚点和 Source Receipt。
- [语义数据库健康维护 v1](./agent/semantic-health-v1.md) — F37 的确定性健康报告、风险分级和低风险 reindex retry。
- [反馈、修订与逻辑 Undo v1](./agent/feedback-revision-v1.md) — F38 的五类反馈收据、显式确认和补偿 revision。
- [Skill 首次安全安装 v1](./agent/safe-bootstrap-v1.md) — F39 的显式授权、Release 校验、源码回退和 doctor 验收。
- [可选内置 Agent Runtime](./agent/embedded-agent-runtime.md) — v0 后候选；第一版由 Codex/Claude Code 按 Skill 调用统一 MSQL 核心。
- [索引发现 Sub-agent](./agent/index-discovery-subagent.md) — 已撤销混合融合设计的历史说明。
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
- [MATCH Fusion v1](./query/match-fusion-v1.md) — 已撤销的 F21 混合评分历史实现。
- [Router Tree v1](./query/router-tree-v1.md) — F22 Database 级历史实现与 Table 级迁移差距。
- [Index Discovery v1](./query/index-discovery-v1.md) — 已撤销的 F23 候选融合历史实现。
- [MSQL Result Envelope v1](./query/result-envelope.md) — 单条/批量统一结果、稳定错误码、warning 与截断兼容规则。
- [Agent 语义目录索引（Router）](./query/semantic-routing.md) — 每个 Table 一棵多层语义树，AI 逐层找到叶子 RowID。
- [语义树检索质量链路](./query/retrieval-quality.md) — AI 从 Table Router 逐层导航到 RowID，再 SQL 回表。
- [上下文生命周期](./query/context-lifecycle.md) — 当前重点：索引缓存、污染、失效和平台限制。
- [Query Workspace 与缓存边界](./query/working-set-cache.md) — 区分 Agent 临时状态、物理 Page 缓存和查询结果缓存。

## 存储

- [原生极简存储格式](./storage/native-minimal-store.md) — 当前优先：文件位置、Header、事务 Frame、逻辑 Record 与恢复边界。
- [存储引擎术语](./storage/terminology.md) — 与 MySQL/InnoDB 对齐的标准命名和 Memora 独有概念。
- [Buffer Pool](./storage/buffer-pool.md) — daemon 中缓存文件 Page，最近访问的 Page 按 LRU 或近似算法保留与淘汰。
- [Buffer Pool Page Loading v1](./storage/buffer-pool-page-loading-v1.md) — F87 的 Page Table、single-flight、pin Handle 与读写 latch 契约。
- [Buffer Pool Eviction v1](./storage/buffer-pool-eviction-v1.md) — F88 的容量、young/old LRU、pin victim 与 scan protection 契约。
- [Buffer Pool Dirty Flush v1](./storage/buffer-pool-dirty-flush-v1.md) — F89 的 committed Modify、flush list 与 WAL-before-data 契约。
- [B+ Tree Node Codec v1](./storage/btree-node-codec-v1.md) — F90 的 internal/leaf slotted payload、golden 与 corruption 契约。
- [B+ Tree Point Search v1](./storage/btree-point-search-v1.md) — F91 的 root-to-leaf separator、identity、level 与精确 Get 契约。
- [B+ Tree Range Cursor v1](./storage/btree-range-cursor-v1.md) — F92 的边界、limit、leaf-link 续读与 poison 契约。
- [B+ Tree Single-Node Upsert v1](./storage/btree-single-node-upsert-v1.md) — F93 的 leaf/internal 有序 insert/replace 与容量原子性契约。
- [B+ Tree Split v1](./storage/btree-split-v1.md) — F94 的 variable-size leaf/internal split、separator promotion 与 root grow 契约。
- [B+ Tree Leaf Delete v1](./storage/btree-leaf-delete-v1.md) — F95 的单 leaf 精确删除与 tombstone handoff 边界。
- [B+ Tree Rebalance v1](./storage/btree-rebalance-v1.md) — F96 的 sibling merge/redistribute、parent separator 与 root shrink 契约。
- [B+ Tree Mutation Plan v1](./storage/btree-mutation-plan-v1.md) — F97a 已冻结的多层私有 mutation、split/rebalance 传播与 allocator handoff。
- [MVCC、Undo Log 与 Redo Log](./storage/mvcc-undo-redo.md) — 版本、并发、回溯和恢复。
- [Committed Change Log（Binlog）与未来同步](./storage/binlog-and-sync.md) — 第一用途是 Admin 展示数据与语义索引变化；同步、PITR 后续复用。
- [物理与检索索引](./storage/indexing.md) — B+ Tree、倒排索引及聚簇/非聚簇方向。
- [中间 Route Synopsis](./query/route-synopsis.md) — 可选私有子树总结、按需读取预算及随 reshape 原子更新规则。
- [来源强度与复核证明](./data/source-provenance.md) — conversation/anchor/reviewed 分级、History 证据与 challenge-bound review。
- [Index Generation Manifest v1](./storage/generation-manifest-v1.md) — F25 的三类独立 generation、原子发布、query pin 与 GC。
- [Logical Snapshot v1](./storage/logical-snapshot-v1.md) — F26 的后端无关逻辑备份、兼容迁移、未知字段和确定性哈希。
- [SQLite 兼容迁移边界](./storage/sqlite-compatibility-migration.md) — 旧 reader 隔离于独立工具，主程序拒绝静默回退。
- [Tablespace、Page 与 Record 布局](./storage/tablespace-page-record-layout.md) — Data File、Extent、Page、混合字段和 Schema 演化。
- [Page Codec v1](./storage/page-codec-v1.md) — F81 的 16 KiB Header、Page Type、CRC32C 与确定性编解码边界。
- [Page File Manager v1](./storage/page-file-manager-v1.md) — F82 的 space manifest、Page 定位 I/O、连续分配与 reopen 边界。
- [WAL Record Stream v1](./storage/wal-record-stream-v1.md) — F83 的 Segment/Record、LSN、CRC32C、append/scan 与 durable offset。
- [WAL Durable Transaction v1](./storage/wal-durable-transaction-v1.md) — F84 的连续 change/commit、digest、fsync 成功边界与 poisoned 状态。
- [Crash Recovery v1](./storage/crash-recovery-v1.md) — F85 的 committed Page redo、Page LSN 幂等与 FPI torn Page 修复。
- [WAL Segment Set v1](./storage/wal-segment-set-v1.md) — F86a 的连续 Segment ID/LSN、显式 roll 与跨段事务顺序。
- [Checkpoint Publish v1](./storage/checkpoint-publish-v1.md) — F86b 的 Page durability barrier、durable marker 与恢复起点。
- [WAL Segment Reclaim v1](./storage/wal-segment-reclaim-v1.md) — F86c 的 retained manifest、旧段删除与中断恢复。
- [Durable WAL Frontier v1](./storage/wal-durable-frontier-v1.md) — F97b1 已完成；为 crash repair 提供独立可信的 durable byte boundary。
- [WAL Recovery Open v1](./storage/wal-recovery-open-v1.md) — F97b2 已完成；严格验证 frontier 前缀并持久清理 speculative tail。
- [Tree Control v1](./storage/tree-control-v1.md) — F97c1 已完成的 slot 1 control Page 与 bootstrap 格式。
- [Tree Control v2](./storage/tree-control-v2.md) — F97c4 分离 physical generation 与逐提交 revision 的替代格式。
- [Root/Allocator Redo v1](./storage/root-allocator-redo-v1.md) — F97c2 已完成的 metadata payload codec。
- [Root/Allocator Redo v2](./storage/root-allocator-redo-v2.md) — F97c4 将 metadata 前置状态改为 publication revision。
- [Tree Metadata Recovery v1](./storage/tree-metadata-recovery-v1.md) — F97c3 的 root-last recovery、验证与幂等边界。
- [Tree Commit Preparation v1](./storage/tree-commit-preparation-v1.md) — F97d1 的 Mutation Plan 严格校验与确定性 redo 顺序。
- [Instance、Database 与 Table](./storage/instance-database-table.md) — 一个本地实例承载多个逻辑数据库。
- [macOS Instance 数据目录](./storage/macos-instance-directory.md) — 默认 datadir、缓存/日志边界和自定义路径规则。
- [Instance Format 升级与回滚 v1](./storage/instance-format-upgrade-v1.md) — F49 的兼容状态、v1→v2、完整性备份、迁移 journal 与 `doctor repair`。
- [Database 物理目录](./storage/database-file-layout.md) — 每库 data/history、每表 Tablespace 和独立索引 generation。

## 导出与调研

- [Obsidian Wiki 导出 v1](./export/obsidian-wiki.md) — F45 的稳定 ID 页面、跨库 Wikilink、Export Profile 与增量 manifest。
- [市场调研入口](./research/competitors.md) — Agent 记忆、AI 数据库、直接竞品与市场空白。
- [Agent 记忆与个人知识产品](./research/market-memory-systems.md) — Mem0、Letta、Graphiti、Basic Memory、Memvid 等。
- [AI 数据库与检索基础设施](./research/market-databases.md) — seekdb、Memoria、HelixDB、LanceDB、Chroma、Qdrant 等。
- [市场空白与定位](./research/market-positioning.md) — 可借鉴能力、核心风险与可能壁垒。

## 计划

- [Feature 产品与用户故事门禁](./planning/feature-product-gate.md) — 每个 Feature 开工前与合入前的强制产品审查。
- [小 Feature TDD 与合入协议](./planning/feature-tdd-protocol.md) — F81 以后单一结果、RED/GREEN/REFACTOR、故障矩阵和逐项合入规则。
- [F81 Page Codec 开工与完成门](./planning/f81-page-codec-gate.md) — 单一结果、RED 清单、明确不做与完整测试门。
- [F82 Page File Manager 开工与完成门](./planning/f82-page-file-manager-gate.md) — 单 space 文件 I/O 的 RED、故障矩阵与完成证据。
- [F83 WAL Record Stream 开工与完成门](./planning/f83-wal-record-stream-gate.md) — 单 Segment 字节流、LSN 与 corruption TDD 门。
- [F84 WAL Durable Transaction 开工与完成门](./planning/f84-wal-durable-transaction-gate.md) — commit digest、Sync 顺序与故障注入门。
- [F85 Crash Recovery 开工与完成门](./planning/f85-crash-recovery-gate.md) — committed redo、幂等 Page LSN 与 FPI 修复门。
- [F86a WAL Segment Set 开工与完成门](./planning/f86a-wal-segment-set-gate.md) — 多 Segment roll/reopen 与故障注入门。
- [F86b Checkpoint Publish 开工与完成门](./planning/f86b-checkpoint-publish-gate.md) — barrier、marker、Sync 与恢复起点门。
- [F86c Segment Reclaim 开工与完成门](./planning/f86c-segment-reclaim-gate.md) — manifest authority、删除顺序与重开门。
- [F87 Page Loading 开工与完成门](./planning/f87-page-loading-gate.md) — fake loader、single-flight、pin/latch 与 race 完成门。
- [F88 Buffer Pool Eviction 开工与完成门](./planning/f88-buffer-pool-eviction-gate.md) — 硬容量、young/old victim、pin 与并发 miss 完成门。
- [F89 Dirty Page Flush 开工与完成门](./planning/f89-dirty-page-flush-gate.md) — Page LSN、dirty victim、WAL 顺序与 write fault 完成门。
- [F90 B+ Tree Node Codec 开工与完成门](./planning/f90-btree-node-codec-gate.md) — internal/leaf golden、容量、seed corpus 与 corruption 完成门。
- [F91 B+ Tree Point Search 开工与完成门](./planning/f91-btree-point-search-gate.md) — 单路径精确 Get、边界、cycle 与 corruption 完成门。
- [F92 B+ Tree Range Cursor 开工与完成门](./planning/f92-btree-range-cursor-gate.md) — 跨 leaf 有界续读、不重不漏与链损坏完成门。
- [F93 B+ Tree Insert 开工与完成门](./planning/f93-btree-insert-gate.md) — 单 Node upsert、reference model 与 no-space 原子性完成门。
- [F94 B+ Tree Split 开工与完成门](./planning/f94-btree-split-gate.md) — leaf/internal 字节平衡切分、root grow 与原子失败完成门。
- [F95 B+ Tree Delete 开工与完成门](./planning/f95-btree-delete-gate.md) — 单 leaf 精确删除、邻居保持与 reference-model 完成门。
- [F96 B+ Tree Rebalance 开工与完成门](./planning/f96-btree-rebalance-gate.md) — sibling merge/redistribute、child mapping 与 root shrink 完成门。
- [F97 Durable Root 拆分 Review](./planning/f97-durable-root-gate.md) — 多层 mutation、WAL crash-open、root/allocator redo 与 durable commit 的拆分门；F97a 已完成。
- [F97a B+ Tree Mutation Plan 开工与完成门](./planning/f97a-btree-mutation-plan-gate.md) — 私有多层 mutation 的 RED matrix 与完成证据。
- [F97b WAL Recovery Open 拆分 Review](./planning/f97b-wal-recovery-open-review.md) — 当前格式无法区分 durable commit 与 speculative tail，建议拆出 durable frontier。
- [F97b1 Durable WAL Frontier 开工与完成门](./planning/f97b1-durable-wal-frontier-gate.md) — 已完成的双槽 control、outcome unknown、fault matrix 与证据。
- [F97b2 WAL Recovery Open 开工与完成门](./planning/f97b2-wal-recovery-open-gate.md) — repairing open 的严格 authority 校验、可重入截尾与故障矩阵。
- [F97c1 Tree Control Codec 开工与完成门](./planning/f97c1-tree-control-codec-gate.md) — slot 1 codec、bootstrap 与 corruption 完成证据。
- [F97c2 Root/Allocator Redo Codec 开工与完成门](./planning/f97c2-root-allocator-redo-gate.md) — metadata payload、版本与字段校验。
- [F97c3 Tree Metadata Recovery 开工与完成门](./planning/f97c3-tree-metadata-recovery-gate.md) — root-last recovery、幂等与故障矩阵。
- [F97c4 Tree Revision Separation 开工与完成门](./planning/f97c4-tree-revision-separation-gate.md) — 修正 physical generation 与 publication revision 冲突。
- [F97d Durable Tree Commit 拆分 Review](./planning/f97d-durable-tree-commit-review.md) — generation 阻断证据及 F97d1–F97d3 拆分。
- [F97d1 Tree Commit Preparation 开工与完成门](./planning/f97d1-tree-commit-preparation-gate.md) — 纯计划校验、Page/free/allocator/root redo 与 root-last。
- [当前实现缺口审计](./planning/implementation-gap-audit-2026-07-31.md) — 对照技术文档与公开代码，区分真实未实现、部分实现和状态漂移。
- [存储内核小 Feature 计划](./planning/row-read-foundation-feature-plan.md) — F81–F109 的 Page、WAL、Buffer Pool、B+ Tree、真实 RowID、MVCC、迁移、COW 与 Change Log。
- [Admin 与本地可观察性小 Feature 计划](./planning/visual-inspection-feature-plan.md) — F109–F122 的读取协议、loopback API、内嵌前端及逐页面验收。
- [F81 之后的 Feature 规划](./planning/next-feature-plan.md) — 取数基础、可视化、真实 AI 质量、语义自治、产品化和按数据触发的存储演进。
- [原生闭环后续 Feature 计划](./planning/native-features-review.md) — 已批准的 F53a–F72 拆分、依赖、闭环与门禁。
- [F72 AI-native 用户故事门](./planning/f72-ai-native-story-gate.md) — 全部产品故事、实际 MSQL、proof 文件、宿主与许可的最终验收。
- [F80 真实发行用户故事门](./planning/f80-real-release-story-gate.md) — 双宿主、隔离 Instance、公开 CLI 和 mutation 后顶层 Route 重查的运行时 PASS。
- [AI-native 真实使用审计](./planning/ai-native-live-ux-audit-2026-07-30.md) — 以发行二进制实测冷启动、Route、修改、恢复和维护；撤销 F72 的产品级 PASS。
- [真实审计后 Feature 计划](./planning/post-audit-feature-plan.md) — F73–F80 修复修改、恢复、反馈、维护、reshape、Route synopsis、来源与真实故事门。
- [F52 原生文件格式开工门](./planning/f52-native-format-gate.md) — 下一 Feature 的故事、格式、边界与待确认项。
- [开发与验证路线](./planning/roadmap.md) — 先验证 AI-native 体验，再进入完整存储内核。
- [TDD 开发总计划](./planning/tdd-development-plan.md) — 按独立 feature branch/commit 推进，测试先行并设置阶段质量门。
- [Phase A 退出验收](./planning/phase-a-exit-evidence.md) — 干净 datadir 下 CLI、daemon、并发 MSQL parse、重启和全量门禁证据。
- [Phase B 退出验收](./planning/phase-b-exit-evidence.md) — 万行 CRUD、索引删除重建、快照哈希、重启和进程级闭环证据。
- [Phase C 退出验收](./planning/phase-c-exit-evidence.md) — Canonical Skill、双宿主、冷启动、冲突边界和无模型 Key 的跨 feature 证据。

## 开发

- [测试约定](./development/testing.md) — Unit、integration、e2e、race、隔离目录和确定性 Testkit。
- [CLI Database Workflow v1](./development/cli-database-workflow.md) — F27 的 exec/query/doctor、统一 MSQL 与进程级垂直链路。
- [Scripted Host Harness v1](./development/scripted-host-harness-v1.md) — F29 的无模型 transcript 重放、错误注入、最终数据库和用户回复断言。
- [Codex Adapter v1](./development/codex-adapter-v1.md) — F40 从 Canonical Skill 确定性派生 Codex metadata、命令规则与 e2e fixture。
- [Claude Code Adapter v1](./development/claude-code-adapter-v1.md) — F41 的 `.claude/skills` 包装、turn 级命令权限与跨宿主 digest 兼容。
- [AI-native Benchmark v1](./development/ai-native-benchmark-v1.md) — F42 的五类可重放旅程、八维评分、baseline adapter 与确定性报告。
- [无向量语义 Route Benchmark v2](./development/no-vector-route-benchmark-v2.md) — F124–F126 的 corpus、真实模型运行、能力曲线和共同安全 fanout。
- [AI-native 发布门 v1](./development/ai-native-release-gate-v1.md) — 已撤销的 F51 历史评测及失效原因。
- [安全与隐私门 v1](./development/security-privacy-gate-v1.md) — F46 的 Database scope、外部路径、approval、审计脱敏与 doctor 检查。
- [macOS Release 制品 v1](./development/macos-release-artifacts-v1.md) — F47 的双架构 Mach-O、可复现归档、版本 manifest、checksum 与 smoke 契约。
- [GitHub Release 自动化 v1](./development/github-release-automation-v1.md) — F48 的签名 tag 门、最小权限、双架构 smoke、Skill bundle 与 draft 发布契约。
- [干净机器验收 v1](./development/clean-machine-acceptance-v1.md) — F50 的隔离 HOME、Skill HTTPS 安装、首条记忆、重启查询、诊断包与双架构发布阻断报告。
- [进程配置与宿主边界](./development/process-configuration.md) — 非秘密启动配置的优先级，以及不接收 Codex/Claude 模型密钥的边界。
- [本地 IPC 协议](./development/ipc-protocol.md) — 长度前缀 JSON、协议版本、并发请求和连接级 Session 生命周期。
- [ADR-0001：SQLite 原型 Store](./decisions/0001-prototype-store.md) — 已被 ADR-0003 取代的历史原型决策。
- [ADR-0002：v0 不内置 Agent Runtime](./decisions/0002-defer-embedded-agent.md) — F43 基于 Skill 覆盖审计 defer Provider/ask loop，并冻结重新开启条件。
- [ADR-0003：原生极简 Store 优先](./decisions/0003-native-minimal-store-first.md) — 先自有文件格式，再接现有逻辑层并迁出 SQLite。
- [ADR-0004：RowID 快速目录与本地最小 MVCC](./decisions/0004-fast-row-directory-minimal-mvcc.md) — MVCC/写锁仍有效；内存目录与 B+ Tree 后置部分已被 ADR-0005 取代。
- [ADR-0005：B+ Tree 是必做的持久化主索引](./decisions/0005-btree-mandatory-primary-index.md) — F90–F102 接通持久化 B+ Tree 与真实 RowID 路径；物理策略由 ADR-0006 细化。
- [ADR-0006：MySQL 式 Page/Buffer Pool/WAL，COW 用于 generation](./decisions/0006-mysql-page-buffer-wal-cow.md) — 16 KiB Page、单实例 Buffer Pool、Redo recovery 与限定 COW 职责。

## 文档规模约束

- 一个文件只回答一个主要问题。
- 目标不超过约 150 行；超过后优先按子问题拆分。
- 不保存聊天逐字稿。
- 设计例子只保留能解释协议的最小片段。
