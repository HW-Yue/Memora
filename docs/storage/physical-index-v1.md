# 物理索引:把常驻内存的索引全部搬进 B+ 树

状态:**迁移设计**(2026-08-31)。落实[架构原则](../product/architecture-principles.md)
**第四条**「一切都要有物理存储,启动只给钩子,查的时候按需去文件里取」。
不是独立规范——与架构原则冲突时以架构原则为准。

编写原则同[存储层总览](./README.md):每条「现状」断言都能指到具体文件与行。

## 为什么现在写

第四条准则 2026-08-31 才写进最高规范。在那之前,`File.records` 只是存储总览
里一句「这是要消除的目标」——**记录了现象,但没有判据,所以没有约束力**。
这就是它一直没被排期的原因。

现在它命中判据第 3 条(**没有容量、没有淘汰,却是唯一的索引**),
是四条准则里唯一一条**会随时间自动恶化**的违反。

## 1. 现状:要消除的三处

| # | 结构 | 位置 | 随什么增长 | 性质 |
|---|---|---|---|---|
| 1 | **`nativestore.File.records`** | `store/native/file.go` | **历史上写过多少条记录** | 唯一物理索引,无容量无淘汰 |
| 2 | `routevector.Generation.vectors` | `routevector/model.go:125` | 语义索引规模 | 全部 Route 向量常驻 |
| 3 | Catalog/Change 树只存逻辑 Locator | 偏差 12 | — | 正文仍回记录文件,间接依赖 #1 |

**#1 是主目标**,#3 是它的调用方之一,#2 是独立的一处(向量,不在本文范围,
单独排)。

### `File.records` 干四件事,不是一件

```go
f.records  map[recordKey]recordMeta   // recordKey{kind, id}
```

| 职责 | 代码 |
|---|---|
| ① `(kind,id)` → 字节在哪 | `Get`(`file.go:507`)、`Location`(`:543`) |
| ② 拒绝重复 ID | `Put`(`:281`)、`Commit`(`:324`/`:346`) |
| ③ 枚举某个 kind / 全部 | `IDs`(`:599`)、`Records`(`:621`) |
| ④ 恢复时判断记录归属 | `scan()`(`:690`–`:735`) |

**四件都要有去处,少一件就删不掉。** 只把读改成按偏移取,只解决了①。

## 2. 家底:要用的东西全都已经写好了

这一节是本设计最重要的一节——**这不是造新结构,是接线**。

| 需要 | 仓库里 | 状态 |
|---|---|---|
| B+ 树(点查/范围/分裂/删除/再平衡) | `store/btree` | ✅ 生产在用 |
| 多页原子提交 | `store/treecommit` | ✅ 生产在用 |
| **通用聚簇对象树** | **`store/objectindex`** | ⚠️ **写好、测好、零生产调用方** |
| 按偏移直读(不查 map) | `File.ReadAtLocation` | ⚠️ 写好,零生产调用方 |
| 提交回吐物理偏移 | `Transaction.Location` | ⚠️ 写好,只用来判存在性 |
| 「我追到哪了」游标模式 | fulltext 追平游标 | ✅ 生产在用,可照抄 |

`objectindex` 的包注释一字未改地写着它的用途:

> the clustered store for objects that keep one record per identity:
> **Routes, Relations, memberships, configuration and the rest.**
> The leaf holds the object itself, so resolving (kind, id) is a B+Tree descent
> and nothing more — no second file, and
> **no process-resident directory of every record that ever existed.**

最后那句就是 `File.records`。**这棵树是专门为了干掉它造的。**
它的 API 正好覆盖①②③:`Get(kind, id)` / 树里查得到即存在 / `Page(kind, afterID, limit)`。

**这与 `Roll`／`PublishCheckpoint`／`Reclaim` 是同一种情况**:写好、测好、
文档冻结、零调用方(那三个是 E0 阶段 4 接上的,已知风险 7a 随之关闭)。

## 3. 目标结构

```
每个对象,按身份点查 = 一次 B+Tree 下降,叶子即正文

objectindex(每个 kind 一段键空间)
  ├── Route          (kind=8)   ← 现在:仅内存表
  ├── Relation       (kind=7)   ← 现在:仅内存表
  ├── Configuration  (kind=11)  ← 现在:仅内存表
  ├── SnapshotMeta   (kind=10)  ← 现在:仅内存表
  └── Opaque         (kind=1)   ← 现在:仅内存表

Row  → 每表聚簇树 + 每表 history 树        ✅ 已完成(E4/E5)
Catalog → catalog 树,**叶子改存正文**       ← 本文阶段 4
```

**Row 不并进来。** 它的聚簇索引是每表的树,键是 `row_id`;
把它塞进按 `(kind,id)` 排的通用树,等于把刚做完的每表分区退回去。
`objectindex` 的包注释自己就写明了这个例外。

### 为什么是聚簇(叶子存正文),不是非聚簇(叶子存偏移)

两条路都成立,`Location`＋`ReadAtLocation` 那条也是现成的。选聚簇:

- **一次下降就拿到数据**,不用第二跳;
- **不依赖偏移永久有效**。非聚簇要求记录永不移动——今天成立(只追加、
  没有 compaction),但那是一条**没写下来的隐含前提**,将来做 compaction
  会被它绊住;
- 树已经是聚簇的,`objectindex` 直接可用,不用再造一层。

代价是正文在记录文件与树里各有一份。**这个代价 Row 已经付过并接受了**
(versions 树叶子存正文),对象这一族的正文比 Row 小得多。

## 4. 分阶段与验证门

每阶段一条独立可验证的性质。**恢复是全程风险最高的部分**——
阶段 5 动的是权威结构的索引,改错了是静默的数据丢失。

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 | 接上 `objectindex`:generation 里开一棵 objects 树,建/开/重建走通 | 既有库开机行为逐字不变;树可从记录文件重建 |
| 2 | **Route 迁进去**,读面切换 | `OPEN ROUTE`／`route_paths` 逐字一致;`Enumerations()` **归零** |
| 3 | Relation／Configuration／SnapshotMeta／Opaque 迁入 | 各自读面逐字一致;`Enumerations()` 保持零 |
| 4 | Catalog 树叶子改存正文 | `DescribeTable` 逐字一致;不再回记录文件取正文 |
| 5 | `File.records` 四个职责全部转出,`scan` 降级为**修复路径** | **开库不再全扫**;崩溃后重开逐字一致 |

**跨阶段基线**:切换前后比对 `SELECT`、`SHOW HISTORY`、`AS OF`、`OPEN ROUTE`、
`SHOW CHANGES`、Catalog Atlas 与逻辑快照哈希。

**每阶段都要重跑**「已删除 Row 从任何面都拿不到」
(`internal/daemon/f227_row_relation_archive_test.go`)。

### 阶段 2 为什么排在最前

Route 是**核心对象里唯一还完全靠内存表的**,而且两条路都在等它:

- 点查已在本轮改成有界点探(`nativerouter.Get` 从 1 起探到缺号即止),
  但**索引本身还是那张 map**;
- 整棵树的遍历(`nodes()` → `file.IDs`)**仍是全扫**,`Roots`／`Children`／
  健康扫描都走它。

所以它同时命中判据第 2 条(代价与库里有多少东西相关)和第 3 条。

### 阶段 5:不需要跨文件原子,只需要一个游标

这是本设计里唯一一处真正新的机制,而且它有现成的模式。

**问题**:记录追加与索引更新如果不在同一次提交里,崩在中间就是
「记录在文件里,但没人知道它在哪」——而它已经被 COMMIT 标记认成已提交。
数据没丢,但找不到,等于丢了。

**不用跨文件原子来解**。树里存一个**「我索引到文件哪个偏移了」的游标**,
与树的内容同一个事务落盘。开库时:

```
打开树(读根页)          ← 这就是准则里说的「钩子」
读游标 → offset N
从 N 扫到文件尾           ← 通常零条或几条
```

全扫从**常规路径**降级为**修复路径**(索引缺失或损坏时才跑),
与 generation 的 COW 重建是同一个思路。

**这个模式仓库里已经在跑**:fulltext 的追平游标存在 fulltext 树自己里,
和它描述的文档同一个事务落盘——所以两者永远不会各说各话
(见[派生索引追平](./derived-index-catchup-v1.md))。照抄。

## 5. 明确不做

- **不动 Row 的每表树**。它已经达标,并进通用树是倒退;
- **不做 compaction**。选聚簇正是为了不依赖「偏移永久有效」;
- **不在本文处理 `routevector.Generation.vectors`**(偏差 14)。
  它是向量那条独立链路的问题,随语义索引规模增长而不是随写入次数,
  且尚未评估;要单独排;
- **不删 `File.ReadAtLocation`／`Transaction.Location`**。选了聚簇之后它们
  在本设计里没有调用方,但它们是非聚簇路线的原语,留着,标注清楚。

## 6. 与其他迁移的关系

- **前置:无。** B+ 树、treecommit、objectindex、游标模式全部就绪;
- **可并行:** 与 A 阶段(Agent 侧)不重叠;
- **解开的东西:** 阶段 5 完成后,「开库时间随库龄增长」这条消失——
  这是[已知偏差](./README.md) 11 的正解。

## 关联

- [架构原则](../product/architecture-principles.md) **第四条**(上位规范)
- [存储层总览](./README.md) §7「那张常驻内存的表」、§11 偏差 11／12／14
- [派生索引追平](./derived-index-catchup-v1.md)(游标模式的先例)
- [每表一棵树](./per-table-tree-v1.md)(Row 那一族的先例)
