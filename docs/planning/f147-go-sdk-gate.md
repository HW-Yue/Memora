# F147 Go SDK 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 唯一主要结果

Go 调用方只导入 `github.com/HW-Yue/Memora/sdk/memora`，以版本化 Request/Envelope 和可并发
Client 复用现有 daemon IPC `msql.execute`。

## RED

- `Dial` 只接受绝对规范化 data dir，通过 daemon socket 建立长连接；不打开 Instance。
- `Request`、StatementInput、Authorization、Envelope 与 error 都由公开 SDK package 定义，不泄漏 internal 类型。
- Request version、source、statement 数量、Authorization v2/L0–L3 在发送前验证。
- daemon `memora.result/v1` 完整保留 Rows、Column、cursor、revision、notice/error 及复杂扩展 JSON。
- IPC remote error 转换成公开稳定 `RemoteError{Code, Message}`；context cancel/deadline 保持原义。
- Client 支持并发 Execute、幂等 Close，并允许注入 Transport 做调用方测试。

## 边界

F147 不创建第二套 HTTP API、不内嵌 daemon、不暴露 Page/索引/MVCC，也不承诺尚未冻结的高级
mutation helper；第一版只稳定 MSQL batch wire contract。

## 完成门

公开 API 编译示例、wire round trip、validation no-call、remote error、并发/race、Close 与全仓 CI
全绿后合入。下一项 F148。

## 完成证据

公开 package 示例、Request/Authorization no-call validation、MSQL wire、typed/Raw Envelope、
RemoteError、显式空 Route membership、relative data dir、并发 Execute、Close 与 race/全仓 CI
均通过。下一项 F148。
