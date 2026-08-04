# F185b：Query Agent Release Gate

状态：已完成（2026-08-04）；Feature 完成门 PASS，当前产品 release decision 为 INCOMPLETE；
2026-08-05 用户决定延期真实三臂质量复跑。

## 唯一主要结果

消费三个 F185a arm 各自绑定的 F183 public scorecard 与 F184 public evaluation，生成严格、可复算、
可审计的 Query release report；完整同身份矩阵中至少一个 arm 达到固定质量门，才允许对外宣称
Query Agent 质量通过并由报告选定默认 arm。

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

## 完成证据

- `internal/answerrelease` 已实现固定 Policy v1、三 arm hash 自动配对、同身份校验、覆盖率/质量门、
  逐题最低分、最佳质量差值和 input-token 优先的确定性选择；报告可独立验签并能对原证据重建；
- `build-query-release-gate` 严格加载六份公开 JSON，以新文件原子发布；`failed/incomplete` 仍留下
  报告但退出非零。错绑、身份/逐题性能漂移、未知/重复/缺 arm、symlink、unknown field、尾随 JSON、
  篡改和覆盖均 fail closed；
- RED 提交为 `6a89a7d`、`7736c0e`、`49ddb46`；GREEN/修复提交为 `0e58b59`、`f1bcce1`、
  `62240cc`；受影响包 unit/race 与全量 format、vet、unit、race、integration、e2e、cross-build 全绿；
- 三条真实 Kimi `moonshot-v1-8k` arm 已各跑 12 题并生成公开 scorecard/evaluation。Atlas-only、
  Atlas+Lexical、Prefetch 的 MSQL calls 分别为 12、12、22，证明执行链不同；Provider 共出现 33 次
  HTTP 429 和 3 次 wire failure，因此三臂均为 0/12 scored；
- [真实 release report](../../benchmarks/answer-retrieval-v1/f185b-kimi-real-20260804-release.json)
  hash 为 `sha256:baca24ac7d3e012e9dbee5ff8536950913910a93c40d0ed335e47def7b075708`，状态
  `incomplete`、无默认 arm，三个 arm 都明确记录 coverage/metric/matrix incomplete reasons。

完成门结论：PASS，指 F185b 能可靠地产生并阻断 release；不代表 Query Agent 产品质量通过。

## 2026-08-05 产品策略调整

用户确认当前先接受真实 Provider required tool-call smoke、确定性完整 Query loop 和三 arm 实际
MSQL/Frame 差异作为“链路可继续开发”的证据，真实大批量三臂质量复跑延期。当前 INCOMPLETE
报告继续保留，不能改写为 PASS，也不能据此声称答案质量达标或选出默认 arm。

因此本门继续作为**质量发布门**，不再作为后续实验性 Feature 的**开发启动门**。F186 可以按独立
Review/TDD 开发，但在真实矩阵通过前只能标为实验性能力；scripted 分数仍不得替代真实证据。
