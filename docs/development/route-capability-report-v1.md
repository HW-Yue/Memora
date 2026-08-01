# Route Capability Report v1

状态：F126 实现规格。

## 输入与身份

版本为 `memora.route-capability-report/v1`，policy 为
`memora.route-capability-policy/v1`。输入只能是已通过 F125 `ValidateAgainst` 的原始报告；
Capability Report 绑定 Corpus、Real Host Contract、Canonical Skill、MSQL protocol、全部
source report hash 与可选 price-card digest。重复 run ID 或这些身份漂移一律拒绝。

## 分桶与统计

每个 arm 都生成以下独立 slice，避免稀疏全维度笛卡尔积掩盖样本数：

- aggregate；fanout；depth；difficulty；language；host/model；
- 原始 cell/level/predictor/prefetch/失败与安全计数；
- level top-1、exact path、RowID success、predictor recall、prefetch hit；
- 比例指标的 Wilson 95% 区间；
- model/tool/fallback calls、input/output/mispredict token 的均值与 Router delta；
- embedding、CPU scan、MSQL、model、end-to-end 的 p50/p95；
- vector generation bytes、rebuild 和 peak memory 的最大值。

价格必须由 `memora.route-price-card/v1` 以每百万 token 的 micro-USD 整数提供，并绑定
Provider/model；未知价格显示 unavailable，不从当前市场价格或 endpoint 推断。

## 共同默认门

候选顺序为 hybrid-prefetch、hybrid、vector、lexical。候选要成为共同默认，必须相对同一
scenario/profile 的 Router cell 同时满足：

1. 每个 matched cell 的 RowID success 不下降，aggregate exact-path success 不下降；
2. unauthorized locator、wrong fact read、permission denial、truncation 全为零，且全部完成；
3. 平均 model calls 至少减少 5%；
4. aggregate end-to-end p95 不高于 Router；
5. 每个 host/model、fanout、depth slice 也不发生 RowID success 下降。

没有候选全门通过时默认 Router，并逐项记录拒绝原因。该决策是建议制品；F126 不直接修改
Canonical Skill，后续 Feature 必须显式消费。

## 构建入口与当前证据

```text
go run ./cmd/build-route-capability-report \
  --corpus /abs/repo/benchmarks/route-retrieval-v1.json \
  --canonical-skill /abs/repo/skills/memora \
  --source /abs/private/route-run.json \
  --price-card /abs/private/price-card.json \
  --output /abs/private/route-capability.json
```

`--source` 可重复，price card 可省略。当前开发机能发现 Codex 与 Claude Code CLI，但仓库
没有真实 F125 driver config、Provider 凭据或原始 source report；F126 不把单元测试的
synthetic observation 当真实模型结果。因此当前产品默认仍是 Router，待受控真实报告通过
同一 Builder 后才可能改变。

## 关联

- [Route Benchmark Runner v1](./route-benchmark-runner-v1.md)
- [Route Retrieval Benchmark v3](./route-retrieval-benchmark-v3.md)
