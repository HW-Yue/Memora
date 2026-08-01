# F151–F163 按证据触发门

状态：执行中；每个门先冻结条件与 workload，再记录结果。未越门槛只代表当前延后，
不是永久取消。

## F151 Compaction

状态：已评估，进入条件未成立，延后。

- 冻结门槛：固定宽度的 256 条 current-row locator，在基线树上连续更新 16 轮后，
  Page file 的 `final_pages / baseline_pages > 1.25` 才进入 Compaction。
- 命令：`go test -run TestCompactionEvidenceGate -v ./internal/store/currentrowindex`
- 环境：darwin/arm64，Go 当前项目 toolchain，16 KiB Page，单实例 Buffer Pool。
- 结果：基线 5 Page，最终 5 Page，空间放大 `1.00x`；未越过 `1.25x`。
- 结论：当前等宽 revision churn 走原位 Page 更新；不为尚未出现的空间问题加入
  generation rewrite、reader handoff 和回收故障域。

## F152 Free Page Reuse

状态：门槛成立并已实现。

- 冻结门槛：320 个 Database 的 Catalog 基线删除末尾 10% 并加入等量新对象后，
  `final_pages / baseline_pages > 1.20` 即实现复用。
- 命令：`go test -run TestFreePageReuseEvidenceGate -v ./internal/store/catalogindex`
- RED：9→11 Page，浪费 22.22%，越过 20% 门。
- GREEN：分配优先使用同计划 recycled Page，再使用重启后从 durable free image 重建的
  free set；同一 workload 为 9→9 Page，浪费 0%。
- 恢复证据：retire/flush/reopen 后可发现 free Page；split 优先复用并保持 lookup 等价；
  非 free 候选、错误顺序/身份和 publish 冲突均拒绝。

## F153 Secondary Indexes

状态：已评估，进入条件未成立，延后。

- 冻结门槛：至少两个 canonical AI journey 明确要求在 10,000+ Row 上执行非
  `row_id` 精确字段/范围谓词；随后 bounded scan p95 超过 20 ms 才进入索引设计。
- 命令：用 `jq` 汇总全部 turn，并匹配 `secondary index|10,?000|non-row_id|field predicate`。
- 结果：canonical suite 共 65 turn，显式需求 0；第一层产品门未成立，因此不以合成
  SQL benchmark 替代真实故事。
- 结论：MSQL 已可用有界 scan 执行 typed predicate；继续以语义 Route + RowID 主路径为主，
  不提前引入每次写入都需维护的通用 Secondary Index。

## F154 Buffer Pool Scaling

状态：已评估，进入条件未成立，延后。

- 冻结门槛：32 个 resident hot Page 的 parallel Fetch/Release 在 5 次运行中最慢均值
  超过 5 µs/op，或 warm-up 后发生 loader miss，才拆分 Buffer Pool instance。
- 命令：`go test -run '^$' -bench BenchmarkBufferPoolHotHitParallel -benchmem -count 5
  ./internal/store/buffer`
- 环境：darwin/arm64，Apple M4，10 logical CPU；32 hot Page、64 Frame、无 I/O。
- 结果：234.9–236.8 ns/op，48 B/op，1 alloc/op；warm-up 后全部命中。
- 结论：最慢均值只占门槛 4.74%，当前单 Pool mutex 没有形成资源瓶颈；保留 benchmark，
  不增加分片映射、跨池预算和 rebalance 故障域。

## F155 Advanced I/O Scheduler

状态：已评估，进入条件未成立，延后。

- 冻结门槛：真实 Page file 上串行 dirty flush 64 × 16 KiB（1 MiB），5 次运行中最慢
  均值超过 5 ms/batch，才引入并发 Page Cleaner、自适应 I/O capacity 或队列优先级。
- 命令：`go test -run '^$' -bench BenchmarkFlushDirty64PageBatch -benchmem -count 5
  ./internal/store/buffer`
- 环境：darwin/arm64，Apple M4；已 resident Page、durable WAL frontier、真实 `pwrite`，
  不把 checkpoint/fsync 混入 Page Cleaner 测量。
- 结果：356.733–359.502 µs/batch；最慢值为门槛的 7.19%。
- 结论：简单有界串行 scheduler 尚未造成 flush 延迟压力；2.39 MB/batch 的 clone allocation
  单独保留为内存优化观察项，不足以授权新的 I/O 并发协议。

## F156 Physical Undo

状态：已评估，结构进入条件未成立，延后。

- 冻结门槛：生产写路径出现 uncommitted dirty Page steal，或 Row body 在 commit 前原位
  覆盖，任一成立即进入 Physical Undo/Purge 设计。
- 命令：运行 `TestRuntimeCommitPublishesDurablePagesAndReopenRecovers`、
  `TestRuntimeRejectsBeforeWALWithoutPoisoning`、`TestCrossObjectMutationFailureLeavesNoPartialObjects`，
  并审计 `CommitTransaction` 与 `PublishBatch` 的唯一生产调用顺序。
- 结果：Tree Runtime 先获得带 COMMIT 的 durable WAL transaction，之后才一次发布 Page batch；
  commit 前 mutation 保持私有，跨对象 staging 失败不留下 Row/History/Relation 部分状态。
- 结论：当前是 no-steal + immutable revision，不存在 Physical Undo 要撤销的物理状态；Redo
  recovery 与逻辑补偿继续承担各自边界。

## F157 Advanced MVCC

状态：已评估，产品进入条件未成立，延后。

- 冻结门槛：canonical journey 中出现 multi-writer 或明确要求 serializable、repeatable
  read、snapshot isolation 中任一强隔离语义，才扩展当前最小 MVCC。
- 命令：用 `jq` 在 65 个 canonical turn 匹配上述需求，并运行 snapshot reference-model
  与 same-base concurrent update 测试。
- 结果：明确需求 0；固定 snapshot 分页与后续 mutation 对拍一致，同一 base revision 的
  并发更新仍只有一个成功。
- 结论：单 writer、snapshot sequence、immutable revision 和精确对象写锁覆盖现有旅程；
  不增加 transaction graph、predicate lock 或多 writer validation。

## F158 Lock Waits/Deadlock

状态：已评估，产品进入条件未成立，延后。

- 冻结门槛：至少两个 canonical journey 因 fail-fast `conflict` 无法完成、且有明确可接受
  wait budget，才加入 lock wait、timeout 和 deadlock detector。
- 命令：用 `jq` 匹配 65 个 turn 的 lock-wait/deadlock 需求，并执行
  `go test -v ./internal/store/objectlock`。
- 结果：明确需求 0；seeded reference model、原子 batch conflict、cancel 和 opposite-order
  并发全部通过，后者产生一个 winner 而不等待/死锁。
- 结论：对本地 Agent 写入，快速返回稳定 conflict 让上层重新读取/重计划更可解释；不引入
  等待队列、公平性、超时和 wait-for graph。

## F159 Replication

状态：已评估，产品进入条件未成立，延后。

- 冻结门槛：出现明确 primary→replica 旅程，包含拓扑、期望 RPO/RTO、读一致性与
  failover owner；仅“换宿主读取同一 Instance”不算复制。
- 命令：用 `jq` 匹配 canonical turn 的 replica/replication/standby/failover，并运行
  committed Change Log cursor/index 测试，确认未来输入基础没有腐化。
- 结果：65 turn 中明确复制需求 0；Change Log 仍可按 commit sequence 确定性读取。
- 结论：逻辑变化流只是未来武器库，不自行升级成网络拓扑；不引入 replica identity、
  acknowledgement、lag、promotion 或 split-brain 处理。

## 后续门

F160–F163 到达时在本文件追加冻结条件、命令、环境、原始摘要和结论；如果条件成立，
先另开实现 Feature，不把大实现塞进证据门提交。
