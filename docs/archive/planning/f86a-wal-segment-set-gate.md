# F86a WAL Segment Set 开工与完成门

状态：完成，2026-07-31。

## 产品门

- 唯一结果：WAL 可在多个连续 Segment 间显式 roll、重开并保持全局事务顺序；
- 依赖：F83 Stream、F84 Durable Transaction、F85 Recovery 已完成；
- 格式：见 [WAL Segment Set v1](../../storage/wal-segment-set-v1.md)；
- 明确不做：checkpoint 发布、recovery 起点、Segment 删除、自动大小策略；
- 用户执行授权：2026-07-31，源自 F81–F109 全部 Feature 持续实施授权；
- 拆分原因：rolling、checkpoint publish、reclaim 是三个独立故障域；
- 开工前结论：PASS。

## RED

`go test ./internal/store/wal` 必须因 Segment Set 未实现而失败：

- create/commit/roll/commit/close/reopen/cross-segment scan；
- 全局 LSN、Segment ID、transaction ID 连续/唯一；
- empty active、未提交尾部和 concurrent commit/roll 拒绝或串行；
- 缺号、未知文件、错 identity/start LSN、损坏 Segment 拒绝；
- create/roll fault 不切换 active，重试后收敛；
- close 与 reopen 保持稳定状态。

实际 RED：全部目标因 `WAL Segment Set is not implemented` 失败，未使用编译失败
或坏 fixture。

## 完成证据

- targeted 连续 10 次、`go test ./...`、`go test -race ./...`、`go vet ./...`：PASS；
- `./scripts/ci.sh`：format、vet、unit、race、integration、e2e、cross-build 全 PASS；
- create/commit/roll/commit/close/reopen/cross-segment scan：真实目录 PASS；
- 全局 LSN、Segment ID、transaction ID 顺序与并发 Commit/Roll race：PASS；
- empty active、未提交尾部、跨 Segment duplicate、缺号、未知文件、错 Header/start
  LSN、bit flip：全部拒绝；
- 确定性 Segment create fault 不切换 active，旧 Segment 可继续 commit 并重试 roll；
- 未包含 F86b checkpoint、recovery 起点或 F86c Segment 删除。

完成后结论：PASS。
