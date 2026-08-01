# F101 Table Row Cursor 开工与完成门

状态：PASS（2026-08-01）。

## 产品审查

用户结果：Table Row 列表按 current index 叶链分页，返回 RowID/revision/state locator，
不再为一个 Page 解码全表 Row revision。

依赖：F90–F100 已完成。F101 只增加 F99 current tree 的范围读取 API，不实现正文
回读、MSQL 切换、HistoryPage、跨调用 snapshot 或写锁。

## RED matrix

1. 当前 native List 会调用 IDs(Row)、解码全部逻辑 Row 后排序；
2. Current Row Index 没有 Table prefix range Page API；
3. after-exclusive、has_more、Table isolation 和三种状态没有契约；
4. 多 leaf 分页没有不重不漏 reference-model 证据；
5. reopen、key/tree corruption 与并发 race 证据缺失。

## GREEN 边界

- 为 Current Row Index 新增 Page(table, after, limit)；
- 复用 B+ Tree Cursor 和 F99 Locator，不新增写路径或持久格式；
- 每次最多读取 limit + 1 个物理叶项；
- 不隐式过滤 tombstone，不读取 Row 正文，不做 scan fallback。

## 完成门

- RED 只因 Page API/行为缺失；
- targeted 重复、跨 leaf reference model、reopen、corruption、race；
- F100/F99/F98/F97d3 与全仓 test/race/vet；
- scripts/ci.sh 全门通过；
- 更新为 PASS，独立 commit 后 fast-forward 合入本地 main。

## 完成证据

- RED：Current Row Index targeted test 仅因 Page/CursorPage API 缺失失败；
- 20 次重复通过 F101、F100、F99、F98、F97d3 与 B+ Tree 回归；
- 900 Row、每页 17 项的跨 leaf reference model 全量等价且不重不漏；
- 覆盖空表、Table isolation、三种状态、after-existing/missing exclusive、
  limit/has_more/next-after 与 crash-before-flush reopen；
- 合法外层 Page 中篡改 key、造成 key/Locator scope 不一致时稳定返回 corruption；
- 全仓 test、race、vet 与 scripts/ci.sh 全部通过。
