# 执行计划

状态：2026-08-11 生效。**这是当前唯一的工作队列。**
战略层理由见[路线 v2](./roadmap-v2.md)，问题依据见[已知风险](../development/known-risks.md)。

每项都是可独立派发的工单：有前置、改动范围、RED 和完成判据。
**按编号顺序执行**，除非标注「可并行」。所有项仍须按
[TDD 协议](./feature-tdd-protocol.md)逐项 Review 与授权后实现。

## 已就地定下的决定

这些此前是悬空的，现按下述结论执行，不再讨论：

| 决定 | 结论 | 依据 |
| --- | --- | --- |
| F185b 死锁怎么解 | **Policy v2**：保留三 arm 矩阵与身份校验，替换度量与阈值。不整体退役 | [F222](./f222-release-gate-policy-v2.md) |
| 确定性指标阈值定多少 | **首轮不定**。A4 首轮走 `report` 模式产出分布，阈值由该分布冻结后才启用 `gate` | F222 |
| 语义重建不对称性选 A 还是 B | **先执行 B**（吸收 Agent 默认偏向多写），A（外部原文归档）列为第 12 项 | [讨论稿](../data/semantic-rebuild-asymmetry.md) |
| 工作集淘汰策略 | **v1 冻结为 LRU + Pinned 最后淘汰**。相关性淘汰作为第 10 项，由 A4 数据决定是否需要 | [F220](./f220-query-working-set.md) |
| A1 用朴素累积还是工作集 | **工作集**。朴素累积会在实现 F220 时整个作废 | 路线 v2 |

## 阶段 0：可用性缺陷（最高优先，插在所有阶段之前）

### 0. F226 Stage 1 收敛 poison 作用域 —— **已完成（2026-08-11）**

- **前置**：无。这是当前队列的真正队头
- **规格**：[F226](./f226-per-database-fault-isolation.md)（Stage 1 部分）
- **改动**：`internal/pagestoremigration/authority.go`
- **RED**：证明一个 Database 发布失败后，另一个 Database 的 SELECT 与 INSERT
  都返回 `ErrAuthorityPoisoned`
- **完成**：读不再因写发布失败而失效；`poisoned` 从全实例布尔改为受影响 Database
  集合；失败信封点明范围；`closed` 仍阻断全部；reopen 后集合正确重建
- **为什么最高优先**：现在改一个 Database 的字段出错会让**整个 Instance 读写全停**，
  这是可用性缺陷而非优化。Stage 1 改动集中在一个文件，先拿回可用性

## 阶段 A：让 Agent 真的能导航

出口判据：第 5 项产出三组可复现对照结论，且第 1、2 项修复前后的同题对照显示
导航深度实际变化。**在此之前不开新能力面。**

### 1. F221 Evidence 充分性与导航终止条件

- **前置**：无。这是队列头。
- **规格**：[F221](./f221-evidence-sufficiency.md)
- **改动**：`internal/agent/query_agent.go`、`query_agent_trace.go` 及对应测试
- **RED**：证明一条零行 SELECT 当前会终止导航并迫使模型作答
- **完成**：零行 SELECT 不终止导航；无 `substantive` 证据时拒绝作答而非编造；
  默认预算放宽到 8/6；F181 既有 golden 逐条更新而非放宽断言
- **为什么第一**：改动最小，且后面每一项都依赖「循环能走多跳」

### 2. F220 Query Working Set Stage 1

- **前置**：第 1 项。循环只能走一跳时没有第二跳可加速，命中率数字无意义
- **规格**：[F220](./f220-query-working-set.md)（Stage 1 部分）
- **改动**：`internal/agent/` 新增工作集类型与渲染；`query_agent.go` 替换
  `previousCall`/`previousResult`；Hook 指标扩展
- **RED**：证明当前循环无法跨两个以上 turn 保留任何 Row 或路径记忆
- **完成**：正向条目携带完整 Route 链路；保守失效（`Capture()` 水位线比对）；
  LRU + Pinned 最后淘汰；独立字节预算；紧凑表格渲染；命中率与节省指标进入
  F204/F207 链路；写入导致水位线前进后下一 turn 必须冷启动且**不得**返回过期 Row
- **不做**：负向记忆、相关性淘汰、精确失效、跨 Session（见第 10 项）

### 3. F219 确定性答案评分

- **前置**：无硬依赖，但排在第 2 项后，以便首轮评分测的是修好的循环
- **规格**：[F219](./f219-deterministic-answer-scoring.md)
- **改动**：`internal/answerevaluation/`（case 状态、`MetricScores` 可缺失、
  `judge_error_code`）；ground truth 扩展 evaluator-only 期望 RowID 与字段
- **RED**：证明当前 `validScores` 无法表达「四取三」，且无确定性主判定字段
- **完成**：主指标为 `route_hit`/`field_hit`/`retrieval_correct`，判定输入只能是
  真实 MSQL transcript；judge 指标逐个可缺失且缺失退出分母；
  **Agent 自述命中但 transcript 不支持时判为未命中**

### 4. F222 Release Gate Policy v2

- **前置**：第 3 项
- **规格**：[F222](./f222-release-gate-policy-v2.md)
- **改动**：`internal/answerrelease/`、`cmd/build-query-release-gate/`
- **完成**：`report`/`gate` 双模式；`report` 不输出 pass/fail 与默认 arm；
  `gate` 在阈值未冻结时拒绝运行；v1 输入 fail closed；已发布的 v1 报告不动

### 5. A4 三组小规模对照

- **前置**：第 1–4 项全部完成
- **依据**：[ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)
- **三组**（每组同模型、同语料、只变一个变量）：
  1. 三 arm 对照（atlas-only / +lexical / +prefetch）
  2. 强/弱模型建索引的能力梯度对照（查询侧固定同一模型）
  3. 工作集冷启动 vs 预热
- **完成**：`report` 模式报告发布；**产出物之一是冻结 `gate` 阈值并写回 F222**；
  结论不因换模型而反转的部分单独标注为架构结论

## 阶段 A′：写入正确性（可并行，不阻塞阶段 A）

### 5b. F224 Row 必须可导航

- **前置**：无。与阶段 A 互不依赖
- **规格**：[F224](./f224-mandatory-row-route.md)
- **改动**：`internal/nativerow`（执行点）、`internal/skillwrite/policy.go`
  （`validateSnapshot` 空数组漏洞）
- **RED**：证明不带 `route_leaf_ids` 的 INSERT 当前提交成功且 Row 零归属
- **完成**：live Row 必须 ≥1 个 Route 归属；UPDATE 的「nil = 保留既有」语义不变；
  DELETE 豁免；`msql.execute` 直连与 skillwrite 两条路径都被拦；存量无 Route Row
  不追溯清理，继续由 `semantichealth` 报告
- **为什么单列**：无 Route 的 Row 是静默数据丢失——写进去了但语义导航永远到不了，
  与查询侧的导航修复是两个独立故障域

### 5c. F225 Row 必须可展示

- **前置**：无。与 5b 同执行点，建议同批实现
- **规格**：[F225](./f225-mandatory-row-summary.md)
- **改动**：`internal/nativerow`（执行点，与 F224 同处）、`internal/semantichealth`
  （新增 `unsummarized_row`）；SKILL.md 强约束已先行落地（见下）
- **RED**：证明 summary 为空或缺列的 INSERT 当前提交成功
- **完成**：live Row 的 summary role 列必须存在且 trim 后非空；Table 无 summary 列时
  写入失败并指明加列出路；UPDATE 不触碰 summary 时保留既有值；DELETE 豁免；
  引擎只判定「非空」不判定质量
- **已完成的部分**：SKILL.md 四项强约束与 contract.json 示例已于本轮落地并重新生成
  adapters；剩余的是引擎侧强制

## 阶段 C：工程稳态（可并行，不阻塞阶段 A）

### 6. CI 增加 Linux runner

`.github/workflows/ci.yml` 的 `test` job 改为 matrix，加 `ubuntu-latest`。
完成判据：两个平台全绿。依据[已知风险](../development/known-risks.md)第 8 条。

### 7. 引入 golangci-lint

启用 staticcheck、errcheck、ineffassign 三项；`scripts/ci.sh` 增加 `lint` stage。
存量告警一次性修完或显式 `//nolint` 加理由，不留基线豁免文件。
依据风险 9。

### 8. 文档解析内存回归门

把「峰值堆 ≈ 正文 7 倍」写成回归测试（EPUB/DOCX/PDF 各一），
并把 `DefaultEPUBAdapterConfig.MaxTotalUncompressedBytes`、
`DefaultPDFAdapterConfig.MaxFileBytes`/`MaxDecompressedBytes` 下调到
与实测能力一致的量级；超限时清晰失败。依据风险 4、5。

### 9. 读路径与 Session 边界

- `ListCommittedChanges`、`GetCommittedChange` 摘除 `BeginWrite` 耦合，
  或加注释说明为何必须序列化（风险 6）；
- `msqlservice.OpenSession` 增加会话数上限（风险 7）；
- `treecontrol.EncodeBootstrap` 改为返回 error，使「生产代码零 panic」成为
  可断言不变量（风险 11）；
- `parser.go:28` 空分支改为直接调用或删除（风险 10）。

**注意**：第一条是第 10 项精确失效的硬前置。

## 阶段 B：把 AI-native 主张补实

前置：阶段 A 出口判据达成。**未达成前不开工。**

### 10. F220 Stage 2

负向记忆（探过为空的路径）、相关性淘汰（由第 5 项第 3 组数据决定是否需要）、
精确失效（前置：第 9 项第一条）。

### 11. 跨 Session topic 身份与有界恢复

Query Workspace 的跨会话持久化与恢复。需先出独立规格。

### 12. 原文可恢复性：候选 A

让 Memora 引用但不拥有外部原始资料归档，打通「用户重新提供原文 → 重新吸收 →
与现有 Row 做 diff」。收据已有 locator 与 content digest，缺的是重吸收与 diff 语义。
需先出独立规格。依据[语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)。

### 13. 写入反馈回路

检索失败与人工修正回流到建模决策，形成可观测信号。需先出独立规格。

### 14. Route 自治维护

~~初始 fan-out~~ 已由 [F223](./f223-route-branch-fanout-limit.md) 交付：`branch_fanout`
是 Database 级配置，越界硬失败并给出两条出路。**剩余部分**：超量时由 AI 自动提出拆分
与合并提案（而不是等待下一次写入失败），以及超限子树的批量重构。需先出独立规格。

## 阶段 D：不在当前路线

保留设施、不再投入，恢复条件见各自文档：**F226 Stage 2 物理文件按 Database 拆分**
（2026-08-20 已评估并延后——最热读路径本来就跨库，拆分会把 Catalog Atlas 与
`SHOW LEXICAL LOCATIONS FROM ALL TABLES` 变成常态 fan-out，而主导损坏模式是共享
引擎代码缺陷，拆文件对它零作用）、大语料批量评测（F212–F215、
候选 F216–F218）、OCR/视觉运行时（候选 F209）、内置 `memora ask` 产品化、
Compaction／Secondary Index／Advanced MVCC／Replication／PITR／多设备同步／
Apple Accelerate／HNSW。

## 立即生效的策略变更（无需工单）

**吸收 Agent 的 worthiness 默认偏向多写。** 理由：过度抽取可恢复（删 Row），
抽取不足不可恢复（原文在 Job 释放后回收）。这个不对称是严重的，
在第 12 项完成前，默认必须偏向多写。写入
[资料吸收](../data/assimilation.md)与吸收 Agent 的 prompt 约束。

## 关联

- [路线 v2](./roadmap-v2.md) — 为什么是这个顺序
- [已知风险](../development/known-risks.md) — 每项工单对应的问题依据
- [当前系统能力](../product/system-capabilities.md) — 不要重复实现已有能力
- [TDD 协议](./feature-tdd-protocol.md) — 每项实现前的授权与验收规则
