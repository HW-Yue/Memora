# 共享循环 redo log：一套日志、固定大小、双指针

状态：**迁移设计**（2026-08-22）。取代原 E0「WAL 回收接线」——
那一版只打算给现有的段式日志接上 checkpoint 与回收，本文换成 InnoDB 的形态：
**全实例一套 redo log，固定大小，循环使用。**

编写原则同[存储层总览](./README.md)：每条「现状」断言都能指到具体文件与行。

## 为什么换方案

原方案是「滚段 + 回收旧段」（PostgreSQL 形态）：磁盘占用**靠 checkpoint 策略去防**。
新方案是固定环：**结构上不可能涨**。这与[架构原则](../product/architecture-principles.md)
§2 判据 3 一致——结构性消除一类 bug，胜过为它写扫描与修复工具。

**但循环不能替代 checkpoint。** 腾出环空间的**正是** checkpoint（推进 checkpoint LSN
要先刷脏页）。两种设计的区别只在**不做 checkpoint 时会发生什么**：

| | 不 checkpoint 的后果 |
|---|---|
| 段式（现状） | 磁盘**静默**涨到天上 |
| 循环 | 环写满，**写入停住并报错** |

把静默的无限增长换成响亮的背压，严格更好。barrier + checkpoint 那套活两边都要做。

## 1. 现状：本来就支持共享，只是没那么接

**日志层已经是多 space 的**：

- 每条 `Record` 自带 `SpaceID`；
- 恢复按 `spaces[record.SpaceID]` 路由到对应 page store
  （`internal/store/wal/recovery.go:197`），找不到就 `ErrMissingSpace`；
- `RecoverSegmentSet(set, spaces map[uint64]PageStore)`
  （`internal/store/wal/checkpoint.go:112`）收的就是一张表。

**但接线只传了一个条目**：

```go
// internal/store/treecommit/runtime.go:61
wal.RecoverSegmentSet(set, map[uint64]wal.PageStore{config.SpaceID: store})
```

于是每棵树一套日志（`manifest.go:68-71` 的
`catalog.wal`／`current.wal`／`versions.wal`／`fulltext.wal`，changeindex 另有一套）。

**「每棵树一套」是接线选择，不是日志层的限制。**

## 2. 合并带来的三件事

### 2.1 跨树原子性（最要紧，原计划没看到）

`Authority.PublishMutation`（`pagestoremigration/authority.go:418`）一次要写
versions、fulltext、current 三棵树——**三套 WAL、三次独立提交**。
崩在中间，三棵树就不一致。

现有的四个 phase checkpoint（`phaseRowBodyCommitted`／`phaseRowVersionPublished`／
`phaseRowFulltextPublished`／`phaseRowCurrentPublished`）和 `poisonPublication`，
**正是在给这个缺口打补丁**。

一套共享日志 = **一次提交 = 跨树原子**，这套补丁可以整个拿掉。
这正面命中[架构原则](../product/architecture-principles.md) §1 的判据 1
（「一次逻辑操作横跨多个互不保证原子性的事务域」）。

### 2.2 循环才有意义

固定大小 × 每棵树一套 = **磁盘正比于树数**。而
[每表一棵树](./per-table-tree-v1.md)会让树数正比于**表数**——
一个只有十几条数据的个人库要预留 N × 环大小，不合理。

**先合并成一套，固定环才是全局硬上界。**

### 2.3 与 InnoDB 对齐

InnoDB 是一个 buffer pool + 一份 redo log 服务整个实例。这里两样都是每棵树一份。
buffer pool 的那一半见[每表一棵树](./per-table-tree-v1.md) §5.5，是同一个根因。

## 3. 循环怎么做

**LSN 语义完全不变。** 它是全局单调的字节位置（`Segment.startLSN + segmentHeaderSize`
即段内首个 LSN），InnoDB 也一样——循环的只是**文件空间**：

```
offset = (LSN - ringBase) mod ringSize
```

所以 `treecontrol.State.LSN`、页头 LSN、durable frontier、checkpoint 记录
**一律不动**。

**两个指针**：

- **write LSN**：下一条记录写到哪（现有 `nextLSN`）；
- **checkpoint LSN**：最老的、其改动尚未刷进页文件的 LSN（现有 `Checkpoint.RecoveryLSN`）。

两者之间是「在用」区间。写入前检查是否会覆盖 checkpoint LSN 之前的空间：
会，就先强制一次 checkpoint（刷脏页 + 推进尾指针）；推不动就**背压**，不是覆盖。

## 4. barrier 仍然要写

`PublishCheckpoint(barrier DurabilityBarrier)` 要一个能
`FlushThrough(recoveryLSN) error` 的对象，而**生产代码里没有任何实现**，
只有 `wal/checkpoint_test.go:245` 一个测试 recorder。

buffer pool 的帧**不记 LSN**，无法只刷「到某个 LSN 为止」。
**保守解法：忽略入参，把脏页全刷干净**——全刷之后任何 LSN 都被满足，是正确的，
只是比必要的多刷。代价有界（合并后是一个 pool 的容量）。
这是经典 sharp checkpoint，**要在注释里写明为什么忽略入参**，否则会被当成 bug。

## 5. 分阶段与验证门

每阶段独立可验证。**恢复是全程风险最高的部分**——改错了是静默的数据丢失，
所以每一阶段的门都必须包含「重开后逐字一致」。

| 阶段 | 内容 | 独立可验证的性质 | 状态 |
|---|---|---|---|
| 1 | 一个 generation 一套共享 redo log（**仍是段式**）；恢复一次带上全部 space | 恢复逐字一致；四棵树共用一个 WAL 目录 | **已完成** |
| 2 | 跨树提交合并为**一次** WAL 提交 | 在三棵树写入之间注入故障，重开后三棵树一致（此前不一致） | **已完成** |
| 3 | ~~拆掉四个 phase checkpoint 与 poison 补偿~~ | **前提错误，见下** | **已核实：无可拆** |
| 4 | barrier + checkpoint 接线 | checkpoint 能推进；`LatestCheckpoint()` 不再恒为 false | 待做 |
| 5 | 固定环 + 双指针；`offset = (LSN-base) mod size` | 持续写入下磁盘**恒定不涨**；环绕点恢复正确；写满时背压报错而不是覆盖 | 待做 |

**阶段 2 的故障注入是这份设计的核心证据**——它同时证明了旧缺陷存在与新设计修好了它。
实测到的 RED（`TestCrossTreePublicationIsAtomicUnderFault`）：

| 故障点 | versions | fulltext | current |
|---|---|---|---|
| `row-version-published` | 有 | 无 | 无 |
| `row-fulltext-published` | 有 | 有 | 无 |

### 阶段 1／2 落地时改了原计划的两处

**一、事务号从「每棵树各自一套」变成「每个 generation 一套」。**
四棵树的 bootstrap 原先都用事务号 1，日志一合并立刻 `ErrDuplicateTransaction`
——`SegmentSet` 本来就按日志去重。改为从 `DurableFrontier().LastTransactionID`
取下一个。这一撞本身就是证据：此前四套日志互不知情，跨树提交没有共同的事务空间。

**二、阶段 2 必须动 `internal/store/wal`，原文说不用，是错的。**
`parseTreeMetadata`（`tree_recovery.go:18`）原来强制**一个事务只描述一棵树**：
至多一条 allocator redo、恰好一条 root redo，且 root **必须是事务的最后一条**。
三棵树塞进一个事务直接判 `ErrCorrupt`。

改法是把事务定义为**按 space 分段的拼接**，每段仍守原规则（页 redo → 可选
allocator → root 收尾）。同一 space 的记录必须连续；一个 space 在另一个 space
之后再次出现是损坏流，不是第二段——段边界是判断 root redo 归属的唯一依据。
单树事务是它的退化情形，旧格式一字未变。

### 阶段 3 的前提是错的：那些补偿补的不是这个洞

原文假设四个 phase checkpoint 是生产侧的补偿逻辑。**逐条读过代码，不是。**

`Authority.checkpoint` 字段**在生产代码里从未被赋值**——全仓只有测试设置它，
`checkpointPhase`（`authority.go:609`）在生产里恒等于「立即返回 nil」。
它们是**纯故障注入接缝**，运行时零成本；拆掉只丢测试能力，换不来任何简洁。

`poisonPublication` 更不能拆，因为**它补的洞阶段 2 根本没堵**：

```
PublishMutation:
  commit()            ← 原生存储文件的事务
  CommitGroupFunc()   ← generation 三棵树的事务（阶段 2 合并到这里）
```

这仍是**两个互不保证原子性的事务域**。崩在两者之间，原生文件领先于
generation，开机由 `reconcile`（`authority.go:804`）从原生文件追平——
`TestAuthorityRowPublicationFaultsPoisonAndReopenConverges` 注入的
`phaseRowBodyCommitted` 正是这个接缝。

**阶段 2 让「三棵树彼此」原子，没让「原生文件 ↔ generation」原子。**
后者要等[每表一棵树](./per-table-tree-v1.md)与写入形态的三份日志——
业务 Row 自己住进 B+ 树之后，「原生存储文件」这个独立事务域才消失。
它同样命中[架构原则](../product/architecture-principles.md) §1 判据 1，
只是收口点在 E4／E6，不在这里。

结论：阶段 3 **不做删除**，改为把这条边界写清楚，
免得下一个人误以为写入已经全链路原子。

### 阶段 2 引入的接口

- `treecommit.CommitGroup(id, members)`：多棵树一次 WAL 提交。按 SpaceID 排序，
  既固定加锁顺序（两个交叠的组不会死锁），也保证每棵树的记录在事务里连续；
- `treecommit.CommitGroupFunc(id, collect)`：`collect` 里每个 Index
  在**持有自己写锁的状态下**校验并生成 plan，锁一直持到组提交结束——
  否则读者可能看到组里一棵树更新了、另一棵还没有；
- 三个 Index 各加一个 `StageX`（`StageAppend`／`StageReplaceBatch`／`StageApply`），
  无事可做就不入组；
- `Runtime.Commit` 变成 `CommitGroup` 的单成员情形，只有一份实现。

### 一处必须一起改的地方

**每棵树一套日志的旧 generation 会被开机强制 COW 升级**
（`OpenAuthority` 判 `generation.log == nil`）。它可能四棵树俱全、
reconcile 无事可做——**唯一的缺陷是结构性的**：日志分家就没法一次提交。
往它上面写等于把阶段 2 刚买到的原子性又还回去，所以照「缺一棵树」的老路子重建。

## 6. 明确不做

- 不动 LSN 语义（全局单调字节位置，循环的只是文件空间）；
- 不给 buffer pool 加 per-page LSN 追踪（那会让 `FlushThrough` 精确，
  但属于 buffer pool 的独立改造）；
- 共享 **buffer pool** 是另一件事，见[每表一棵树](./per-table-tree-v1.md) §5.5——
  同一个根因，但与本文互不阻塞。

## 关联

- [写入形态](../product/write-model.md) §3／§5／§6（三份日志与恢复）
- [架构原则](../product/architecture-principles.md) §1（高内聚低耦合）
- [每表一棵树](./per-table-tree-v1.md) §5.5（共享 buffer pool，同一根因）
- [存储层总览](./README.md) 第 2 节、[已知风险](../development/known-risks.md) 7a
- 现有冻结规格：[Segment Set](./wal-segment-set-v1.md)、
  [Checkpoint Publish](./checkpoint-publish-v1.md)、
  [Segment Reclaim](./wal-segment-reclaim-v1.md)——
  阶段 5 之后前两份需重写，第三份（回收）随段式一并退场
