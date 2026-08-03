# F183：端到端 Answer Runner

状态：已批准；正在执行 RED → GREEN → REFACTOR。

## 唯一主要结果

提供一个可执行的隔离 benchmark host：从 F182 public manifest 出发，在一次性干净 Memora
Instance 中只经版本化 MSQL 物化 fixture，再用 F181 Query Agent 跑完全部 blind tasks，原子输出
公开 scorecard 与私有 diagnostics。F183 不读取 ground truth、不计算答案正确率，也不产品化
`memora ask`。

## 用户故事与标准旅程

- `US-F183-01`：开发者用同一命令得到可重跑的真实模型答案、延迟、调用数和 token 原始数据；
- `US-F183-02`：公开报告可展示最终回答表现，但不暴露 RowID、RouteID、MSQL 或私有诊断；
- `US-F183-03`：失败题保留完整内部 Trace/evidence，能够判断是物化、检索、SQL、预算还是模型失败；
- `US-F183-04`：内置 Agent 与外部 Agent 使用同一 daemon/SDK/MSQL 协议，不存在 benchmark 后门。

```text
strict LoadManifest（无 ground-truth 文件）
→ clean Instance + daemon + Go SDK
→ MSQL CREATE DATABASE/TABLE/ROUTE、SET ALIASES、INSERT membership
→ MSQL 精确回读并冻结 MaterializationReceipt
→ 每题仅投影 BlindTask → QueryAgent → final answer + SELECT evidence + Trace
→ 原子发布 scorecard.json + diagnostics.json
```

## 确定性物化

- fixture stable ID 不伪装成引擎分配 ID；receipt 保存 fixture→actual 的 Database/Table/Column/
  Route/Row 映射和实际 revision；
- Column fixture ID 只在所属 Table 内唯一，receipt 统一用 `tableFixtureID/columnFixtureID` 复合键；
- 每张 Table 的原生 root 由引擎创建；fixture 的逻辑 root 作为其下一层 branch；不符合 path-segment
  约束的 display name 使用 fixture ID 导出的稳定小写 path name，并连同原 aliases 写入 alias 集；
- DDL 文本只使用经过白名单验证并正确 quote 的 identifier/literal；Row/Route 值全部参数绑定；
- INSERT 携带 fixture snapshot provenance、schema revision 和完整单 Leaf membership；
- 物化完成后只经 MSQL 回读每个 Table、Route alias 和 Row 值；任一缺失、额外、revision/值不符
  均拒绝运行模型。MaterializationReceipt 绑定 manifest hash、fixture snapshot hash 与规范 SHA-256。

## Blind、报告与失败语义

- Runner API 只接受已验证 `Manifest`，不接受 `Bundle`、`GroundTruth`、reference answer 或 facts；
- Agent 每题只收到 `BlindTask`、授权 Database、冻结预算、model 和当前 MSQL 结果；
- `scorecard.json` 包含 run/corpus/snapshot/provider/model/arm/prompt/code identity、题目、最终答案、
  success/failure、端到端耗时、Provider/MSQL/tool 调用和 token 原始计数；质量状态固定 `not_scored`；
- `diagnostics.json` 以 public scorecard SHA-256 交叉绑定，保存 MaterializationReceipt、BlindTask、
  error、SELECT evidence 与脱敏 Trace；不保存 API Key；
- 单题 Provider/MSQL/预算失败记录 missing/failure 后继续后续题；context cancel 和物化失败是 run-level
  failure，不发布半份 artifacts；输出目录必须是尚不存在的绝对规范路径，以目录 rename 一次发布；
- 两份 JSON 均有版本、严格 validator 和自身 hash；公开文件不得含 fixture/actual RowID、RouteID、
  MSQL source、evidence rows 或 private error message。

## 边界与完成门

- 唯一 Provider 入口仍是 F177 port；命令默认 Kimi 中国站 OpenAI-compatible endpoint，base URL、
  model、secret env name 和 timeout 都由参数注入，secret 只延迟读取环境；
- 不引入 Eino、Ragas/Python、答案 evaluator、价格表、Router/Vector arm 比较、QuerySession 或 Admin；
- parser/executor scripted RED；clean daemon/SDK integration；报告泄漏/权限/原子发布/cancel 失败矩阵；
- 固定 12 题 fake Provider 全跑；真实 Kimi 跑完整 suite，保存公开 receipt、原始计数与失败标记；
- format、vet、unit、race、integration、e2e、cross-build 与 Agent import allowlist 全绿。

用户执行授权：2026-08-03 用户要求继续顺序完成后续 Feature；F182a 已解除唯一已知 MSQL 缺口。

开工前结论：PASS。
