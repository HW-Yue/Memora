# F180：OpenAI-compatible HTTP Provider

状态：已完成；单项 Review、RED → GREEN → REFACTOR、真实 API smoke 与全部完成门均已通过。

## 唯一主要结果

用 Go 标准库实现 `agent.Provider` 的非流式 OpenAI-compatible HTTP adapter，并用真实 Kimi API
完成一次强制 tool-call smoke。F180 不实现 Query loop、不接 daemon/CLI、不保存模型配置或密钥，
也不引入厂商 SDK、自动重试、streaming、价格计算或 Admin 页面。

## 配置与密钥

- 构造参数只含 API root、secret 名、timeout、请求/响应 byte 上限和可选 RoundTripper；
- secret 只在 `Complete` 时通过注入的 `ProviderSecretResolver` 解析，构造不读环境、不联网；
- 提供进程环境 resolver，但 key 值不进入 Config、Database、Trace、日志、error 或 fixture；
- 每次调用重新解析 secret 以支持轮换，不缓存为 Provider 长期字段；
- endpoint 默认只允许 HTTPS；测试只额外允许数值 loopback HTTP，拒绝 userinfo/query/fragment；
- 禁止 redirect，避免 Bearer credential 被转发到另一个 origin。

## Wire 映射

请求固定 POST `<api-root>/chat/completions`，设置 Bearer、JSON、`stream=false`，映射 message、
function tools、tool choice 和 `max_completion_tokens`。响应只接受一个 index=0 choice，将
`stop|tool_calls|length`、assistant/tool call 和 usage 映射回 Memora-owned 类型。

Kimi 顶层 `cached_tokens` 与常见 `prompt_tokens_details.cached_tokens` 都可读取；冲突值拒绝。
`reasoning_content` 和未知扩展字段被忽略且不会进入 ProviderResponse/Trace。tool arguments 必须是
JSON object 字符串，最终仍由 F177 contract 校验。

协议依据为 Kimi 官方 [Chat Completion API](https://platform.kimi.com/docs/api/chat)：中国站 API root
为 `https://api.moonshot.cn/v1`，认证使用 Bearer，非流式响应含 model、choices、finish reason、
usage，tool result 通过匹配 `tool_call_id` 回传。opt-in smoke 默认中国站，并允许通过
`MEMORA_KIMI_API_BASE_URL` 选择与 key 所属账户一致的官方 endpoint；Provider 生产配置本来就不
写死厂商地址。smoke 默认使用支持标准 `tool_choice=required` 的 `moonshot-v1-8k`；需要思考模式
的 K2.x/K3 扩展不在 F180 的厂商中立 wire 中偷渡，后续通过独立能力协商 Feature 评估。

## 错误与预算

- request/response body 都有硬 byte 上限；response 只允许一个 JSON object 且禁止 trailing value；
- 非 2xx 只返回 status code，不回显远端 error body；transport/secret/wire error 使用稳定脱敏 sentinel；
- context cancel/deadline 保留 `errors.Is` 语义；一次 `Complete` 最多一次 HTTP 请求且不自动重试；
- adapter 自己再次验证 Request/Response，不能依赖调用方一定经过 Gateway。

## 完成证据

- RED golden 覆盖普通回答、tool-call transcript、缓存 token、延迟初始化和 secret 轮换；
- malformed/status/oversize/redirect/cancel 测试证明单次调用、预算和脱敏边界；
- opt-in Kimi smoke 只报告 model、finish reason、usage 与脱敏 digest，不保存正文或 key；
- 主模块无第三方 Provider SDK，Agent import allowlist、race、完整 CI 与 cross-build 全绿。

用户执行授权：2026-08-03 用户要求持续顺序完成后续 Feature。本 Review 只批准上述 F180 范围。

开工前结论：PASS。

## 完成结论

- 标准库 adapter 已实现严格 request/response wire、body budget、单次调用、取消、禁止 redirect、
  延迟 secret 解析和脱敏 HTTP/transport 错误；
- Provider tool call ID 已与函数名校验分离，接受有界可打印 opaque ID，真实 Moonshot 返回的
  `name:index` 形态可原样进入后续 tool result；
- `MOONSHOT_API_KEY` 只由 fresh login shell 的进程环境解析；中国站
  `https://api.moonshot.cn/v1` 使用 `moonshot-v1-8k` 成功返回 required `memora_probe` tool call，
  `finish_reason=tool_calls`，usage 完整，未记录 key 或响应正文；
- format、vet、unit、race、integration、e2e 与 cross-build 全绿。

完成门结论：PASS。F180、F181、F182 已共同解除 F183 的前置依赖。
