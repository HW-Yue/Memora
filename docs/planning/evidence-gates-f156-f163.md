# F156–F163 按证据触发门

状态：执行中；前序空间与资源门见 [F151–F155](./evidence-gates-f151-f163.md)。

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

## 后续门

F162–F163 到达时在本文件追加冻结门槛、命令、环境、原始摘要和结论。
