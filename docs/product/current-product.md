# 当前产品基线

状态：2026-08-05 当前权威产品快照；详细实现状态见
[Feature 状态](../planning/feature-status.md)。

## 产品形态

Memora 是面向 AI 的本地个人语义数据库。Codex、Claude Code 等宿主通过 Canonical Skill
和唯一 MCP/MSQL 入口读写；AI 负责语义判断和逻辑建模，引擎负责权限、类型、事务、历史、
索引、恢复与物理正确性。

权威数据是自描述 Database 中可独立修改的语义 Row，不是聊天记录、文档 chunk 或
Embedding。每个 Table 有多层语义 Route；每个 Leaf 最多定位一个活跃 Row，同一 Row
可以属于多个 Leaf。AI 逐层导航到唯一 RowID，再用 SQL 回表读取事实。
Catalog、字面位置和 Route-only Vector 只能预测导航候选，不能成为事实来源。

## 已实现的使用入口

- 本地 daemon、Unix socket、CLI、长连接 Go SDK；
- Canonical Skill 及 Codex/Claude Code adapter；
- 现代与兼容版 MCP stdio，唯一工具为 `memora_execute`；
- MSQL Catalog、Row、History、Relation、Route、事务与管理语句；
- 本地只读 Admin，浏览 Catalog、Route、Row、History、Change 和 Route Trace；
- Database Package 的签名、安装、升级、撤销、fork 与三方 merge；
- Instance 备份、恢复、搬迁、格式升级、诊断和 macOS LaunchAgent；
- macOS arm64/amd64 签名制品及受门禁约束的 GitHub Release 流程。

## 已实现的语义链路

- AI 自描述建库、建表和字段建模；
- 有界 Catalog Atlas 与 Table 级逐层 Route 导航；
- Route-only Lexical/Vector 候选，以及全内容、权限先行、有界 cursor 的 Lexical Locations；
- 有界投机预取，预测失败时回到确定性逐层导航；
- 带 revision 的精确读取、Mutation Plan、关系、历史与逻辑补偿；
- Route/Schema 变更计划、审批后原子执行和结构健康扫描；
- 标准库 OpenAI-compatible Provider adapter；密钥按调用解析，真实 Kimi 中国站 tool-call smoke 已通过；
- MSQL-only 只读 Query Agent、12 题冻结 corpus、clean Instance answer runner，以及把实际 SQL Row
  映射给隔离 Ragas v0.4.3 evaluator 的外部质量报告链路；
- Host Input capture 与显式 worthiness decision receipt；
- 文档/仓库资料的临时吸收、EPUB 结构解析、覆盖调度、独立复核、正式 MSQL 短事务和包含实际
  Row/Relation ID、revision、commit sequence 的 Source Receipt；

## 已实现的存储底座

- 16 KiB Page、Page File、WAL、durable frontier、checkpoint 与 crash recovery；
- 单 Instance Buffer Pool、WAL-before-data、young/old 淘汰；
- 持久化 B+ Tree 的 point/range、split、delete、rebalance 和多层原子提交；
- Catalog、当前 Row、Row Version 三类权威索引，Catalog/Route/Row Fulltext 派生树及 Table Row cursor；
- statement snapshot、精确对象写锁、Change Log 和 COW generation replacement；
- free Page reuse；旧 SQLite 只保留为显式迁移工具，不是运行时 fallback。

## 当前还不是完整产品的部分

- F127 已有真实宿主证据协议，但真实双宿主 AI 用户故事报告仍为 `INCOMPLETE`；
- 外置 Agent Hook、统一 session 指标和本地分析平台尚未实现；
- F182–F185b 已完成冻结语料、端到端 Runner、外部评分、三个真实执行检索 arm 和 release gate，
  但真实 Kimi 三 arm 共 36 题均因 wire/429 上游错误而不可评分；gate 状态保持 INCOMPLETE；
- 用户已决定延期大批量真实质量复跑，允许继续开发实验性 QuerySession；这不等于答案质量通过，
  面向用户的正式 `memora ask` 发布承诺和默认 arm 选择仍然延后；
- “何时值得写入”的质量评测后置；查询、Route 和事实读取的真实大批量质量复跑同样延期，
  现有 runner/gate 保留供后续恢复；
- 全内容倒排位置、统一 MSQL Service、QuerySession、短网页写入和 EPUB 吸收的 F189–F199
  组件已完成；下一步只跑一条冻结 EPUB 干净全链路，不恢复批量模型评分；
- Query Workspace 的跨会话恢复、跨 session topic 身份仍未冻结；
- Compaction、Secondary Index、Advanced MVCC、Replication、PITR、多设备同步、
  Apple Accelerate 与 HNSW 均未达到证据门，当前不实现。

## 当前真实性边界

代码和机械测试已经形成完整数据库原型；这不等同于真实长期 AI 使用质量已经达标。
真实 Query 质量证据仍缺失，但单网页与整本资料吸收所需的独立组件已具备。后续仍需在无 Provider
限流/协议错误的固定运行上补齐三 arm 外部质量分，
让既有 release gate 选出默认 arm；外置 Hook 再观察真实宿主环境中的分支、上下文、调用和延迟。

## 关联

- [AI-native 产品宪章](./ai-native-product-charter.md)
- [Feature 状态](../planning/feature-status.md)
- [后续路线](../planning/future-roadmap.md)
- [MSQL](../query/msql.md)
- [语义 Router](../query/semantic-routing.md)
