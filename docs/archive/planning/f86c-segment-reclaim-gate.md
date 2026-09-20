# F86c Segment Reclaim 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：只删除 durable checkpoint 已完全覆盖的旧 Segment，并可安全重开；
- 依赖：F86a Segment Set、F86b Checkpoint Publish 已完成；
- 格式：见 [WAL Segment Reclaim v1](../storage/wal-segment-reclaim-v1.md)；
- 明确不做：自动策略、PITR、远端归档、retained Segment 截断；
- 用户执行授权：2026-07-31，源自全部 Feature 持续实施授权；
- 开工前结论：PASS。

## RED

`go test ./internal/store/wal` 必须因 Segment reclaim 未实现而失败：

- checkpoint in later Segment→manifest→删除旧段→close/reopen/recovery；
- 无 checkpoint、checkpoint 与 active 同段、retained 缺号均不得误删；
- manifest write/Sync/rename/directory Sync fault 在发布前保留全部 Segment；
- 删除单个文件与最终 directory Sync fault 可重开并在重试后收敛；
- manifest bit flip、伪造 checkpoint/first Segment/high-water 拒绝；
- 已回收 transaction ID 不得重用。

RED 已确认：首次运行 `go test ./internal/store/wal` 时，上述用例均因明确的
`WAL Segment reclaim is not implemented` 失败，而非编译或测试装配错误。

## 完成门

- `go test -count=10 ./internal/store/wal`：PASS；
- `go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh` 的 format、vet、unit、race、integration、e2e、cross-build：PASS；
- checkpoint 后 manifest 发布、旧段删除、close/reopen 与 recovery：PASS；
- manifest write/Sync/rename/首次 directory Sync fault 保留全部 Segment：PASS；
- 删除失败与最终 directory Sync fault 后可重开，重试确定性收敛：PASS；
- manifest bit flip、有效 CRC 伪造 checkpoint、非法 high-water、retained 缺段均拒绝：PASS；
- 已回收 transaction ID 重用拒绝：PASS；
- 不包含 F87 Buffer Pool：PASS。

完成门结论：PASS。
