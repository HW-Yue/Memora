# F146 MCP Adapter 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

提供一个 newline-delimited JSON-RPC stdio MCP server，让 MCP 客户端通过唯一的
`memora_execute` 工具调用现有 daemon `msql.execute` 边界。

## RED

- 支持当前 `2026-07-28` 的 per-request `_meta`、`server/discover`、`resultType` 和 tools 能力。
- 为现有宿主兼容 `2025-11-25`、`2025-06-18` initialize/initialized 生命周期。
- `tools/list` 只暴露一个稳定、确定顺序的 MSQL 工具，避免工具上下文膨胀。
- `tools/call` 严格解码 source/statements，要求每条 StatementInput 有有效 Authorization v2。
- 成功与 MSQL 失败都原样保留 `memora.result/v1` structuredContent；后者设置 `isError`。
- 未知 method/tool、畸形 JSON、错误版本、缺失现代 `_meta` 返回稳定 JSON-RPC error。
- stdout 只含单行 MCP 消息；EOF 正常退出；单行与消息大小有硬上限。

## 边界

adapter 不打开 Instance、不缓存事务、不新增“记忆 CRUD”语义；它只连接 daemon。F146 只做
stdio，不做 HTTP transport、资源、prompt、sampling 或 elicitation。

## 完成门

modern/legacy 握手、tool list/call、Authorization、协议错误、framing、daemon 失败、race 与全仓
CI 全绿后合入。下一项 F147。

## 完成证据

modern discover/list/call、两个 legacy initialize 版本、initialized 顺序、唯一工具、严格
Authorization、结构化失败 Envelope、稳定错误码、超长行、CLI stdout 纯协议、race 与全仓 CI
均通过。下一项 F147。
