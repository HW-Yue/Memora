# MSQL Indexed Point-Get v1

状态：F102 已完成；读取契约已冻结。

## 唯一结果

配置了 indexed point reader 的 MSQL Executor 对精确
`WHERE row_id = literal|parameter` 使用 F98–F100 的持久化 B+ Tree，且不调用旧
Catalog/Row 全量扫描。未配置的 legacy constructor 保持兼容，daemon 默认 authority
等 F105–F107 迁移完成后再切换。

本 Feature 只改变 autocommit SELECT 的物理读取路径；MSQL、RowID、projection、
Result Envelope、语义 Router 和正文格式不变。

## 读取链路

```text
database/table name
→ Catalog Lookup Index
→ exact native Catalog records + Table-scoped Column cursor
→ Current Row Index 或 Row Version Index
→ exact native Row revision record
→ existing projection / WHERE / Result Envelope
```

- current point-get 先读取 Current Row locator，再读取同 revision 的 immutable version
  locator；两者必须完全一致；
- `AS OF REVISION` 直接读取 revision locator；
- `AS OF COMMIT_SEQUENCE` 读取不晚于目标 sequence 的 floor locator；
- locator 与 Catalog、正文的 Database/Table/Row/revision/sequence/state/schema 任一不一致
  都是 corruption；
- Catalog 表的 Column 只遍历该 Table 的 `column/name` 前缀并按原生记录 order 恢复，
  不枚举完整 Catalog。

## 兼容与错误

- current Row 不存在、deleted 或 superseded：SELECT 成功且返回空 rows；
- AS OF 目标不存在：稳定 `not_found`；
- 非精确 predicate 继续使用既有 Table scan；
- indexed lane 的 not-found、corruption、取消或 I/O 错误不得回退旧扫描；
- 成功结果的 columns、rows、类型、truncated 与 legacy lane byte-for-byte JSON 等价；
- active explicit transaction 暂走 transaction 自身读取以保留 own writes；F103 冻结
  snapshot/root 可见性后再统一。

## 明确不做

- 不让 Page Store 成为新写 authority，不在 daemon 创建默认 index 文件；
- 不双写 Catalog/Row mutation，不迁移旧数据；
- 不切换非精确 Table scan、SHOW HISTORY、mutation 或 Router；
- 不定义 snapshot visibility、锁或静默 fallback。

## 关联

- [Catalog Lookup Index v1](../storage/catalog-lookup-index-v1.md)
- [Current Row Index v1](../storage/current-row-index-v1.md)
- [Row Version Index v1](../storage/row-version-index-v1.md)
- [MSQL SELECT Planner v1](./msql-select.md)
