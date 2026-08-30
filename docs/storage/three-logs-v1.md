# 三份日志各司其职，binlog 是唯一恢复依据

状态：**迁移设计**（2026-08-30）。落实[写入形态](../product/write-model.md)
§3「写入流程（含日志与两阶段提交）」与 §5「日志体系：三份日志各司其职」。
不是独立规范——与写入形态冲突时以写入形态为准。

前置 E4、E5 **均已完成**，所以 binlog 记录的是**定型后**的结构
（每表一棵业务树 + 一棵 history 树、按表递增的 RowID、归属在变更日志里）。
这正是本项排在最后的原因：binlog 是恢复的唯一依据，
记完再改结构，等于把恢复依据也一起改掉。

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行。

## 1. 现状：三个空目录

| 写入形态要求 | 现状 | 证据 |
|---|---|---|
| change log 是**事务回滚的 undo 依据** | 是归属来源 + `SHOW CHANGES` 时间线 + fulltext 追平驱动，**唯独不是 undo** | `nativerow.attributionFor`、`internal/pagestoremigration/fulltext_catchup.go` |
| redolog 有 `prepare`／`commit` 两字段 | 只有一种事务边界记录 | `store/wal/segment.go:44` `TypeCommit`，无 prepare |
| binlog 独立成日志，且是**唯一恢复依据** | **不存在** | 全仓无实现 |
| 实例目录下有 redo／undo／binlog | 三个目录**建了，从来没人往里写** | `instance/instance.go:39-41` |

最后一条值得单独说：`redo/`、`undo/`、`binlog/` 三个目录在每个实例里都被创建，
而**没有任何代码写入它们**。真正在用的 redo WAL 住在 generation 目录里
（`page-index-v1/redo.wal`），与那个 `redo/` 无关。
三个空目录恰好写着这份设计要补的三样东西。

### 今天的恢复依据是什么

**原生存储记录文件**（`internal/store/native`）。它是真相之源：Catalog、Row
的每个版本、Relation、Route、变更封套都作为记录追加在里面。generation
（B+ 树那一套）是**派生**的——开机时从记录文件对账，必要时整个 COW 重建。

所以「唯一恢复依据」这条**部分已经成立**：确实只有一个源。
缺的是它没有被摆成一份**顺序日志**，而是一个既要点查又要顺序重放的文件。

## 2. 为什么顺序不是 binlog 先做完再补标记

写入形态 §3 的顺序是 change log → redo `prepare` → binlog → redo `commit`。
两阶段标记的**用途**是崩溃后判断「binlog 那一步写完了没有」。

所以**没有 binlog 的时候，prepare／commit 无事可判**：
今天一个 WAL 事务要么带着提交记录完整落盘、要么没有，本身已经是原子的。
先加标记等于造一个没有对手方的机制——正是[已知风险](../development/known-risks.md)
7a 那类「写好了、测好了、没人调用」的失败模式，本路线一直在消灭它。

**所以本设计的阶段顺序是 binlog 在前、标记在后**，与写入形态列举流程的
书写顺序不同。流程是运行时的先后，阶段是建造的先后，两者不必一致。

## 3. binlog 记什么

**记「本次数据更改」，不是 SQL**（写入形态 §3）。理由那里写明了：
时间戳必须是最初写入的时间，不能等 SQL 执行时才打。

一条 binlog 记录 = 一次事务，内含：

- 提交序号与提交时间（最初写入的时间）；
- 归属（actor／source／reason 等）——与变更封套同源；
- 这次事务改动的每个对象的**终态**：Row 的新版本（业务表）、
  以及该版本在 history 表里的那一条。两者绑定在同一条记录里，
  因为写入形态 §3 要求「每次更改同时绑定两张表」。

**history 表也在 binlog 里**，所以后期重建时它从 binlog 一并恢复，
不需要第二个来源。这与 E5 的落地一致：history 树是派生的，
它的权威内容就是每个 Row 版本本身。

## 4. 三份日志的边界（落到本仓库的具体对象）

| 日志 | 本仓库的载体 | 职责 | 不做什么 |
|---|---|---|---|
| change log | `ObjectKindCommittedChange` 封套 | 事务回滚的 undo 依据 | 不做崩溃恢复依据 |
| redolog | `internal/store/wal` 的 SegmentSet | 判定事务提交状态（prepare／commit） | 不做重建来源 |
| binlog | **新增**，实例目录 `binlog/` | 唯一恢复依据 | 不参与点查 |

**change log 收窄**是三者里唯一的「减法」：它今天还兼着归属来源与
fulltext 追平驱动。收窄不等于把那两件事挪走——归属确实属于「这次事务做了什么」，
fulltext 追平读的也是同一批事实。收窄指的是**恢复不再依赖它**，
以及它获得 undo 所需的「改动前」那一半。

**这是本设计最实质的新增**：今天的封套只记「改成了什么」（`AfterRevision`），
没有「原来是什么」。没有前像就没有 undo，所以 undo 能力是要**造**出来的，
不是接线接出来的。

## 5. 分阶段与验证门

每阶段一条独立可验证的性质。**恢复是全程风险最高的部分**——
改错了是静默的数据丢失，所以每一阶段的门都必须包含「重放后逐字一致」。

| 阶段 | 内容 | 独立可验证的性质 |
|---|---|---|
| 1 | binlog 成为一份真的顺序日志，每次提交追加一条 | 一个空实例重放 binlog 后，Catalog／Row／history 与原库**逐字一致** |
| 2 | redolog 加 `prepare`／`commit`，binlog 写成功后才 commit | 崩在 prepare 之后、binlog 之前 → 该事务未提交；崩在 commit 之后 → 已提交 |
| 3 | 变更封套补「改动前」，change log 成为 undo 依据 | 事务中途失败，按 change log 把业务表与 history 表退回改动前 |
| 4 | 恢复改为只读 binlog；记录文件退为点查存储 | 删掉 generation 与派生索引后，只凭 binlog 重建出逐字一致的库 |

**跨阶段的逐字一致基线**：切换前后比对 `SELECT`、`SHOW HISTORY`、
`AS OF REVISION`／`AS OF COMMIT_SEQUENCE`、`OPEN ROUTE`、`SHOW CHANGES`、
Catalog Atlas 与逻辑快照哈希。

**删除契约的回归**：每一阶段都要重跑「已删除 Row 从任何面都拿不到」
（`internal/daemon/f227_row_relation_archive_test.go`）——
一份能重放出旧版本的日志，是这条契约最容易被绕过的地方。

## 6. 明确不做

- **不做 PITR、不做复制、不做多设备同步。** binlog 是它们的前置，
  但它们各自是独立产品决定，不在本设计里；
- **不把记录文件删掉。** 阶段 4 只让恢复不再依赖它；
  它仍然是点查的存储（B+ 树从它派生）。把它也换掉是另一份设计；
- **不改 LSN 语义、不改 generation 格式。** 本设计只加一份日志与两个标记。

## 关联

- [写入形态](../product/write-model.md) §3／§5（上位规范）
- [共享循环 redo log](./shared-circular-redo-v1.md)（redolog 的现状与环）
- [每表一棵树](./per-table-tree-v1.md)（binlog 要记录的、已定型的结构）
- [存储层总览](./README.md)、[执行计划](../planning/execution-plan.md) E6
