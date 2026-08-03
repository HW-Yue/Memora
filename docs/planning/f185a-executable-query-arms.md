# F185a：可执行 Query Bootstrap Arms

状态：已批准；准备执行 RED → GREEN → REFACTOR。

## 唯一主要结果

让 F183 `arm_id` 从报告标签变成可验证的 Query Bootstrap 执行配置：同一冻结任务可选择
`atlas-only-v1`、`atlas-lexical-v1` 或 `atlas-lexical-prefetch-v1`，三者产生不同 MSQL
transcript 和不同模型首轮 Frame，但共享相同 Router/SELECT 事实边界。

F185a 不计算质量阈值、不比较报告、不决定产品发布；这些属于 F185b。

## 用户故事与标准旅程

`US-QUERY-ARM`：维护者在不改题目、snapshot、Provider、prompt 或预算的前提下切换候选武器，
得到真实不同的实验输入，而不是给同一条链路换名字。

```text
atlas-only-v1
→ MSQL: SHOW CATALOG ATLAS ... COMPACT
→ Atlas continuation（如有）
→ LLM → 正常 Route/SELECT

atlas-lexical-v1
→ 单 batch MSQL: SHOW CATALOG ATLAS ...; SHOW LEXICAL LOCATIONS ...
→ Atlas continuation（如有）
→ LLM → 正常 Route/SELECT

atlas-lexical-prefetch-v1
→ 与 atlas-lexical-v1 相同的首批 batch
→ 按 lexical 顺序对最多两张 Atlas 已知 Table 批量 SHOW ROUTES AT ROOT
→ LLM → 命中使用预取，未命中正常 Route fallback → SELECT
```

三个 arm 的最终答案都必须来自成功 SELECT evidence；Atlas、lexical、prefetch 仍只导航。

## 协议与兼容

- `BootstrapProfile` 使用上述三个版本化常量，并写入 `BootstrapFrame.profile`；模型明确知道哪些
  候选源被禁用，不能把 atlas-only 的空 lexical 误解为“查询零命中”；
- `QueryRequest` 显式携带 profile；现有未设置 profile 的内部调用暂时规范化为当前行为
  `atlas-lexical-prefetch-v1`，避免一次性破坏非 benchmark 调用；
- F183 `RunConfig.ArmID` 和 CLI `--arm` 只接受三个稳定 ID，未知值在物化、Provider 或 MSQL 前失败；
- Frame 和 Trace 记录规范化后的实际 profile，公开 scorecard 的 `arm_id` 必须与其一致。

不改变 MSQL wire、存储、Row、Route membership、revision、事务或恢复格式。

## Vector 边界

F124d 已有 Route-only CPU exact MSQL，但当前 answer corpus 没有 Route vector generation，Query Agent
也没有把短问题编码到指定 embedding space 的公开 `QueryEncoder`。因此 Vector 暂不列为可执行 arm；
把手工答案向量、隐藏标签或任意模型输出塞进 benchmark 会污染盲测。

Vector 仍是 ADR-0007 的可选候选武器。只有公开问题经版本化 encoder 生成 query vector、fixture Route
经同一 space 生成可重建 generation 后，才单独 Review Vector arm，不阻塞 Atlas/lexical 的发布证据。

## 失败与上下文预算

- profile 只关闭候选源，不改变 Atlas 完整性、权限 scope、Query tool budget 或 SELECT 要求；
- atlas-only 不执行 lexical MSQL；atlas-lexical 不执行 root prefetch MSQL；prefetch 失败仍保留现有 fallback；
- 每个 profile 继续受同一 Atlas、lexical、root 和总 Frame byte budget 约束；禁用字段输出空有界容器；
- unknown/空的 F183 arm、profile 与 arm 不一致、非法 budget 或 malformed Envelope 均 fail closed；
- 不自动重试 Provider/MSQL，不从其他 arm 借结果，不用 predictor 候选作为答案。

## RED 与完成门

- `TestBootstrapProfilesProduceDistinctMSQLTranscripts`：同一问题分别期待 1、1、2 次 Bootstrap MSQL，
  且 Frame profile/lexical/root 内容不同；当前实现因无 profile 而失败；
- `TestRunnerArmIDSelectsBootstrapProfile`：三个 F183 arm 驱动不同 clean Query transcript，scorecard 标签
  与实际 Frame 一致；未知 arm 在 materialization/Provider/MSQL 前失败；
- 现有 default profile、Atlas continuation、prefetch fallback、Frame byte、Trace 脱敏测试保持全绿；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

用户执行授权：2026-08-03 用户要求连续逐项执行后续 Feature；2026-08-04 F185 Review 发现 arm
仅为标签，按既有“一 Feature 一个结果”规则拆为 F185a/F185b。

开工前结论：PASS。
