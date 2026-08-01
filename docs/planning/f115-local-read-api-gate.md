# F115 Local Read API 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-OBSERVE、US-ENGINE、US-DEVELOPER。
- 用户结果：Admin 后续页面获得一个临时、本地、只读且 scope-safe 的 MSQL 入口。
- 标准旅程：启动 `memora admin --scope work --no-open` → bootstrap session →
  `SHOW DATABASES COMPACT` / `SHOW TABLES FROM work COMPACT` → 收到 CLI 等价 envelope。
- 作用边界：只增加 loopback HTTP 交付面；不改变 Database/Table/Row/Route、revision、
  transaction、Page 或索引。
- 上下文预算：API 复用各 MSQL list/Row 协议的既有 limit/cursor；request 最大 256 KiB、
  最多 32 条 input，不扩大 Route Frame。
- 永久边界：无 Store 旁路、无正文/Vector 权威、无公网、无 Provider、无客户端 scope。
- 架构选择：临时 Go `net/http` Gateway 是可删除适配层；Unix socket + MSQL 仍是权威路径。
- 唯一主要结果：安全执行只读 MSQL 的临时 loopback API。
- 明确不做：HTML/JS/CSS、自动打开浏览器、具体 Admin 页面、远程 API、登录。
- 开工前结论：PASS。

## RED 清单

- `internal/msql/readquery`：语法错误后的 mutation 仍可能越过现有 CLI 只读检查；
- `internal/adminapi`：mutation、客户端 Authorization、错误 scope、跨 Origin/Host、无
  Cookie/CSRF、token 重放、超限/未知 JSON 必须在 daemon 调用前失败；
- `internal/adminapi`：允许读取的响应必须与 daemon/CLI envelope 字节语义等价；
- `internal/adminapi`：context cancel 后端口必须释放，随机源/daemon 故障不得留后台服务；
- `internal/cli`：`admin` 缺 scope、未显式 `--no-open` 或未知参数必须是 usage error。

RED 命令：

```text
go test ./internal/msql/readquery ./internal/adminapi ./internal/cli
```

当前失败原因应是 package/命令/API 尚不存在，而不是坏 fixture 或编译无关代码。

## 完成证据

- RED 先因 `readquery`、Gateway 与 `admin` 命令不存在而失败，随后逐项转绿；
- `go test -race ./internal/msql/readquery ./internal/adminapi ./internal/cli` 通过；
- 测试覆盖错误恢复后的 mutation、客户端 Authorization、scope、Host/Origin、一次性
  token、Cookie/CSRF、过期、超限、随机源/daemon 故障和关闭后端口复用；
- 真实 loopback→daemon 旅程只看到 `work`，读取 `secret` 返回 `permission_denied`，
  允许请求的 `memora.result/v1` 与直接 daemon 执行相同（除 request ID）；
- `scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、e2e、cross-build 全绿；
- 无 Store/Provider/公网/客户端 scope/HTML 旁路；完成后结论：PASS。
