# F156–F163 按证据触发门

状态：已完成；前序空间与资源门见 [F151–F155](./evidence-gates-f151-f163.md)。

## F156 Physical Undo

状态：已评估，结构进入条件未成立，延后。

- 门槛：生产写路径出现 uncommitted dirty Page steal，或 Row body 在 commit 前原位覆盖。
- 证据：Tree Runtime 先取得带 COMMIT 的 durable WAL transaction，之后才发布 Page batch；
  commit 前 mutation 私有，跨对象 staging 失败无部分状态。
- 结论：当前 no-steal + immutable revision 不需要 Physical Undo/Purge。

## F157 Advanced MVCC

状态：已评估，产品进入条件未成立，延后。

- 门槛：canonical journey 出现 multi-writer 或明确强隔离语义。
- 证据：65 turn 明确需求 0；snapshot reference-model 与 same-base concurrent update 通过。
- 结论：单 writer、snapshot sequence、immutable revision 和精确对象写锁覆盖现有旅程。

## F158 Lock Waits/Deadlock

状态：已评估，产品进入条件未成立，延后。

- 门槛：至少两个 journey 因 fail-fast conflict 无法完成且给出可接受 wait budget。
- 证据：明确需求 0；object-lock reference model、cancel、batch conflict 与 opposite-order 通过。
- 结论：保持 one-winner/no-deadlock，不增加等待队列与 wait-for graph。

## F159 Replication

状态：已评估，产品进入条件未成立，延后。

- 门槛：明确 primary→replica 拓扑、RPO/RTO、读一致性与 failover owner。
- 证据：65 turn 明确需求 0；Change Log commit-sequence cursor/index 仍全绿。
- 结论：逻辑变化流只保留为未来输入，不自行升级成网络拓扑。

## F160 PITR

状态：已评估，产品进入条件未成立，延后。

- 门槛：恢复到明确 wall-clock/commit sequence，并冻结窗口、保留预算和恢复目标验证。
- 证据：65 turn 明确需求 0；latest backup 搬迁恢复和 History 不冒充任意时间点重放。
- 结论：等待真实 RPO/RTO 故事，不默认无限保留 Change Log。

## F161 Multi-device Sync

状态：已评估，产品进入条件未成立，延后。

- 冻结门槛：至少两个可离线写入的 device identity，需要双向同步，并明确 concurrent edit
  merge/conflict、删除、权限与 key distribution；Host switch 或离线 Instance move 不算。
- 命令：用 `jq` 匹配 65 个 turn 的 multi-device/bidirectional/offline merge，并回归
  Instance Move、Backup 与 Database Package 测试。
- 结果：明确需求 0；当前只有同一 Instance 的多 Host 访问和显式离线搬迁。
- 结论：不引入 device clock、causal frontier、双向 conflict resolution 或云端协调者。

## F162 Apple Accelerate Route Scan

状态：已评估，资源进入条件未成立，延后。

- 冻结 workload：16-way、3 层完整语义树共 4,368 Route，384 维 float32、Top K 16；
  代表一次 Table Route navigation 的保守上界。
- 冻结门槛：Apple M-series 上 5 × 30 query 的任一 p95 超过 10 ms，或 transient allocation
  超过 16 MiB/query，并且 Accelerate 可保持 reference 等价，才实现平台 backend。
- 命令：`go test -run '^$' -bench BenchmarkRouteExactResourceGate -benchmem
  -benchtime=30x -count 5 ./internal/routeexact`
- 环境：darwin/arm64，Apple M4，10 logical CPU；pure-Go float64 accumulate exact backend。
- 结果：p95 2.393–2.434 ms，8.226 MB/query，4,391 alloc/query；分别低于门槛 75.7%
  与 49.8%。独立随机 reference、tie-break 与授权先过滤测试全绿。
- 结论：当前 Route 扫描相对 LLM 调用仍是毫秒级小项；不加入 cgo/vDSP、平台构建矩阵和
  浮点等价故障域。无权限的能耗采样不伪造成证据，未来若有稳定 receipt 可重新开门。

## F163 HNSW Route Backend

状态：已评估，资源进入条件未成立，延后；未实现 HNSW。

- 冻结 workload：四棵 16-way、3 层完整 Table Route 树，共 17,472 Route；384 维
  float32、Top K 16、单本地 Agent query。
- 冻结门槛：Apple M-series 上 5 × 30 query 的任一 exact p95 超过 50 ms 或 transient
  allocation 超过 64 MiB/query；越门后 ANN 必须在冻结 corpus 达到 Recall@16 ≥ 0.98，
  两层条件同时满足才实现 HNSW。
- 命令：`go test -run '^$' -bench 'BenchmarkRouteExactResourceGate/routes=17472'
  -benchmem -benchtime=30x -count 5 ./internal/routeexact`
- 环境：darwin/arm64，Apple M4，10 logical CPU；相同 pure-Go exact/reference backend。
- 结果：p95 9.527–9.957 ms，33.171 MB/query，17,500 alloc/query；最坏值分别只占
  latency/memory 门的 19.9%/49.4%。
- 结论：exact 资源门未越，故 ANN Recall 条件按冻结的“且”关系短路，不制造无需求 HNSW
  实现或虚假 Recall receipt。CPU exact 保持默认，可替换 backend 接口边界继续保留。

## 路线结论

F151–F163 全部执行：F152 因证据越门已实现 durable free Page reuse；其余候选均留下
可复现结论并延后。后续真实规模或产品故事变化时，从对应冻结命令重新测量，不沿用旧机器
数字作永久结论。
