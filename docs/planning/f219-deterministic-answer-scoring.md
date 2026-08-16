# F219：确定性答案评分与部分指标表示

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
依据 [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)。

## 唯一主要结果

答案评测的主结论由不依赖模型的确定性判定产生：给定冻结的每题期望 RowID 与期望字段，
评测器判断 Query Agent 的真实 MSQL evidence 是否到达正确 Row、是否回表取到正确字段，
并输出可复算的命中报告。LLM judge 分数降级为可选附加信号，其缺失或失败不改变主结论、
不使整题作废。

一个主要结果，不含恢复批量运行、不含新增 arm、不含 Provider 变更。

## 当前缺陷

`internal/answerevaluation/output.go` 的 `validScores` 对 `scored` 要求四个 judge 指标全部非空、
对 `evaluator_failed` 要求四个全部为空，不存在部分成功的表示。任一指标 NaN 或 429 会丢弃整题
的全部分数。F215 的单指标有限重试没有解除该约束，真实运行仍为 9 题中 3 题得分、6 题作废。

小样本对照实验无法承受这种与 Memora 质量无关的数据损失。

## 协议边界

- 每题 ground truth 扩展 evaluator-only 的期望 RowID 集合与期望字段 locator；
  ground truth 仍不进入 Agent，仍只由 evaluator 读取；
- 主指标为确定性布尔/计数：`route_hit`（导航到达期望 Row）、`field_hit`（SQL 回表取到期望字段）、
  以及二者派生的逐题 `retrieval_correct`；判定输入只能是 Agent 真实产生的 MSQL transcript 与
  返回的 RowID/revision，不能是模型自述；
- case 状态从三态扩展为：主判定成功且 judge 完整、主判定成功且 judge 部分缺失、主判定成功且
  judge 全缺、`runner_failed`。judge 侧失败单独记 `judge_error_code`，不再吞掉主判定；
- `MetricScores` 允许逐指标缺失，缺失指标在聚合时退出分母而不是使整题退出分母；
  报告必须分别公布每个 judge 指标的实际样本数；
- 报告继续绑定 corpus revision、snapshot、ground truth 与 evaluator 输入输出的 SHA-256；
- 公私分离不变：确定性判定结果可进公开 scorecard，期望 RowID/字段本身不进公开报告。

## 明确不做

- 不在本 Feature 恢复任何批量真实模型运行；
- 不放宽 F185b release gate 的阈值，也不用确定性命中率替代该门的质量结论；
- 不引入新的 judge Provider，不修改 Ragas adapter 的隔离边界；
- 不把确定性期望值反馈给 Agent 或写入 Memora Database。

## RED 与完成门

- RED 先证明部分指标评分、确定性命中判定与 `judge_error_code` 在当前类型下无法表示；
- 四取三、四取一、四全缺三种 judge 结果均能产出主判定成立的有效报告，且聚合分母正确；
- judge 全部失败时报告仍给出完整确定性主结论，`counts` 与每指标 samples 自洽；
- 期望 RowID 命中、未命中、导航到达但回表字段错误、导航未到达四类情形逐一有 golden；
- Agent 自述命中但 MSQL transcript 不支持时判定为未命中（防止模型自证）；
- 旧版无确定性字段的 ground truth 与旧报告仍可读，版本迁移 fail closed 而非静默降级；
- 报告 hash 稳定、逐字 golden、`-race` 与完整 CI 全绿。

## 依赖与后续

- 依赖 F182 corpus / F183 runner / F184 evaluator 的现有结构，不改其公私分离契约；
- 完成后才可执行 ADR-0010 的两项小规模对照：三 arm 同模型对照，以及强/弱模型建索引的
  能力梯度对照；两项均需独立 Review 与授权。

## 关联

- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
- [F184 外部答案质量评测](./f184-external-answer-evaluation.md)
- [F185b Query Agent Release Gate](./f185b-query-release-gate.md)
