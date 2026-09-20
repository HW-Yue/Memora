# F184：外部答案质量评测 Adapter

状态：已完成；2026-08-04 通过完成门。

## 唯一主要结果

把 F183 的严格公私报告与 F182 evaluator-only ground truth 转换为版本化评测输入，经独立外部
evaluator 得到最终答案与真实 SELECT evidence 的标准质量分，并输出不含 reference/evidence 的
可审计评测报告。F184 不修改 Query Agent、不重跑检索、不形成 release gate。

## 标准旅程

```text
F182 manifest + ground-truth
+ F183 scorecard + diagnostics
→ Go 严格校验 identity/hash/case 对齐
→ 临时 0600 evaluator-input.json
→ 外部 evaluator process（CI scripted；真实 adapter 为 Ragas v0.4.3）
→ 严格读取 evaluator-output.json
→ 聚合完成率、p50/p95、调用/token 与四项质量指标
→ 原子发布 evaluation.json
```

## 数据映射与指标

- `user_input` = BlindTask question，`response` = F183 final answer，`reference` = F182 reference answer；
- `retrieved_contexts` 只来自成功 SELECT evidence：每个结果 Row 是一个按 Column 顺序规范化的
  JSON context，排除 `row_id/revision/commit_sequence/row_state/schema_version` 等系统身份字段；
- 固定 Ragas collections API 指标：`FactualCorrectness`、`Faithfulness`、`ContextPrecision`、
  `ContextRecall`，分值范围 0..1；adapter 与版本均写入报告；
- F183 失败题标记 `runner_failed`，不调用 judge、不伪造 0 分；外部 evaluator 单题失败标记
  `evaluator_failed` 并继续；聚合 mean 只统计该指标有效样本，同时单列 runner/evaluator coverage；
- p50/p95、Provider/MSQL/tool calls 与 tokens 直接聚合 F183 原始计数，不让外部 judge 重定义。

## 隔离、安全与版本

- F183 Runner 仍不读取 ground truth；只有独立 F184 命令同时持有四份输入；
- external process 只读临时评测输入和显式输出路径；API key 只由其配置的环境变量解析，不进入
  argv、输入、报告或 stderr；临时目录在 process 结束后删除；
- Memora 二进制与安装包不依赖 Python、Ragas、LangChain 或厂商 SDK；`tools/ragas/` 是开发工具；
- Ragas 固定 v0.4.3，并使用官方推荐的 `ragas.metrics.collections` 直接 `.ascore()` API；judge
  model/base URL/secret env name 均注入，不写死 DeepSeek、Kimi 或 OpenAI；
- Python 3.9 显式锁定 `eval-type-backport` 兼容依赖；Adapter 强制关闭 Ragas usage analytics；
- evaluator 可返回空 `hash`，由 Go 信任边界严格解码、校验 input/case/status 后规范化签名，避免
  Python/Go 浮点 JSON 表示差异；
- report 自身规范 SHA-256，拒绝 case 缺失/重复/重排、NaN/Inf、范围外分数、输入 hash 漂移和
  adapter 输出未知字段。

## 非目标与完成门

- 不在本 Feature 定阈值、比较 Router/Vector arms、自动重试/限流或宣称答案质量通过；
- 不把 Ragas 的 chunk 假设反灌回 Memora；context 永远是实际 SQL Row，不是机械文档切片；
- scripted evaluator 覆盖 12 题成功、runner/evaluator 失败隔离、篡改、取消和 secret 泄漏；
- Python adapter `py_compile` 与 fake OpenAI-compatible judge smoke；真实 judge 运行可因账户限流标为
  evidence incomplete，但不能伪造分数；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

研究基线：Ragas 官方 v0.4.3（2026-01-13 发布）文档；collections API 的四个指标均由 judge LLM
执行，旧 metrics/SingleTurnSample 示例仅作迁移兼容，不用于新实现。

用户执行授权：2026-08-03 用户要求继续逐项完成后续 Feature；F183 已完成并产出可验证公私报告。

开工前结论：PASS。

## 完成证据

- `internal/answerevaluation` 已实现严格 input/output/report、四项 mean 与 coverage、nearest-rank
  p50/p95、原始调用/token 聚合和公开字段白名单；报告不含 question/answer/reference/context/evidence；
- 外部进程只继承显式环境，临时目录 `0700`、文件 `0600`，覆盖未知字段、错绑 hash/case、取消、
  stderr secret 泄漏和 Go 边界签名；`run-answer-evaluation` 以新文件原子发布、拒绝覆盖；
- scripted 12 题覆盖 10 scored、1 evaluator_failed、1 runner_failed；Python unit、`py_compile`
  通过；真实 Ragas v0.4.3 collections 对本地 OpenAI-compatible fake judge 发出 8 次结构化请求，
  四项指标均成功返回；
- F183 真实 Kimi 证据生成公开报告
  `benchmarks/answer-retrieval-v1/f184-kimi-real-20260803-evaluation.json`，hash 为
  `sha256:63361640c633ba5d860aa95a454a73453f49869a2e9e269fc0cea5ed6e57556b`；结果为
  12 runner_failed、0 scored，准确表示上游 wire/429 使质量证据不完整，不伪造 0 分；
- 2026-08-06 DeepSeek V4 Flash smoke 的当前提交回执已成功发布到外置 ExFAT 盘：9 个题目完成
  Runner、3 个题目 runner_failed；Ragas judge 在启用单指标有限重试后得到 3 个 scored、6 个
  evaluator_failed，仍只证明评分桥接和发布链路可执行，不能宣称答案质量通过。evaluation report
  的质量覆盖仍保持不完整边界。
- format、vet、unit、race、integration、e2e、macOS 双架构 cross-build，以及新增命令的
  darwin/arm64、linux/amd64 交叉构建全部通过。

完成门结论：PASS。F184 证明评分协议、隔离和可观测报告已完成；不证明 Query Agent 质量通过。
下一项 F185a 先让 arms 真正改变执行链，F185b 再冻结阈值并建立 release gate。
