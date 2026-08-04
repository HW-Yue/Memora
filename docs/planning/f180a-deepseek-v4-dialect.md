# F180a：DeepSeek V4 非思考 Provider 方言

状态：已批准，2026-08-05 开工。

## 唯一主要结果

在现有标准库 OpenAI-compatible HTTP Provider 边界增加显式、可验证的
`deepseek-v4-non-thinking` 请求方言，使 `deepseek-v4-flash` 能复用 Memora-owned Provider port
和多轮 Tool Call。F180a 不引入 Eino/厂商 SDK、不修改 Query loop、不保存 hidden reasoning，
也不把 DeepSeek 类型写入 MSQL、Trace 或 Agent 协议。

## 用户故事与固定旅程

- `US-F180A-01`：用户选择 DeepSeek V4 Flash 后，同一 Query Agent 能发起 required Tool Call，
  回传 MSQL 结果并继续生成最终答案；
- `US-F180A-02`：切换厂商只改变 HTTP adapter 配置，不改变 Agent/MSQL 请求；
- `US-F180A-03`：未选择方言的 Kimi/标准 OpenAI-compatible golden 完全不变。

```text
ProviderRequest（厂商中立）
→ OpenAI-compatible adapter + deepseek-v4-non-thinking
→ POST /chat/completions
   max_tokens=<MaxOutputTokens>
   thinking={"type":"disabled"}
→ ProviderResponse（厂商中立，不含 reasoning_content）
```

## 契约与边界

- 方言是构造期枚举，未知值 fail closed；零值保持现行标准映射；
- DeepSeek V4 方言使用 `max_tokens`，不得同时发送 `max_completion_tokens`；
- 显式关闭思考模式，避免 Tool Call 后必须回传 `reasoning_content`，并保持 F177 不保存 hidden
  reasoning 的永久边界；
- endpoint、secret resolver、body budget、单次请求、禁止 redirect 和脱敏错误语义不变；
- model 仍由调用方传入，方言不偷偷改写为某个模型名。

协议依据：DeepSeek 官方 Chat Completion 与 Thinking Mode 文档。V4 默认启用思考模式；思考模式的
Tool Call 后续请求必须回传 `reasoning_content`，而非思考模式无需扩展 Memora Provider contract。

## RED 与完成门

- 新 golden 先证明当前 wire 错用 `max_completion_tokens` 且缺少 `thinking.disabled`；
- GREEN 后 DeepSeek 方言输出精确 JSON，标准 golden 零变化；
- 未知方言构造失败，oversize/secret/redirect/cancel 行为不回归；
- opt-in 真实 smoke 使用 `DEEPSEEK_API_KEY`，只需一次 required Tool Call 成功；Key、正文和 hidden
  reasoning 不落盘。无 Key 时机械完成门可通过，真实 smoke 保持待补证据；
- format、vet、unit、race、Agent import allowlist 与全量回归通过。

用户执行授权：2026-08-05 用户要求持续执行至 F204；模型限速时每条模型链一次成功 smoke 即可。

开工前结论：PASS。
