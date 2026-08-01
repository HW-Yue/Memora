# F103 Snapshot Visibility 开工与完成门

状态：已完成；产品门与完成门 PASS。

## 产品门

- 目标故事：`US-QUERY`、`US-HISTORY`、`US-CONFLICT`；长读取不混入后来 commit，
  事务仍能读取自己的 staged Row。
- 标准旅程：捕获 high-water → 多次 point/page read → 并发 commit → 旧 view 不变 →
  新 autocommit statement 可见新 revision。
- 影响范围：Row read visibility 与 private overlay；不改变语义 Router、MSQL 或正文。
- 上下文：纯引擎内部，不增加任何模型上下文。
- 永久边界：commit sequence、WAL LSN、Tree revision、Schema revision 各自独立。
- 架构确认：逻辑 sequence snapshot 取代不可持久的旧-root pin；F107 必须按 high-water
  最后发布顺序接写路径。
- 用户执行授权：F81–F109 持续授权已记录。
- 唯一主要结果：固定 read view 在并发更新后仍返回同一 committed Row，并读 own writes。
- 开工前结论：PASS。

## RED matrix

| Case | 当前缺口 | 期望 |
| --- | --- | --- |
| high-water | Version Tree 无 O(log B) snapshot 边界 | Append 原子推进且 reopen 保持 |
| long point read | F102 总读 latest Current | update/delete/insert 后旧 view 不变 |
| new statement | 无 statement capture | 新 view 看见新 commit |
| AS OF cap | 后来 revision 可能越过 view | revision/commit 不晚于 snapshot |
| Table pages | 跨页期间 Current 会变化 | 不重不漏，不泄漏 snapshot 后 Row |
| own writes | indexed view 无 transaction overlay | staged insert/update/delete 优先可见 |
| rollback | overlay 可能泄漏 | discard 后共享读结果不变 |
| corruption/reopen/race | marker 或调度错误被掩盖 | 稳定错误、恢复一致、无 data race |

首个 RED：indexed Executor 的 PointReads 尚不能捕获 sequence，且 Version Index 没有
`HighWater/VisibleAt`。

## 完成门

- high-water/legacy anchor codec、原子 Append、legacy 与已发布历史封存、idempotency、
  reopen、corruption；
- 可控 writer 调度下 statement/transaction point 与多页 reference model；
- overlay insert/update/delete、rollback、1000 Row 边界与 race；
- F98–F102 回归、targeted repetition、全仓 unit/vet/race/CI；
- 完成后结论：PASS。statement fresh-snapshot、固定 point/page view、AS OF cap、
  own-writes/discard、1000 Row 界限、可控 publish 调度与 120 Row reference model
  已覆盖；targeted repetition、全仓 unit/vet/race/CI 均通过。
