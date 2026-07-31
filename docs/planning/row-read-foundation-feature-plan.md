# 存储内核小 Feature 计划

状态：F81–F109 执行顺序及持续实现已获用户授权；F81–F97c4 已完成，下一项为
F97d1 Tree Commit Preparation。后续仍逐项 Review、测试、验收和合入，但无需等待
重复授权。

## 当前缺口与目标

```text
当前：exact RowID SQL → 重读 Catalog → 列举/排序全部 Row revision → offset Map
目标：MSQL → B+ Tree → Buffer Pool → Page/Record
写入：private write set → Redo WAL → committed view
重建：COW generation → validate → root swap
```

物理决策见 [ADR-0005](../decisions/0005-btree-mandatory-primary-index.md)和
[ADR-0006](../decisions/0006-mysql-page-buffer-wal-cow.md)。

## Page 与 WAL

| Feature | RED 先证明 | GREEN 的唯一结果 | 明确不做 |
| --- | --- | --- | --- |
| F81 Page Codec | golden 不能 round-trip；错 header/checksum 被接受 | 16 KiB Page 编解码与校验 | 文件 I/O、WAL |
| F82 Page File Manager | 短读写/错 identity/reopen 返回错误结果 | 按 space/page identity 安全 ReadAt/WriteAt | cache、WAL |
| F83 WAL Record Stream | 半条、错 CRC、乱序 LSN 被接受 | segment append/scan 与 durable offset | 事务 commit、recovery |
| F84 WAL Durable Transaction | COMMIT 未 fsync 也报告成功 | transaction boundary 与 durable COMMIT | redo apply、checkpoint |
| F85 Crash Recovery | 未提交尾部被重放；重复恢复改坏 Page | committed redo 幂等重放、FPI 修复 torn Page | checkpoint 回收 |
| F86a Segment Set | 单 Segment 无法安全滚动 | 连续 ID/LSN 的 roll、reopen、跨段 scan | checkpoint、删除 |
| F86b Checkpoint Publish | 未刷 Page 也推进恢复起点 | durability barrier 后发布 checkpoint | Segment 删除 |
| F86c Segment Reclaim | 删除仍被恢复需要的 WAL | 只回收 checkpoint 完全覆盖的旧 Segment | 后台策略 |

Page 强制 golden/seed corpus/corruption/reopen；WAL 强制覆盖每个 write/fsync fault
point、truncate、bit flip、乱序与 subprocess crash。F86c 已通过独立完成门；业务
写路径仍须等待后续 Buffer Pool、B+ Tree 与迁移 Feature 独立验收后才能切换。

## Buffer Pool

| Feature | RED 先证明 | GREEN 的唯一结果 | 明确不做 |
| --- | --- | --- | --- |
| F87 Page Loading | 同 Page 重复 I/O；使用中被释放 | page table、single-flight、pin/unpin、latch | 淘汰、dirty |
| F88 Eviction | pinned 被淘汰；扫描挤掉全部热点 | 有界 young/old LRU | dirty flush、分片 |
| F89 Dirty Flush | WAL 未 durable 就刷 Page；dirty 被丢弃 | page LSN、flush list、WAL-before-data | 自适应 cleaner |

使用 fake pager 和可控调度验证确定性状态；受影响 package 必须通过 `-race`，容量
测试必须证明 Frame 数不超过硬上限。

## B+ Tree

| Feature | RED 先证明 | GREEN 的唯一结果 | 明确不做 |
| --- | --- | --- | --- |
| F90 Node Codec | 损坏节点/非法 slot 被接受 | internal/leaf codec 与 checker | 查找和 mutation |
| F91 Point Search | 空树/多层/边界 key 定位错误 | root-to-leaf 精确查找 | range、mutation |
| F92 Range Cursor | leaf link 续读重漏或越界 | 有界 forward cursor | mutation |
| F93 Insert | 未满节点插入后顺序或替换错误 | 单节点 insert/update | split |
| F94 Split | 满节点插入丢 key 或 separator 错 | leaf/internal split、root grow | delete |
| F95 Delete | 删除后 key 仍可见或误删邻居 | key 删除与 tombstone handoff | rebalance |
| F96 Rebalance | underflow 后叶链/占用/root 错 | borrow、merge、root shrink | 持久事务接线 |
| F97 Durable Root | commit/reopen 丢 root 或 crash 不一致 | F97a–F97c4 已完成；下一项 F97d1，再执行 F97d2–F97d3 | 业务 key space |

F90–F97 使用手工 fixture、排序 reference model、保存 seed 的随机操作和每步不变量
检查。F93/F95 的中间树只用于 package 内测试，不能在 F94/F96 完成前接业务路径。
F97 拆分 Review 见 [F97 Durable Root 开工门](./f97-durable-root-gate.md)。
F97a 冻结契约见
[B+ Tree Mutation Plan v1](../storage/btree-mutation-plan-v1.md)。
F97b 修订证据见
[WAL Recovery Open 拆分 Review](./f97b-wal-recovery-open-review.md)。

## 真实数据路径

| Feature | RED 先证明 | 唯一结果 |
| --- | --- | --- |
| F98 Catalog Lookup | Describe 重读完整 Catalog | identity/name/alias/Schema revision 走树 |
| F99 Current Row | RowID Get 扫其他 revision | current Row locator 走树 |
| F100 Row Version | as-of/history 扫其他 Row | revision/sequence locator 走树 |
| F101 Table Cursor | 分页 decode 全表且重漏 | live/tombstone Row 有序分页 |
| F102 MSQL Point-Get | exact predicate 仍走旧 IDs | Executor 切新索引且 envelope 等价 |
| F103 Snapshot Visibility | 长 reader 混入新 commit | statement/transaction snapshot 与 own writes |
| F104 Write Lock | 同对象写同时通过 | 精确 Row/Schema/Route 排他锁，无 gap lock |
| F105 Migration Reader | 旧 Store 不能确定枚举/计划 | 只读 inventory 与迁移 plan |
| F106 Migration Apply | 中断得到混合 authority | apply/verify，失败恢复完整旧 Store |
| F107 Default Switch | 新写仍可进入旧路径 | Page Store 成为唯一新 authority |
| F108 COW Replacement | 失败 rebuild 改坏当前 root | build/validate/atomic root swap |
| F109 Change Log | rollback 或半事务出现在时间线 | 同 WAL 事务的完整 change envelope |

F98–F109 都覆盖 success、not-found/conflict/corruption、reopen 和上一 Feature 回归。
不得保留旧全量扫描作为静默 fallback；F107 需用测试证明旧 authority 不可到达。

## 每项 TDD 与合入

1. Review 一项 Feature 的用户结果、依赖、RED matrix 和不做范围；
2. 本地确认 RED 因能力缺失失败，不因编译、随机时间或坏 fixture 失败；
3. 只写使当前 Feature GREEN 的最小实现；
4. 运行 targeted、全量、race、vet、format 及对应 fault/fuzz-seed suite；
5. 记录格式/恢复兼容证据，独立合入 `main`；
6. 合入后才 Review 下一项。

详细规则见[小 Feature TDD 与合入协议](./feature-tdd-protocol.md)。
