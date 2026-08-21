# 聚簇行存储 v1：当前数据在树里，历史版本在链上

状态：**设计定稿，实施中**。取代 [Page Store Authority v1](./page-store-authority-v1.md)
描述的过渡形态，兑现 [Tablespace/Page/Record 布局](./tablespace-page-record-layout.md)
里"当前 Row、聚簇索引和二级 B+ Tree"那个一直被标为后置候选的终点。

## 一句话

**当前数据在 B+Tree 里，按 rowid 定址；历史版本不进任何树，只靠记录自带的物理
指针成链。进程里与数据量相关的常驻内存只有定容 buffer pool。**

## 不变量

1. **聚簇键不含版本号。** Row 的聚簇键是 `(table_id, row_id)`，一个存活的 Row
   一个条目。版本号是写入时的乐观并发令牌（`expected_revision`），不是访问路径；
2. **叶子里就是正文。** 索引叶子直接持有对象内容，读到叶子即读到数据。
   索引不得只存逻辑标识而把正文放在别处——那样必然要再造一张"名字→位置"的表；
3. **历史版本只按物理地址到达。** 旧版本永远不按名字查，因此不需要任何索引；
4. **同一份内容只存一处。** 二级索引存指向，不存正文；
5. **常驻内存有上界。** 与数据量相关的结构一律经 buffer pool，容量固定。

## 为什么这么定

### 版本号不属于访问路径

`SELECT … WHERE row_id = X` 只需要 rowid。`expected_revision` 是写入时防并发覆盖的
令牌。`SELECT … AS OF REVISION <n>` 里的版本号只能来自 `SHOW HISTORY` 或写入回执，
是人拿到结果后的显式追查。没有一个理由让版本号进聚簇键。

把它放进聚簇键的代价是可以算的。若键为 `row_id‖revision` 升序，一行的所有版本在
叶子里紧挨着、当前版本排最后。实测一条编码后的 Row 约 243 字节，16 KiB 的 Page
约装 67 条——**读一个当前行，会把同一行的 66 个旧版本一并装进 buffer pool**。
1000 行 × 100 版本约 24 MB 叶子，其中当前数据只有 243 KB。
最热的路径替最冷的数据买单。

### 为什么全内存记录表能被删掉

一张"名字 → 文件位置"的全量常驻表，存在的唯一理由是**有东西要按名字定址**。一旦

- 每一类对象的**当前**状态都在树里（按名字定址 → 由树回答），
- **历史**版本只经前一版指针到达（按物理地址定址 → 不需要索引），

就没有任何东西需要那张表了。它不是被搬到别处，是**不再有人向它提问**。

## 架构

```
SELECT … WHERE row_id = X
  → currentrowindex (table_id, row_id)   一行一条的小树，一次下降
    → 叶子里就是当前行的完整内容            到此结束

SHOW HISTORY / AS OF / MVCC 可见性
  → 从当前行记录出发，顺 previous 指针一跳一跳回走

其余每一类对象
  → objectindex (kind, id) → 叶子里就是正文
```

### 每类对象的归宿

| 对象 | 当前状态 | 历史版本 |
| --- | --- | --- |
| Row | `currentrowindex` 叶子（正文 + 该版本元数据） | 指针链 |
| Database / Table / Column | `objectindex` 叶子 | 指针链 |
| Route / RouteMembership / RouteRowMembership | `objectindex` 叶子 | 指针链 |
| Relation | `objectindex` 叶子 | 指针链 |
| Configuration / SnapshotMeta / Opaque | `objectindex` 叶子 | 指针链 |
| CommittedChange | `objectindex` 叶子 | 不可变，无历史 |

`catalogindex`（名字 → ID）与 `changeindex`（sequence → ID）是**二级索引**，
只存指向。`rowversionindex` 整棵移除，snapshot high-water 迁入 current 树。

### `database.memora` 的新定位

它不再是通用记录存储，而是**版本区**——InnoDB undo 区的对应物，只装被指针链
引用的历史版本。链上的记录永远不按名字查，所以版本区不需要索引，
也因此不需要那张常驻表。

### 尺寸

- Page 16 KiB；单条编码记录目标 8 KiB；
- 超限**硬失败并点名**是哪条记录、哪个 kind、多大。overflow Page 尚未实现，
  不做隐式截断也不跨页拆分；
- buffer pool 定容，沿用 `internal/store/buffer`。

## 历史与 MVCC 怎么走链

每个版本自带 `commit_sequence`。可见性判断从当前行出发，一路回退到第一个
`commit_sequence <= snapshot` 为止——**通常 0 跳**，因为当前版本就可见。
这与 InnoDB 沿 `DB_ROLL_PTR` 回溯 undo 直到满足 read view 是同一种做法。

`SHOW HISTORY` 与 `AS OF REVISION` 同样顺链。一个改过 500 次的 Row 查第 42 版要走
458 跳：**接受**。罕见的显式查询付 O(版本数) 随机读，好过让每一次当前行读取都去
下降一棵被历史撑肿的树。稀疏跳点索引（每 N 版一个锚点）是独立候选项，
需先有基准证据再引入。

删除的 Row 带走自己的历史，见 [F227](../planning/f227-object-archive.md)：
`SHOW HISTORY` 只按 row_id 寻址，而删除的 Row 不出现在任何列表里。

## 兼容

三种历史格式必须读出同样结果，迁移不得丢内容：

1. 引入链之前写的记录（无链指针）；
2. 带链指针的记录（`flagPreviousLocation` 与定宽尾部）；
3. 曾写进版本树、叶子带正文的条目。

迁移扩展 `internal/pagestoremigration` 已有的"从已提交 Record 构建 generation"，
不另起炉灶。树集合的增减属于同一类改动：generation 版本号 +1，
旧版本号继续映射到冻结的旧树表，既有库开机自动 COW 升级。

## 实施顺序

| 阶段 | 内容 | 独立可验证的性质 |
| --- | --- | --- |
| 0 | 本文档与被它取代的两份文档 | — |
| 1 | 正文从版本树挪进 `currentrowindex`；读路径不再触碰版本树 | 读一行的 Page 读取次数不随该行版本数增长 |
| 2 | 恢复 previous 指针链；`AS OF`／`SHOW HISTORY`／MVCC 走链 | 三种历史查询逐字一致 |
| 3 | 移除版本树；其余 kind 进 `objectindex`；generation 升版 | 既有库开机自动升级，内容逐字不变 |
| 4 | Catalog／Change 正文进树，降级为二级索引 | 顺带消除"每读一条记录重读整个 Catalog" |
| 5 | 删除常驻记录表；`Open()` 只扫 checkpoint 之后的尾部 | 常驻堆与启动耗时都不随历史总量增长 |
| 6 | Route 向量常驻 map：先实测再决定 | 占用可观则进树，否则明确记为有界 |

## 验证门

- **热路径不碰版本树**：读当前行时对版本树的访问计数为 0；
  读一行的 Page 读取次数不随该行版本数增长；
- **内存**：写 N 行 × M 版本 + K 个 Route/Relation，常驻堆随 N×M×K 的曲线必须变平；
  buffer pool 容量设死后读远超容量的数据不 OOM；
- **不扫全库**：`native.File.Enumerations()` 计数器在阶段 3 之后所有读路径恒为 0，
  阶段 5 之后该计数器连同 `IDs`／`Records` 一并消失；
- **启动**：`Open()` 耗时不随历史总量增长；
- **逐字一致**：每阶段切换前存 `SELECT`、`AS OF REVISION`、`AS OF COMMIT_SEQUENCE`、
  `SHOW HISTORY`、`SHOW ROUTES`、`SHOW CHANGES`、Catalog Atlas 基线，切换后逐字比对；
- **崩溃恢复**：树提交与阶段 5 的 checkpoint 各造一次写中断，
  重开必须落在最后一个完整事务；
- **generation 升级**：旧版本库开机自动升级，内容逐字不变。

## 关联

- [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)
- [Page Store Authority v1](./page-store-authority-v1.md)（过渡形态）
- [Buffer Pool](./buffer-pool.md)
- [MVCC、Undo 与 Redo 边界](./mvcc-undo-redo.md)
- [ADR-0006：MySQL 式 Page/Buffer Pool/WAL](../decisions/0006-mysql-page-buffer-wal-cow.md)
