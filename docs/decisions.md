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
