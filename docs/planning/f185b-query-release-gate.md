# F185b：Query Agent Release Gate

状态：已批准；准备执行 RED → GREEN → REFACTOR。

## 唯一主要结果

消费三个 F185a arm 各自绑定的 F183 public scorecard 与 F184 public evaluation，生成严格、可复算、
可审计的 Query release report；只有完整同身份矩阵中至少一个 arm 达到固定质量门时，才允许 F186
把 QuerySession 作为产品候选继续开发。

F185b 不运行 Query Agent、不调用 judge、不读取 private diagnostics/ground truth 正文，也不实现
`memora ask`。

## 输入矩阵与身份

必须恰好提供并一一配对：

- `atlas-only-v1`；
- `atlas-lexical-v1`；
- `atlas-lexical-prefetch-v1`。

evaluation 通过 `public_scorecard_sha256` 绑定 scorecard。三组证据必须具有相同 corpus ID/revision、
snapshot ID/SHA、Provider/model、prompt、code revision、evaluator adapter/version、judge model 和
ground-truth SHA；run ID 与 source hash 必须各不相同。缺 arm、重复、未知 arm、身份漂移、篡改或
非严格 JSON 均拒绝建报告，不降级比较。

## Policy v1

每个 arm 至少 12 题，且 runner、evaluator 和四项 metric 都必须 100% 有效，否则整个矩阵状态为
`incomplete`，不选默认 arm。

完整矩阵中，arm 必须同时满足：

- factual correctness mean ≥ 0.85，且逐题最低值 ≥ 0.50；
- faithfulness mean ≥ 0.90，且逐题最低值 ≥ 0.80；
- context precision mean ≥ 0.80；
- context recall mean ≥ 0.85；
- factual correctness 与 faithfulness 均不得落后完整矩阵最佳值超过 0.03。

最终答案质量是硬门；context 指标用于阻止“答案偶然答对、证据却错误”的 arm 放行。多个 arm
通过时，按总 input tokens、Provider calls、端到端 p95、固定 arm 顺序依次选择，优先减少模型
上下文与调用成本。全部证据完整但无 arm 达标时状态为 `failed`。

## 公开报告与命令

新增 `memora.query-release-gate/v1` 报告，包含 policy、冻结身份、六个 source hash、每 arm 的
counts/performance/metrics/逐题最低分、稳定 reason code、总体状态和默认 arm；不含问题、答案、
reference、SQL evidence、Trace 或私有错误。

`build-query-release-gate` 接受三个可重复 `--scorecard`、三个可重复 `--evaluation` 和一个绝对新
`--output`，严格加载后按 hash 自动配对，以同目录新文件原子发布且拒绝覆盖。

## RED 与完成门

- 完整合格矩阵 PASS，并按 context/token 成本选 arm；顺序变化不改变报告；
- runner/evaluator 缺样本输出 INCOMPLETE；完整低质量矩阵输出 FAILED；
- identity drift、错绑、重复/未知/缺 arm、篡改、unknown JSON field 和 token overflow fail closed；
- report hash、重算、source binding、公开字段白名单、原子发布和 CLI 失败语义覆盖；
- 当前真实 Kimi 失败 receipt 只能证明 release 未通过；没有三个真实可评分 arm receipt 时，产品
  状态保持 INCOMPLETE，不能用 scripted 分数冒充；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

本门只覆盖 F182 自有 12 题 regression corpus，不声称代表公开 RAG 排行；CRUD-RAG、RGB、
HotpotQA、MIRACL 继续作为独立外部效度候选。

用户执行授权：2026-08-03 用户要求连续逐项完成后续 Feature；F185a 已完成真实执行 arms。

开工前结论：PASS。
