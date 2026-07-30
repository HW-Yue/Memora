# 开发与验证路线

状态：长期路线讨论稿。当前先完成原生极简底座；顺序按产品风险，而不是按数据库教科书章节。

## Phase 0：定义和实验材料

交付：

- 以 `Memora` 作为公开品牌，按 `memora` 优先、`memoradb` 兜底的规则完成 CLI、包名、仓库和域名核验；
- 冻结 AI-native 产品契约 v0；
- 建立多项目对话、资料吸收、修订和冷启动接管数据集；
- 定义 MSQL 最小语法和 JSON envelope；
- 定义 Query/Mutation Agent 的输入输出预算。

退出条件：核心任务、指标和失败判定可以自动运行，不再只靠主观演示。

## Phase 1：macOS Go 垂直切片

先实现一个仅面向 macOS 的本地 Go 可执行文件，端到端跑通而不追求完整存储内核：

```text
memora
memora daemon
memora init
memora --stdio
memora exec
memora query
memora export
memora pack
memora install
memora open
memora doctor
```

包含：本地 daemon 与 IPC、统一 MSQL 执行核心、Data Dictionary、
Database/Table/Column、Semantic Row、revision、关系、Table 级 Router 和稳定
JSON 错误。底层可以是可替换的简化实现，但不得定义上层产品协议。

退出条件：自然对话能够自主建模、SQL 取数、精确修订并导出 Wiki；一个逻辑 Database 可以打包、校验、安装和只读直接问答。

## Phase 2：Agent 集成

- 完成 Canonical Skill 的 read/write 流程、有界 Route Frame 和 Mutation Receipt；
- 实现 AI 对 Table 级语义树的逐层 MSQL 导航、RowID 返回和 SQL 回表；
- 实现 Router membership 反向索引、`pending_reindex` 队列、tombstone，以及 Row/子树/Database 三级重建与原子切换；
- 完成 Query Workspace 生命周期、daemon 生命周期、交互 CLI 和 `--stdio` bridge；
- 发布供外部 Agent 调用 `memora exec` 的 Canonical Skill；
- 接入 Codex/Claude，验证宿主无关性；
- 保留宿主侧 Query Sub-agent 作为可选兼容路径。

退出条件：20～50 轮主题切换中，主上下文增长、工具调用和召回达到候选门槛。

自带模型 Provider 和 `memora ask` 不进入 v0 关键路径；Skill-first 体验通过后，再依据独立使用需求决定是否实现。

## Phase 3：语义自治质量

- Schema 查重、alias、merge/rename/migration；
- 记录 worthiness、split/merge、contradiction 和 supersede；
- 资料吸收 inventory、coverage 和 source receipt；
- Row/Schema/关系与 Table 级语义树的一致性维护和局部优化；
- memory feedback 和可审计修订候选；
- 与人工 Markdown 整理、字面搜索和传统精确查询做用户负担对照。

退出条件：连续 50 次以上自主建模不出现未报告的结构熵增；逐层语义树召回、
Row 拆分重组和冷启动接管通过产品故事门。

## Phase 4：原生写入/读取闭环（当前优先）

先实现：

- `.memora` Header、单 Record Frame、Put/Get 和 close/reopen；
- 真实 Catalog/Row typed payload 的写入/读取；
- MSQL INSERT → restart → SELECT by RowID；
- 再逐项接入其他对象，之后才增加事务与崩溃恢复；
- 最后执行 SQLite snapshot 迁移、回读验证、默认切换和清理。

退出条件：新 Instance 只使用自有文件；旧数据可迁移；CRUD、History、Relation、
Table Router、重启和损坏拒绝全部运行于原生底座。

B+ Tree 已确认为原生底座闭环后的必做持久化主索引。Page/COW/Redo、完整 Buffer
Pool、Secondary Index、高级 MVCC/Undo 与同步仍不作为一次性“大内核”实现，
分别按已 Review Feature 和实测数据进入。

## Phase 5：个人数据库产品化

- 稳定 Obsidian Wiki 增量导出；
- Instance 搬迁、校验、备份和恢复，以及数据库包升级/冲突策略；
- Policy、隐私域和跨 Database 关系；
- CLI 安装升级和 Skill 分发；
- 可选 MCP adapter；
- benchmark 报告和兼容矩阵。

## 不提前做

- 实现任何 HNSW、Embedding、Vector/cosine 或等价相似度路径；
- 在协议未稳定前优化磁盘格式；
- 在 source fidelity 未解决前批量吸收重要资料；
- 在 Schema 健康测试前允许无约束自动迁移；
- 在公开标识的重名和可用性风险解决前发布公共包和命令名。

## 关联

- [TDD 开发总计划](./tdd-development-plan.md)
- [尚未确认的问题](./open-questions.md)
- [质量模型与验收](../product/quality-model.md)
- [市场空白与定位](../research/market-positioning.md)
