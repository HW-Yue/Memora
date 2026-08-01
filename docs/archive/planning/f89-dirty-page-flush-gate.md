# F89 Dirty Page Flush 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：已提交 Page 可标记 dirty，并且只在 WAL durable 后安全写回；
- 依赖：F84 Durable Transaction、F87 Page Loading、F88 Eviction 已完成；
- 契约：见 [Buffer Pool Dirty Flush v1](../../storage/buffer-pool-dirty-flush-v1.md)；
- 明确不做：业务 commit 接线、checkpoint fsync、后台 cleaner；
- 用户执行授权：2026-07-31，源自全部剩余 Feature 持续实施授权；
- 开工前结论：PASS。

## RED

`go test ./internal/store/buffer` 必须因 Dirty Page Flush 未实现而失败：

- Modify 仅在 WAL durable 且 Page LSN 单调时发布，callback failure 不泄漏；
- identity/非法 Page mutation 拒绝且原 Frame 不变；
- dirty Frame 不可淘汰，Flush 成功后可成为 victim；
- Flush 再次检查 WAL durable；不足时 PageWriter 零调用且 dirty 保留；
- PageWriter write fault 保持 dirty，retry 成功清理；
- PageWriter 输入与缓存 Page 深复制隔离；
- FIFO flush list、limit、partial failure/report/remaining 确定；
- 同 Frame concurrent Flush 只需一次有效写，Modify 与 Flush 由 latch 排斥；
- read-only Pool 与半配置 Pool 返回稳定错误。

RED 已确认：首次运行 `go test ./internal/store/buffer` 时，全部 F89 用例均因明确的
`Dirty Page Flush is not implemented` 失败，而非编译或 fixture 错误。

## 完成门

- `go test -count=20 ./internal/store/buffer`、`go test -race ./internal/store/buffer`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS；
- durable + monotonic Page LSN、callback/identity/invalid Page 原子拒绝：PASS；
- fake event log 证明每次 Page write 前先检查 WAL durable：PASS；
- durability query fault、LSN 不足、PageWriter fault 均保持 dirty，retry 收敛：PASS；
- dirty victim 排除，Flush 后可淘汰：PASS；
- FIFO limit、partial failure、joined error 与 remaining report：PASS；
- concurrent Flush 单次有效写，PageWriter I/O 与 Modify latch 排斥：PASS；
- read-only/半配置稳定拒绝且 PageWriter 深复制隔离：PASS；
- 不包含 F90 B+ Tree Node Codec：PASS。

完成门结论：PASS。
