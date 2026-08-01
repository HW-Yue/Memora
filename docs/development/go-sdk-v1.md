# Go SDK v1

状态：F147 已实现。

## 公开入口

```go
client, err := memora.Dial(ctx, memora.DialOptions{DataDir: "/absolute/instance"})
defer client.Close()

envelope, err := client.Execute(ctx, memora.Request{
    Version: memora.RequestVersion,
    Source:  "SHOW DATABASES LIMIT 8",
    Statements: []memora.StatementInput{{Authorization: memora.Authorization{
        Version: memora.AuthorizationVersion,
        Actor: "my-agent", AuthorizedDatabases: []string{"work"},
        DefaultLevel: memora.LevelRead,
    }}},
})
```

import path 是 `github.com/HW-Yue/Memora/sdk/memora`。公开 package 自己定义 Request、
StatementInput、MutationOptions、Authorization、Envelope 与 RemoteError，不要求调用方导入
任何 `internal/` package。

## 契约

- `memora.go-sdk-request/v1` 在本地校验 source、batch/授权预算和 Authorization v2。
- Client 通过由 data dir 派生的私有 Unix socket 长连接调用 daemon `msql.execute`。
- `memora.result/v1` 暴露 typed rows/columns/errors/cursors/revisions；RowDetail 与 Discovery
  保留为 RawMessage，完整原始 Envelope 可由 `RawJSON()` 取得。
- `Execute` 可并发使用；IPC request ID 负责关联响应；`Close` 幂等。
- daemon IPC error 转成公开 `RemoteError`；context cancel/deadline 仍可用 `errors.Is` 判断。

## 边界

SDK 不打开数据文件、不实现本地 SQL 引擎、不提供 Page/索引/MVCC API。`NewClient(Transport)`
用于调用方测试与受控 transport 适配；生产默认使用 `Dial`。
