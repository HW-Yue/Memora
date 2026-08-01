# Local Read API v1

状态：F115 已完成并验收；2026-08-01 冻结。

## 用户结果与边界

`memora admin --scope work --no-open` 启动一个临时 `127.0.0.1` Gateway，让本地
Admin 通过与 `memora query` 相同的 MSQL/result envelope 读取数据。Gateway 只连接
daemon Unix socket，不打开 Store，不提供 HTML、登录、公网监听或模型 Provider。

启动必须至少给一个 `--scope`，最多 32 个。Gateway 为所有 statement 注入固定的
`memora.authorization/v1`（actor 为 `user:admin`）；HTTP 请求只能提供参数，不能提供
Authorization、MutationOptions、approval 或物理访问选项。

## Session 与 HTTP

进程只监听随机 loopback port，并输出一个 `memora.admin-session/v1` JSON descriptor：

```json
{"version":"memora.admin-session/v1","origin":"http://127.0.0.1:49152","url":"http://127.0.0.1:49152/#token=...","expires_at":"..."}
```

fragment 中 256-bit bootstrap token 不发送给 HTTP server。前端用它调用一次
`POST /api/v1/session`（Bearer token）；成功后 token 失效，响应设置 HttpOnly、
SameSite=Strict session Cookie，并返回独立 CSRF token。Session 默认 15 分钟，进程
退出或过期后全部凭据失效。F116 起，页面刷新可在精确 Origin 下用仍有效的 Cookie
恢复同一 session 的 CSRF；这不会复活或重放 bootstrap token。

所有请求都校验精确 Host；有状态 POST 还必须有与 descriptor 相同的 Origin。
`POST /api/v1/msql` 同时要求有效 Cookie 和 `X-Memora-CSRF`。只接受
`application/json`、严格 JSON、256 KiB body 和最多 32 条 statement input。

## Read contract

请求结构是：

```json
{"source":"SHOW TABLES FROM work LIMIT 16 COMPACT","statements":[{"parameters":{"named":{}}}]}
```

`statements` 省略或数量与已解析 statement 数相等；每项只能包含 `parameters`。
允许 `SHOW`、`DESCRIBE`、`DESCRIBE ROUTE`、`SELECT`、`OPEN ROUTE`。校验必须遍历
错误恢复后的全部 batch item，任何 mutation/transaction/management item 都在调用
daemon 前拒绝。允许请求的 HTTP body 是 daemon 返回的原始 `memora.result/v1`
envelope，不另造前端结果协议。

## 关闭与故障

SIGINT/SIGTERM、context cancel 或 session 到期会关闭 HTTP server 和 Listener；关闭
后同一端口可重新绑定。随机源、监听、daemon 调用或响应写入失败都不得降级为无 token、
扩大 scope 或继续后台监听。错误响应不回显 token、Cookie、MSQL 参数或 Row 内容。
