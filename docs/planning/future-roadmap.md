# 后续路线

状态：2026-08-05 当前方向；只描述下一阶段目标，不构成批量 Feature 实现授权。

F169 之后的逐项依赖、候选编号和完成门见
[F169 之后的开发序列](./post-f169-development-plan.md)。本文继续维护阶段目标，不重复展开 Feature。
F204 之后的执行顺序见[后续开发计划](./post-f204-development-plan.md)，其中候选 F205–F211 均需独立 Review。

## 已完成的基础收口

- 已建立唯一的当前产品说明、Feature 状态账本和后续路线；
- 旧计划与被取代规格已移入归档，不再和当前设计并列；
- F164–F167 已删除旧 benchmark/runtime gate、硬编码 Skill Query、`MATCH` 残留和
  Row Agent-index generation，保留项均有迁移、兼容、对拍或当前测试职责。

## 近期：真实可用性

1. 在干净机器上重新验证安装、daemon、Skill/MCP、首次写入、查询、修订、Admin 和备份；
2. 补齐 F127 真实双宿主用户故事证据，不用脚本结果冒充 AI 质量；
3. 修复真实旅程暴露的协议、上下文和错误交互问题，再考虑扩大能力。

## 下一评测链：内置 Agent 与外置 Hook

静态 benchmark 使用 Memora 控制的内置评测 Agent。它读取冻结题目和 Database snapshot，
只能调用公开 MSQL，不读取 ground truth；Runner 评分逐层分支、候选 Recall@K、完整路径、
最终 RowID、事实读取、调用数、上下文和分段延迟。

Codex、Claude Code 等外置 Agent 不作为统一静态基准，只通过 Hook 上报发往 Memora 的调用，
按 host/session/model/Skill 条件分析真实表现。跨 session topic 身份另行设计。

优先顺序：

1. 中立 MSQL 协议、共享执行服务、依赖守卫和 Bootstrap Frame 已完成；
2. Provider port、run/session/turn/trace 与真实 Kimi OpenAI-compatible adapter 已完成；
3. 只读 Query Agent、冻结 corpus、clean Instance runner 和隔离 Ragas 外部评分已完成；
4. 固定检索 arms 与 F185b release gate 已实现；当前真实 Provider 矩阵为 INCOMPLETE，真实大批量
   质量复跑已由用户决定延期，0 样本报告继续保留且不得冒充质量通过；
5. 实验性交互 QuerySession、受控 L1 Write Gateway、单网页写入链和冻结 EPUB 单 claim 全链验收
   已完成；真实多 claim 前仍需补当前 native backend 的多 statement 原子提交；
6. DOCX、文本层 PDF、OCR 证据门和外置 Hook（F201–F204）已独立完成；统一分析平台仍后置；质量门通过前不宣称
   `memora ask` 已达到发布质量，也不选默认 arm；Trace 先输出开发用报告，不扩展 Admin。

API Key 只进入操作系统密钥存储或进程环境，不进入 Database、日志、报告或导出。评测 Agent
不能拥有绕过 Parser、Policy、预算和事务的内部接口。

## 后期产品候选

- 将验证后的评测 Agent Runtime 复用为可选 `memora ask`；
- Query Workspace 的生命周期、跨会话 checkpoint 和权限绑定；
- 语义树自动优化的长期反馈闭环；
- Wiki 显式回流、多设备同步和冲突语义；
- 商业包分发、公开身份和许可证流程。

## 只由证据触发的内核能力

Compaction、Secondary Index、Buffer Pool 分片、高级 I/O、Physical Undo、Advanced MVCC、
锁等待/死锁、Replication、PITR、多设备同步、Accelerate 与 HNSW 保持延后。只有真实 workload
越过已冻结资源或产品门，才创建对应 Feature；不以数据库功能清单为理由提前实现。

全内容倒排索引不再属于本节的证据候选。ADR-0008 已确认需求，按 F170–F174 依次交付
reference model、持久化 posting、Row/Catalog/Route 原子维护和有界 MSQL location 查询。

## 永久边界

- 不把完整文档、chunk、Row Embedding 或聊天转录变成权威事实索引；当前 live Row 与
  语义索引允许建立可重建 lexical postings，但命中只返回位置；
- Agent 只做逻辑语义决策，所有正式操作经过公开 MSQL；
- Router/predictor 只导航，最终事实始终 SQL 回表；
- 大模型质量与引擎正确性分层测试，不用一方替代另一方；
- 未完成真实证据时明确标记，不把机械测试包装成产品完成。

## 关联

- [当前产品基线](../product/current-product.md)
- [F169 之后的开发序列](./post-f169-development-plan.md)
- [查询 Agent Feature 序列](./query-agent-feature-sequence.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
- [Feature 状态](./feature-status.md)
- [评测 Agent 与 Hook](../development/evaluation-agent-observability.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
