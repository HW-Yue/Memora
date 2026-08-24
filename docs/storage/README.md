# 存储层：当前实现总览

本文描述存储层**现在实际是什么样**，是读懂这一层的唯一入口。
其余文档按 Feature 切分、在各自验收门通过时冻结，是证据链而非现状描述——
本文每一节末尾指向对应的冻结规格。

设计终点见[写入形态](../product/write-model.md)——最高产品参考规范。
现役实现与终点的差距在文末「已知偏差」一节逐条列出。

（此前的设计终点[聚簇行存储 v1](../archive/storage/clustered-row-storage-v1.md)
已于 2026-08-22 被写入形态取代并归档。）

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

**一个 generation 一套 redo log**（`redo.wal`，四棵树共用；changeindex 另有一套）。
日志层本来就是多 space 的——`Record` 带 `SpaceID`、`RecoverSegmentSet(spaces map)`
按 space 路由（`recovery.go:197`）——从前只是接线成了每棵树一套。

因此**跨树发布是一次 WAL 提交**：`PublishMutation` 通过
`treecommit.CommitGroupFunc` 把 versions／fulltext／current 三棵树并进同一个
事务，崩在中间三棵树不会各说各话。一个事务里每棵树一段记录，段内仍是
「页 redo → 可选 allocator → root 收尾」（`wal/tree_recovery.go` 的
`parseTreeMetadata`）。旧的每树一套日志的 generation 开机即 COW 升级。
进度与剩余阶段见[共享循环 redo log](./shared-circular-redo-v1.md)。

段**没有容量上限、也不会自动滚**，而 `Roll`／`PublishCheckpoint`／`Reclaim`
零生产调用方——所以实际上永远只有一个段，无限增长（[已知风险](../development/known-risks.md) 7a）。

细节：[WAL Record Stream](./wal-record-stream-v1.md)、
[Durable Transaction](./wal-durable-transaction-v1.md)、
[Durable Frontier](./wal-durable-frontier-v1.md)、
[Recovery on Open](./wal-recovery-open-v1.md)、
[Segment Set](./wal-segment-set-v1.md)、[Segment Reclaim](./wal-segment-reclaim-v1.md)

## 3. Buffer Pool

- **每棵树一个实例，各自硬容量上限**：`buffer.Config{Capacity, OldFrames, Writer, Durability}`
  （`internal/store/buffer/buffer.go:44-49`）。
  **注意这与 InnoDB 不同**——InnoDB 一个 buffer pool 服务所有表空间，
  这里 `buffer.New` 全仓只有一个调用点（`treecommit/runtime.go:90`，在 `OpenRuntime` 内），
  而 `OpenRuntime` **每棵树调一次**（`pagestoremigration/generation.go:125`）。
  pool 的 loader 闭包还把 `SpaceID` 写死，结构上无法共享；
- Page Table 按 `buffer.Key{SpaceID, PageID}` 定位 frame；
- 淘汰参考 InnoDB 的 **young/old LRU 与 midpoint insertion**，
  保护热点不被扫描冲掉（`young`／`old` 两条链表，`buffer.go:51-61`）；
- frame 具 pin count、dirty 标记；`Stats()` 暴露
  frames／loading／pins／young／old／evictions／dirty；
- `PublishBatch` 做原子批量发布（`internal/store/buffer/batch.go:20`）。

**每个 pool 各自有上界，但总量 = 容量 × 树数，不是全局有界的。**
现在 4 棵树 × 512 帧 × 16 KiB = 32 MiB，尚可接受；
但[每表一棵树](./per-table-tree-v1.md)会让树数正比于表数
（每表业务树 + history 树 = 16 MiB/表），**总量随表数增长**。
所以**共享 buffer pool 是那次迁移的前置项，不是优化项**。
另见第 7 节与数据量相关的另一处常驻结构。

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
[写入形态](../product/write-model.md)要消除的目标。

`File.Enumerations()`（`file.go:455`）计数全库扫描（`IDs`／`Records`），
作为"读路径不得枚举全库"的回归护栏。

细节：[原生最小 Store](./native-minimal-store.md)、
[Exact Object Write Lock](./exact-object-write-lock-v1.md)

## 8. 各类对象现在存在哪

「目标」一列按[写入形态](../product/write-model.md)填写。

| Object Kind | 正文在哪 | 索引 | 目标（写入形态） |
| --- | --- | --- | --- |
| Row（当前版本） | versions 树叶子 | current 树给 revision | **该表专属的聚簇 B+ 树**，叶子即正文 |
| Row（历史版本） | versions 树叶子 | versions 树 | **该表专属的 history 表**，键 `(row_id, 序号)` |
| History | 记录文件 | 内存表 | **升格为每业务表一张 history 表**（不是删除） |
| Database／Table／Column | 记录文件 | catalog 树 + 内存表 | 树叶子 |
| Route | 记录文件 | **仅内存表** | 树叶子；**叶子直接挂 RowID** |
| 两种 Membership | 记录文件 | **仅内存表** | **整个删除**——叶子直接挂 RowID，不再有独立对应关系 |
| Relation | 记录文件 | **仅内存表** | 树叶子 |
| Configuration／SnapshotMeta／Opaque | 记录文件 | **仅内存表** | 树叶子 |
| CommittedChange | 记录文件 | change 树 | **分离为独立的 change log**，与 binlog 不同文件 |

「仅内存表」的那几行意味着：读它们只能靠 `file.IDs(kind)` 全库枚举，
或按名字查那张常驻表。

注意 History 一行与上一版设计相反：曾计划整个删除、把归属并入 Change Log
（`48ef5b6` 已按此加了 `Row.ChangeSequence` 外键），写入形态改为让它升格成表。

## 9. 三个容易混淆的概念

文档里从没把这三样分开讲过，这是设计被反复误解的一个来源：

| 概念 | 是什么 | 粒度 | 谁用 |
| --- | --- | --- | --- |
| **snapshot** | MVCC read view，一个 commit sequence 水位 | 每次读 | 引擎内部事务可见性。`Authority.Capture()` → `versions.HighWater()` |
| **版本链／旧版本** | 这一行过去长什么样，是**数据** | 每个版本 | `AS OF`、`SHOW HISTORY`、MVCC 回溯 |
| **Change Log** | 谁改的、为什么改，是**归属** | 每个**事务** | `SHOW CHANGES`、审计、同步 |

`snapshot` 不是"数据库快照文件"，是事务可见性水位。
`ObjectKindHistory` 记录目前是第二份归属拷贝，与 Change Log 的
`change.Metadata`（actor／source／reason／sourceReceiptID）重复，
`change.Entry` 里甚至有 `HistoryLocator` 字段直接指向它
（`internal/change/model.go:70`）。

**去重方向已改**：曾计划删掉 History、让归属只留在 Change Log 里
（`48ef5b6` 的 `Row.ChangeSequence` 外键即为此）。
[写入形态](../product/write-model.md)改为反向去重——归属归 **history 表**
（每业务表一张，键 `(row_id, 序号)`），change log 收窄为**事务回滚的 undo 依据**。

同时注意日志的命名：[Change Log 与未来同步](./binlog-and-sync.md)那份文档把
Change Log 叫做 "Binlog"，而写入形态里 change log / redolog / binlog 是**三份不同的
日志**，binlog 才是唯一恢复依据。

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

现役实现与[写入形态](../product/write-model.md)的差距，按该规范的四个结构性要求分组。
**尚未排期**——写入形态于 2026-08-22 确立，实施计划待定。

### A. 每张表一棵独立 B+ 树（写入形态 §1）

1. **全实例共用一棵 current 树 + 一棵 versions 树**，靠键里嵌 `table_id` 区分
   （`currentrowindex/codec.go:42` 的 `encodeKey(tableID, rowID)`）。表不是物理分区，
   而是一个过滤谓词；扫一张表要扫过所有表的条目；
2. **正文在 versions 树而不是聚簇叶子**。读一个当前行要降两次树，
   第二次降的是按版本建键、随历史增长的大树；
3. **RowID 是全局 UUID**（`nativerow/service.go:1355` 的 `uuidSource`），
   规范要求**按表递增**；`IDSource.Next()` 连表参数都不接受，改造要动接口签名。

### B. history 独立成表（写入形态 §1／§7）

4. **`ObjectKindHistory` 是扁平记录**，不是每业务表一张 B+ 树表，
   也没有 `(row_id, 序号)` 复合键，读不了范围扫；
5. **业务行没有 `history_id` 字段**。`48ef5b6` 加的是 `Row.ChangeSequence`
   （指向 Change Log 事务），方向与规范相反，去留待定。

### C. 语义索引叶子直接挂 RowID（写入形态 §2）

迁移设计已单独成文：[叶子直挂 RowID](./leaf-rowid-v1.md)——
membership 的职责拆解、每项新归宿、对外可见面变更与分阶段顺序。

6. **Membership 是独立关系**（`router/model.go:43`），native 侧是两个 object kind
   （9 正向、13 反向，`store/native/file.go:56-65`）；`router.Node` 上没有任何能放
   RowID 的字段。约 310 处引用散在 32 个文件里；
7. **Route／Relation／Config 仅有内存表**：`objectindex` 已建好但没有调用方，
   这些对象目前只能全库枚举。

### D. 三份日志各司其职（写入形态 §3／§5／§6）

8. **binlog 与 change log 混装在同一个文件**。`database.memora` 事实上已在扮演
   binlog（append-only、BEGIN/COMMIT 成帧、所有二级结构从它重建），
   但 change envelope（`ObjectKindCommittedChange`）也塞在同一个记录流里；
9. **redolog 没有独立的 prepare 阶段**。当前只有单个 commit record + digest
   （`store/wal/transaction.go`），规范要求 `prepare` → binlog 写成功 → `commit`；
10. **change log 目前不参与回滚**。规范给它的职责是事务 undo 依据，
    现在它只做归属／审计，`Rollback()` 只是丢弃未提交的内存缓冲。

### E. 与规范无关的既有欠账

存储层之外还有若干缺陷、耦合与重复（含 **redo WAL 从不 checkpoint／回收**
这一条），逐条证据见[架构审计 2026-08](../development/architecture-audit-2026-08.md)，
不在此重复。

11. **`File.records` 常驻表仍在**，`Open()` 仍逐条 CRC 扫完整个文件；
    这是与数据量相关的唯一无上界常驻结构（见第 7 节）；
12. **Catalog／Change 树只存逻辑 Locator**，正文仍在记录文件；
    `nativerow.table()` 每读一条记录就重读一遍整个 Catalog；
13. **Overflow Page 未实现**：单条编码记录超过 8 KiB 硬失败，不跨页拆分；
14. **`routevector.Generation.vectors`** 把全部 Route 向量常驻内存
    （`internal/routevector/model.go:125`），随语义索引规模增长，尚未评估。
