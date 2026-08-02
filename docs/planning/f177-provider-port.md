# F177：Memora-owned Provider Port

规划状态：已通过单项 Review，批准按 RED → GREEN → REFACTOR 实现。

## 唯一主要结果

在 `internal/agent` 冻结与厂商、HTTP 和编排框架无关的非流式 Provider port、严格 Request/Response
验证和可复用 scripted fake。F177 不调用网络、不读取 API Key、不实现 OpenAI-compatible adapter，
也不建立 Query loop；真实 Kimi/DeepSeek/OpenAI-compatible HTTP 属于 F180。

## 协议范围

- Request：版本、model、system/user/assistant/tool messages、tools JSON Schema、tool choice、max output tokens；
- Response：版本、model、assistant message、`stop|tool_calls|length` finish reason、标准 token usage；
- Tool call：稳定 call ID、工具名、严格 JSON object arguments；tool result 用 matching call ID；
- 不包含 API key、base URL、header、厂商 SDK 类型、hidden reasoning、价格或数据库协议对象。

v1 为一次性 completion，不预留伪 streaming channel。真正流式交互、事件与 Trace 分别由 F178/F186
冻结；Provider error 和 context 取消原样返回，gateway 不自动重试。

## 验证与依赖

- outbound Request 在调用 Provider 前 Validate，inbound Response 在交给 loop 前 Validate；
- message role/字段组合、工具名唯一性、JSON Schema/arguments、finish reason 与 usage 必须自洽；
- `internal/agent` 根生产包仍只依赖标准库和 `protocol/msql`；
- `internal/agent/agenttest` 作为显式测试支持包可额外导入 Agent 自有协议类型，但 CI 继续禁止
  SDK、daemon、MSQL Service、Instance 和任何数据库内核依赖。

## Fake Harness 与完成证据

- scripted Provider 逐次精确匹配完整 Request、返回预设 Response/error、记录调用并检查漏调/多调；
- tool-call 与 tool-result 多轮 transcript 可确定性重放；
- invalid outbound 不调用、invalid inbound 拒绝、error/cancellation 不重试；
- fake 并发安全，Agent import guard 反例保持有效；
- unit/race/vet 与完整 CI 全绿，不访问网络或用户凭据。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F177 范围。

开工前结论：PASS。
