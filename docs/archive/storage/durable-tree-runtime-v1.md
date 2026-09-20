# Durable Tree Runtime v1

状态：F97d3 已实现并验收，PASS；依赖 F97d1、F97d2，下一项为 F98。

## 唯一结果

一个 Tree space 由单 writer Runtime 串联：

```text
Open → WAL recovery → 读取/创建 bootstrap control → 建立 Buffer Pool
Commit(plan) → Prepare → preflight → WAL durable → PublishBatch → 新 state 可见
```

Runtime 不解释业务 key、不创建 Catalog/Row 索引，也不拥有或关闭 Page Store 与 WAL。

## 冻结接口

```text
OpenRuntime(SegmentSet, PageStore, RuntimeConfig) → Runtime + RecoveryReport
Runtime.State() → Tree control state
Runtime.Read(page_id) → committed Page
Runtime.Commit(transaction_id, MutationPlan) → WAL receipt + new state
Runtime.FlushDirty(limit) → Buffer flush report
```

`RuntimeConfig` 固定 `space_id`、Buffer `capacity` 与 `old_frames`。WAL Segment Set 直接
提供 durable LSN；Page Store 同时作为 Buffer loader/writer 和 recovery target。

## Open 与 recovery

- 每次 Open 先对同一 space 执行幂等 WAL recovery，再创建 Buffer Pool；
- 没有 committed Tree 且 slot 1 缺失时，先写并 Sync bootstrap control；
- recovery 或 bootstrap Sync 失败不返回半初始化 Runtime；
- control 必须能按 v2 解码，且 Buffer 中的初始 control 与磁盘状态一致；
- WAL 已 durable、Buffer 未发布或未刷盘的事务在 reopen 后必须收敛到相同 state。

## Commit 顺序

1. Runtime mutex 串行化 writer，并拒绝 poisoned Runtime；
2. 以当前 control 调用 F97d1 `Prepare`；
3. preflight 所有 existing/new/retired Page 与 control，不写 WAL；
4. WAL 事务写入、Sync 并发布 durable frontier；
5. 使用 WAL 分配的 record LSN 构造 Page after-image 和 root-last control；
6. 调用 F97d2 `PublishBatch`，成功后一次更新 Runtime state。

参数、计划、重复 transaction ID 或 preflight 失败发生在 durable WAL 前，不 poison。

## Poison 与恢复

- WAL `outcome unknown`、WAL 自身 poisoned、或 WAL durable 后 batch 构造/发布失败，
  Runtime 立即 poison；
- poison 后所有 Commit 返回稳定 `ErrRuntimePoisoned`，不得猜测并继续；
- 已返回成功的 Commit 必须同时具有 durable WAL 与原子 committed Buffer view；
- 唯一恢复方式是丢弃 Runtime/Buffer，重新 Open WAL 与 Page Store 并执行 recovery；
- dirty flush 失败保留 dirty Page，不 poison Commit Runtime，仍由 checkpoint/重试处理。

## 并发与边界

- v1 只承诺一个 Runtime writer；并发同 base plan 按 mutex 顺序处理，首个成功后其余
  作为 stale plan 拒绝，不在 Runtime 内静默重算 AI/Planner 决策；
- `Read` 通过 Buffer Pool，可看到完整旧 batch 或完整新 batch，不能看到 control 先行；
- F103 才增加跨多次读取的 snapshot；F104 才增加跨 Runtime 的对象写锁；
- Runtime 不自动 Roll、Checkpoint、后台 Flush 或关闭外部依赖。
