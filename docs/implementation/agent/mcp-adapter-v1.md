# MCP Adapter v1

状态：F146 已实现。

## 入口与协议

```text
memora mcp [--data-dir /absolute/instance]
```

命令在 stdin 读取、stdout 写出一行一个 UTF-8 JSON-RPC 消息；日志和错误只写 stderr。
实现遵循 MCP stdio framing，并兼容：

- `2026-07-28`：无状态 per-request `_meta`、`server/discover`、`resultType`；
- `2025-11-25` 与 `2025-06-18`：initialize → initialized 生命周期。

参考：[MCP 2026-07-28 stdio](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)、
[MCP Tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)。

## 唯一工具

`tools/list` 只返回 `memora_execute`。输入是 MSQL `source` 与逐语句 `statements`；每个
StatementInput 必须携带有效 `memora.authorization/v2`。这保持工具上下文稳定，也避免另造
add/search/delete memory 一类会削弱 Database/Table/Schema 模型的接口。

adapter 调用 daemon `msql.execute`，不打开 Instance。完整 `memora.result/v1` 同时进入
`structuredContent` 和兼容 text block；Envelope `ok:false` 映射为 MCP `isError:true`，但不丢
错误码、cursor、Route Frame 或 mutation receipt。

## 边界

- 只实现 stdio 与 tools；没有 HTTP、resource、prompt、sampling、elicitation。
- MCP 客户端的信任提示不能替代引擎 Authorization L0–L3。
- 最大单行 1 MiB、最多 128 条 StatementInput；未知工具或协议错误不进入 daemon。
- adapter 不持有事务或会话状态；事务权威仍在 daemon IPC session/执行引擎。
