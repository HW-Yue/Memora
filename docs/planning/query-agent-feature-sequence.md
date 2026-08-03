# 查询 Agent Feature 序列

状态：2026-08-03 候选序列；冻结依赖和验收顺序，不构成整批实现授权。

## 目标链路

第一条 Agent 产品垂直链只做只读查询，并优先减少 LLM 调用，不把毫秒级数据库工作
省成第二套旁路：

```text
用户问题
→ Agent-owned Bootstrap Builder（尚未调用模型）
  → MSQL：完整有界 Catalog Atlas + 全内容 lexical locations
  → 可选预取最多两张高可能性 Table 的根 Route
→ 紧凑 Query Bootstrap Frame
→ LLM #1：一次选择多个 Table，并选择已预取 Route 或要求展开
→ MSQL：Route 导航 / RowID SELECT
→ 后续 LLM：只携带当前 Route Frame 与事实 Row
→ final answer + SELECT evidence
```

Atlas 默认在上下文预算内覆盖全部授权 Database/Table；超预算时必须暴露 cursor 和
`truncated`，不能因 lexical 零命中排除未读对象。Lexical、Vector 和预取只是可丢弃预测，
最终答案只能来自 SQL 回表。错误预取增加少量数据库读取，不能改变可见结果。

## 依赖图

```text
F170–F174 全内容 lexical location
→ F175a 协议中立化
→ F175b 统一 MSQL Service
→ F175c Agent 边界守卫
→ F176 Bootstrap Frame
→ F177 Provider port ─┬→ F179 Runtime ADR ─┬→ F180 Kimi/OpenAI provider ─┐
→ F178 Event/Trace ───┘                    └→ F181 Query Agent → F182 corpus
                                                            F180 + F181 + F182
                                                                    ↓
F182a Route alias MSQL → F183 runner → F184 外部评分 → F185 release gate → F186 QuerySession
```

## A：统一执行入口（F175a–F175c）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F175a（已完成） | 抽出仅含版本化 Request/Envelope 的 `protocol/msql` | SDK wire golden 不变；协议包不 import `internal/*` |
| F175b（已完成） | 单实例共享 `MSQLService`，IPC 与同进程 adapter 共用 | parity、独立 Session、取消/回滚、并发冲突和 race |
| F175c（已完成） | 建立 `internal/agent` 的 MSQL-only port 与 fake harness | import allowlist；Agent 测试不打开 Instance |

三项不能合并成一个大 Feature：协议兼容、运行时并发和架构依赖是三个独立故障域。

## B：先冻结上下文与观测（F176–F178）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F176（已完成） | 确定性 Query Bootstrap Frame assembler | 无模型测试覆盖 Atlas、lexical、预取命中/回退、snapshot 与 byte budget |
| F177（已完成） | Memora-owned Provider port 与 scripted fake | 无厂商类型泄漏；确定性 tool-call transcript |
| F178（已完成） | Agent Event/Trace/Usage 信封 | run/session/turn、模型、工具、token、费用、分段耗时可重放且正文脱敏 |

Trace 必须早于第一个真实 Provider，避免先上线模型调用、后补不可复现的埋点。Bootstrap
先于 loop，使上下文预算、投机成本和 MSQL 旅程可以在完全没有 API Key 时独立验证。

## C：最小模型闭环（F179–F181）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F179（已完成） | Eino 与薄自研 loop 的 spike/ADR | 选择 Memora-owned 薄 loop，保留重评触发器 |
| F180（已完成） | OpenAI-compatible HTTP Provider | Kimi 真实 API smoke；懒初始化；Key 只从 SecretResolver/进程环境进入请求 |
| F181（已完成） | 只读 Query Agent | 只调用 MSQL；输出 final answer、实际 SELECT evidence 与完整 Trace |

F180 不引入厂商 SDK；base URL、model 和能力由配置注入。真实 Key 不进 Config、Database、
日志、fixture 或报告。F181 的 loop/fake 验收不依赖具体厂商 adapter，因此可独立完成；真实模型
runner 所需 F180、F181 与 F182 corpus 已齐备。F181 仍只运行在隔离 benchmark host，不开放
`memora ask`。

## D：外部标准评分与产品化（F182–F186）

| Feature | 唯一主要结果 | 完成证据 |
| --- | --- | --- |
| F182（已完成） | 冻结 answer corpus/manifest | source、snapshot、问题、隐藏答案、版本、许可和 strict golden 完整 |
| F182a（执行中） | Route alias MSQL round-trip | fixture alias 经 MSQL 写入/读回，revision、posting 与 fault rollback 一致 |
| F183 | 端到端 answer runner | public scorecard 与 private diagnostics 分离，可复现实验 arm |
| F184 | Ragas 等外部评分 adapter | correctness、事实正确性、延迟、token、调用数和费用落盘 |
| F185 | Query Agent release gate | 固定阈值比较 Router-only、Lexical、Vector、预取；失败不产品化 |
| F186 | 交互式 QuerySession | 流式事件、取消、预算和有界恢复；复用 F181 loop，不复制执行器 |

Ragas/Python 只属于开发和 CI 工具链。对外第一指标是最终答案正确性；Route 选择、
Recall@K、回退和 SQL 重试只作为内部诊断。F185 通过前不承诺 `memora ask` 产品能力。

## 共同行为门

- Agent 每次工具调用提交完整 MSQL batch，不跨模型等待持有事务；
- 同一题固定 snapshot、模型、prompt/Skill、预算、Provider 参数和价格版本；
- 模型输出不得直接成为执行结果，所有 tool arguments 严格解码并经过 Policy；
- Bootstrap、loop、Provider、runner 分别可用 fake 独立测试；
- 外部 Agent/CLI 和内置 Agent 的同一 MSQL 请求必须得到等价 Envelope。

## 关联

- [F169 之后的开发序列](./post-f169-development-plan.md)
- [Agent 的 MSQL 边界与依赖注入](../agent/agent-msql-dependency-injection.md)
- [语义路由投机预取](../query/speculative-route-prefetch.md)
- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
