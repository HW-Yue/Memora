# 后续路线

状态：2026-08-02 当前方向；只描述下一阶段目标，不构成批量 Feature 实现授权。

## 近期：产品基线与真实可用性

1. 完成文档和旧代码收口，保证只有一个当前产品说明和一个 Feature 状态账本；
2. 在干净机器上重新验证安装、daemon、Skill/MCP、首次写入、查询、修订、Admin 和备份；
3. 补齐 F127 真实双宿主用户故事证据，不用脚本结果冒充 AI 质量；
4. 修复真实旅程暴露的协议、上下文和错误交互问题，再考虑扩大能力。

## 下一评测链：内置 Agent 与外置 Hook

静态 benchmark 使用 Memora 控制的内置评测 Agent。它读取冻结题目和 Database snapshot，
只能调用公开 MSQL，不读取 ground truth；Runner 评分逐层分支、候选 Recall@K、完整路径、
最终 RowID、事实读取、调用数、上下文和分段延迟。

Codex、Claude Code 等外置 Agent 不作为统一静态基准，只通过 Hook 上报发往 Memora 的调用，
按 host/session/model/Skill 条件分析真实表现。跨 session topic 身份另行设计。

优先顺序：

1. 冻结 observation/receipt 和 run/session/turn/trace 身份；
2. 实现隔离的 Provider driver 和内置评测 Agent；
3. 接入现有 Route Benchmark Runner 与本地报告；
4. 实现外置 Hook 和本地分析视图；
5. 最后再评测写入时机、write/ignore 和 Skill prompt 质量。

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

## 永久边界

- 不把完整文档、chunk、Row Embedding 或聊天转录变成权威事实索引；
- Agent 只做逻辑语义决策，所有正式操作经过公开 MSQL；
- Router/predictor 只导航，最终事实始终 SQL 回表；
- 大模型质量与引擎正确性分层测试，不用一方替代另一方；
- 未完成真实证据时明确标记，不把机械测试包装成产品完成。

## 关联

- [当前产品基线](../product/current-product.md)
- [Feature 状态](./feature-status.md)
- [评测 Agent 与 Hook](../development/evaluation-agent-observability.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
