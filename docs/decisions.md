# 决策日志

状态：运行中的判断记录，不是 ADR 系列；规范级结论仍进 [`decisions/`](./decisions/)。
按 `~/.dsh/AGENTS.md` 约定，向顾问咨询后的结论当场写在这里。追加，不覆盖。

## 2026-09-20 · SQLite 基座如何成为开发基线

对象：`rewrite/adr0011`（领先 `origin/main` 32 个提交，`main` 独有提交 0）如何落到默认分支。
状态：**候选，待授权**。

**结论**：在 `main` 上落**一个**「SQLite baseline」提交（`git merge --squash` 或等价做法），
`rewrite/adr0011` 保留为远端存档 tag（如 `archive/adr0011-rewrite`）；不做 fast-forward。

**理由**

- 32 个提交是一次改写的收敛轨迹（含「先删错再修回」），不是 32 个独立 Feature。
  留在 `main` 上会长期污染 `blame` 与 `bisect` 的信噪比。
- 基线的语义是**一个点**，不是一段过程。ADR-0011 已承载这次改写的解释价值，
  提交序列是冗余的第二份叙事。
- 仓库规则要求每项 Feature 独立 Review、授权、验收、合入。这 32 个提交从未走过该流程，
  也从未跑过 CI；fast-forward 等于追认一批未合规提交为「已合规历史」。

**弃选**：fast-forward 全部 32 个提交。代价是放弃改写过程的逐提交可 bisect 性，
以及单个删除动作的 `blame` 归因粒度——改写以删除为主，损失小。

**前置条件（与选项无关，必须先做）**：`.github/workflows/ci.yml` 给整个 gate run 设了
`CGO_ENABLED=1`。Go 只在 `CGO_ENABLED` **未设**时才在交叉编译时自动关 cgo，
因此 `GOOS=linux` sweep 在 macOS runner 上必然失败，GitHub CI 必红。
本地实测：不设该变量 `./scripts/ci.sh` 全绿（EXIT=0）；设成 `1` 则死在 `vet`（EXIT=1）。

## 2026-09-21 · Agent 与引擎的分界：语义归 Agent，确定后果归引擎

对象：哪些是 Agent 发的指令，哪些是引擎在同一事务里内部展开的。
状态：**方向性结论**（顾问判断，待授权；两项表面变更见下）。

**判据**：**语义由谁决定**。需要一个引擎猜不出的自由选择（名字、边界、归属、裁决）→
必须是 Agent 的一句；完全由已写入的行状态 + 已定规则推导出唯一结果 → 收进引擎，作为行写入的
**确定后果**。这与架构原则 §1「一次逻辑操作一个事务域」同源：确定后果必须和触发行写入
同事务，不能让 Agent 再补一句。

**归属**：Agent 管选库表与内容、节点起名/层级/改名/移动、拆分边界、合并对象、冲突裁决；
引擎管叶子生灭与挂载、删行后的归档/剪枝/摘链接/删 history、revision 与 history 与变更信封、
空节点清理、扇出/权限/revision guard。一句话：**挂在行上的东西归引擎，需要起名和归属的归 Agent。**

**两项表面变更（待授权）**

1. `DELETE ROUTE` 从 Agent 表面退役——删空节点没有自由度，是删行或重构的确定后果；
   留着它会开出第二条路径，让 Agent 删掉还挂着 live 行的节点。核心里已有该语句
   （parser/executor/security 都认），但现役文档与 Skill 从未暴露它。
2. INSERT 允许**隐式建路径**：Agent 给出的路径名就是它的语义选择，补齐中间节点与叶子是
   确定后果，同事务完成。显式 `CREATE ROUTE` 保留给先搭空骨架。

**弃选**：把 `CREATE ROUTE` 也收进引擎（让 Agent 只写行、路径由引擎从内容猜）——那是让
引擎猜语义，违反"引擎不替 Agent 选叶子、不起名字"。

**落盘**：[Agent 与引擎的分界](./query/agent-engine-boundary.md)。

## 2026-09-21 · 空分支清理：所有让它变空的操作都做

对象：语义树里「变空即物理删除、递归向上剪枝」这条规则的触发面。
状态：**方向性结论**（用户定）。

**结论**：**任何让它变空的操作都触发清理**，不只在删行：删行、换挂载（UPDATE 改
`route_leaf_id`）、MOVE 把子节点移走、`APPLY ROUTE MUTATION` 之后不再有子节点的分支。
清理与触发它的操作**同一个事务**，递归向上；根变空一并删除、Catalog 的
`router_root_id` 置空，回到「还没有语义树」的合法空状态。

**连带修订**：`Route 配套表` 原来那句「**节点不物理删除**」被取代。现在分两种——
**有接替者的身份变化**（SPLIT／MERGE）旧节点置 `deprecated` + `successor_ids`、保留
（树外旧 `route_id` 的解析入口）；**没有接替者的变空**物理删除。已废弃且带接替者的节点
不算空壳，不被清空规则删掉。

**理由**：只在删行路径上做，MOVE 和换挂载就会留下空壳分支，还得靠一次额外重构去清——
同一条不变量留两个触发面，就是架构原则 §1 说的那种耦合症状。

**弃选**：保留空的根（树是表的一部分，空根当伞）——那会留一个没有任何内容的命名节点，
且让剪枝规则在根上破例。归档里有恢复所需的全部内容，需要时由 Agent 重建。

**落盘**：已修订 [Route 配套表](./product/route-companion-table.md)「重构与废弃节点」、
[行删除](./product/row-delete-archive.md) 第 4 步、[行生命周期](./product/row-lifecycle-successor.md)
的拆分叶子例外、[Agent 与引擎的分界](./query/agent-engine-boundary.md)。

## 2026-09-21 · history 指针写在 history 表；与 successor_ids 是同一条边的两面

对象：拆分／合并后新行 history 的来源指针放哪，以及它与源行 `successor_ids` 的关系。
状态：**方向性结论**（用户定），本条把 `history 谱系` 的未决项结清。

**结论**

1. **指针写在 history 表里**：落在**新行 history 的首条记录**（它创建的那一条）上；
   MERGE 指向多个来源，所以是列表。
2. **`successor_ids` 与 history 指针同时存在**，不合并、不二选一。它们是同一条身份边的
   **两个方向**：`successor_ids` 在源行上（旧 → 新，供读路径立刻解析出当前行），
   history 指针在 history 表里（新 → 旧，供回溯不断链）。都只存稳定 ID，同事务落盘，
   两个方向都 O(1)——两边都不能省，省一个就得扫表，而两个都是读路径。
   这是「同一份事实存两遍」的**写明例外**（[架构原则 §2](./product/architecture-principles.md)），
   与「叶子 `row_id` ↔ 行 `route_leaf_id`」同一个模式。
   （`successor_ids` 现状：源行上的字段，reshape 发布时写入全部接替者；`DB.Successors` 沿链解析、
   设跳数上限防环。语义树节点上另有同名机制管废弃 route 节点的接替，是另一套对象。）
3. **源行 revision 不推进。** 身份变化不是这一行的内容版本——推进却不写 history 会在
   `(row_id, revision)` 上留一个没有记录的空洞，破坏「读一行完整历史是一次范围扫」。
   `superseded` 由**状态 + `successor_ids`** 表达。

**弃选**：只留一个方向（省一份指针）——省哪个都得让读路径扫表。

**落盘**：[history 谱系](./product/history-lineage.md) 定稿；已修订
[行生命周期](./product/row-lifecycle-successor.md) 的两类变化表（`superseded` 用词 + revision 口径）。

## 2026-09-21 · 级联摘链接的预算口径：guard 管目标行，级联另设界并如实报数

对象：删除第 5 步会写别的行，`max_affected_rows` 与结果信封怎么算。
状态：**方向性结论**（用户选 C）。

**结论**：① `max_affected_rows` 保持**目标行**语义（DELETE 校验 1 行），不因级联变大而被拒；
② 级联**另设一个界**（单次删除能摘的链接条数上限），超界拒绝、让 Agent 分步清理；
③ 结果**如实报数**：`affected_rows` 仍是目标行，级联行数另外报出并照常进 `mem_changes`。

**理由**：宪章要求每一步有界，级联不能例外；但把级联塞进 `max_affected_rows`，会让一次正常的
删除随「别人链了它多少」随机失败——把两件事绑进同一道 guard，正是架构原则 §1 判据 3 的症状。

**弃选**：A 把级联算进 `max_affected_rows`——需要 Agent 先数出对面有几行，而 `links` 现在
**既不可读也不可写**（只有字段存在），前提都不具备，且读与写之间有 race。
B 不给级联设界、只在结果里报——`max_affected_rows` 就不再是"这次请求写多少"的上界。

**待定**：级联上限的具体数值与是否进库内配置，随[行链接](./product/row-links.md)实现时定。

**落盘**：[行删除](./product/row-delete-archive.md) 的「级联写的预算口径」。

## 2026-09-21 · 删除改为归档式物理删除；history 只记原地修改

对象：DELETE 的语义，以及 SPLIT／MERGE 后 history 的归属。
状态：**方向性结论**（用户在讨论中定），两处未决见下。

**结论一 · 删除 = 归档后物理删除**。一个事务里：① 删除前的完整语义树路径与节点/行内容
写进**归档表**；② 主表整行删除（含 `route_leaf_id`）；③ 删掉它那一个叶子；④ 自底向上剪枝
——父节点摘掉这个子节点后**没有其它子节点才连父一起删**，还有其它子节点就只摘 `child_ids`
里的这一项；⑤ **摘链接**：链接两面都存，被删行自己的 `links` 就是「谁指向我」的清单，
逐个到对面行里把这一条删掉，对面行照常 `revision + 1`、写 history；⑥ 该行 history 全部删除，
不归档。**恢复不由引擎做**：归档表是引擎逻辑的终点，Agent 读归档自己重建。

**补定（2026-09-21）· 入向链接同步摘掉，不走懒更新**。理由：两面都已经在库里，
被删行的 `links` 就是反向索引，倒推即得，不需要扫全库找悬空链接；留着悬空就违反了
[行链接](./product/row-links.md)「两面必须一致」。

**结论二 · history 只记原地修改**。SPLIT 时源行 history 不动，两个新行各建自己的 history
并**指向源行 history**；MERGE 的新行 history 指向多个来源（所以指针是列表）。源行仍留主表，
用一个字段记录新 row id、读时懒解析。

**理由**：删除要可恢复，但引擎不该背恢复语义；归档 + 物理删让主表与语义树不留死节点。
history 只承载「这一行被改过什么」，身份变化用指针表达，源行历史不被污染，回溯不断链。

**弃选**：① 只置 `deprecated` 留墓碑（现状）——主表与树上长期堆死节点，恢复也没有自足快照；
② 引擎提供 RESTORE——恢复的判断属于 Agent，引擎只保证归档自足。

**未决**（下次定）：history 指针字段的位置；它与 `successor_ids` 是否重复
（[架构原则 §2](./product/architecture-principles.md)）；级联摘链接的写预算口径
（`max_affected_rows` 现在只算目标行）。

**落盘**：[行删除](./product/row-delete-archive.md)、[history 谱系](./product/history-lineage.md)；
已修订 `write-model` §1.2、`query-model` §7、`row-lifecycle-successor`、`row-links`、`msql-mutation`。

## 2026-09-21 · 挂载定为 1:1：一行只占一个叶子

对象：叶子与数据行的挂载基数，以及这份挂载事实存哪边。
状态：**方向性结论**（用户在讨论中定），字段改名待契约变更。

**结论**：**一行只占一个叶子**，取代「同一 Row 可属于多个 Leaf」。挂载存**两边**
（叶子上的 `row_id` + 行上的单值 `route_leaf_id`），不改结构；字段名单数化，未挂载用空串。

**理由**（顾问判断，当场采纳）

- 1:1 之后两边的代价从「列表同步」降为「两个标量互指」，收益不变：`OPEN ROUTE` 与
  `SELECT` 带路径仍是 O(1)。只存一边就要给 `row_id` 建索引做反查——那仍是冗余结构，
  只是改名叫索引，还多一次 B-tree 查找与跨表 join；让最热的导航路径去数据表里扫更差。
- 例外的正当性因此**从性能换成可判定性**：两边都是标量，一致性成为一条可断言的等式
  `leaf.row_id = r` 且 `row.route_leaf_id = leaf`，而不是一份要同步的列表关系。
- **迁移零 schema 改动**：字段本来就是 TEXT 里的 JSON，读时取首元素、写时写单值；
  一次性校验扫出 `len > 1` 的行，报错让人手工拆。

**副作用**：一次删除只对应一条叶子路径（[行删除](./product/row-delete-archive.md) 的
「删哪些叶子」问题随之消失）；`route-mutation-plan` 里「多个 Leaf 定位同一 Row 才能合并」
不再是合法状态。

**待办**：`route_leaf_ids` 是**已发布的外部契约名**（Skill、`contract.json`、两个适配器副本）。
改名是一次契约变更，要与契约版本一起动；现行 option 名暂留在解释器与 Skill 里。

**落盘**：已修订 `write-model` §1.3／§4.4、`query-model` §3、`route-companion-table`、
`row-lifecycle-successor`、`charter`、`semantic-routing`、`msql`、`msql-mutation`、
`row-navigable`、`skill-write-v1`、Skill 与两个适配器副本。

## 2026-09-20 · 落地顺序：先修 CI，再压基线，再补核心包回归

对象：`rewrite/adr0011` 落地这一块的拆分、顺序与最大风险。
状态：**方向性结论，待授权**（顾问咨询后落盘；不改变上一条「压一个 baseline 提交」的候选地位）。

**结论**：顺序是 修 `ci.yml` → 在分支上见一次真实绿 CI → squash 一个 baseline 提交落 `main`、
分支留 tag → 补 `catalog`／`row`／`instance` 三个核心包的最小回归 → 再开「行必须可导航」。

**理由**

- `ci.sh` 全绿 ≠ GitHub CI 会绿：本地 macOS 与 runner 的 cgo／交叉编译环境不是一回事；
- 先见绿 CI 再 squash：否则 `main` 首次 CI 就红，回滚一个 22.5k 行的 squash 提交代价极大；
- squash 前至少让 `catalog`／`row` 有可跑回归，否则 34 个提交压成一个不可二分点，
  将来回归只能靠读代码定位（15 个包现在零测试，集中在 cli、daemon、adminapi、instance、
  catalog、skillschema、skillwrite、routetrace、row）。

**弃选**：先做「行必须可导航」再落地。产品上它确实是下一件，但把它压在一个从未见过
CI 的基线之上，等于把两处风险叠在同一提交里。

**前置（与选项无关）**：`.github/workflows/ci.yml` 把 `CGO_ENABLED=1` 设在整个 gate run 上，
`GOOS=linux` sweep 在 macOS runner 上必然失败。应把它下沉到需要 cgo 的 job／stage。

## 2026-09-20 · 停用 F 流水号

**结论**：现役工作用题目，不再编号。`F1`–`F228` 只作为旧引擎考古标签。
分支 `feature/<short-name>`。ADR 仍用四位编号（0002–0012）。

**理由**：流水号来自自研引擎 TDD 序列。现行文档曾经引用九十多个 F 编号，
实体规划只剩四个，代码完全不认识它们。再从 F229 往下续，只会把考古和现役混在一起。

**弃选**：另起 `S1` / `N1` 新序列。新数字同样会在下一次改写后变成噪音。

## 2026-09-20 · 现役文档只留当前形态

**结论**：`docs/planning/` 只留队列、TDD、产品门和「行必须可导航」讨论稿。
F 时代 Feature 稿与过程稿进 archive。现役规格去掉「目标形态已改 / 不能当设计依据」横幅。
一叶一行和 fan-out 写在 [写入形态](./product/write-model.md)，不再单开 planning 文件。
