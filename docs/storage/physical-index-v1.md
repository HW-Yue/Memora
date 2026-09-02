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
| 3 | Relation 迁入;Configuration 改顺链读 | 各自读面逐字一致;`Enumerations()` 保持零 | ✅ |
| 4 | **Catalog 正文进 objects 树** | `DescribeTable` 逐字一致;不再回记录文件取正文 | ✅ |
| 5 | 开库不再全扫,`File.records` 删除 | **路线未定**,见[记录文件的索引与权威](./record-index-and-authority-v1.md) | 队头 |

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

准则第四条要求「查的时候按需去文件里取」。**开库之后的活路径上已经清零**,
门是 `TestALiveWorkloadNeverSweepsTheRecordFile`:四次写(插入／更新／建关系)
加九个读面(点查／列表／AS OF／SHOW HISTORY／关系点查与列举／SHOW UNDER／
DescribeTable／Catalog 快照),`Enumerations()` 增量为零。

**开库本身仍然全读,而且应该全读**:generation 是派生的,从记录日志把它建
出来正是全读的用途。变的是「开完之后再也不扫」。

已消除:

| 位置 | 触发频率 | 换成了什么 |
|---|---|---|
| `nativerouter.nodes()` | 每次遍历语义树 | objects 树按 kind 范围扫 |
| `nativecatalog.Read()`(读面) | 每次 `DescribeTable` | catalog 树 + objects 树 |
| `nativecatalog.Read()`(写面) | 每次改 schema | `SnapshotCatalog` |
| `nativecatalog.stageVersion` | **每写一个对象一遍** | 调用方交出上次发布的 Catalog |
| `nativechange.NextSequence` | **每次写** | 从 change 树 high-water 往前探 |
| fulltext 追平的 Catalog／Route 重建 | **每次写** | Trees |
| `nativerow.NextCommitSequence` | **每次写(两遍)** | versions 树的持久分配器 |
| `nativerow.table()` | **每次 Row 读写** | `Authority.TableByIdentity`(无锁) |
| `nativerow.GetRelation` | 每次关系点查 | objects 树一次下降 |
| `nativerow.ListRelations` | 每次关系列举 | 范围扫(原先是**平方级**:扫一遍收 ID,再逐 ID 各扫一遍) |
| `nativeconfig.history`／`policyHistory` | 每次读配置 | revision 链从 1 顺着点读 |

**剩下的 `file.IDs()` 调用点,全部不在活路径上**(逐条核过):

- **无 generation 时的回退**:`nativerouter.nodes()`、`nativerow.GetRelation`／
  `ListRelations`／`List`／`ReadAsOfCommit`、`nativecatalog.Read()` ——
  只在 `objects == nil`／`authority == nil` 时走,那正是没有树可读的情形;
- **重建路径本身**:`pagestoremigration.Reader.Build` 及其下的
  `AllRowVersions`／`CurrentRelations`／`Catalog`;
- **快照导出**:`nativesnapshot`,它要产出的就是全库;
- **零生产调用方**:`nativechange.ListAfter`(change 树已接管)、
  `store/nativekv`(整个包只有测试在用)。

### commit 序号:从「扫出来的最大值」改成持久分配器 ✅

`NextCommitSequence` 原先是 `max(全部 Row 的 commit, 全部 Relation 的 commit)+1`
——**每次写扫两遍全库**。它挡在一个具体的地方:`rowversionindex.HighWater()`
只给出 Row 那半边,而 Relation 也从同一个序号空间取号,却**完全不经过权威**。

做法(与 Row ID 计数器同形):versions 树的 high-water 标记就是分配器;
每次发布把它推过自己写的一切,Relation 通过 **commit floor** 一起算进去;
建库时从 Plan 的 Relation 种下(否则 COW 重建会丢掉只有 Relation 用过的号段)。
**分配失败会烧掉一个号**——commit 序号只用于 `AS OF` 定位与排序,不要求连续
(要求连续的是 change 序号,那是另一个分配器,不受影响)。

### 还没做:按端点查关系仍是范围扫

`ListRelations(endpoint, outgoing)` 要按**端点**查,而 `objectindex` 只按
`(kind, id)` 建键。迁进去之后按 ID 点查是一次下降,但按端点列举要走遍全部
Relation ——**代价已经从「历史上写过多少条」变成「当前有多少条」,
但不是点查**。要真点查需要一棵按端点建键的二级索引。
个人库的关系数量有限,所以没有随之排期;**要做就单独定,不要在迁移里顺手决定。**

### 一棵树在一次 group 里只能 stage 一次——这条现在是**做不到**,不是「记住」

Route、Relation、Catalog 正文共用 objects 树。同一次 `CommitGroupFunc` 里对它
调两次 stage 会**死锁**:第一次把写锁交给了 group,第二次等的正是那把锁,
而 group 要等这次调用返回才能提交。**什么都不超时、什么都不打日志,进程就停了。**

一开始只在方法注释里写了「每个 group 只 stage 一次」。**注释不算数**——
下一个加 stage 方法的人忘了就又是这个坑。改成结构上进不去:

- `treecommit.Group.Stage(runtime, lock, plan)` 是**唯一**的入组方式,
  `add` 已改为不导出;
- 它**先认领(claim)、后加锁**。顺序是关键:一旦 Index 已经卡在自己的互斥量上,
  就没人还能发现这件事了。重复认领直接返回 `ErrTreeAlreadyStaged`,
  报在犯错的那一行;
- 六个 `Stage*` 方法(catalog／current／versions／fulltext／objects×2)全部走它,
  顺带把重复的加锁/解锁样板去掉了;
- **另一个 goroutine 把同一棵树 stage 进别的 group 是正常争用**,必须照旧阻塞
  等第一个 group 提交完。认领是按 group 记的,所以这条路没被动。

门:`TestStagingOneTreeTwiceInOneGroupIsRefusedNotHung`。把认领判断关掉,
它会挂到自己的 10 秒超时;打开就返回错误。另有两条一起钉住:合成一批 stage
必须照常原子,以及两个 group 并发 stage 同一棵树必须都成功。

### 阶段 5 的路线未定(2026-09-02)

这里原本只写了一条路:「树里存一个『我索引到文件哪个偏移了』的游标,开库读
游标、只扫尾部,全扫降级为修复路径」。那条路成立,但它**不是唯一解**,
而选哪条取决于一个还没定的产品问题。

两条路:

- **非聚簇**——记录文件旁边放一个 B+ 树索引文件(`(kind,id) → 偏移`),
  与记录文件一起算权威。改动小、可逆,但**放弃 compaction**(偏移必须永久有效);
- **聚簇转正**——提交点上移到树的 group commit,记录文件退出正确性路径。
  数据只存一份、保留 compaction 自由,但**失去「树坏了整个重建」这层保险**,
  且 generation 升版语义要重新定义。

决定它的问题是:**记录文件将来要不要回收被覆盖版本占的空间。**
完整对照与两条路都要先过的硬门,见
[记录文件的索引与权威](./record-index-and-authority-v1.md)。

顺带纠正本文 §3「为什么是聚簇」那一节留下的印象:选聚簇是对的,但**今天的
形态是聚簇树 + 记录文件里一份完整重复**,数据存了两遍。那一节写明的代价
(「正文在记录文件与树里各有一份」)就是阶段 5 要收的账。

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
