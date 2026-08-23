# 已知风险

状态：2026-08-11 建立。收录已确认存在、但未被现有 Feature 文档记录的问题。
按严重度排序。每条给出代码位置和判断依据，不写推测。

修好一条就从这里移除并写进[系统能力](../product/system-capabilities.md)。

## 已修复（保留一轮供追溯）

### 0. 一个 Database 出错，所有 Database 读写全停 —— **Stage 1 已修复**

`internal/pagestoremigration/authority.go` 的 `poisonPublication`（`:541`）只做
`authority.poisoned = true`——全实例单一布尔，没有任何 Database 或对象作用域。
`healthyLocked`（`:568`）见到它就返回 `ErrAuthorityPoisoned`，而
`BeginWrite`（`:199`）**和 `lockRead`（`:554`）都查它**。

后果：任一 Database 的一次 Page 发布失败，整个 Instance 的全部 Database
既写不了也**读不了**。读被连带阻断尤其没必要——已提交的旧 generation 完好，
读它是安全的。

叠加成因：所有 Database 共用一套物理文件（实测 `databases/page-index-v1/` 下
单套 Page/WAL，没有按库分目录），所以物理故障域也等于整个 Instance。
这与 [原生 Store](../storage/native-minimal-store.md) 第 21 行声称的
`databases/db_<stable-id>/database.memora` 不一致——实现漂移了。

**2026-08-11 [F226](../planning/f226-per-database-fault-isolation.md) Stage 1 已实现**：
读不再受 poison 影响；poison 按 Database 收敛（Row/Route 按受影响库，Catalog 按
实际变更的库，generation 替换保持 Instance 级）；`BeginRowWrite` 早失败并在错误里
点名受影响 Database。

物理文件仍是全 Instance 共用，**这是 2026-08-20 评估后的有意选择，不是遗留缺口**：
最热读路径本来就跨 Database，拆分会给它加上常态 fan-out，而主导的损坏模式是共享
引擎代码缺陷，拆文件对其零作用。评估、替代方案与重新评估的触发条件见 F226 Stage 2。

## 严重：会导致产品主张不成立

### 1. Query Agent 只有一步记忆，多跳导航结构上不可能

`internal/agent/query_agent_wire.go:42` 的 `makeQueryProviderRequest` 每轮最多构造 4 条消息：
system prompt、user（问题 + Bootstrap Frame）、assistant（**上一次**工具调用）、
tool（**上一次**结果）。`query_agent.go` 循环里 `previousCall` 与 `previousResult` 是
**覆盖**而非追加。

后果：第 3 轮看不到第 1 轮的调用与结果。需要「OPEN ROUTE → 收窄 → SELECT」的多跳查询
在结构上无法完成，模型也会重复已经失败过的查询，因为它没有尝试过的记录。

这直接解释了 `post-f204-development-plan.md` 里那句「下一步不是扩大样本，而是先修正
Query Agent 的多轮导航/SQL 选择行为」，也解释了真实运行 9/12 与 3/12 的结果。
这不是模型能力问题，是循环实现问题。

### 2. 第一条 SELECT 就终止导航，哪怕它返回零行

`query_agent.go` 的 `if len(result.Evidence) > 0 { choice = ProviderToolChoiceNone }`：
一旦 `result.Evidence` 非空，工具调用被强制关闭，模型必须立即作答。

而 `selectEvidence()`（`query_agent.go:346`）只要求 `Statement == "SELECT"` 且
`Status == succeeded` 且 `Error == nil`——**不检查行数**。一条返回零行的 SELECT
同样算作 evidence，同样立刻锁死后续导航，逼模型凭空作答。

第 1、2 条叠加后，Query Agent 实际可用的推理深度接近一次性猜测。默认预算
（`MaxProviderCalls: 4`、`MaxToolCalls: 3`）在这两条约束下形同虚设。

### 3. AI-native 的全部主张压在最薄的一层上

约 14 万行是工程扎实的常规数据库；差异化主张（AI 自主语义建模优于 chunk + embedding）
完全落在 `internal/agent` 的约 2.3 万行里，而这一层既有第 1、2 条的结构缺陷，
也从未产出过可用的质量数字。

这不是代码缺陷，是投入结构问题，记在这里是为了不让它被 F 编号的完成度掩盖。

## 中等：会在真实使用中暴露

### 4. 文档解析的配置上界与实现能力差约 50 倍

实测放大倍率为峰值堆 ≈ 正文 7 倍（约 2.6 KB 堆／IR 节点）。而配置默认：

- `DefaultEPUBAdapterConfig`：`MaxTotalUncompressedBytes: 512 MiB`；
- `DefaultPDFAdapterConfig`：`MaxFileBytes: 128 MiB`、`MaxDecompressedBytes: 512 MiB`。

按 7 倍推算，一个刚好卡在上界的文档需要约 3.5 GB 堆。上界没有拦住会打爆 daemon 的输入，
它**允许**这类输入。典型文档（正文 1–5 MiB）完全没问题，问题只在上界本身没意义。

### 5. 解析未流式化，抵消了 SourceStore 的流式设计

F191 明确「上传全程流式处理，不把整本书读进内存」，用 32 KiB buffer 写盘。
但解析阶段：

- `pdf_adapter.go:147` 把整个 PDF 一次 `io.ReadAll` 进内存；
- `epub_adapter.go:283`／`docx_adapter.go` 的 `archive.cache[locator]` 缓存每个读过的条目，
  **无淘汰、无上限**（只受归档声明总量约束）。

流式纪律止步于存储边界，解析阶段全部退化为整载。

### 6. 两个读路径去抢单写者门

`internal/pagestoremigration/authority.go` 的 `writeGate` 是容量 1 的信号量（`:133`），
全实例写串行——对本地单用户这是合理取舍。但 `ListCommittedChanges`（`:237`）与
`GetCommittedChange`（`:271`）这两个**读**操作也调 `BeginWrite`，会被任意写阻塞；
其余读路径走的是 `lockRead` 的 RWMutex。若是有意为之（change log 需序列化读），
应加注释说明；否则是可摘除的耦合。

⚠️ [F220](../planning/f220-query-working-set.md) 的精确失效方案依赖
`ListCommittedChanges`，**必须先修这条**，否则工作集每 turn 校验都会与写入竞争。
F220 Stage 1 因此采用保守全丢，绕开该依赖。

### 7. MSQL Session 无数量上限与空闲回收

`internal/msql/service/service.go:66` 的 `OpenSession` 对同一 id 复用，对新 id 无条件创建，
`sessions` map 没有容量上限，也没有空闲超时。正常断连由
`databaseHandler.SessionClosed`（`daemon/execute.go:789`）回收，所以常规使用不泄漏。
但长连接内轮换 session id 的调用方会让 map 单调增长。当前是本地单用户，风险低，
属于"应加上界"而非"正在出问题"。

### 7a. redo WAL 永不回收，无界增长

`internal/store/wal/` 的 `SegmentSet.Roll`（`segment_set.go:397`）、
`PublishCheckpoint` 与 `LatestCheckpoint`（`checkpoint.go:32`）、
`Reclaim`（`reclaim.go:39`）**四个都是零生产调用方**（各有 12–52 处测试引用）。
生产对 `wal` 包只用 `OpenSegmentSet`／`CreateSegmentSet`／`RecoverSegmentSet`。

后果：WAL 从不滚段、从不 checkpoint、从不回收，随写入量单调增长；
恢复起点也永远是最初那一段，重启重放时间随库龄增长。

代码已写、已测、文档已冻结（`docs/storage/{wal-segment-set,checkpoint-publish,
wal-segment-reclaim}-v1.md` 三份都写「F86a/b/c 已完成」），**缺的只是接线**——
主要待决的是触发时机（按段数？按字节？按 checkpoint 间隔？）。
三份文档已加"已实现但未接线"注记。

**2026-08-22 已排期为[执行计划](../planning/execution-plan.md) E0，是当前队头**——
它是本文件里唯一随时间持续恶化的一条。

方案已从「给段式日志接上回收」改为
[共享循环 redo log](../storage/shared-circular-redo-v1.md)：全实例一套日志、
固定大小、循环使用。段式把磁盘占用交给 checkpoint 策略去防，固定环让它
**结构上不可能涨**——不 checkpoint 的后果从"磁盘静默涨到天上"变成"写入背压报错"。
同一改动顺带堵上跨树提交不原子的洞（见该文档 §2.1）。

### 7b. schema 与 route 变更不加对象锁

`internal/store/objectlock/objectlock.go` 的 `SchemaKey`（`:46`）与
`RouteKey`（`:50`）非测试调用方均为 **0**，只有 `RowKey` 有 1 个调用方。
锁机制设计成三级，实际只用一级。

当前 daemon 串行发布写事务，所以**未必正在出问题**；但"设计了三级、只用一级"
本身会让人误判并发安全边界。应先判定是缺陷还是不需要——若并发写入永不开启则
删掉那两个 Key，若要开则补上。**不要维持现状。**

### 7c. `EXPORT WIKI` / `INSTALL PACKAGE` 能解析、执行必失败

词法（`msql/lexer/token.go:54-55`）与解析（`parser.go:1699,1734`）齐全，
执行时 `executor/package.go:19`、`executor/wiki_export.go:18` 判空后固定返回
`CodeUnsupported`——生产的 `newNativeDatabaseHandler` 不注入 `Packages`/`Wiki`，
只有测试用的 `newDatabaseHandler`（`daemon/execute.go:58,86`）注入。

Database Package 有 7 份产品文档，读文档会以为能用。
**功能保留**（2026-08-22 裁定），属接线缺失。前置：`dbpackage`／`wikiexport`
现在依赖 legacy service 层，接线要么一并迁移、要么先解耦。

### 7d. 向量检索没有生产发布方

`routevector.Service.Publish`（`service.go:27`）只有测试调用；生产只在
`daemon/lifecycle.go:218` 构造服务并经 `OpenActive` 只读。
无发布方 ⇒ 无 generation ⇒ `USING VECTOR` 实际返回 `PredictorUnavailable`。

同时 `Generation.vectors`（`routevector/model.go:125`）把一个 generation 的全部
route 向量装进内存，`OpenActive` 每次查询重新加载并重新校验，无缓存。
方向已裁定保留，见[候选预测器只给路径](../query/predictor-path-only-v1.md)。

### 7e. `SELECT ... AS OF` 曾能读回已删除 Row 的完整内容 —— **已修复**

**先澄清判据**，因为本条最初写错过一次：删除的契约是
**语义上不可达且不可逆**，**不是物理擦除**。F227
（[f227-object-archive.md](../planning/f227-object-archive.md) §「存储层的真相」）
明说磁盘上的字节还在、要等 Compaction 才谈得上回收，并要求
「**不要对用户承诺"数据已被抹除"**」。所以"逻辑快照里还留着已删行的字节"
**本身不是缺陷**——本条最初据此判定"规则漏了"，是误判，已订正。

按「可达性」这个正确判据复查七条读路径，查出一处真漏：
**`SELECT ... AS OF REVISION|COMMIT_SEQUENCE` 没有任何删除态检查**。
`IndexedReader.AsOfRevision`／`AsOfCommit` 从 locator 直接 `readBody` → `project`
返回，而同一文件的 `Get` 有检查。放大它的两点：`Delete` 是 `deleted := current`，
**保留了 Values**；row_id 与前后 revision 可以从不设防的 `SHOW CHANGES` 拿到。
于是任何留着 ID 的人都能点名任一版本，把每一列的值读回来——
正是 `HistoryPage` 注释里写明并堵上的那个威胁，隔壁没堵。

**2026-08-22 已修复**：`IndexedReader.refuseDeleted` 与
`Service.refuseDeleted` 覆盖 authority 与 repository 两条路径，
按 Row 的**当前**状态判断（不是目标版本的——读 superseded 旧版本正是 AS OF 的本职），
返回 `CodeNotFound`，与 `SHOW HISTORY` 一致。`Restore` 的删除检查同时提前到读取
目标版本之前，以免报错退化成裸的 not found。
契约与全部把关面已写进[查询形态 §7](../product/query-model.md)。

**仍然接受、只作记录的**：已删除 Row 的正文与归属会随逻辑快照与 Database Package
交给第三方（`wikiexport` 过滤，`nativesnapshot`／`dbpackage` 不过滤）。
已核实导入后仍不可达——`Import` 从 history 重建状态并保留墓碑，所有读面照样拒绝——
所以不违反契约。三者不一致这一事实记在
[架构审计](./architecture-audit-2026-08.md)。

## 轻微：工程卫生

### 8. CI 只有 macOS runner

`.github/workflows/ci.yml` 只跑 `macos-latest`。项目在 Linux 上可编译、全测试通过
（本次已验证），但没有 Linux CI 保护，darwin-only 假设会静默漏过。
加 `ubuntu-latest` 到 matrix 成本接近零。

### 9. 没有 linter

只有 `gofmt -l` 与 `go vet`。16 万行代码建议加 `golangci-lint`，至少
staticcheck／errcheck／ineffassign 三项。全库 `panic()` 仅 1 处、`TODO/FIXME` 为 0，
基线很干净，正适合上 linter 保持。

### 10. 单文件过大与占位分支

`cli/cli.go` 1,820 行、`msql/parser/parser.go` 1,759 行、`agent/pdf_adapter.go` 1,300 行。
AGENTS.md 对文档定了约 150 行上限，对代码没有对等约束。

`parser.go:28` 有一个空的 `if parser.matchKind(lexer.KindSemicolon) {}`，
块内只有一句「F12 将会…」的注释。行为正确（消费尾随分号后要求 EOF），但写法会误导，
应改为直接调用或删除。

### 11. 唯一的 panic 在可达路径上

`internal/store/treecontrol/control.go:44` 的 `EncodeBootstrap` 在 `Encode` 失败时
`panic(err)`。当前调用方传的都是合法 spaceID，实际不可达；但它是全库唯一的 panic，
改成返回 error 可以让「生产代码零 panic」成为可断言的不变量。

## 关联

- [系统能力](../product/system-capabilities.md)
- [路线 v3](../planning/roadmap-v3.md)
- [语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)
- [ADR-0010 小规模高质量评测](../decisions/0010-small-scale-high-quality-evaluation.md)
