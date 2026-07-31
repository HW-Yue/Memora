# F86b Checkpoint Publish 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：只有 Page durability barrier 成功后才能持久发布恢复起点；
- 依赖：F85 Recovery、F86a Segment Set 已完成；
- 格式：见 [Checkpoint Publish v1](../storage/checkpoint-publish-v1.md)；
- 明确不做：Segment 删除、自动 checkpoint、Buffer Pool cleaner；
- 用户执行授权：2026-07-31，源自全部 Feature 持续实施授权；
- 开工前结论：PASS。

## RED

`go test ./internal/store/wal` 必须因 checkpoint publish 未实现而失败：

- commit→barrier→checkpoint write→Sync→receipt 与 reopen；
- recovery 只重放 checkpoint 之后的事务；
- barrier/write/Sync fault 不推进 latest checkpoint；
- 无新 commit、错 ID/LSN/covered Segment、损坏 payload 拒绝；
- checkpoint 可位于刚 Roll 的空 active Segment；
- concurrent commit/roll/checkpoint 保持串行顺序。

实际 RED：全部目标因 `WAL checkpoint publish is not implemented` 失败，未使用
编译失败或坏 fixture。

## 完成证据

- targeted 连续 10 次、`go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh`：format、vet、unit、race、integration、e2e、cross-build 全 PASS；
- commit→Roll→barrier→checkpoint write→Sync→reopen receipt：PASS；
- Segment Set recovery 只重放 checkpoint 之后的事务，F85 Page LSN 规则保持；
- barrier、checkpoint write、Sync fault 均不推进进程内 latest checkpoint；
- 无新 commit 拒绝；重算 Record CRC 后伪造 ID/recovery LSN/covered Segment 仍拒绝；
- checkpoint 后两个 Segment 均保留，未包含 F86c 删除或自动策略。

完成后结论：PASS。
