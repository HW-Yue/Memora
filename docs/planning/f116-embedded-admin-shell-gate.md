# F116 Embedded Admin Shell 开工与完成门

状态：已完成；持续执行授权覆盖 F110–F163。

## 产品门

- 用户故事：US-OBSERVE、US-HUMAN、US-DEVELOPER。
- 用户结果：用户运行一个 binary 即可打开本地 Admin 容器并看到可信 session 状态。
- 标准旅程：`memora admin --scope work` → 浏览器打开 fragment bootstrap URL →
  fragment 清除 → session ready → 后续页面共用同一只读 MSQL client。
- 作用边界：只增加只读展示壳；Database/Table/Row/Route/revision/transaction 均不改变。
- 上下文预算：壳不读取数据；后续请求继续服从 F110–F115 limit/cursor/body 预算。
- 永久边界：无 Node runtime/CDN/遥测/Store 旁路/Provider/业务页面/正文缓存。
- 架构选择：原生 ES module + CSS 的最小 bundle 是可替换展示层，Go embed manifest
  和 HTTP contract 不绑定某个前端框架。
- 唯一主要结果：发行 binary 离线提供可安全启动的 Admin Shell。
- 明确不做：Catalog/Route/Row/Change/Diff/Trace 页面和任何 mutation UI。
- 开工前结论：PASS。

## RED 清单

- bundle 缺失、增加或 hash tamper 时仍启动，或 asset 依赖运行时文件/外部 URL；
- `/catalog/...` 深链路 404、未知 `/assets`/`/api` 却回落 HTML、POST 返回 HTML；
- HTML 含 inline code，响应缺 CSP/no-referrer/nosniff/frame 防护或 cache 策略错误；
- fragment/token/CSRF 进入 URL query、DOM、Web Storage、日志或静态资源；
- CLI 默认不打开、`--no-open` 仍打开，或 browser open 失败后 Gateway 不关闭；
- darwin/arm64 或 darwin/amd64 binary 缺 bundle，浏览器无法完成 bootstrap→ready。

RED 命令：

```text
go test ./internal/adminui ./internal/adminapi ./internal/cli
```

当前失败应由 embed bundle、shell handler 和默认 browser open 尚不存在导致。

## 完成证据

- RED 先因 `adminui` bundle、shell handler 与默认 browser open 不存在而失败；
- embed manifest 验证精确文件集、size/SHA-256、tamper/missing/extra；路由测试覆盖
  index、深链路、asset、未知 API/asset、GET/HEAD/POST、ETag/cache 与安全 headers；
- CLI 测试覆盖默认 open、`--no-open`、open callback 失败后 Gateway 关闭和端口释放；
- `go test -race ./internal/adminui ./internal/adminapi ./internal/cli` 通过；
- 用当前源码构建真实 `memora` binary，启动真实 instance/daemon/Gateway 后在 Chrome
  完成 bootstrap→ready；URL fragment 清空、无 console error，刷新 `/catalog/work`
  后通过 Cookie 恢复仍为 ready；视觉检查通过；
- `scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、e2e、darwin 双架构
  cross-build 全绿；实际浏览器已证明 bundle 位于 binary 而非工作目录；
- 无 Node/CDN/遥测/Provider/业务页面/Store 旁路；完成后结论：PASS。
