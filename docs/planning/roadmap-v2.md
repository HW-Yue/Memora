# 路线 v2：AI-native 差距与后续计划

状态：2026-08-11 建立。取代散落在 `post-f169-development-plan.md`、
`post-f204-development-plan.md` 和 Feature 状态「新候选」一节的候选清单。

前置阅读：[系统能力](../product/system-capabilities.md)（现在有什么）、
[已知风险](../development/known-risks.md)（哪里有问题）。

> **要派发工作请用[执行计划](./execution-plan.md)。** 本文档说明为什么是这个顺序；
> 执行计划是编号工作队列，每项带前置、改动范围、RED 和完成判据。

## 为什么要重排

到 F215 为止累计两百多个 Feature 编号，账本按**时间顺序**记录，没有按**主题**收敛。
结果是：判断「系统现在能做什么」必须读四十多个 F 文档，而「还差什么」散在三份计划里，
彼此还有冲突。

新结构分四层，各自单一职责：

| 文档 | 回答 | 更新时机 |
| --- | --- | --- |
| [系统能力](../product/system-capabilities.md) | 现在是什么 | 能力交付或移除时 |
| [已知风险](../development/known-risks.md) | 哪里有问题 | 发现或修复问题时 |
| 本文档 | 接下来做什么、**为什么是这个顺序** | 阶段完成或方向变化时 |
| [执行计划](./execution-plan.md) | **派发什么工单**、前置与完成判据 | 每项工单完成时 |
| [Feature 状态](./feature-status.md) | 某能力的历史证据 | 仅追加，不再作为导航入口 |

F 编号继续用于单项开发的 TDD 与授权，但**不再是理解系统的入口**。

## 距离「真正的 AI-native」还差什么

产品宪章说：AI 决定语义结构，引擎保证物理正确性。引擎那半边已经达标。
AI 那半边有五个真实差距，按依赖顺序排列。

### 差距 1：Agent 不会导航（结构缺陷，非能力不足）

见[已知风险](../development/known-risks.md)第 1、2 条。当前循环只有一步记忆，
且第一条 SELECT（哪怕零行）就终止导航。一个「AI-native 数据库」的 AI 无法完成多跳导航，
它就只是一个长得像 AI 接口的数据库。

**这是所有其他 AI-native 工作的前置项。** 在它修好之前，任何检索质量数字都在测量
一个被人为致残的循环，不是在测量架构。

### 差距 2：没有跨轮与跨会话的记忆身份

`QuerySession` 每个 turn 都是独立的 `QueryAgent.query()` 调用，各自重新 bootstrap，
不携带上一轮的问答。所以追问（「那原因呢？」）无法工作。

「个人数据库」隐含 AI 随时间了解你；当前每个问题都从零开始。
文档已承认「Query Workspace 的跨会话恢复、跨 session topic 身份仍未冻结」。

### 差距 3：写入决策没有反馈回路，且不可逆

AI 判断「什么值得写」，写完之后没有任何信号告诉它写得好不好。检索失败不会回流到建模。
更严重的是原文在 Job 释放后回收，语义分解不可重建——最依赖模型能力的那层恰好不可重建。
见[语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)。

真正 AI-native 的系统会随时间修正自己的 schema；当前每次吸收都是一次性的。

### 差距 4：Route 结构建了但不自治维护

AI 建 Route，但数据增长后没有重组机制。今天 15 个 Leaf 可以工作，
10k Row 时的 fan-out 行为没有定义。候选 F211 一直未实现。

### 差距 5：AI 是自带的

`memora ask` 未发布，模型依赖外部宿主。这是 Skill-first 的**有意选择**，不是缺陷，
但要清楚：当前「AI-native」的 AI 由用户提供，产品本身不保证 AI 质量。

## 分阶段计划

阶段之间是硬依赖，不要并行。每阶段末尾有明确的出口判据。

### 阶段 A：让 Agent 真的能导航（当前唯一在办阶段）

**顺序是硬依赖**：A2 最小、必须最先；A1 依赖 A2（循环只能走一跳时，没有第二跳可加速，
工作集命中率数字没有意义）。

| 项 | 内容 | 依据 |
| --- | --- | --- |
| A2 | 证据判定引入行数与充分性条件，零行 SELECT 不再终止导航；`ToolChoiceNone` 改由模型显式声明完成或预算耗尽触发。改动小，先做 | 风险 2 |
| A1 | [F220](./f220-query-working-set.md) Query Working Set Stage 1：Row + 完整 Route 链路的有界工作集，替换单步 `previousCall/previousResult`；保守失效、LRU + 独立预算、紧凑渲染、命中率指标 | 风险 1 |
| A3 | [F219](./f219-deterministic-answer-scoring.md) 确定性主评分与部分指标表示 | ADR-0010 |
| A4 | 按 [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md) 跑三组小规模对照：三 arm 同模型对照；强/弱模型建索引的能力梯度对照；工作集冷启动 vs 预热 | ADR-0010、F220 |

A1 采用 [F220](./f220-query-working-set.md) 的语义工作集，而不是朴素地累积原始 transcript。
两者都能修复多轮记忆缺陷，但工作集额外提供去重、横向导航和负向记忆，且不是会被丢弃的
中间产物——它就是 F220 的 Stage 1。朴素累积则会在实现工作集时整个作废。

**出口判据**：A4 三组对照给出可复现结论，且 A1/A2 修复前后的同题对照显示导航深度实际变化。
在此之前不开新能力。逐项工单、改动范围与完成判据见[执行计划](./execution-plan.md)第 1–5 项。

### 阶段 B：把 AI-native 主张补实

依赖阶段 A 出口。

- B1 [F220](./f220-query-working-set.md) Stage 2 与跨会话：负向记忆、相关性淘汰、
  （若指标支持）精确失效，以及跨 Session topic 身份与有界恢复（差距 2）。
  精确失效前必须先修[已知风险](../development/known-risks.md)第 6 条；
- B2 写入反馈回路：检索失败与人工修正回流到建模决策，形成可观测信号（差距 3）；
- B3 原文可恢复性：候选 B（worthiness 默认偏向多写）已于 2026-08-11 立即生效，
  候选 A（外部原文归档 + 重吸收 diff）为[执行计划](./execution-plan.md)第 12 项（差距 3）；
- B4 Route 自治维护：初始 fan-out、超量拆分、合并，由 AI 判断并留 revision/理由（差距 4）。

**出口判据**：一个真实用户的真实资料，连续使用两周以上，追问可用、Route 未退化。

### 阶段 C：工程稳态（可与 A/B 并行，成本低）

- C1 CI 加 `ubuntu-latest`（风险 8）；
- C2 引入 `golangci-lint`，至少 staticcheck／errcheck／ineffassign（风险 9）；
- C3 文档解析加内存实测门：把峰值堆／正文倍率写成回归测试，并把配置上界改到
  与实测能力一致（风险 4）；
- C4 大文件解析流式化，或明确降低上界并在超限时清晰失败（风险 5）；
- C5 读路径摘除写门耦合或补注释（风险 6）；Session 加上界（风险 7）；
- C6 拆分超大文件、清理占位分支、消除唯一 panic（风险 10、11）。

C1、C2 与阶段 A 并行执行，不等待任何前置；两项加起来不到一小时。

### 阶段 D：已延后，不在当前路线

均已执行证据门或有明确延后理由，**保留设施、不再投入**：

- 大语料批量评测：F212–F215 与候选 F216–F218（[ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)）；
- OCR/视觉运行时：候选 F209，等 F203 返回 `eligible`；
- 内置 `memora ask` 产品化：等阶段 A、B 出口；
- Compaction、Secondary Index、Advanced MVCC、Replication、PITR、多设备同步、
  Apple Accelerate、HNSW：见 [Feature 状态](./feature-status.md)的证据门表格。

## 明确不做

- 不在阶段 A 出口前开新能力面；
- 不以扩大评测样本量替代修复导航缺陷；
- 不因为 F 编号显示「已完成」就认为对应能力已验证；
- 不把 OCR 引擎、本地 embedding 权重或浏览器运行时并入主安装包。

## 关联

- [系统能力](../product/system-capabilities.md)
- [已知风险](../development/known-risks.md)
- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
- [AI-native 产品宪章](../product/ai-native-product-charter.md)
