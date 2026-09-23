# 整层读取：读取端不设宽度上限，走不完就说走不完

状态：**实现计划**（未开工，等开工）。前置实测见 `docs/decisions.md`。

## 为什么读取端不该有宽度上限

一层的大小由**写路径**约束：`route_policy.branch_fanout`（默认 12，一次最多 +4，上限 100）的含义是
"一个 root/branch 最多挂几个活孩子"，在 `createNode`（每次新建 Route）与 ROUTE MUTATION 计划
（重新挂载）两处都拒。读取端的 `router.CompleteNodes` 注释写的就是这件事：

> A route layer is bounded by the Database's `route_policy.branch_fanout`, **not by a read-side
> budget**, so there is nothing to page — and a listing that somehow did not fit is an error rather
> than a page.

所以 `jev_tree.py` 里的 `MAX_FRONTIER = 4` 是**读取端自己发明的一个上限**，与系统设计矛盾：它把写路径
已经保证合法的一层，按到达顺序砍掉一部分。正确的读取语义是——**当前层有多少个节点就读多少个，整层交给
jev 判**（`choose_layer` 本来就是这样），没有宽度上限。

## 实测：把砍刀摘掉会怎样（/tmp 里的副本，不动仓库，真库只读）

| 问题 | 决策 | jev 时间 | 真落点 | 结果 |
|---|---|---|---|---|
| memora 当前的缺口有哪些 | 12 | 4.43 s | 31 | **命中 `/当前状态/当前缺口`**，`incomplete` |
| 向量 rekey 的机制是什么 | 12 | 4.44 s | 6 | **命中 `/接口与检索/向量 rekey`**，`incomplete` |

这两问在 `MAX_FRONTIER = 4` 下都够不到。摘掉之后真正的界变成**全局工作预算**：12 次 jev 调用用尽，
各剩 1 个分支（`/运行与宿主`）没走，`incomplete: true`。

## 修法（一个 Feature，一个分支）

1. **删掉读取端的宽度上限**（`MAX_FRONTIER` 及其"按到达顺序保留前 N 个"的截断）。
2. **没走完的分支不许写进 `landings`**：`budget: jev calls` / `budget: wall clock` / `budget: depth`
   一律进单独的"停在这里"字段（带原因与已走/未走计数），并令 `incomplete: true`。现在它们混在
   `landings` 里，只靠 `termination` 区分——这正是"够不到答案"长得像"搜过、没有"的来源。
3. **保留并如实报告全局工作预算**（`MAX_JEV_CALLS = 12` / `MAX_SECONDS = 30` / `MAX_DEPTH = 5`）：
   它们现在是唯一的界。目录层（Database / Table）的 `MAX_FLAT_*` 不受影响——那不是 Route 层。
4. **Skill 文案**：`incomplete` 与"停在这里"要写清怎么读；`incomplete` 时的正确动作是**收窄**
   （显式 `table=`，或把要求拆成一个主题），因为那减少的是待走的分支数。

## 判据

- RED：两库两表那个可复现用例，断言 `modules` 的根分支不被丢、目标行落地。
- 断言 `landings` 里**不含**任何未展开的节点；没走完的分支出现在"停在这里"字段且 `incomplete: true`。

## 坑

- 去掉宽度上限后落点会变多（实测缺口那问 31 个真落点）：**召回换来了噪声**，这是有意的——jev 判不
  出来时宁可整层给出来，也不按顺序赌。Skill 要教调用方怎么用这个结果。
- `incomplete` 是新的读法：模型看到 `incomplete` 却当完整答案用，是这一块最容易出的错。
- **别再引入第二个宽度上限**（哪怕换个名字）；界只许有全局工作预算这一种。
