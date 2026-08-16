# ADR-0010：小规模高质量评测优先，大语料批量评测延期

状态：Accepted，2026-08-11。取代 F185b 之后「恢复批量真实质量复跑」的默认路线；
F212–F215 与候选 F216–F218 转为 Deferred。

## 背景

F182–F185b 建成了冻结 corpus、端到端 Runner、外部评分器、三个可执行 arm 和 release gate；
F212–F215 又补齐了公开大语料准备、MIRACL/MTRAG 归一化、确定性 retrieval scorer 和低并发退避。
设施齐全，但至今没有产出一个可用的质量结论：

- F184 真实 Kimi 运行 12 题全部 `runner_failed`；
- F185b 三 arm 共 36 题因 33 次 HTTP 429 与 3 次 wire failure 全部不可评分；
- F215 DeepSeek V4 Flash 续跑后 9/12 成功，其中 3 题得分、6 题 `evaluator_failed`。

## 两个驱动这次改向的判断

### 绝对质量分不耐久，架构对照分才耐久

用某个具体模型在某个具体时间点跑出的 Recall@K 或答案分，随模型换代立即失效；为了抬高这个数字
而在引擎里堆补偿弱模型的启发式规则，会在模型变强后变成必须清理的负债。

但 F185a 的三个 arm 是**同模型、同语料、只变架构**的对照，模型能力在差值里被约掉。
arm 之间的 delta 回答的是「分层语义路由值不值它的复杂度」，这是架构结论，不随模型进步失效。
同理，模型强度梯度（强/弱模型各建一次索引，用同一固定模型查询）把「语义索引依赖模型能力」
从判断变成可测量，并给出能力下限。

因此评测的目标从「取得质量分」改为「取得对照证据」。对照证据在小样本下即可成立，
不需要大语料。

### 现行评分是全有全无的，小样本下它是主要噪声源

`internal/answerevaluation/output.go` 的 `validScores` 要求 `scored` 状态下
`factual_correctness`、`faithfulness`、`context_precision`、`context_recall` 四个指标全部非空，
`evaluator_failed` 要求四个全部为空；类型层面不存在「四取三」的表示。任何单个指标的
judge 调用返回 NaN 或撞上 429，整题的另外三个分数一并丢弃。

F215 已加入单指标有限重试，仍是 6/9 失败，说明卡点不是重试次数而是这条规则本身。
大样本下这种损失会被平均掉；N=12 时丢一题就是丢掉 8% 的数据集，且丢失原因与 Memora 质量无关。

## 决策

1. **当前评测路线为小规模、高质量、可复现的对照实验**，不再以扩大样本量为方向。
   样本规模服从「每题结果都能被人工核对」的约束。

2. **主指标改为确定性检索命中判定**，不依赖模型：每题冻结正确 RowID 与关键事实，
   判定导航是否到达正确 Row、SQL 回表是否取到正确字段。完全可复现、无 flaky、无 judge 成本。

3. **LLM judge 降级为次要参考**，只用于答案措辞质量。judge 失败不得影响主结论，
   也不得使整题作废；评分表示必须允许部分指标缺失。

4. **F212–F215 标记为 Deferred，冻结保留**。代码、数据准备器、suite 适配器和 scorer 都不删除、
   不再继续投入。恢复条件见下节。候选 F216–F218（公开语料 → Agent 吸收 → retrieval run 桥接层）
   同步 Deferred。

5. **F185b release gate 维持有效**，继续阻止「质量已通过」和「默认 arm 已选定」的对外声明。
   小规模对照实验不自动满足该门。

   2026-08-11 补充：Policy v1 要求四项 judge 指标 100% 有效，与本 ADR 第 3 条
   「允许指标部分缺失」直接矛盾——照原样保留会使该门在新体系下永远输出 `incomplete`，
   即一个死锁而非一个标准。已由 [F222](../planning/f222-release-gate-policy-v2.md) 解除：
   保留三 arm 矩阵与身份校验，替换度量与阈值；阈值不预先编造，由首轮 `report` 模式的
   实际分布冻结后才启用 `gate`。

## 恢复条件

同时满足才重新评估大语料批量评测：

1. 小规模对照已给出稳定、可复现的 arm 间架构结论，且结论不因换模型而反转；
2. 确定性主指标链路连续多次运行零 harness 失败；
3. 出现小样本无法回答的问题，且该问题确实阻塞产品决策。

「Deferred」不是失败或漏做，与 F151–F163 的处理一致：交付物是可复现的设施、执行过的证据门
和当前不继续投入的结论。

## 结果

- 新增候选 [F219](../planning/f219-deterministic-answer-scoring.md)：确定性主评分与部分指标评分表示；
  它是恢复任何评测运行前的前置项。
- F212–F215 文档与 [Feature 状态](../planning/feature-status.md)同步标记 Deferred。
- 不因为改向小规模就放宽结论口径：没有通过的质量门仍写 `INCOMPLETE`，不得从对照实验外推出
  Recall/MRR 或答案质量承诺。

## 关联

- [F185b Query Agent Release Gate](../planning/f185b-query-release-gate.md)
- [F219 确定性答案评分](../planning/f219-deterministic-answer-scoring.md)
- [F204 之后的开发计划](../planning/post-f204-development-plan.md)
- [质量模型](../product/quality-model.md)
