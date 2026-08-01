# F87 Page Loading 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：同一 Page 只装载一次，并在 Handle 生命周期内保持 pin 与 latch 正确；
- 依赖：F81 Page Codec、F82 Page File Manager 已完成；
- 契约：见 [Buffer Pool Page Loading v1](../../storage/buffer-pool-page-loading-v1.md)；
- 明确不做：容量、淘汰、dirty、flush、WAL 接线；
- 用户执行授权：2026-07-31，源自全部剩余 Feature 持续实施授权；
- 开工前结论：PASS。

## RED

`go test ./internal/store/buffer` 必须因 Page Loading 未实现而失败，而不是编译或
fixture 错误：

- 64 个同 Key 并发 Fetch 只调用一次 Loader，并各自持有/释放一个 pin；
- cache hit 不重复 I/O，不同 Key 可并行装载；
- Loader error 广播给当前等待者且不缓存，后续 Fetch 可重试；
- Loader 返回错 space/page identity 时拒绝且不缓存；
- 重复 Release、release 后访问返回稳定错误且 pin 不下溢；
- 同 Frame 多 reader 并发，exclusive 排斥 read/exclusive，不同 Frame 不互阻；
- Release 等待同 Handle 正在执行的回调；
- Loader 原始 payload 与回调快照均不能反向修改缓存 Page。

调度使用 channel barrier，不依赖 sleep 或概率。

RED 已确认：首次运行 `go test ./internal/store/buffer` 时，全部用例均因明确的
`Buffer Pool Page Loading is not implemented` 缺失能力失败，而非编译或 fixture
错误。

## 完成门

- `go test ./internal/store/buffer`、`go test -count=20 ./internal/store/buffer`：PASS；
- `go test -race ./internal/store/buffer`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS；
- 64 caller single-flight、每 caller 独立 pin、cache hit 无重复 I/O：PASS；
- 不同 Key 并行装载、32 waiter 失败广播、失败后 retry：PASS；
- identity mismatch 不缓存、payload 双边界深复制：PASS；
- 多 reader、exclusive 排斥、跨 Frame 不互阻、Release 等待回调：PASS；
- 重复 Release 与 release 后访问稳定拒绝，pin 不下溢：PASS；
- 不包含 F88 Eviction 或 F89 Dirty Flush：PASS。

完成门结论：PASS。
