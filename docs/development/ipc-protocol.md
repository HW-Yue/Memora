# 本地 IPC 协议

状态：F08 传输与 session 核心、F08b daemon socket、F16c `msql.execute` session 接线均已实现。

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

daemon 暴露 `ping`、`msql.parse` 和 `msql.execute`。`msql.parse` 只返回版本化 Batch AST 或精确词法/语法 issue，用于 CLI 诊断；`msql.execute` 接收 source 与逐 statement 的 parameter/mutation options，并返回 `memora.result/v1` Envelope。执行请求复用同一 Lexer、Parser 和 AST，不得增加 SQL 字符串旁路。

## 并发与 Session

一个连接对应一个随机 `session_id`，可承载多个并发 request。响应可能乱序，客户端必须用 `request_id` 关联。

请求 context 受三类事件约束：

- 客户端提供的 deadline；
- 客户端显式取消；
- socket 断开或 daemon 关闭。

客户端 deadline 转为协议毫秒值时向上取整，避免服务端因精度截断提前超时。无论超时或取消先在客户端还是服务端被观察到，返回错误都必须分别满足 Go `errors.Is(err, context.DeadlineExceeded)` 或 `errors.Is(err, context.Canceled)`，调用方不依赖竞态路径判断语义。

连接结束后，daemon 先取消并等待该连接的活跃请求，再调用一次 session cleanup。F16c 已在该 hook 中幂等关闭 Batch Session 并回滚未提交 Store transaction；daemon shutdown 也会关闭仍注册的全部 session。

## 安全边界

IPC 只监听本机 Unix socket。socket 位于当前用户临时目录的 `memora-<uid>/` 下，以规范化 datadir 的 SHA-256 前 12 字节作为 Instance 文件名。目录为 `0700`、socket 为 `0600`，并校验目录属于当前用户。

路径不得超过 macOS 103 字节的有效上限；当前用户临时目录过长时回退到 `/tmp/memora-<uid>/`。活 socket 绝不替换；连接失败的 stale socket 可以清理；同名非 socket 文件必须保留并报错。

`daemon start` 只有在 lock、socket 和 `ping` 都就绪后才成功返回。正常退出清理 socket；客户端输入仍视为不可信数据，frame 上限不能被请求覆盖。
