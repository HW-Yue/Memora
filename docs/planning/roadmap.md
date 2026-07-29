# 开发与验证路线

状态：长期路线讨论稿。顺序按产品风险，而不是按数据库教科书章节。

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

包含：本地 daemon 与 IPC、统一 MSQL 执行核心、文件 Page Buffer Pool、Data Dictionary、Database/Table/Column、Semantic Row、revision、关系、Agent 词项与机械 N-gram 混合倒排、Router、稳定 JSON 错误。底层可以是临时简化实现，但协议和测试不可一次性抛弃。

退出条件：自然对话能够自主建模、SQL 取数、精确修订并导出 Wiki；一个逻辑 Database 可以打包、校验、安装和只读直接问答。

## Phase 2：Agent 集成

- 完成 Canonical Skill 的 read/write 流程、有界 Context Pack 和 Mutation Receipt；
- 实现索引发现 Sub-agent 的逐层导航、混合倒排评分、定位返回和主 Agent SQL 回表；
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
- Router + Agent 词项 + 机械 N-gram + graph 融合排序；
- memory feedback 和可审计修订候选；
- 与 Basic Memory、Mem0/Vector baseline 做对照。

退出条件：连续 50 次以上自主建模不出现未报告的结构熵增；无向量召回和冷启动接管通过质量门槛。

## Phase 4：存储内核

产品体验成立后再实现：

- Tablespace、Data File、Page、Extent、Segment；
- 最新 Record Store、Undo version chain、History Store、B+ Tree 和 Posting Run；
- Buffer Pool、锁、MVCC、Undo Log、Redo Log、逻辑 Binlog 和恢复；
- 多进程协调、compaction、校验、备份、Binlog 增量重放和故障注入；
- format version 与迁移工具。

退出条件：事务、崩溃恢复、索引原子可见和跨版本兼容测试通过。

## Phase 5：个人数据库产品化

- 稳定 Obsidian Wiki 增量导出；
- Instance 搬迁、校验、备份和恢复，以及数据库包升级/冲突策略；
- Policy、隐私域和跨 Database 关系；
- CLI 安装升级和 Skill 分发；
- 可选 MCP adapter；
- benchmark 报告和兼容矩阵。

## 不提前做

- 在检索实验前实现完整 HNSW；
- 在协议未稳定前优化磁盘格式；
- 在 source fidelity 未解决前批量吸收重要资料；
- 在 Schema 健康测试前允许无约束自动迁移；
- 在公开标识的重名和可用性风险解决前发布公共包和命令名。

## 关联

- [TDD 开发总计划](./tdd-development-plan.md)
- [尚未确认的问题](./open-questions.md)
- [质量模型与验收](../product/quality-model.md)
- [市场空白与定位](../research/market-positioning.md)
