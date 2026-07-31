# F88 Buffer Pool Eviction 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：Frame 数受硬上限约束，young/old LRU 只淘汰未 pin Frame；
- 依赖：F87 Page Loading 已完成；
- 契约：见 [Buffer Pool Eviction v1](../storage/buffer-pool-eviction-v1.md)；
- 明确不做：dirty/flush/WAL、后台等待、自适应参数；
- 用户执行授权：2026-07-31，源自全部剩余 Feature 持续实施授权；
- 开工前结论：PASS。

## RED

`go test ./internal/store/buffer` 必须因 Eviction 未实现而失败：

- invalid Capacity/OldFrames 配置拒绝；
- 单队列退化模式遵守精确 LRU，cache hit 更新 MRU；
- 热 Page 晋升 young 后，大于容量的顺序扫描只冲刷 old；
- young 超目标时 LRU 降级，随后按 old LRU 淘汰；
- pinned 与 loading Frame 永不被淘汰；全部不可淘汰时返回 `ErrPoolFull`；
- Release 后 retry 可选出原先 pinned victim；
- 64 个不同 Key 并发 miss 时 Frame/Loader 数从不超过硬上限；
- 被淘汰 Key 再次 Fetch 重新装载且 pin/latch 语义不变。

RED 已确认：首次运行 `go test ./internal/store/buffer` 时，Eviction 用例因明确的
`Buffer Pool Eviction is not implemented` 失败，而非编译或 fixture 错误。

## 完成门

- `go test -count=20 ./internal/store/buffer`、`go test -race ./internal/store/buffer`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS；
- 单队列逐步 reference resident sequence 与精确 eviction count：PASS；
- hot Page 晋升后 19-Page scan 只冲刷一个 old slot：PASS；
- young LRU 降级、old LRU victim 与被淘汰 Key reload：PASS；
- pinned/loading 不淘汰、全 pinned 返回 `ErrPoolFull`、Release 后 retry：PASS；
- 64 个不同 Key 并发 miss 时同时成功/驻留严格限制为 Capacity 8：PASS；
- victim 删除后的 Loader failure 留空并可重试，不复活旧 Frame：PASS；
- 不包含 F89 Dirty Page Flush：PASS。

完成门结论：PASS。
