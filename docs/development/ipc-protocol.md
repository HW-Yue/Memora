# 本地 IPC 协议

状态：F08 传输与 session 核心已实现；daemon socket 绑定由 F08b 完成。

## 边界

CLI 和其他本机 Agent 宿主通过 Unix domain socket 连接 Memora daemon。IPC 只承载请求，不允许客户端直接打开数据库文件，也不把 SQLite 或未来原生内核的物理接口暴露出去。

## Frame

每条消息使用：

```text
payload_length u32 big-endian | UTF-8 JSON payload
```

v1 单个 frame 默认上限为 1 MiB。接收端先读取并校验 4 字节长度，再分配 payload；超限、截断或非法 JSON 只关闭当前连接。使用长度前缀而非换行分隔，避免 MSQL、JSON 字段或未来批量请求中的换行产生歧义。

## 协议消息

请求的传输字段为：

```json
{
  "version": 1,
  "request_id": "request-1",
  "method": "ping",
  "timeout_ms": 1000,
  "payload": {}
}
```

响应回传相同 `request_id`、连接的 `session_id`、可选 payload 和传输错误。F08 的错误结构只服务于协议版本、取消和 handler 边界；MSQL 成功、warning、batch 和 statement error 的稳定 Envelope 由 F09 定义，不能把 F08 临时结构当业务结果契约。

未知 JSON 字段默认忽略，以允许同一大版本中的向前兼容；不支持的 `version` 必须返回结构化 `protocol_version` 错误，不能把请求交给 handler。

## 并发与 Session

一个连接对应一个随机 `session_id`，可承载多个并发 request。响应可能乱序，客户端必须用 `request_id` 关联。

请求 context 受三类事件约束：

- 客户端提供的 deadline；
- 客户端显式取消；
- socket 断开或 daemon 关闭。

连接结束后，daemon 先取消并等待该连接的活跃请求，再调用一次 session cleanup。后续事务状态机会在这里回滚未提交事务；F08 只冻结生命周期 hook，不执行事务。

## 安全边界

IPC 只监听本机 Unix socket。socket 目录与文件必须仅当前用户可访问；客户端输入仍视为不可信数据，frame 上限不能被请求覆盖。socket 的短路径、stale 清理和 CLI 健康检查见 F08b。
