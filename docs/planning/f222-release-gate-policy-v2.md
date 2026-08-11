# F222：Release Gate Policy v2（确定性指标）

状态：候选，2026-08-11 提出；尚未 Review、尚未获得实现授权。
解除 [F185b](./f185b-query-release-gate.md) Policy v1 与
[ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md) 之间的死锁。

## 要解决的死锁

F185b Policy v1 要求「每个 arm 至少 12 题，且 runner、evaluator 和**四项 metric 都必须
100% 有效**，否则整个矩阵状态为 `incomplete`」，四个阈值全部定义在 LLM judge 指标上。

ADR-0010 把 judge 降级为次要参考并允许指标部分缺失。两者直接矛盾：
在新测量体系下，judge 部分缺失是被接受的常态，而 Policy v1 见到任何指标缺失就判
`incomplete`。**结果是 F185b 永远不可能通过——它不是一个标准，是一个死锁。**

## 决策

**保留** F185b 的身份校验、三 arm 矩阵配对与默认 arm 选择机制（这正是 ADR-0010 要的
架构对照）；**替换** Policy v1 的度量与阈值。三个选项中选定「Policy v2」方案，
不整体退役 F185b。

## 唯一主要结果

新增 `memora.query-release-gate/v2`：主判定建立在 [F219](./f219-deterministic-answer-scoring.md)
的确定性检索命中指标上，judge 指标转为可选记录且缺失不影响矩阵完整性；
报告支持 `report` 与 `gate` 两种模式。

## Policy v2

### 矩阵完整性

- 仍要求恰好三个 arm、同身份（corpus、snapshot、Provider/model、prompt、code revision、
  ground-truth SHA 一致，run ID 与 source hash 互不相同）；
- 完整性判据改为：**每个 arm 的每道题都有确定性主判定结果**
  （`retrieval_correct` 为 true/false，不得缺失）。
  judge 指标缺失**不**影响完整性；
- runner 失败仍使该题不可判定，仍导致 `incomplete`。

### 两种模式

| 模式 | 用途 | 行为 |
| --- | --- | --- |
| `report` | A4 首轮 | 不判 pass/fail，只输出三 arm 的确定性指标分布、逐题结果与对照差值 |
| `gate` | 首轮之后 | 按已冻结阈值判定，并选择默认 arm |

**阈值不预先编造。** `gate` 模式的阈值由 A4 首轮 `report` 的实际分布冻结，
冻结动作是一次显式的 Review，写入本文档后才允许启用 `gate`。
这避免了「拍一个数字然后让系统去凑」。

### 默认 arm 选择

沿用 v1 的成本优先规则，但排序键改为：先 `retrieval_correct` 率，
再总 input tokens、Provider calls、端到端 p95、固定 arm 顺序。
judge 分数不参与选择。

### 公开报告

`memora.query-release-gate/v2` 包含：policy 版本与模式、冻结身份、六个 source hash、
每 arm 的确定性指标与逐题结果、judge 指标的实际样本数与均值（可为空）、
稳定 reason code、总体状态、默认 arm（`gate` 模式下）。
不含问题、答案、reference、SQL evidence、Trace 或私有错误。

### v1 报告

已发布的 v1 报告（`f185b-kimi-real-20260804-release.json`，状态 `incomplete`）
保持不变、不重算、不改写。v2 不向后兼容 v1 输入，明确拒绝而非降级。

## 明确不做

- 不整体退役 F185b，不丢弃三 arm 矩阵机制；
- 不在 `report` 模式下输出任何 pass/fail 或默认 arm；
- 不用确定性命中率替代答案质量结论——它衡量的是检索正确性，不是回答质量；
  「质量已通过」的对外声明在阈值冻结并通过 `gate` 前仍然不成立；
- 不引入新的 judge Provider。

## 改动范围

- `internal/answerrelease/`：model、build、report、publish、artifacts；
- `cmd/build-query-release-gate/`：增加 `--mode report|gate`；
- 不触碰 `internal/answerevaluation` 的公私分离契约（那是 F219 的范围）。

## RED 与完成门

- RED 先证明当前 Policy v1 在 judge 指标部分缺失时必然输出 `incomplete`，
  且不存在表达确定性主判定的字段；
- judge 四指标全缺、部分缺、全有三种输入下，只要确定性主判定齐全，
  矩阵均为完整；
- `report` 模式不输出 pass/fail 与默认 arm；`gate` 模式在阈值未冻结时拒绝运行；
- 默认 arm 选择在确定性指标并列时按成本键确定，顺序变化不改变结果；
- identity drift、错绑、重复/未知/缺 arm、篡改、unknown field、v1 输入
  全部 fail closed；
- 报告 hash 稳定、可独立验签、可从原证据重建；原子发布、拒绝覆盖；
- 目标测试、`-race` 与完整 CI 全绿。

## 关联

- [执行计划](./execution-plan.md)
- [F185b Query Agent Release Gate](./f185b-query-release-gate.md)
- [F219 确定性答案评分](./f219-deterministic-answer-scoring.md)
- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
