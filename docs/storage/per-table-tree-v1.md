# 每表一棵树：业务表、history 表与按表递增的 RowID

状态：**迁移设计**（2026-08-22）。落实[写入形态](../product/write-model.md)
§1「每张表 = 一棵独立的 B+ 树」、§1.2「history 独立成表」与
§2.4「RowID 按表递增」。不是独立规范——与写入形态冲突时以写入形态为准。
**尚未排期，未开始实现。**

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
| 1 | generation 支持**动态树集合**（按 Catalog 里的表开树），仍只开现有四棵 | 既有库开机行为逐字不变；树集合可增删 |
| 2 | 每张业务表一棵聚簇树，键收缩为 `row_id`；读路径切过去 | 扫一张表不再读到其他表的条目；`SELECT`／`OPEN ROUTE` 逐字一致 |
| 3 | 表树内保留键做 RowID 计数器；`IDSource` 加表标识并收敛实现 | 同一张表连续插入拿到连续 ID；重开后不回退、不重号 |
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

## 6. 与其他迁移的关系

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
