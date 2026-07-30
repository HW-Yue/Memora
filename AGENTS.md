# Memora 协作规则

## 设计文档同步

- 本项目处于长期架构设计和实现阶段。
- 对话形成新的稳定判断、修改既有方向或明确产品边界后，主动更新 `docs/` 中的对应文档，不等待用户再次提醒。
- 不把仍在发散讨论的想法伪装成最终决定；使用“讨论稿”“候选”“待验证”或“方向性结论”标记成熟度。
- 新结论与旧文档冲突时，修订旧内容或显著标记其已被取代，并链接到当前设计。
- 新增文档后更新 `docs/README.md`。
- 开始实现前，把已验证的方向性结论提升为独立规格、ADR 或实现计划。
- 讨论优先，不在每轮对话后向用户展示长篇文档工作；只在结论稳定后后台同步最小必要内容。
- 一个主题一个小文件，目标不超过约 150 行；需要扩展时拆分并用链接关联，不继续堆积综合长文档。
- `docs/archive/` 只用于追溯历史，日常任务不要整篇读取归档文档。
- 回答当前问题前，从 `docs/README.md` 选择最少的相关主题文件，不批量读取整个 `docs/`。

## 当前产品原则

- Memora 是由 AI 自主建模、通过标准化语言读写的个人数据库。
- Agent 只表达逻辑数据库操作；Page、索引、MVCC、Undo Log、Redo Log 和恢复由引擎自动完成。
- 实际数据读取和修改通过 MSQL/SQL；语义 Router 只负责发现和导航。
- 不将完整大文档、PDF、图片或机械 chunk 作为 Memora 的持久化内容；AI 吸收外部资料后写入完整、可修改的语义模块。
- 动态数据库索引不写入长期 system prompt；模型上下文只保留紧凑的当前 Route Frame。
- Markdown/Obsidian Wiki 是数据库快照的确定性导出，不是第一阶段的真相源。

## Feature 与 TDD

- 一个 Feature 只允许一个主要结果；出现两个独立故障域、协议或验收旅程时，开工前拆分。
- 所有 Feature 默认逐项 Review、授权、实现、验收和合入；Milestone 不构成整批实现授权。
- 严格执行 RED → GREEN → REFACTOR：先确认测试因缺少目标能力失败，再写最小实现。
- Page/WAL/B+ Tree/MVCC 等内核 Feature 必须有 corruption、reopen、fault injection、reference model 或 race 证据，不能只测 happy path。
- 故意失败的测试不进入 `main`；每项完成时必须独立全绿、可构建、可回滚。
- 详细规则见 `docs/planning/feature-tdd-protocol.md`。
