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
| 3 | ~~Catalog 树只存逻辑 Locator~~ | 偏差 12 | — | ✅ 阶段 4 已解决;Change 树仍然是 |

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

  ├── Database／Table／Column (kind=2/3/4)  ← 阶段 4 已迁入

Row  → 每表聚簇树 + 每表 history 树        ✅ 已完成(E4/E5)
Catalog → catalog 树存 Locator + objects 树存正文   ✅ 阶段 4
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

| 阶段 | 内容 | 独立可验证的性质 | 状态 |
|---|---|---|---|
| 1 | 接上 `objectindex`:generation 里开一棵 objects 树,建/开/重建走通 | 既有库开机行为逐字不变;树可从记录文件重建 | ✅ `5c3e1a3` |
| 2 | **Route 迁进去**,读面切换 | `OPEN ROUTE`／`route_paths` 逐字一致;`Enumerations()` **归零** | ✅ |
| 3 | Relation／Configuration／SnapshotMeta／Opaque 迁入 | 各自读面逐字一致;`Enumerations()` 保持零 | 队头 |
| 4 | **Catalog 正文进 objects 树** | `DescribeTable` 逐字一致;不再回记录文件取正文 | ✅ |
| 5 | `File.records` 四个职责全部转出,`scan` 降级为**修复路径** | **开库不再全扫**;崩溃后重开逐字一致 | |

**跨阶段基线**:切换前后比对 `SELECT`、`SHOW HISTORY`、`AS OF`、`OPEN ROUTE`、
`SHOW CHANGES`、Catalog Atlas 与逻辑快照哈希。

**每阶段都要重跑**「已删除 Row 从任何面都拿不到」
(`internal/daemon/f227_row_relation_archive_test.go`)。

### 阶段 2 做完之后的实际形状

- **写**:记录日志仍是权威,每次写照旧追加;`PublishMutation` 把同一批 Route
  编成 `objectindex.Update`(各自声明接在哪个 revision 后面),**与 Row 的每表树
  进同一个 group** ——落树与落行是一个持久事实;
- **读**:`nativerouter.NewWithObjects` 走对象树。点查是一次 B+ 树下降
  (不再从 revision 1 往上探),整树遍历是**一个 kind 的范围扫**
  (不再 `file.IDs` 全扫);`New(file)` 保留给没有 generation 的调用方——
  迁移 Reader 正是从记录日志建树的那个,它不能反过来读树;
- **树的来源按次解析**(`ObjectSource`),不是开机拿一次:COW 重建会换掉整个
  generation,握着旧树的调用方会从一棵没人再写的树上取答案。

**顺带修出的既存漏报**:`stageLeafMounts` 在同一个记录事务里写叶子侧的
Route revision(叶子记着自己挂着哪一行),但插入与更新两条路径都只调
`PublishRows(rows, nil)` ——这些 revision 从来没报给权威。之前树里没有 Route
所以没人发现。是新加的 compare-and-set 抓出来的。

### 阶段 4 为什么不是「catalog 树叶子改存正文」

本文原先写的是把正文放进 catalog 树自己的叶子。**改了**:正文放进 objects 树,
catalog 树仍然只存 Locator。理由是本文 §2 那条——**能不造就不造**:

- 阶段 1／2 已经把 objectindex 接进 generation 并跑通,`Lookup`／`Bootstrap`／
  组提交都是现成的。走 catalog 树要改它的值格式(locatorVersion 升版)、
  加一个正文供给接口、改 `Replace`／`readEntries` 的 diff;
- catalog 树的 `readEntries` 每次写都把**整棵树读进 map** 来做 diff。
  正文塞进去就等于每次 Catalog 写都把整个 Catalog 正文读进内存——
  正是第四条准则要消灭的形状;
- Catalog、Relation、Configuration 的正文放同一个地方,阶段 3 与阶段 4
  是同一件事,而不是两套机制。

代价是**按名字查要两次下降**(catalog 树 名字→Locator,objects 树 ID→正文)。
但改之前那也是两跳,只不过第二跳落在那张常驻 map 上;现在两跳都是页文件里的
B+ 树下降,都走 buffer pool、都有上界。

阶段 4 的实现要点:

- **objects 树按 kind 整体替换**(`ReplaceKinds`)。Catalog 一次发布交出全部
  Database／Table／Column,而 `DROP_COLUMN` 是真语句——只增不减的族会攒下
  没人指向的正文。不动别的 kind,Route(一次改一个)与 Catalog(只整体发布)
  才能共用一棵树;
- **两棵树同一次 group commit**。读面把「Locator 指向 objects 树里没有的
  对象」判为损坏而不是竞态,凭的就是这一点;
- **`IndexedReader` 不再持有记录文件句柄**——结构性的,不是省略:
  没有东西可以够到它,任何读都无法悄悄退回那张表。

### 阶段 2 为什么排在最前

Route 是**核心对象里唯一还完全靠内存表的**,而且两条路都在等它:

- 点查已在本轮改成有界点探(`nativerouter.Get` 从 1 起探到缺号即止),
  但**索引本身还是那张 map**;
- 整棵树的遍历(`nodes()` → `file.IDs`)**仍是全扫**,`Roots`／`Children`／
  健康扫描都走它。

所以它同时命中判据第 2 条(代价与库里有多少东西相关)和第 3 条。

### 全表扫描清点(2026-09-01,逐条指到代码)

准则第四条要求「查的时候按需去文件里取」。下面是**全库中仍会 `file.IDs()`
全扫的每一处**,按是否在活路径上分开。`File.Enumerations()` 计的就是这些。

**已消除**(本轮):

| 位置 | 触发频率 | 换成了什么 |
|---|---|---|
| `nativerouter.nodes()` | 每次遍历语义树 | objects 树按 kind 范围扫 |
| `nativecatalog.Read()`(读面) | 每次 `DescribeTable` | catalog 树 + objects 树 |
| `nativecatalog.Read()`(写面) | 每次改 schema | `authority.SnapshotCatalog` |
| `nativecatalog.stageVersion` | **每写一个对象一遍** | 调用方交出上次发布的 Catalog |
| `nativechange.NextSequence` | **每次写** | 从 change 树 high-water 往前探 |
| fulltext 追平的 Catalog／Route 重建 | **每次写** | Trees |

**仍在活路径上**(全部收敛到阶段 3 这一件事):

| 位置 | 触发频率 | 挡在哪 |
|---|---|---|
| `nativerow.NextCommitSequence` → `AllRows` + `AllRelationVersions` | **每次写** | commit 序号目前是「扫出来的最大值」,要改成**树里的持久分配器** |
| `nativerow.GetRelation`／`ListRelations` | 每次 Relation 读写 | Relation 未迁入 objects 树 |
| `nativeconfig.history`／`policy` | 每次读配置 | Configuration 未迁入 |
| `nativekv` Opaque | 视调用方 | Opaque 未迁入 |

**合法的全读**(它们要产出的就是全库,不是索引查找):

- `pagestoremigration.Reader.Build` 及其下的 `nativecatalog.Read`／
  `AllRowVersions`／`nativerouter.New(file).nodes()` ——**这是重建路径本身**;
- `nativesnapshot` 导出;
- `nativechange.ListAfter` ——零生产调用方(change 树已接管)。

### commit 序号:从「扫出来的最大值」改成持久分配器

`NextCommitSequence` 现在是 `max(全部 Row 的 commit, 全部 Relation 的 commit) + 1`
——**每次写都要扫两遍全库**。这是剩下的一条里唯一在每次写都跑的。

要点在于它不能只靠 Row 那棵树:`rowversionindex.HighWater()` 已经给出 Row 那半边,
但 Relation 也从同一个序号空间取号,而 **Relation 目前完全不经过权威**
(`commitRelationChange` 直接写记录文件,generation 一无所知)。

方案(与 Row ID 计数器同形,`currentrowindex.StageApplyWithRowIDCounter` 是先例):

1. versions 树加一个 commit 序号计数器键;
2. 计数器在**已有的那次组提交里**推进,不额外加 fsync——所以每次发布都要
   报告它用掉的序号,**Relation 因此必须走权威**;
3. Plan 加 `RelationHighWater`,建库时把计数器种到
   `max(Row 的 commit high-water, Relation 的)` ——否则 COW 重建会丢掉
   只有 Relation 用过的号段;
4. 分配失败会烧掉一个号(留下空洞)。**这是可以接受的**:commit 序号只用于
   `AS OF` 定位与排序,没有任何地方要求它连续(change 序号才要求,那是另一个
   分配器,不受影响)。

做完这一步,Relation 也就顺势进了权威,阶段 3 的迁入随之水到渠成。

### 阶段 3 的一个未决设计问题

`ListRelations(endpoint, outgoing)` 要按**端点**查,而 `objectindex` 只按
`(kind, id)` 建键。迁进去之后按 ID 点查是一次下降,但按端点列举仍然要走遍
全部 Relation ——**比现在好(与 Relation 数量相关,不与历史写入次数相关),
但不是点查**。要真正点查需要一棵按端点建键的二级索引。
**这一条要单独定,不要在迁移里顺手决定。**

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
