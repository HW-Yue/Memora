# 每表一棵树：业务表、history 表与按表递增的 RowID

状态：**迁移设计**（2026-08-22）。落实[写入形态](../product/write-model.md)
§1「每张表 = 一棵独立的 B+ 树」、§1.2「history 独立成表」与
§2.4「RowID 按表递增」。不是独立规范——与写入形态冲突时以写入形态为准。

**阶段 1–3 已完成**（E4）：动态树集合、每表一棵聚簇树、按表递增的 RowID。
阶段 4–6（history 独立成表）尚未开始。落地形态与两处订正见 §7。

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行。

## 为什么三件事合成一份

它们是**同一套机制**：

- history 表就是 per-table 树的一个实例（`notes` ↔ `notes_history`）；
- 「RowID 按表递增」需要一个**按表**的计数器，而计数器的自然归宿就是那张表自己的树；
- 三件分开做，等于把「按表切分」这套代码写三遍，还要处理三次 generation 升版。

所以本文一次讲完，实施时按 §5 的阶段推进。

## 1. 现状

| 写入形态要求 | 现状 | 证据 |
|---|---|---|
| 每张表一棵独立 B+ 树 | 全实例共用一棵 | `currentrowindex/codec.go:42` `encodeKey(tableID, rowID)`——表只是键的前缀 |
| history 独立成表 | 是第 6 号 object kind 的扁平记录 | `store/native/file.go:54` `ObjectKindHistory` |
| 业务行带 `history_id` | 没有这个字段 | `internal/row/model.go` 的 `Row` 结构 |
| RowID 按表递增 | 全局 UUID | `nativerow/service.go:1383` `uuid.NewString()` |

generation 目前固定开四棵树（`pagestoremigration/generation.go:34-37`）：
`catalog`／`current`／`versions`／`fulltext`，树集合写死在
`expectedTrees`（`manifest.go:67`）。

**「表」现在是一个过滤谓词，不是物理分区**——扫一张表要扫过所有表的条目。

## 2. 目标结构

```
每个 Database
  ├── catalog 树          （不变，全库一棵：名字 → ID）
  ├── changeindex         （不变，全实例一棵）
  ├── <table_id>          （每张业务表一棵：聚簇，键 row_id，叶子即正文）
  ├── <table_id>_history  （每张业务表配一棵：键 (row_id, 序号)）
  └── fulltext            （派生索引，见「与其他迁移的关系」）
```

聚簇键从 `(table_id, row_id)` 收缩为 `row_id`——表已经由「哪棵树」表达了，
键里不必再带。

## 3. 按表递增的 RowID

### 3.1 计数器放哪

**放该表自己的树里，作为一个保留键。** 仓库里已有这个范式且只有这一处：
`rowversionindex` 把 snapshot high-water 存成树内保留键
（`codec.go:122` `snapshotHighWaterKey()` 返回 `{keyVersion, keySnapshotWater}`，
由 `index.go:285` `HighWater()` 读取），并与数据写在同一次提交里。

照搬它：每棵表树留一个 `nextRowIDKey`，分配 RowID 与写入行**同一事务**推进。
这样计数器不可能与数据不一致——不需要第二个结构，也不需要跨树协调。

**不要**沿用 `NextCommitSequence`（`nativerow/repository.go:359`）那种
「扫全部记录取最大值 +1」的做法：它是现存的弱范式，规模一大就是全扫。

### 3.2 接口要动签名

`IDSource`（`nativerow/service.go:39`）现在是 `Next() (string, error)`，
**不接受表参数**——按表递增就无从谈起。这个 1 方法接口在仓库里
**被重复定义 8 次**（见[架构审计](../development/architecture-audit-2026-08.md) §3.2），
`type uuidSource` 一行实现被声明 20 次。

改造时一并收敛：签名加表标识，实现收进一个共享小包。这是审计里
「同一个小接口抄很多遍」那条的第一个真实动因——**顺手做，不要单独立项**。

### 3.3 RowID 的对外形态

按表递增意味着 RowID 在**表内**唯一，不再全局唯一。
这一条要在设计评审时明确定下，因为它影响对外面：

- 现在的 `row_<uuid>` 全局唯一，`SHOW CHANGES`、`route_leaf_ids`、
  Relation 的两端都直接拿它当身份；
- 按表递增后，**任何跨表引用都必须带上表标识**。

Relation（`internal/relation`）两端各存 `DatabaseID/TableID/RowID`，已经带了表，
**不受影响**；要逐一核对的是 `SHOW CHANGES` 的 `ObjectID`、
`change.Entry.RelatedObjectIDs`、以及 `route_leaf_ids` 的反向——
叶子上的 RowID 是否要带表标识（叶子本身有 `TableID`，大概率不用）。

## 4. history 表

键 `(row_id, 序号)`，序号在同一 row_id 内递增——同一行的全部变更物理相邻，
读完整历史是一次范围扫（写入形态 §1.2）。

三件必须一起定的事：

1. **业务行的 `history_id`** 存最新一条的复合键，是「只看最近一次改动」的点查入口，
   不承担串联全部版本的职责；
2. **删除的契约**：Row 被删除后，它在 history 表的整个 `(row_id, *)` 区段
   **一并不可达**（[查询形态 §7](../product/query-model.md)）。
   history 是一张真的表，而表默认可查——这条不明写，新读面会一个一个漏；
3. **`Row.ChangeSequence` 的去留**在这一步裁定。它是 `48ef5b6` 为
   「删掉 History、归属只留在变更日志」那条**已废弃**路线加的外键；
   history 升格成表之后，归属存在 history 表里，这个字段大概率回退。
   注意它已被刻意排除在逻辑快照之外（`json:"-"`），回退不影响快照哈希。

## 5. 分阶段与验证门

每阶段一条独立可验证的性质。generation 版本号 +1、既有库开机 COW 自动升级，
沿用 `internal/pagestoremigration` 已有的「从已提交 Record 构建 generation」，
不另起炉灶——注意 `expectedTrees`（`manifest.go:67`）目前是**固定长度的树表**，
改成按表动态开树是这次最实质的结构变化。

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 ✅ | generation 支持**动态树集合**（按 Catalog 里的表开树） | 既有库开机行为逐字不变；树集合可增删 |
| 2 ✅ | 每张业务表一棵聚簇树，键收缩为 `row_id`；读路径切过去 | 扫一张表不再读到其他表的条目；`SELECT`／`OPEN ROUTE` 逐字一致 |
| 3 ✅ | 表树内保留键做 RowID 计数器 | 同一张表连续插入拿到连续 ID；重开后不回退、不重号 |
| 4 | 每张业务表配一棵 history 表，键 `(row_id, 序号)`；Row 加 `history_id` | 读一行完整历史是一次范围扫；`SHOW HISTORY` 逐字一致 |
| 5 | 停写 `ObjectKindHistory`，读路径切到 history 表 | 新事务不产生第 6 号 kind 记录；旧记录仍可读 |
| 6 | 删除 `ObjectKindHistory`；裁定 `Row.ChangeSequence`；generation 升版 | 既有库自动升级，内容逐字不变 |

**跨阶段的逐字一致基线**：切换前后比对 `SELECT`、`SHOW HISTORY`、
`AS OF REVISION`／`AS OF COMMIT_SEQUENCE`、`OPEN ROUTE`、`SHOW CHANGES`、
Catalog Atlas 与逻辑快照哈希。

**删除契约的回归**：阶段 4 起，每一阶段都要重跑
「已删除 Row 从任何面都拿不到」那组断言
（`internal/daemon/f227_row_relation_archive_test.go`）——
history 变成表是这条契约最容易被漏掉的地方。

## 5.6 阶段 1–3 的落地形态与两处订正

### 订正一：RowID 保持全局唯一，号段前带表的 space

§3.3 把「RowID 变成表内唯一、跨表引用必须带表标识」列为要在评审时定下的事。
实现时定下来了：**保持全局唯一**，ID 形如 `row_<表space 16 位十六进制>_<号>`。

理由是一条硬约束：**原生存储按裸 RowID 给行记录做键**
（`nativerow/repository.go` 的 `StageInitial`／`revisionRecordID`）。
两张表都从 1 号起，会在那里直接撞车——这是实现时被 RED 抓到的，
不是推演出来的。

把那个记录身份改成按表限定是可以做的，但它是对**真相之源文件**的迁移，
而原生文件**没有 generation 那样的 COW 重建路径**，得单独出设计。
不能当作本次的副产品顺手做掉。

space 由表 ID 推导，所以写进 ID 里**没有记录任何新事实**，
只是把「哪张表」这个已知量放进了命名空间。
写入形态 §2.4 要求的是「每张表各自维护自己的递增序列」——这一条满足了。

### 订正二：计数器参数是下界，不是赋值

`StageApplyWithRowIDCounter` 的计数器参数是**下界**：调用方传本批行里最大的号，
计数器只上不下。第一版写成「低于已存值就报冲突」，结果任何改老行的写入都被拒
（改 `row_..._1` 时批内最大号是 1，而计数器已经到 2）。
RED 是 daemon 里那三条删除／改行测试。

### 阶段 2 的另外两处

- **v5 的固定树只有三棵**（catalog／versions／fulltext），没有 `current`。
  v4 仍可打开，开机时 COW 重建；
- **读面一行没改**：`CurrentLookup` 本来每次就带表 ID（因为键里有），
  现在那个表 ID 用来**选树**而不是拼前缀。新增的 `tableCurrentRows` 做这个分派。

## 5.5 硬前置：共享 buffer pool

**已完成（E3.5）。** 本节以下描述的是改造前的状态，保留作为背景；
落地形态见节末的「已落地」。

现状与 InnoDB 不同：InnoDB 一个 buffer pool 服务所有表空间，
而这里**每棵树一个 pool**——`buffer.New` 全仓只有一个调用点
（`treecommit/runtime.go:90`，在 `OpenRuntime` 内），`OpenRuntime` 每棵树调一次
（`generation.go:125`）。pool 的 loader 闭包还把 `SpaceID` 写死：

```go
if key.SpaceID != config.SpaceID { return page.Page{}, ...ErrInvalid }
```

**结构上就无法共享。**

现在 4 棵树 × 512 帧 × 16 KiB = 32 MiB，尚可接受。但本文把树数变成**正比于表数**
（每表业务树 + history 树），于是内存变成 **16 MiB/表**——
10 张表 160 MiB，100 张表 1.6 GB。

这直接推翻[架构原则](../product/architecture-principles.md)与写入形态里
「常驻内存有上界」那条：单个 pool 有上界，**总量没有**。

**所以要先做**：一个 pool 服务所有树，`buffer.Key{SpaceID, PageID}` 本来就带
`SpaceID`（`buffer.go`），Page Table 已经能区分不同树的页——
缺的是把 loader 从「写死单个 space」改成「按 key 路由到对应 page manager」，
以及容量从"每树一份"改为一份总量。

### 已落地

- `buffer.Router` 同时实现 `Loader` 与 `PageWriter`，按 `SpaceID` 分派。
  刷脏页不需要额外上下文：**页头自己带 `SpaceID`**（`page.Header.SpaceID`），
  writer 直接按它找文件；
- `RuntimeConfig` 加 `Pool` 字段。给了就用共享的，不给就自建——
  单棵树的调用方（changeindex、各处测试）一行不用改；
- generation 打开时建**一个** Router 与**一个** pool，把每棵树的 page manager
  注册进去，再逐棵 `AttachRuntime`。容量因此是一份总量；
- 闸门：`TestGenerationUsesOneSharedBufferPool` 断言所有树拿到的是**同一个**
  pool 指针，并且常驻帧数不超过那一份预算。

**change index 那棵树仍自带一个 pool**，这是有意的：它一棵、固定、
不随表数增长，与本节要防的「正比于表数」无关。它自带独立的 redo log，
折进 generation 的 pool 需要先合并日志，收益不抵改动。

## 6. 与其他迁移的关系

- **硬前置：共享 buffer pool**（见 §5.5）——**已完成**，内存不再随表数增长；
- **前置：派生索引解耦。** fulltext 目前在 `PublishMutation` 里与三棵树同批发布
  （`pagestoremigration/authority.go:485`），一次 INSERT 跨两个无原子性的事务域。
  先把它改成变更日志驱动的重放（模板见 `change_index.go:269`
  `authorityChangeTree.reconcile`），本次要动的树就少三棵、checkpoint 少四个；
- **可并行：[叶子直挂 RowID](./leaf-rowid-v1.md)。** 它动 router 与 Row 的
  `route_leaf_ids` 字段，与本文的树切分不重叠；
- **后置：三份日志与恢复。** binlog 应当记录**定型后**的结构，
  所以排在本文之后，避免记完再改。

## 关联

- [写入形态](../product/write-model.md)（上位规范）、[查询形态](../product/query-model.md)
- [架构原则](../product/architecture-principles.md) §2（能用一张表就用表）
- [叶子直挂 RowID](./leaf-rowid-v1.md)、[存储层总览](./README.md)「已知偏差」A/B 组
- [架构审计](../development/architecture-audit-2026-08.md) §2.1（耦合）、§3.2（接口重复）
