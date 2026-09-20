# Snapshot Visibility v1

状态：F103 已完成；Row read-view 契约已冻结。

## 唯一结果

Row reader 以一次捕获的 durable commit-sequence high-water 作为逻辑 read view；
autocommit 每条精确 SELECT 捕获一次，显式事务可复用同一 view 并优先读取私有
Row overlay。后续 commit 不改变已存在 view 的结果。

旧 B+ Tree root 不是 snapshot authority：普通 mutation 可更新相同 Page ID。Current
Row Tree 只枚举稳定 RowID，实际可见 revision 始终由 append-only Row Version Tree
按固定 sequence 求 floor。

## 持久 high-water 与发布顺序

Row Version Tree 新增单一 `snapshot/high-water` key，值是所有已完整发布 Row version
的最大正 commit sequence；只有 legacy sequence 0 时值为 0。它与一次 Append 的全部
revision/commit/identity/legacy-anchor key 在同一 F97d3 WAL transaction 发布。

新 Page 写路径必须保持：

```text
append immutable Row body
→ atomically update all Current Row locators
→ atomically append all Version locators + advance high-water
```

读者可以在前两步后捕获旧 high-water：超前 Current locator 会按旧 sequence 回溯，
因此仍不可见。high-water 不得早于正文或 Current 发布；F107 接线必须验证该顺序。

sequence 0 没有时间线位置；Version Tree 另存每 Row 的最高 legacy revision anchor，
只供 snapshot floor 在第一个正 sequence 发布前保持旧 Row 可见。显式 `AS OF COMMIT`
仍不把 legacy anchor 伪装成 commit 结果。所有 sequence 0 locator 必须在空 Version
Tree 的第一次原子 Append 中完成离线导入；high-water marker 一旦存在，legacy import
即封存，只允许幂等重放已有 revision。正常运行时的新 mutation 必须使用正 sequence，
避免无时间线位置的 legacy revision 穿透已经固定的 read view。

marker 同时封存已经发布的正 sequence 历史：后续新增 revision 的 commit sequence
必须严格大于当前 high-water；小于或等于 high-water 的新历史 locator 会在 WAL 前整批
拒绝，已有 locator 的幂等重放除外。同一 publication batch 可以带多个递增 sequence，
high-water 原子前进到其中最大值；因此捕获后的 Append 不会改变该 snapshot 的 floor。

## Read View

- current point-get：Current key 确认稳定身份，Version `VisibleAt(row_id, snapshot)`
  决定 revision；当前 locator 不晚于 snapshot 时必须与 visible locator 相同；
- AS OF revision：目标 locator 的 sequence 必须为 0 或不晚于 snapshot；
- AS OF commit：实际上限为 `min(requested, snapshot)`；
- Table Page：F101 的 physical RowID page 与 transaction overlay 按 RowID 合并，每个
  base Row 再求 snapshot floor；cursor 以前次已扫描的 RowID 为 exclusive 边界；
- private overlay 最多 1000 个 Row，point-get 和 Table Page 都优先返回 own writes；
- discard/rollback 后 view 关闭，overlay 不进入共享 Index、WAL 或正文文件。

## 错误与边界

- high-water/legacy anchor 缺失、倒退、codec 损坏或 locator 顺序矛盾是 corruption；
- marker 创建后的新 sequence 0 revision，或不晚于 high-water 的新历史 revision，都是
  conflict，且不得写入 WAL；
- snapshot 前不存在的 Row 返回 not-found，之后 insert/update/delete 不泄漏进旧 view；
- Page 可返回少于 limit 个 locator；被 snapshot 过滤的 physical key 仍推进 cursor，
  保持单次 I/O 有界；
- Catalog/Schema 的 MVCC、写锁、Page Store 写接线分别留给后续 Feature；F103 不把
  mutable root、wall clock 或 WAL LSN 当作 commit sequence。
- F100 时代已存在但缺少 high-water marker 的非空 Version Tree 不静默猜测；它在
  indexed read 时报告 corruption，由 F106 在 Page Store 切权前离线重建完整树。

## 完成证据

- statement 每次只捕获一个 snapshot；后续 statement 可见新 commit，旧 view 不变；
- Current 已超前但 Version 未发布、发布后旧 view、正常新 view三种调度结果正确；
- point、AS OF revision/commit 与跨页 cursor 在后续 update/delete/insert 后匹配
  reference model；
- own update/delete/insert 覆盖 point/page，discard 不泄漏，overlay 上限 1000；
- high-water/anchor 原子 WAL、迟到历史封存、幂等、reopen、corruption 与并发 race；
- targeted repetition、全仓 unit/vet/race/integration/e2e/cross-build 全绿。

## 关联

- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
- [Row Version Index v1](./row-version-index-v1.md)
- [Table Row Cursor v1](./table-row-cursor-v1.md)
- [MSQL Indexed Point-Get v1](../query/msql-point-get-v1.md)
