# 存储层：当前实现总览

本文描述存储层**现在实际是什么样**，是读懂这一层的唯一入口。
其余文档按 Feature 切分、在各自验收门通过时冻结，是证据链而非现状描述——
本文每一节末尾指向对应的冻结规格。

设计终点见[聚簇行存储 v1](./clustered-row-storage-v1.md)。
现役实现与终点的差距在文末「已知偏差」一节逐条列出。

**编写原则**：每条断言都能指到具体文件或函数。指不到的不写。

---

## 1. 物理层：Page

- Page 固定 **16 KiB**，Header **64 字节**，可用负载 `Size - HeaderSize`
  （`internal/store/page/page.go:12-17`）；
- Header 含 magic `MEMOPG01`、format version、Page Type、`space_id`、`page_id`、
  generation、LSN、负载长度与 **CRC32C**（Castagnoli 表，`page.go:28`）；
  checksum 错误一律硬失败，不返回旧值或空结果；
- Page Type：`Data`／`BTreeInternal`／`BTreeLeaf`／`Free`／`Manifest`／
  `Overflow`／`TreeControl`（`page.go:34-42`）。**`Overflow` 只有类型常量，
  尚无实现**——超大记录目前硬失败而不是溢出到额外 Page；
- 定址：`pageOffset(pageID) = pageID * Size`（`internal/store/page/file.go:264`），
  一个 `space_id` 对应一个 Data File，由 `page.Manager` 管理，
  只 `ReadAt`／`WriteAt` 单页，不整文件读入内存
  （`Read`/`Write`/`Sync`/`PageCount`，`file.go:122-216`）。

细节：[Page Codec v1](./page-codec-v1.md)、[Page File Manager v1](./page-file-manager-v1.md)

## 2. Redo WAL

- segment 集合与滚动：`internal/store/wal`，`CreateSegmentSet`／`OpenSegmentSet`；
- 单 writer 的事务语义、durable frontier（哪些 LSN 已落盘）、
  打开时的恢复重放、checkpoint 之后的 segment 回收；
- dirty Page **只有在对应 WAL 已持久化之后**才允许刷回（no-steal）。

细节：[WAL Record Stream](./wal-record-stream-v1.md)、
[Durable Transaction](./wal-durable-transaction-v1.md)、
[Durable Frontier](./wal-durable-frontier-v1.md)、
[Recovery on Open](./wal-recovery-open-v1.md)、
[Segment Set](./wal-segment-set-v1.md)、[Segment Reclaim](./wal-segment-reclaim-v1.md)

## 3. Buffer Pool

- **单实例、硬容量上限**：`buffer.Config{Capacity, OldFrames, Writer, Durability}`
  （`internal/store/buffer/buffer.go:44-49`）；
- Page Table 按 `buffer.Key{SpaceID, PageID}` 定位 frame；
- 淘汰参考 InnoDB 的 **young/old LRU 与 midpoint insertion**，
  保护热点不被扫描冲掉（`young`／`old` 两条链表，`buffer.go:51-61`）；
- frame 具 pin count、dirty 标记；`Stats()` 暴露
  frames／loading／pins／young／old／evictions／dirty；
- `PublishBatch` 做原子批量发布（`internal/store/buffer/batch.go:20`）。

**这是与数据量相关的唯一一处有上界的常驻内存。** 见第 7 节的例外。

细节：[Buffer Pool](./buffer-pool.md)、[Page Loading](./buffer-pool-page-loading-v1.md)、
[Eviction](./buffer-pool-eviction-v1.md)、[Dirty Flush](./buffer-pool-dirty-flush-v1.md)、
[Atomic Publish](./atomic-buffer-publish-v1.md)

## 4. B+Tree

- 节点：`btree.Node{Kind, Level, NextLeafPageID, LeftmostChild, LeafEntries,
  InternalEntries}`（`internal/store/btree/node.go:43-51`）；
- `LeafEntry{Key, Value []byte}`——**value 是任意字节，上限约一个 Page 的负载**，
  装得下整条记录；
- `InternalEntry{Key, RightChild uint64}`——**`RightChild` 是子页 `page_id`**；

> **B+Tree 内部导航是按物理地址跳转的。** 内部节点存子页 `page_id`，
> `pageOffset(pageID) = pageID * Size` 直接算出文件偏移，单页 `ReadAt` 进 Buffer Pool。
> 叶子之间由 `NextLeafPageID` 串成链，范围扫沿链前进。
> 这一条被反复误解过，写在这里以免再犯。

- 能力：点查（`NewSearcher`／`Get`）、范围游标（`NewCursor`／`Next`，
  `internal/store/btree/cursor.go:29,42`）、单节点 upsert、split、rebalance、
  leaf delete、以及把一批改动规划成 Page 变更集的
  `MutationPlanner`（`internal/store/btree/plan.go:31`）。

细节：[Node Codec](./btree-node-codec-v1.md)、[Point Search](./btree-point-search-v1.md)、
[Range Cursor](./btree-range-cursor-v1.md)、[Single Node Upsert](./btree-single-node-upsert-v1.md)、
[Split](./btree-split-v1.md)、[Rebalance](./btree-rebalance-v1.md)、
[Leaf Delete](./btree-leaf-delete-v1.md)、[Mutation Plan](./btree-mutation-plan-v1.md)

## 5. 树的提交协议

- `treecontrol` 持有每棵树的根状态
  `State{SpaceID, Generation, Revision, RootPageID, NextPageID, LSN}`，
  落在 `TreeControl` 类型的 Page 上；
- `treecommit.Runtime` 是各索引包共用的运行时：
  `State()`／`FreePageIDs()`／`Read(pageID)`／`Commit(transactionID, plan)`／
  `FlushDirty(limit)`（`internal/store/treecommit/runtime.go:137-239`），
  配置为 `RuntimeConfig{SpaceID, Capacity, OldFrames}`；
- 提交是 **durable-then-publish**：WAL 先落盘，再发布可见状态；
- 整代替换走 **COW generation replacement**：旁路构建新 generation，
  完整验证并 durable 之后一次切换 marker，失败不改变旧代。

细节：[Tree Control v2](./tree-control-v2.md)、
[Root/Allocator Redo v2](./root-allocator-redo-v2.md)、
[Tree Commit Preparation](./tree-commit-preparation-v1.md)、
[Durable Tree Runtime](./durable-tree-runtime-v1.md)、
[Tree Metadata Recovery](./tree-metadata-recovery-v1.md)、
[Checkpoint Publish](./checkpoint-publish-v1.md)、
[Crash Recovery](./crash-recovery-v1.md)、
[COW Generation Replacement](./cow-generation-replacement-v1.md)、
[Free Page Reuse](./free-page-reuse-v1.md)

## 6. 现有的树

一个 Database 目录下有一个 **generation**（当前格式
`memora.page-index-generation/v3`，`internal/pagestoremigration/manifest.go:23`），
含四棵树；Change 树独立于 generation 之外。

| 树 | space_id | key | leaf value | 位置 |
| --- | --- | --- | --- | --- |
| catalog | `MEMCAT` | 6 种：Database/Table/Column 的 ID 与 name | **逻辑 Locator**，无正文 | `store/catalogindex` |
| current | `MEMCUR` | `(table_id, row_id)` | **逻辑 Locator**，无正文 | `store/currentrowindex` |
| versions | `MEMVER` | 4 种：`revision`／`commit`／`identity`／`legacy` | revision 键带 **Row 正文 + history 元数据**，其余无 | `store/rowversionindex` |
| fulltext | `MEMFTX` | object／owner／posting | 倒排 posting | `store/fulltextindex` |
| change | `MEMCHG` | commit sequence | 逻辑 Locator + checksum | `store/changeindex` |
| **objects** | — | `kind‖id` | **正文** | `store/objectindex`，**已建好、尚未接线** |

`versions` 树按 `(rowID, revision)` 建键，**一个版本一个条目**；
`revisionKey` 让同一行的版本在叶子里连续排列，当前版本排在最后。

细节：[Catalog Lookup Index](./catalog-lookup-index-v1.md)、
[Current Row Index](./current-row-index-v1.md)、
[Row Version Index](./row-version-index-v1.md)、
[Table Row Cursor](./table-row-cursor-v1.md)、
[Snapshot Visibility](./snapshot-visibility-v1.md)、
[Committed Change Envelope](./committed-change-envelope-v1.md)、
[Committed Change Page Index](./committed-change-page-index-v1.md)、
[Page Index Generation v3](./page-index-generation-v3.md)、
[Page Store Authority v1](./page-store-authority-v1.md)（过渡形态）

## 7. 记录文件 `database.memora`

- **append-only**：`nativestore.Transaction` 只有 `Put`／`Commit`／`Rollback`，
  **没有 Delete**（`internal/store/native/file.go`）。删除只能是语义上的；
- 13 种 object kind（`file.go:49-61`）：Opaque／Database／Table／Column／Row／
  History／Relation／Route／RouteMembership／SnapshotMeta／Configuration／
  CommittedChange／RouteRowMembership；
- 每条记录：header（长度、kind、schema、id 长度、负载长度、CRC32）+ id + 负载；
- 事务用 `BEGIN`／`COMMIT` 标记包裹，`COMMIT` 带整批内容的 SHA-256；
  打开时扫描，未完成的尾部被截断（`scan()`、`recoveryOffset`）；
- 可变对象用**版本化记录 ID**：`id` 是第 1 版，`id@%020d` 是第 N 版
  （`nativerow.revisionRecordID`、`nativecatalog.stageVersion`）。

### 那张常驻内存的表

`File.records` 是 `map[{kind, id}] → {payloadOffset, payloadLength, payloadCRC,
schemaVersion}`（`file.go:77`）。

**它不是 cache——没有容量、没有淘汰。它是这个文件唯一的物理索引**：
给定 (kind, id) 只有它知道字节在哪，每次 `Get` 都要先查它。
`Open()` 时 `scan()` 从头读完整个文件、逐条 CRC 校验、丢掉负载、只留偏移。

由于每个版本是一条独立记录，**这张表随"历史上写过多少次"增长，
不随活跃数据增长**。一行改 100 次就是 100 个永不释放的条目。
这是与数据量相关的唯一无上界常驻结构，也是
[聚簇行存储 v1](./clustered-row-storage-v1.md) 要消除的目标。

`File.Enumerations()`（`file.go:455`）计数全库扫描（`IDs`／`Records`），
作为"读路径不得枚举全库"的回归护栏。

细节：[原生最小 Store](./native-minimal-store.md)、
[Exact Object Write Lock](./exact-object-write-lock-v1.md)

## 8. 各类对象现在存在哪

| Object Kind | 正文在哪 | 索引 | 目标（见新设计） |
| --- | --- | --- | --- |
| Row（当前版本） | versions 树叶子 | current 树给 revision | **current 树叶子** |
| Row（历史版本） | versions 树叶子 | versions 树 | 记录文件，仅按指针链到达 |
| History | 记录文件 | 内存表 | **整个删除**，归属并入 Change Log |
| Database／Table／Column | 记录文件 | catalog 树 + 内存表 | objects 树叶子 |
| Route／两种 Membership | 记录文件 | **仅内存表** | objects 树叶子 |
| Relation | 记录文件 | **仅内存表** | objects 树叶子 |
| Configuration／SnapshotMeta／Opaque | 记录文件 | **仅内存表** | objects 树叶子 |
| CommittedChange | 记录文件 | change 树 | objects 树叶子 |

「仅内存表」的那几行意味着：读它们只能靠 `file.IDs(kind)` 全库枚举，
或按名字查那张常驻表。

## 9. 三个容易混淆的概念

文档里从没把这三样分开讲过，这是设计被反复误解的一个来源：

| 概念 | 是什么 | 粒度 | 谁用 |
| --- | --- | --- | --- |
| **snapshot** | MVCC read view，一个 commit sequence 水位 | 每次读 | 引擎内部事务可见性。`Authority.Capture()` → `versions.HighWater()` |
| **版本链／旧版本** | 这一行过去长什么样，是**数据** | 每个版本 | `AS OF`、`SHOW HISTORY`、MVCC 回溯 |
| **Change Log** | 谁改的、为什么改，是**归属** | 每个**事务** | `SHOW CHANGES`、审计、同步 |

`snapshot` 不是"数据库快照文件"，是事务可见性水位。
`ObjectKindHistory` 记录是第二份归属拷贝，与 Change Log 的
`change.Metadata`（actor／source／reason／sourceReceiptID）重复，
`change.Entry` 里甚至有 `HistoryLocator` 字段直接指向它
（`internal/change/model.go:70`）——它将被删除。

细节：[MVCC、Undo 与 Redo 边界](./mvcc-undo-redo.md)、
[Change Log 与未来同步](./binlog-and-sync.md)

## 10. 实例生命周期

目录布局、跨设备搬迁、备份与恢复、格式升级：

[Database 物理目录](./database-file-layout.md)、
[Instance/Database/Table](./instance-database-table.md)、
[macOS 实例目录](./macos-instance-directory.md)、
[格式升级](./instance-format-upgrade-v1.md)、
[备份](./instance-portable-backup-v1.md)、[恢复](./instance-restore-v1.md)、
[搬迁](./instance-move-v1.md)、[逻辑快照](./logical-snapshot-v1.md)、
[SQLite 兼容迁移](./sqlite-compatibility-migration.md)、
[Route Trace Store](./route-trace-store-v1.md)、
[术语](./terminology.md)、[索引职责边界](./indexing.md)

## 11. 已知偏差

现役实现与[聚簇行存储 v1](./clustered-row-storage-v1.md) 的差距，
括号内是该文档定义的阶段编号：

1. **正文在 versions 树而不是 current 树**（阶段 1）。
   读一个当前行要降两次树，第二次降的是按版本建键、随历史增长的大树；
2. **版本链已被删除**（阶段 2）。写入不再产生 previous 指针，
   解码器仍兼容既有记录；历史读目前靠按 revision 逐个点查；
3. **`objectindex` 已建好但没有调用方**（阶段 3）。
   Route／Relation／Config 等仍只能全库枚举；
4. **Catalog／Change 树只存逻辑 Locator**（阶段 4），正文仍在记录文件；
   `nativerow.table()` 每读一条记录就重读一遍整个 Catalog；
5. **`File.records` 常驻表仍在**（阶段 5），`Open()` 仍逐条 CRC 扫完整个文件；
6. **`ObjectKindHistory` 仍在写**（阶段 1'），与 Change Log 重复；
7. **Overflow Page 未实现**：单条编码记录超过 8 KiB 硬失败，不跨页拆分；
8. **`routevector.Generation.vectors`** 把全部 Route 向量常驻内存
   （`internal/routevector/model.go:125`），随语义索引规模增长，尚未评估。
