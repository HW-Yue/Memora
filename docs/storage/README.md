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

**日志有硬容量。** `maintainRedoLog` 每次成功写入后跑一轮
（`Roll` → `PublishCheckpoint` → `Reclaim`），而在用区间
（checkpoint 恢复 LSN → 写指针）不许超过环：超了报 `wal.ErrRingFull` 背压，
**不覆盖**——那段字节是页文件里还没有的改动的唯一副本。
[已知风险](../development/known-risks.md) 7a 随之关闭。

**fulltext 是派生索引，不在写入事务里**：写入只写权威数据，fulltext 从提交的
变更日志追平（追平跟在写入后面立刻跑，但在它的事务之外，所以没有可见滞后）。
游标存在 fulltext 树自己的第四个 key 前缀里，与文档同事务落盘。
见[派生索引解耦](./derived-index-catchup-v1.md)。

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
`memora.page-index-generation/v8`，`internal/pagestoremigration/manifest.go:22`），
含四棵**固定树**（catalog／versions／fulltext／objects）加**每表两棵**
（current／history）；Change 树独立于 generation 之外。

| 树 | space_id | key | leaf value | 位置 |
| --- | --- | --- | --- | --- |
| catalog | `MEMCAT` | 6 种：Database/Table/Column 的 ID 与 name | **逻辑 Locator**，正文在 objects 树 | `store/catalogindex` |
| current（每表一棵） | 由 `table_id` 派生 | `row_id` | **逻辑 Locator**，无正文 | `store/currentrowindex` |
| versions | `MEMVER` | 4 种：`revision`／`commit`／`identity`／`legacy` | revision 键带 **Row 正文 + history 元数据**，其余无 | `store/rowversionindex` |
| fulltext | `MEMFTX` | object／owner／posting | 倒排 posting | `store/fulltextindex` |
| change | `MEMCHG` | commit sequence | 逻辑 Locator + checksum | `store/changeindex` |
| **objects** | `MEMOBJ` | `kind‖id` | **正文 + revision** | `store/objectindex`，**持有全部 Route、Relation 与 Catalog 正文**；余 Configuration／SnapshotMeta／Opaque（[E7](./physical-index-v1.md)） |

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

**开库之后的活路径上已经不再有全扫**。门是
`TestALiveWorkloadNeverSweepsTheRecordFile`：四次写加九个读面，`Enumerations()`
增量为零。开库本身仍然全读，而且应该全读——generation 是派生的，从记录日志把
它建出来正是全读的用途。剩下的 `IDs()` 调用点全部不在活路径上（无 generation
时的回退、重建路径、快照导出、零调用方），逐条核对见
[物理索引](./physical-index-v1.md)「全表扫描清点」。

**Route 与 Catalog 已经不再经过它**（E7 阶段 2／4）：
`nativerouter.NewWithObjects` 走 objects 树，点查是一次 B+ 树下降，
整树遍历是一个 kind 的范围扫；`nativecatalog.IndexedReader` 从 catalog 树拿
Locator、从 objects 树拿正文，**它连记录文件句柄都不再持有**。
记录日志仍是权威、仍在追加，变的只是读往哪看。

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
**A、B、C 三组已清空**（E3／E4／E5），D 组的规格已编写、实施排在
[执行计划](../planning/execution-plan.md) E6。

### A. 每张表一棵独立 B+ 树（写入形态 §1）✅

**已完成（E4）**，迁移设计见[每表一棵树](./per-table-tree-v1.md)阶段 1–3。

每张业务表一棵聚簇树，键收缩为 `row_id`——哪张表由「在哪棵树里」回答；
RowID 按表递增，计数器是表树里的一个保留键，与拿号的行同一次提交落盘。
一处订正：RowID **保持全局唯一**（号段前带表的 space），因为原生存储按裸
RowID 给行记录做键，改那个身份是对真相之源文件的迁移，需要单独出设计。

### B. history 独立成表（写入形态 §1／§7）✅

**已完成（E5）**，见[每表一棵树](./per-table-tree-v1.md)阶段 4–6。

每张业务表配一棵 history 树，读一行完整历史是一次范围扫。
两处与原设计不同：**不加 `Row.history_id`**（键里的序号就是行自己的
`Revision`，加上它是同一事实存两遍且会漂移）；`Row.ChangeSequence`
**保留并升格**为归属 join 的唯一钥匙，而不是按原计划回退。

### C. 语义索引叶子直接挂 RowID（写入形态 §2）✅

**已完成（E3）**，迁移设计见[叶子直挂 RowID](./leaf-rowid-v1.md)。

叶子带 `RowID` 字段、行带 `route_leaf_ids`，双向挂载在同一事务里落盘；
membership 两个 object kind（9／13）退役，三类语义健康问题结构性消失。
阶段 7 的结论是**不删 `Node.Path`**（量测见该文 §7.3）。

### D. 三份日志各司其职（写入形态 §3／§5／§6）

**规格已编写**：[三份日志](./three-logs-v1.md)（4 阶段）。以下是待做的差距。

8. **binlog 不存在**。`redo/`、`undo/`、`binlog/` 三个目录每个实例都建、
   **从来没人往里写**（`instance/instance.go:39-41`）；真正在用的 redo WAL
   住在 generation 目录里，与那个 `redo/` 无关。
   今天扮演恢复依据的是记录文件 `database.memora`；
9. **redolog 没有独立的 prepare 阶段**。当前只有单个 commit record + digest
   （`store/wal/transaction.go`），规范要求 `prepare` → binlog 写成功 → `commit`。
   注意顺序：两阶段标记的用途是判断「binlog 写完没有」，
   **没有 binlog 时它无事可判**，所以 binlog 必须先做；
10. **change log 目前不参与回滚，而且缺前像**。它今天记的是「改成了什么」
    （`AfterRevision`），没有「原来是什么」——所以 undo 能力是要**造**的，
    不是接线接出来的。

### E. 与规范无关的既有欠账

存储层之外还有若干缺陷、耦合与重复（含 **redo WAL 从不 checkpoint／回收**
这一条），逐条证据见[架构审计 2026-08](../development/architecture-audit-2026-08.md)，
不在此重复。

11. **`File.records` 常驻表仍在**，`Open()` 仍逐条 CRC 扫完整个文件；
    这是与数据量相关的唯一无上界常驻结构（见第 7 节）。
    **2026-08-31 升级为违反[架构原则](../product/architecture-principles.md)
    第四条**（命中判据 3：没有容量、没有淘汰，却是唯一的索引），
    已排为执行计划队头 E7，迁移设计见[物理索引](./physical-index-v1.md)。
    **进度**：阶段 1～4 已完成（objects 树接线、Route、Relation、Catalog 正文，
    加上 Configuration 改顺链读）。**开库之后不再有全扫**；余下的是把开库时的
    `scan` 本身降级为修复路径（阶段 5），那才是这一条真正关闭的时候；
12. **Change 树只存逻辑 Locator**，正文仍在记录文件。
    Catalog 那一半已于 E7 阶段 4 解决（正文进 objects 树）；
    Change 树随 11 收尾一起处理。
    Catalog 写路径的全扫（`ApplySchemaChangePlan` 重建整个 Catalog、
    `stageVersion` 每写一个对象扫一遍）已于同批消除；
13. **Overflow Page 未实现**：单条编码记录超过 8 KiB 硬失败，不跨页拆分；
14. **`routevector.Generation.vectors`** 把全部 Route 向量常驻内存
    （`internal/routevector/model.go:125`），随语义索引规模增长，尚未评估。
