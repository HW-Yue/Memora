# Table Row Cursor v1

状态：F101 已完成；Page 行为契约已冻结。

## 唯一结果

F99 Current Row B+ Tree 可按 Table ID 范围返回有界、有序的 current locator Page，
每项明确携带 live/deleted/superseded 状态。调用方不再枚举 Record ID、解码 Row
正文或重建全表 logical Row 集合后再分页。

F101 复用 F99 的 table_id + row_id 键，不创建重复树。F102 才切 MSQL point-get；
Table List 的正文回读接线留给后续执行器；F103 已在 native `ReadView` 中固定跨页
snapshot，并将 Current 的稳定 RowID 与 private overlay 合并。

## Page 契约

输入：

- Table ID；
- 可空的 after Row ID，语义为 exclusive；
- limit 1–1000。

输出：

- 最多 limit 个 current locator；
- locator 顺序严格等于持久化二进制键顺序；
- has_more；
- 有下一页时返回最后一项 Row ID 作为 next-after cursor。

一次调用只读取 limit + 1 个叶项来判断 has_more。它不按状态隐式过滤：live、
deleted、superseded 都返回，由调用方显式决定可见性；这样即使 Table 充满 tombstone，
Page I/O 仍受 limit 约束。

## 边界

- Table prefix 和 after key 都使用与 F99 相同的版本化长度前缀编码；
- 空 Table 返回空 Page，不把它当成 point not-found；
- after Row ID 即使已不存在，仍从其二进制排序位置继续；
- key 解码和 Locator 的 Table/Row scope 必须一致，否则 corruption；
- 单次 physical Page 在 Index read lock 内看到一致 root；F103 ReadView 对每个 Row 按
  固定 commit sequence 求 Version floor，snapshot 后新增 key 可被扫描但不会泄漏；
- 不返回 Row values，不提供 SQL offset，不做旧 Store scan fallback。

## 完成证据

- 空/单页/多页、exclusive cursor、Table isolation、三种状态；
- 跨 leaf 分页不重不漏，并与排序 map reference model 一致；
- 每页最多 limit，has_more/next-after 正确；
- crash-before-flush reopen 后继续分页；
- key/tree corruption 无 fallback；
- F100/F99/F98/F97d3 回归、targeted repetition、race 与全仓 CI 通过。
