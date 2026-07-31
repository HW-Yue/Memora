# ADR-0004：RowID 快速目录与本地最小 MVCC

状态：部分被 [ADR-0005](./0005-btree-mandatory-primary-index.md) 取代。MVCC 与
写锁边界继续有效；第 3–6 项的内存目录/O(1)/B+ Tree 后置结论不再有效。

## 背景

AI 通过 Table Route Tree 找到稳定 RowID 后，数据层必须像正常数据库一样由纯代码
确定性取数，不能再调用模型或做语义匹配。

当前 Executor 已识别 `WHERE row_id = :id` 并进入精确 Get，但 Catalog Service 会
为 `DescribeTable` 重读并组装全部 Database/Table/Column；原生 Row Repository
也会列出并排序全部 Row record ID，再筛选目标 Row 的版本。底层
`record_id → offset` 虽然是内存 Map，完整逻辑 RowID Get 仍不是 O(1)。

Memora 是本地个人数据库，常态是一个 daemon、单 writer、少量并发 reader。完整
复制 InnoDB 的大规模并发物理体系会增加大量复杂度，却不直接改善当前用户故事。

## 决策

1. SQL、主键、事务可见性、错误和数据类型语义参考 MySQL；具体物理实现不要求与
   InnoDB 相同。
2. 精确 RowID 读取全程由 Go 引擎执行：
   `MSQL → exact row_id plan → Catalog Directory → Row Directory → ReadAt →
   decode/project`。
3. daemon 打开 `.memora` 文件时重建内存 Catalog/Row Directory：
   - `database name/alias → database metadata`；
   - `database + table name/alias → table/schema metadata`；
   - `row_id → latest committed revision record`；
   - `row_id + revision → record offset`；
   - `table_id → ordered live row_id`，供稳定 cursor 分页；
   - 必要的 commit sequence 可见性定位。
4. 精确当前 Row Get 的目标复杂度为平均 O(1)，只对选中的 payload 执行 `ReadAt`、
   CRC、decode 和 Schema 校验。
5. Catalog/Row Directory 是可重建加速状态，不是真相源；丢失后从已提交 Record
   Frame 重建，不能单独改变 Schema、Row、History 或 Route。
6. B+ Tree、固定 Page 和 Buffer Pool 只有在 Row 数、启动扫描或内存占用的真实
   数据证明需要时进入，不为点查预先实现。
7. MVCC 是需要保留的正确性能力，但第一版采用本地最小模型：
   - 单 writer 串行提交，多 reader 使用 committed snapshot；
   - autocommit 语句在语句开始固定可见 commit sequence；
   - 显式事务在开始时固定 snapshot，并能读取自己的未提交写；
   - 对 Row、Table/Schema、Route 等明确逻辑对象使用排他写锁；
   - autocommit 持锁到语句终态，显式事务持锁到 commit/rollback；
   - immutable Row revisions 与 commit marker 决定可见性；
   - rollback 丢弃未发布写，不先实现 in-place 更新和物理 Undo chain。
8. 长期语义 History 与 MVCC 分离：History 永久保存来源和业务 revision；MVCC
   只决定并发可见性。

## 明确不照搬

第一版本不实现 gap/next-key lock、范围锁、锁等待队列、死锁检测、多 Buffer Pool
instance、Page latch、doublewrite、change buffer、adaptive hash、Group Commit
或多种可配置隔离级别。

这些能力只有具体用户故事和 benchmark 证明需要时才单独 Review。未来增加 Redo、
Undo 或完整 Tablespace 不能改变 RowID、MSQL、Route locator 或 History 语义。

F104 已采用多对象写按稳定 key 排序后的非等待 batch try-lock；冲突立即返回
`write_conflict`，从而不建立等待环。锁等待与死锁检测只在 F158 的证据门成立后进入。

## 结果

- RowID 点查可以先获得接近哈希目录的速度；
- 本地低并发场景仍有明确 snapshot，不会读到半完成 Mutation；
- 精确对象写锁阻止同一 Row 或结构被并发覆盖，而不阻塞普通 MVCC reader；
- 文件格式继续简单、append-only、可校验和可重建；
- B+ Tree 已由 ADR-0005 提升为必做主索引；内存 Directory 只保留 cache 职责。
