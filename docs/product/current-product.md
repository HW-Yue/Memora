# 当前产品基线

状态：2026-08-02 当前权威产品快照；详细实现状态见
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
- Lexical Route location、Route-only Vector generation 和 CPU 精确 Top K；
- 有界投机预取，预测失败时回到确定性逐层导航；
- 带 revision 的精确读取、Mutation Plan、关系、历史与逻辑补偿；
- Route/Schema 变更计划、审批后原子执行和结构健康扫描；
- Host Input capture 与显式 worthiness decision receipt；
- 文档/仓库资料的临时吸收、覆盖清单、独立复核和 Source Receipt。

## 已实现的存储底座

- 16 KiB Page、Page File、WAL、durable frontier、checkpoint 与 crash recovery；
- 单 Instance Buffer Pool、WAL-before-data、young/old 淘汰；
- 持久化 B+ Tree 的 point/range、split、delete、rebalance 和多层原子提交；
- Catalog、当前 Row、Row Version 三类权威索引及 Table Row cursor；
- statement snapshot、精确对象写锁、Change Log 和 COW generation replacement；
- free Page reuse；旧 SQLite 只保留为显式迁移工具，不是运行时 fallback。

## 当前还不是完整产品的部分

- F127 已有真实宿主证据协议，但真实双宿主 AI 用户故事报告仍为 `INCOMPLETE`；
- 外置 Agent Hook、统一 session 指标和本地分析平台尚未实现；
- 内置评测 Agent 尚未实现，面向用户的 `memora ask` 继续延后；
- “何时值得写入”的质量评测后置，近期先评测查询、Route 和事实读取；
- ADR-0008 的 reference model 与持久化 posting store 已完成，但尚未接入生产 Row/Catalog/Route
  发布和 RowID location MSQL；当前用户入口仍只有 F124b 的 Route lexical 候选；
- Query Workspace 的跨会话恢复、跨 session topic 身份仍未冻结；
- Compaction、Secondary Index、Advanced MVCC、Replication、PITR、多设备同步、
  Apple Accelerate 与 HNSW 均未达到证据门，当前不实现。

## 当前真实性边界

代码和机械测试已经形成完整数据库原型；这不等同于真实长期 AI 使用质量已经达标。
当前最重要的下一证据是：用标准内置评测 Agent 重放冻结任务，并用外置 Hook 观察真实宿主
环境，验证逐层分支、最终 RowID、上下文、调用数和端到端延迟。

## 关联

- [AI-native 产品宪章](./ai-native-product-charter.md)
- [Feature 状态](../planning/feature-status.md)
- [后续路线](../planning/future-roadmap.md)
- [MSQL](../query/msql.md)
- [语义 Router](../query/semantic-routing.md)
