# 架构审计 2026-08

状态：**某一时点的实测清单**（2026-08-22，`e5134f7` 之后）。

## 怎么读这份文档

**它不是规格，是一次扫描的结果。** 每条都给 `文件:行` 与"调用方计数"这类
可复现的判据，不给主观评价。代码一动就会过期——**过期了请重扫**（命令见文末），
不要把它当规格引用，也不要在它和代码冲突时相信它。

排序按**会不会咬人**，不按代码量。一个五行的缺陷排在一万行的重复前面。

每条的格式：现象 → 证据 → 为什么让架构不清晰 → 建议动作与前置条件。
**本文不排期、不承诺**。

判据参照[架构原则](../product/architecture-principles.md)三条。

---

## 一、缺陷

### 1.1 redo WAL 永不回收 —— **已修复（2026-08-25）**

**当时的现象**：WAL 从不滚段、从不 checkpoint、从不回收，无界增长。

**当时的证据**：`internal/store/wal/` 里这四个符号**零生产调用方**（各有 12–52
处测试引用）：`SegmentSet.Roll`、`PublishCheckpoint`、`LatestCheckpoint`、
`Reclaim`。生产对 `wal` 包只用 `OpenSegmentSet`／`CreateSegmentSet`／
`RecoverSegmentSet`／`Record` 与类型常量。

**为什么当时不清晰**：三份 WAL 文档都写着「F86a/b/c 已完成」。协议正确、有测试、
冻结了文档，就是没人调。「已完成」与「没人调」同时为真而只写前者，会误导读者。

**怎么修的**：`pagestoremigration.maintainRedoLog` 在每次成功写入之后跑一轮
——活跃段超过 4 MiB 就 `Roll` → `PublishCheckpoint` → `Reclaim`。
生产 `DurabilityBarrier`（此前只有测试 recorder）落在 `redoBarrier`。

修的过程中撞出一个**接口自死锁**：`PublishCheckpoint` 持有 SegmentSet 的锁再
回调 barrier，而 barrier 刷页要读 `DurableLSN`，那又要同一把锁。解法是新增
`buffer.Pool.FlushDirtyThrough`，让调用方把已知的 durable LSN **传进去**——
顺带让 barrier 参数**不再被忽略**，no-steal 上界照旧强制。
这大概就是此前没有生产 barrier 的原因。

**门**：`TestRedoLogRollsCheckpointsAndReclaims` 量的是**磁盘字节不随写入次数增长**，
不是「发生过 checkpoint」；外加 reclaim 删段之后重开逐字读回。

### 1.2 `EXPORT WIKI` / `INSTALL PACKAGE` 能解析、执行必失败

**现象**：语法完整，执行固定返回 `CodeUnsupported`。

**证据**：词法有关键词（`msql/lexer/token.go:54-55`），解析产出 AST
（`parser.go:1699` 的 `EXPORT_WIKI`、`:1734` 的 `INSTALL_PACKAGE`）；
执行时 `executor/package.go:19` 与 `executor/wiki_export.go:18`
判 `engine.packages == nil` / `engine.wiki == nil` 直接返回不支持。
只有测试用的 `newDatabaseHandler`（`daemon/execute.go:58,86`）注入了
`Packages`/`Wiki`；生产走 `newNativeDatabaseHandler`，不注入。

**为什么不清晰**：Database Package 有 7 份产品文档、Wiki 导出有 1 份，
读文档会以为能用。

**已执行（2026-09-02）：改判为删。** 原建议「功能保留、把 `Packages`/`Wiki`
接进生产 handler」是错的——要接的那一头（`dbpackage`／`wikiexport`）只接受
legacy 的 `store.Store`，唯一注入点本身零可达调用方，「接线」的实际内容是重写。
两个包整包已删，**语法与 CLI 子命令保留**并固定返回 not implemented，
产品文档降为设计记录。见[执行计划](../planning/execution-plan.md)清理台账。

### 1.3 schema 与 route 变更不加对象锁

**现象**：只有 Row 走对象锁。

**证据**：`internal/store/objectlock/objectlock.go` 三个 Key 构造器的非测试调用方
实测为 `RowKey` 1 个、`SchemaKey`（`:46`）0 个、`RouteKey`（`:50`）0 个。

**为什么不清晰**：锁机制设计成三级，实际只用一级，读代码会以为三级都在生效。

**建议动作**：先判定这是缺陷还是"当前串行写入下不需要"。daemon 串行发布写事务
（见 `docs/storage/mvcc-undo-redo.md`），若并发写入永不开启则两个 Key 应删；
若要开，则应补上。**不要维持现状**——留着不用最容易让人误判。

### 1.4 向量检索没有生产发布方（**已删除整条链**）

**现象**：`USING VECTOR` 实际不可用。

**证据**：`routevector.Service.Publish`（`service.go:27`）只有测试调用；
生产只在 `daemon/lifecycle.go:218` 构造服务，并经 `OpenActive` 只读。
无发布方 ⇒ 无 generation ⇒ 返回 `PredictorUnavailable`。

**已执行（2026-09-02）：整条链删除**（`3ff6136`，−2539 行）。
「记为未启用」那个裁定只对了一半：不造发布方是对的，留着实现是错的——
`Generation.vectors` 是个不设上界的常驻结构，为一个到不了用户手里的功能服务
（见 2.3）。`USING VECTOR` 仍解析，返回「vector retrieval is not implemented」。
重做时方向不变（[候选预测器只给路径](../query/predictor-path-only-v1.md)），
但必须做成盘上索引。

### 1.5 三个导出面对已删除 Row 的处理不一致（已裁定接受）

**现象**：同一个"要不要导出已删除 Row"的问题，三处给了两种答案。

**证据**：
- `wikiexport` **过滤**（`export.go:203` `if stored.State != row.StateLive`）；
- `nativesnapshot` **不过滤**：`internal/nativesnapshot/native.go:85,89` 走
  `AllRows()`／`AllHistory()`，两者都不按 state 过滤；
- `dbpackage` **不过滤**：`Service.Pack` 经 `snapshot.FilterDatabase`
  只按 database 过滤，`RowCount` 把墓碑一起数进去。

> **2026-09-02：三个导出面已剩一个。** `wikiexport` 与 `dbpackage` 整包删除，
> 只有 `nativesnapshot` 还在。不一致因此消失，但**裁定不变**——导出的契约是
> 可达性而不是物理擦除，将来重建那两个面时按 `nativesnapshot` 的做法即可。

实测：给 e2e 剧本加一个"插入即删除"的 Row 后，`doctor` 的 `Rows` 由 5 变 6、
`History` 由 12 变 13。

**为什么记在这里**：这**不违反**删除契约——契约是可达性，而
[F227](../planning/f227-object-archive.md) 明说字节还在是接受的；
且已核实导入后仍不可达（`Import` 从 history 重建状态并保留墓碑，读面照样拒绝）。
**2026-08-22 已裁定：可接受，只记不改。** 记下来是因为三者不一致本身会让人
反复重新发现它，并误判成缺陷——本审计的 known-risks 7e 就误判过一次。

**若将来要改**：真正的动因会是数据披露（交给第三方的包里带着已删内容与归属），
不是契约。改之前注意它会动快照哈希与导入的历史链完整性校验。

---

## 二、耦合

### 2.1 一次 row INSERT 碰约 15 样东西，横跨两个事务域

**证据**：`nativerow.Service.Insert`（`service.go:123`）在一个
`nativestore.File` 事务内：写闸 → Catalog 读 → RowID → `NextCommitSequence`
→ `NextChangeSequence` → Row 记录 → History 记录 → membership 记录 →
change envelope。`commit()` **之后**，`Authority.PublishMutation`
（`pagestoremigration/authority.go:418`）再碰三棵各有独立 `treecommit`/WAL/page
manager 的树（versions／fulltext／current），加四次 phase checkpoint
（`phaseRowBodyCommitted`／`phaseRowVersionPublished`／`phaseRowFulltextPublished`／
`phaseRowCurrentPublished`），失败时 `poisonPublication`。

**为什么不清晰**：两个事务域之间**没有原子性**——所以才需要"阶段 checkpoint"
和"毒化标记"来事后说清自己停在哪。按[架构原则](../product/architecture-principles.md)
§1 的判据，这正是耦合的症状而不是解法。

**好消息**：耦合方向本来是对的。`nativerow`／`nativemutation`
**不 import 任何检索包**（实测），只经 `PageAuthority` 接口
（`nativerow/service.go:43-56`），接口用的是 `[]row.Row`／`[]router.Node`
这类领域词汇，没有索引词汇。**真正的耦合点集中在 `pagestoremigration` 一处**，
这是好事：要解耦只需要动一个地方。

**建议动作与现成模板**：`authorityChangeTree.reconcile`
（`pagestoremigration/change_index.go:269`）已经示范了怎么做——变更日志驱动、
游标推进、256 条一批、读时惰性触发地重放进 `changeindex`。
派生索引（首先是 fulltext）若照它走，`PublishMutation` 能去掉三棵树
和四个 checkpoint 阶段，写入事务只剩"写自己的数据"。
前置：先确认惰性重建对 fulltext 的读一致性要求是否够用。

### 2.2 fulltext 的失败会以两种形态出现

**证据**：文档投影在 `commit()` **之前**做（`apply.go:474`
`projectRowDocuments`），索引写入在 `commit()` **之后**做
（`authority.go:485` `ReplaceBatch`）。所以投影失败 ⇒ 写入失败；
提交后索引失败 ⇒ 发布中毒。

**为什么不清晰**：同一个子系统的故障，一半表现为"你的写入被拒绝"，
一半表现为"数据库进入毒化状态"。调用方无法用一种方式处理。

**建议动作**：并入 2.1 一起解。

### 2.3 每次查询重建全量

**证据**：`routelexical.Search` 每次调用重建整个倒排 map（`search.go:124`），
外加对整个 catalog+route 视图做一次 SHA-256（`search.go:175-297`）；
`routevector.Generation.vectors`（`model.go:125`）把一个 generation 的
**全部** route 向量装进内存，且 `OpenActive` 每次查询重新加载并重新校验，无缓存。

**2026-09-02：向量那一半已随 1.4 整条删除**，不用再测了——
「先测量再改」在这里是个死循环，因为前置（1.4）永远不会满足。
`routelexical` 那一半仍在，仍适用「先测量再改」。

---

## 三、重复

### 3.1 整层 legacy service 生产零调用

**证据**：`row.New`（`row/service.go:73`）的非测试调用方为零——
全部在 `_test.go` 与 `tests/integration`、`tests/e2e`。
`relation.New` 与 `router.New` 只被 `row/service.go:90,93` 调用（其自身已死）。
`catalog.New` 与 `snapshot.New` 只被 `dbpackage`／`wikiexport`／
`daemon/execute.go:79` 调用，而 `newDatabaseHandler`（`execute.go:58`）
的非测试调用方也是零——生产走 `newNativeDatabaseHandler`
（`daemon/lifecycle.go:227`）。

**重要区分**：这些包的 **`model.go` 是活的**——`msql/executor` 的接口签名
（`executor/engine.go:32-46`）整个建立在 `row.Row`／`router.Node`／
`relation.Relation` 之上。**只有 Service 层是死的，模型层不是。**

**为什么不清晰**：读代码时同一个概念有两套实现，且没有任何标记说明哪套在跑。

**建议动作**：删除受[旧代码清理边界](./legacy-code-boundary.md)的规则约束——
必须先证明不在 `cmd/...` 生产依赖图中并先加 RED。

**2026-09-02 进展**：`dbpackage`／`wikiexport` 这两个消费方已整包删除，
1.2 与本条的互相牵制随之解开。剩下的 `catalog.Service`／`row.Service`／
`snapshot.Service`／`daemon.newDatabaseHandler` **只剩测试夹具这一个用途**——
39 个测试文件、8201 行。所以剩余工作是一次**测试夹具迁移**，
不是一次删除；daemon 那 8 个文件迁到 native 栈本身就是覆盖率的改善。
排期见[执行计划](../planning/execution-plan.md)清理台账。

### 3.2 同一个小接口抄很多遍

**证据（实测计数）**：`Clock interface{ Now() time.Time }` 定义 **11 次**；
`IDSource interface{ Next() (string, error) }` **8 次**；
`type systemClock` 与 `type uuidSource` 两个一行实现共声明 **20 次**。
另有成对重复：`FanoutSource`（`semantichealth/model.go:109` 与
`routemutationplan/model.go:160`）、`Tool`（`skillwrite/model.go:71` 与
`skillschema/model.go:81`）、committed-change 读接口
（`nativerow/service.go:58` 与 `msql/executor/change.go:17`）。

**为什么不清晰**：改一处行为要找 11 个地方；新人不知道该实现哪一个。
需要说明的是，Go 里"消费方定义窄接口"是惯例，**重复本身不必然是错**——
真正的成本在那 20 份一行实现，那些没有任何理由重复。

**建议动作**：优先合并 `systemClock`／`uuidSource` 两个实现到一个共享小包；
接口定义是否合并另议。

### 3.3 三套并存

- **读路径三条**：scan repository（`nativerow/repository.go:221,249,291,330`
  的 `file.IDs(ObjectKindRow)` 全量枚举）／`IndexedReader`／`Authority`。
  scan 是 `if service.authority != nil {…}` 之后的**活 fallback**
  （`nativerow/service.go:199,224,466,508,533,582`），
  且仍是 relation、route 与 `ReadAsOfCommit` 的唯一路径；
- **物理存储三种**：page 树 + redo WAL／`database.memora` typed record／
  `nativekv` opaque KV（`daemon/lifecycle.go:182,188` 的 `auxiliary.memora`
  与 `security.memora`，无 WAL、无 page、无 buffer pool）；
- **generation 三个格式版本同时可读**：`pagestoremigration/manifest.go:25,74,109`
  的 `legacyGenerationVersion`／`legacyExpectedTrees`，COW 升级只有测试走过。

**更正**：`IndexedReader` **不是**死代码——它由 `Authority` 在
`pagestoremigration/authority.go:123,128` 与 `replacement.go:173,177` 构造。
本审计早期的一份自动扫描曾误判它无调用方，此处更正。

---

## 四、规模事实（中性）

`internal` 非测试 **96,810 行**，其中 **30,517 行（31%）分布在 27 个不在
`cmd/memora` 依赖图内的包**里。

**这基本正常，不是指控。** 仓库有 24 个 `cmd/` 二进制，只有 `cmd/memora`
是产品，其余是评测、发布与代码生成工具链，本就不该进产品。

值得单独说的是最大的单个包 **`internal/agent`（14,078 行）也在外面**，
只被四个评测/发布二进制引用。它含吸收流程的**读取侧**——EPUB/DOCX/PDF 适配器、
Document IR、OCR 证据门。而吸收的**提交侧**（`REVIEW`/`SUBMIT ASSIMILATION`
语句、`internal/assimilation`、`internal/assimilationcommit`）**在产品里**。

这个切分本身符合 [ADR-0002](../decisions/0002-defer-embedded-agent.md)
（v0 不内置 Agent Runtime，由外部 Skill 驱动）。记在这里是因为
读文档容易以为"文档吸收"是 daemon 的能力。

---

## 五、半迁移

- **`rowversionindex` 的 legacy sequence-zero 键**：`codec.go:118` 的
  `legacyKey`，`index.go:525` 已封口（"legacy sequence-zero import is sealed"），
  但读与比较路径仍在。单向迁移，做了一半；
- **SQLite 迁移是硬停**：`nativemigration/migration.go:18` 的
  `ErrLegacyMigrationRequired` 让 daemon 直接失败，指向独立二进制
  `compat/sqlite-migrator/`，进程内没有迁移路径。

---

## 六、已立项，不在此展开

写入形态确立的四处冲突各有去处，本文不重复：

- history 独立成表、每张表一棵独立 B+ 树、三份日志分工 →
  [存储层总览「已知偏差」](../storage/README.md)；
- 语义索引叶子直挂 RowID → [叶子直挂 RowID](../storage/leaf-rowid-v1.md)；
- 检索只返回路径 → [候选预测器只给路径](../query/predictor-path-only-v1.md)。

---

## 复现方法

本文全部数字由下列命令产出，可直接重跑。

```bash
# 一、哪些包不在产品（cmd/memora）依赖图内
go list -deps ./cmd/memora | grep '^github.com/HW-Yue/Memora/internal/' \
  | sed 's|.*/internal/||' | cut -d/ -f1 | sort -u > /tmp/in-product.txt
ls -d internal/*/ | sed 's|internal/||;s|/||' | sort > /tmp/all.txt
comm -13 /tmp/in-product.txt /tmp/all.txt

# 二、非测试行数（总量 / 单包）
find internal -name '*.go' -not -name '*_test.go' -exec cat {} + | wc -l
find internal/agent -name '*.go' -not -name '*_test.go' -exec cat {} + | wc -l

# 三、某个符号的非测试调用方（判断死代码的唯一判据）
grep -rn 'PublishCheckpoint' --include='*.go' internal cmd | grep -v '_test.go'

# 四、某个小接口被定义了几次
grep -rn 'Clock interface' --include='*.go' internal cmd | grep -vc _test.go
```

**注意第三条**：`grep -v _test.go` 会把定义行本身也算进去，逐条判定时要看清
命中的是定义还是调用。本文每条结论都是这样人工核对过的——
自动扫描至少误判过一次（见 3.3 的更正）。

## 关联

- [架构原则](../product/architecture-principles.md) — 本文的判据来源
- [已知风险](./known-risks.md) — 本文的缺陷已登记在册
- [旧代码清理边界](./legacy-code-boundary.md) — 删除任何东西前先读它
- [存储层总览](../storage/README.md) — 存储层的现状与已知偏差
