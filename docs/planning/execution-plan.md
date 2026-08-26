# 执行计划

状态：**2026-08-22 重排生效。这是当前唯一的工作队列。**
战略层理由见[路线 v3](./roadmap-v3.md)，问题依据见[已知风险](../development/known-risks.md)
与[架构审计](../development/architecture-audit-2026-08.md)。

每项都是可独立派发的工单：有前置、改动范围、RED 和完成判据。
**按编号顺序执行**，除非标注「可并行」。所有项仍须按
[TDD 协议](./feature-tdd-protocol.md)逐项 Review 与授权后实现。

## 这次重排改了什么

上一版（2026-08-11）的 14 项工单**没有一项**来自三份最高准则——因为准则是
2026-08-22 才确立的。本版把 **E 阶段（引擎侧准则符合性）** 插到最前，
Agent 侧原样承接为 A 阶段，**一项不删，只改前置与顺序**。理由见路线 v3。

## 已就地定下的决定

沿用上一版，并补充本轮已裁定的：

| 决定 | 结论 | 依据 |
| --- | --- | --- |
| F185b 死锁怎么解 | **Policy v2**：保留三 arm 矩阵与身份校验，替换度量与阈值 | [F222](./f222-release-gate-policy-v2.md) |
| 确定性指标阈值定多少 | **首轮不定**，`report` 模式产出分布后再冻结 | F222 |
| 语义重建不对称性 | **先执行 B**（吸收 Agent 偏向多写），A 列为 A12 | [讨论稿](../data/semantic-rebuild-asymmetry.md) |
| 工作集淘汰策略 | v1 冻结为 LRU + Pinned 最后淘汰 | [F220](./f220-query-working-set.md) |
| 行→叶子怎么反查 | **Row 加 `route_leaf_ids` 默认字段**，不另建结构；写入时挂载已确定 | [叶子直挂 RowID](../storage/leaf-rowid-v1.md) §5 |
| 语义树路径存不存 | **不存，顺 `ParentID` 实时算**；树会被频繁重构，存下来必然过期 | 同上 §5.1 |
| 向量检索 | **保留**方向，缺的是生产发布方 | 风险 7d |
| 导出面对已删 Row 的过滤 | **可接受，只记不改**——契约是可达性，不是物理擦除 | 审计 §1.5 |
| `EXPORT WIKI`／`INSTALL PACKAGE` | **功能保留**，属接线缺失 | 风险 7c |

---

## E 阶段：引擎侧准则符合性（当前优先）

出口判据：三份最高准则逐条可核对为「已做到」，
[存储层总览「已知偏差」](../storage/README.md)清空 A–D 组。

### E0. 共享循环 redo log

- **前置**：无。**这是当前队头。**
- **规格**：[共享循环 redo log](../storage/shared-circular-redo-v1.md)（5 阶段）
- **进度**：阶段 1（一套共享 redo log）、阶段 2（跨树提交合并为一次 WAL 提交）
  **已完成**；阶段 3 核实后**无可拆**——phase checkpoint 是纯测试接缝，
  poison 补的是「原生文件 ↔ generation」这个阶段 2 没动的事务域（收口在 E4／E6）；
  阶段 4（barrier + checkpoint + 回收接线）**已完成**，
  [已知风险](../development/known-risks.md) 7a 随之关闭；剩阶段 5 固定环
- **依据**：[已知风险](../development/known-risks.md) 7a、
  [架构原则](../product/architecture-principles.md) §1、写入形态 §3／§5／§6
- **改动**：`internal/pagestoremigration/{generation,manifest,authority}.go`、
  `internal/store/treecommit/runtime.go`（接受共享 log + 多 space）、
  阶段 5 才动 `internal/store/wal` 的物理层
- **RED**：
  1. 证明段**没有容量上限也不自动滚**，`LatestCheckpoint()` 恒为 false；
  2. **证明跨树不原子**——在 versions／fulltext／current 三次写入之间注入崩溃，
     重开后三棵树不一致。这条是本项的核心证据
- **完成**：一个 generation 一套 redo log；跨树提交是**一次** WAL 提交；
  四个 phase checkpoint 与 poison 补偿拆除；固定环 + 双指针，
  **持续写入下磁盘恒定不涨**，写满时背压报错而不是覆盖
- **恢复是硬门**：每一阶段都要验「重开后逐字一致」。改错是静默的数据丢失

**为什么从「接段式回收」改成这个**（2026-08-22）：

- 段式回收把磁盘占用**交给 checkpoint 策略去防**；固定环让它**结构上不可能涨**。
  循环不替代 checkpoint——腾出环空间的正是它——但把**静默的无限增长**
  换成**响亮的背压**；
- 查证发现日志层**本来就支持多 space**（`Record.SpaceID`，
  `RecoverSegmentSet(spaces map)`，`recovery.go:197` 按 space 路由），
  只是 `OpenRuntime` 传了个单条目 map（`runtime.go:61`）。
  「每棵树一套」是接线选择，不是限制；
- **顺带堵上一个更要紧的洞**：`PublishMutation` 现在往三套 WAL 各提交一次，
  崩在中间三棵树就不一致——那四个 phase checkpoint 与 poison 标记正是在补这个。
  一套日志 = 一次提交 = 跨树原子，补丁可整个拿掉；
- 固定大小 × 每棵树一套 = 磁盘正比于树数，而每表一棵树后正比于表数。
  **必须先合并，环才是全局硬上界**

### E1. 候选预测器只给路径

- **前置**：无。可与 E0 并行
- **规格**：[候选预测器只给路径](../query/predictor-path-only-v1.md)（4 阶段）
- **改动**：`internal/discovery/frame.go`、`internal/routelexical/search.go`、
  `internal/msql/executor/{route_candidates,lexical_locations}.go`、
  `internal/result/envelope.go`
- **RED**：证明两个语句当前返回 score／reason／matched_fields 且**不返回路径**
- **完成**：只返回 `database` + `table` + 完整语义树路径；排序仍在内部做但不外露；
  对外按路径字典序稳定输出
- **已定**：旧字段**删掉**、版本号提到 `memora.discovery-frame/v2`，不保留不填——
  永远为空的字段是个会被下游当真的谎
- **进度**：阶段 1–3 **已完成**；阶段 4（`row`／`column` 的路径）等 E3
- **为什么第二**：设计已写、小而独立，一次拿下一整条准则；
  `routelexical` 甚至已经读到路径又丢掉（`search.go:281` 读、`:285` 丢）

### E2. 派生索引解耦

- **前置**：无。但**排在 E3–E6 之前**
- **依据**：[架构原则](../product/architecture-principles.md) §1；审计 §2.1、§2.2
- **改动**：`internal/pagestoremigration/authority.go`（`PublishMutation`）、
  `apply.go`（投影时机）
- **模板**：`authorityChangeTree.reconcile`（`change_index.go:269`）——
  变更日志驱动、游标推进、批量、读时惰性触发。**照它做，不另起炉灶**
- **RED**：证明一次 INSERT 跨两个无原子性事务域，且 fulltext 失败会以两种形态出现
  （投影失败 ⇒ 写入被拒；提交后索引失败 ⇒ 发布中毒）
- **完成**：`PublishMutation` 不再同批写 fulltext；四个 phase checkpoint 相应减少；
  写入事务只写自己的数据
- **已定**：读一致性不成问题——追平**跟在写入后面立刻跑**（在它的事务之外），
  读路径再兜一次底，所以没有可见滞后。「解耦」解的是事务，不是时机
- **已定**：游标存在 fulltext 树自己里（第四个 key 前缀 `keyKindMetadata`），
  与文档同事务落盘。备选是 `treecontrol`（为一棵树改所有树的页格式）或单独
  文件（放 generation 里撞内容摘要校验，放外面就没法原子）
- **完成（2026-08-25）**：`PublishMutation` 与 `publishCatalog` 都不再写 fulltext；
  追平由 `fulltext_catchup.go` 按变更日志增量做
- **为什么在结构改动之前**：先做它，后面每一项要动的树就少一棵

### E3. 语义索引叶子直挂 RowID

- **前置**：E2
- **规格**：[叶子直挂 RowID](../storage/leaf-rowid-v1.md)（7 阶段）
- **改动**：`internal/router/model.go`（Node 加 RowID）、`internal/row/model.go`
  （Row 加 `route_leaf_ids`）、`internal/nativerouter/repository.go`（编解码）、
  以及约 310 处 membership 引用散在 21 个非测试文件
- **RED**：证明 `router.Node` 上没有能放 RowID 的字段，叶子→行必须另查一处
- **完成**：membership 两个 object kind（9／13）**退役**（编号不回收、
  `ObjectKindMax` 不下调，理由见规格 §7.1）；变更日志 `route_membership` entry
  并入 `route_node`；三类语义健康问题
  （`stale_membership`／`invalid_membership_scope`／`multi_row_leaf`）**结构性消失**
- **对外可见的能力减少**：语义健康少三项，外加 Route revision 会被数据写入推高；
  两条都已记入[待发布的对外可见变化](../development/release-notes-pending.md)
- **阶段 7 的结论：不删 `Node.Path`**（量测见规格 §7.3）。量测顺带挖出真正的
  瓶颈——`nativerouter.Get` 枚举整库找最新 revision，一页 SELECT 结果就是一页
  全库扫描；改成有界点探后 1555 节点的树从 247 µs 降到 1.03 µs 且不再随树长。
  删 `Path` 的原定收益（RENAME 只写一个节点）不成立：全文／向量／词法三个
  派生索引也物化了同一条路径

### E3.5. 共享 buffer pool ✅

- **前置**：无。但**是 E4／E5 的硬前置**
- **依据**：[每表一棵树](../storage/per-table-tree-v1.md) §5.5
- **现状**：与 InnoDB 不同，这里**每棵树一个 buffer pool**——`buffer.New` 全仓只有
  一个调用点（`treecommit/runtime.go:90`，在 `OpenRuntime` 内），每棵树调一次；
  loader 闭包把 `SpaceID` 写死，结构上无法共享
- **为什么是硬前置**：E4/E5 让树数正比于表数，于是内存变成 **16 MiB/表**
  （每表业务树 + history 树）。10 张表 160 MiB，100 张表 1.6 GB——
  「常驻内存有上界」这条准则会被直接推翻
- **RED**：证明开 N 棵树就有 N 个 pool、常驻内存随树数线性增长
- **已完成**：`buffer.Router` 按 `SpaceID` 分派 loader 与 writer；
  generation 开一个 pool 服务所有树，容量是一份总量。
  `RuntimeConfig.Pool` 给了就用共享的、不给就自建，单树调用方不受影响。
  change index 那棵树仍自带 pool——它一棵、固定、不随表数增长

### E4. 每表一棵独立 B+ 树 + RowID 按表递增

- **前置**：E2、**E3.5**
- **规格**：[每表一棵树](../storage/per-table-tree-v1.md) 阶段 1–3
- **改动**：`internal/pagestoremigration/{generation,manifest}.go`（动态树集合）、
  `internal/store/currentrowindex/`（键收缩为 `row_id`）、
  `IDSource` 签名加表标识并收敛 8 处重复定义
- **RED**：证明扫一张表会读到其他表的条目；证明 `IDSource.Next()` 不接受表参数
- **完成**：每张业务表一棵聚簇树；表树内保留键做 RowID 计数器
  （范式照搬 `rowversionindex.HighWater`）；同表连续插入拿到连续 ID，重开不回退
- **待决**：RowID 变为**表内唯一**后，跨表引用必须带表标识——
  `SHOW CHANGES` 的 `ObjectID`、`change.Entry.RelatedObjectIDs` 要逐一核对

### E5. history 独立成表

- **前置**：E4（同一套机制，**分开做等于写两遍**）
- **规格**：[每表一棵树](../storage/per-table-tree-v1.md) 阶段 4–6
- **改动**：新增每表 history 树；`internal/row/model.go` 加 `history_id`；
  `internal/nativerow/history.go` 读写切换
- **RED**：证明 history 是扁平 object kind、读一行历史不是范围扫
- **完成**：键 `(row_id, 序号)`；读完整历史一次范围扫；`ObjectKindHistory` 删除；
  **已删除 Row 的 `(row_id, *)` 区段一并不可达**
- **一并裁定**：`Row.ChangeSequence`（`48ef5b6`）的去留——它是已废弃路线的遗留
- **回归**：本项起每阶段重跑「已删除 Row 从任何面都拿不到」
  （`internal/daemon/f227_row_relation_archive_test.go`）——
  history 变成表是这条契约最容易漏的地方

### E6. 三份日志与恢复

- **前置**：E4、E5
- **规格**：**尚未编写**。E4/E5 定型后再出，避免 binlog 记完再改
- **范围**：binlog 独立成日志且为唯一恢复依据；redo WAL 加
  `prepare`/`commit` 两阶段标记；change log 收窄为事务回滚 undo 依据
- **为什么最后**：风险最高（动恢复），且 binlog 应当记录定型后的结构

---

## S 阶段：工程稳态（可并行，不阻塞 E）

### S1. `EXPORT WIKI` / `INSTALL PACKAGE` 接线

依据风险 7c。把 `Packages`/`Wiki` 注入生产 handler。
**前置**：`dbpackage`／`wikiexport` 现依赖已死的 legacy service 层
（审计 §3.1），接线要么一并迁移、要么先解耦。

### S2. schema／route 对象锁的去留 ✅

**已裁定：删。** 对象锁在串行写入之上只多买到一件事——同一对象快速失败
而不是排队。这在 Row 热路径上值得有，在 schema／Route 变更上不值得：
它们稀少、结构性，且已有 `EXPECTED REVISION` 这道更精确的乐观并发闸。
`SchemaKey`／`RouteKey`／`Kind` 枚举一并删除，`Key` 收敛为三段。

### S3. 向量检索发布方 ✅

**已裁定：记为「未启用」。** 发布方不是接线问题而是产品问题——
它需要 embedding 提供方与一整套重算策略。在那之前造一个发布方，
是先造结构去满足洁癖。`PredictorUnavailable` 本来就是准确的对外表述。
开启条件与理由见[已知风险](../development/known-risks.md) 7d。

`Generation.vectors` 每次查询全量重载的问题**已修**：按 marker 缓存已打开的
generation。已发布的 generation 不可变且由 manifest 摘要命名，
所以没有失效逻辑要写，只有一个摘要要比。

### S4. CI 增加 Linux runner ✅

`test` job 改成 `os: [macos-latest, ubuntu-latest]` 的 matrix，`fail-fast: false`。
Linux 侧的全套八道门已在容器里实测通过（本仓库的开发容器就是 Linux）。

### S5. 引入 lint stage ✅

`scripts/ci.sh` 新增 `lint` stage，跑 staticcheck、errcheck、ineffassign
三个检查器，版本各自钉死。**没有基线文件也没有豁免清单**：
引入时把存量 130 余条一次修完，所以之后报出来的都是新增的。

- **没用 golangci-lint**，直接跑三个上游工具：少一层版本适配，
  钉版本更直接，`go run` 就能跑，不需要装二进制；
- staticcheck 关掉 style 组。`ST1005` 要求错误串小写，而本产品的错误是
  面向用户的文本、里面是领域对象名（Row、Tree、Page index generation）——
  那是有意偏离 Go 惯例，不是疏漏；
- errcheck 跳过测试文件：测试里忽略的错误下一句断言就会炸出来，
  为 `t.Cleanup` 闭包套壳没有收益；
- 存量修法：`defer x.Close()` 一律改成 `defer func() { _ = x.Close() }()`，
  与仓库本来就在用的写法统一；另外真修了四处 `defer tx.Rollback()`。

### S6. 文档解析内存回归门

把「峰值堆 ≈ 正文 7 倍」写成回归测试（EPUB/DOCX/PDF 各一），
并把三个配置上界下调到与实测能力一致。依据风险 4、5。

### S7. 读路径与 Session 边界 ✅

- `ListCommittedChanges`／`GetCommittedChange` 摘除 `BeginWrite` 耦合，
  或加注释说明为何必须序列化（风险 6）；
- `msqlservice.OpenSession` 增加会话数上限（风险 7）；
- `treecontrol.EncodeBootstrap` 改为返回 error，使「生产代码零 panic」成为
  可断言不变量（风险 11）；
- `parser.go:28` 空分支改为直接调用或删除（风险 10）。

**已全部完成。** 第一条（A10 的硬前置）的落地形态是：读变更日志时先在读锁下
做两次 high-water 比较，索引当前就直接读完，落后才升级到写锁去追平。

---

## A 阶段：Agent 侧（转后，一项未删）

前置：**E 阶段出口判据达成**。理由见[路线 v3](./roadmap-v3.md)「为什么引擎优先」——
简言之，这些工作全部建立在 Row 结构、挂载方式与 history 存法之上，
顺序反过来要返工两遍。

出口判据：A5 产出三组可复现对照结论，且 A1、A2 修复前后的同题对照显示
导航深度实际变化。

| # | 工单 | 前置 | 要点 |
| --- | --- | --- | --- |
| A1 | [F221](./f221-evidence-sufficiency.md) Evidence 充分性与导航终止 | E 阶段 | 零行 SELECT 不终止导航；无 `substantive` 证据时拒绝作答；预算放宽到 8/6 |
| A2 | [F220](./f220-query-working-set.md) Query Working Set Stage 1 | A1 | 正向条目带完整 Route 链路；保守失效；LRU + Pinned 最后淘汰 |
| A3 | [F219](./f219-deterministic-answer-scoring.md) 确定性答案评分 | A2 | 主指标 `route_hit`／`field_hit`／`retrieval_correct`；transcript 不支持时判未命中 |
| A4 | [F222](./f222-release-gate-policy-v2.md) Release Gate Policy v2 | A3 | `report`/`gate` 双模式；阈值未冻结时 `gate` 拒绝运行 |
| A5 | 三组小规模对照 | A1–A4 | 三 arm／强弱模型建索引／工作集冷启动；产出物之一是冻结 `gate` 阈值 |
| A6 | [F224](./f224-mandatory-row-route.md) Row 必须可导航 | **E3** | **判据要重写**：从「有没有 live membership」改为「有没有叶子指向它」，读 `route_leaf_ids` |
| A7 | [F225](./f225-mandatory-row-summary.md) Row 必须可展示 | E 阶段 | summary role 列非空；引擎只判定非空不判定质量。SKILL.md 侧已落地 |
| A10 | F220 Stage 2 | A5、S7 | 负向记忆、相关性淘汰、精确失效 |
| A11 | 跨 Session topic 身份与有界恢复 | A10 | 需先出独立规格 |
| A12 | 原文可恢复性：候选 A | — | 引用但不拥有外部原文归档；需先出独立规格 |
| A13 | 写入反馈回路 | — | 检索失败与人工修正回流到建模决策；需先出独立规格 |
| A14 | Route 自治维护 | E3 | 初始 fan-out 已由 F223 交付；剩余是超量时自动提拆分/合并提案与批量重构 |

**A6 的重排是本次唯一改变依赖关系的地方**：它原本无前置，现在必须等 E3。

---

## 不在当前路线

保留设施、不再投入，恢复条件见各自文档：F226 Stage 2 物理文件按 Database 拆分
（2026-08-20 已评估并延后）、大语料批量评测（F212–F215、候选 F216–F218）、
OCR/视觉运行时（候选 F209）、内置 `memora ask` 产品化、
Compaction／Secondary Index／Advanced MVCC／Replication／PITR／多设备同步／
Apple Accelerate／HNSW。

## 立即生效的策略变更（无需工单）

**吸收 Agent 的 worthiness 默认偏向多写。** 理由：过度抽取可恢复（删 Row），
抽取不足不可恢复（原文在 Job 释放后回收）。在 A12 完成前，默认必须偏向多写。

## 关联

- [路线 v3](./roadmap-v3.md) — 为什么是这个顺序
- [写入形态](../product/write-model.md)、[查询形态](../product/query-model.md)、
  [架构原则](../product/architecture-principles.md) — E 阶段的依据
- [已知风险](../development/known-risks.md)、
  [架构审计](../development/architecture-audit-2026-08.md) — S 阶段的依据
- [Feature 产品门](./feature-product-gate.md)、[TDD 协议](./feature-tdd-protocol.md)
