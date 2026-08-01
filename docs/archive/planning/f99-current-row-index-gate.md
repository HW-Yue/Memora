# F99 Current Row Index 开工与完成门

状态：PASS（2026-08-01）。

## 产品审查

用户结果：Route 已给出 RowID 后，引擎用 Table ID + RowID 直接得到当前已提交
revision 和状态，不再列举、排序并解码其他 Row revision。

依赖：F90–F98 已完成。F99 只建立 current locator key space，不实现 F100 历史
版本键、F101 Table Cursor、F102 Executor 切换或 F103 snapshot visibility。

## RED matrix

1. 当前 ReadIncludingDeleted 会调用 IDs(Row) 并筛选全部候选 revision；
2. 不存在持久化 Table ID + RowID current key space；
3. insert/stale expected/revision jump/commit sequence 倒退没有索引级原子拒绝；
4. tombstone/superseded current locator 与 scope 精确读取缺失；
5. crash-before-flush、reopen、corruption 和 split reference model 证据缺失；
6. 并发 same-base 更新没有“一个提交、其余 WAL 前冲突”证据。

## GREEN 边界

- 新增 Current Row key/Locator codec、point lookup 与原子 Apply；
- 复用 B+ Tree 和 Durable Tree Runtime，不复制 WAL/Page 协议；
- 不修改 Row 正文编码，不接旧 Repository 或 MSQL Executor；
- 不提供 scan fallback、历史查找、范围分页或多版本可见性。

## 完成门

- 先确认 RED 只因目标 API/行为缺失；
- targeted 重复、corruption、reopen、reference model、并发与 race；
- F98/F97d3 回归、全仓 test/race/vet；
- scripts/ci.sh 全门通过；
- 更新为 PASS，独立 commit 后 fast-forward 合入本地 main。

## 完成证据

- RED：targeted test 仅因 Update、Locator、Index、Apply、Lookup 和稳定错误缺失失败；
- 20 次重复通过 current row、F98 Catalog、F97d3 Runtime 与 B+ Tree 回归；
- 700 Row reference model 触发 internal root split，批量更新后逐项等价；
- 32 个 same-base 并发更新仅 1 个提交，31 个在 WAL 前返回 conflict；
- 覆盖整批 stale 原子失败、幂等零 WAL、revision/sequence/schema 单调门、三种状态、
  crash-before-flush reopen、Locator corpus 与合法外层 Page 中的树损坏；
- 兼容 F17 前 commit sequence = 0 的 Row，且后续 revision 必须推进到正 sequence；
- 全仓 test、race、vet 以及 scripts/ci.sh 全部通过。
