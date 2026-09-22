# jev 走树：一条要求进去，一组落点出来

状态：**已实现**（2026-09-22，`skills/memora/scripts/jev_tree.py`）。查询侧的第三条定位路：
融合召回是"相似"的路，语义树是"结构"的路，这条路是**由脚本自己走完结构**、把决定交给 jev。
产品定位见[检索路线](./retrieval-routes-jev.md)，逐层裁决规则见
[jev 作为逐层分支选择器](./jev-branch-selection.md)。

## 一句话

输入一句要求（+ 已授权的库），输出**落点集合**：每条落点是一条语义路径 + 它挂的 Row + revision
+ 终止原因；集合无序、无分数、不排名。`n=1` 不是特例——`n` 只是"这句要求指向几处"。

它和召回的区别要写死：**召回是按相似度排的候选；这里是逐层判定后确信的落点。** 拿到落点仍然要
`SELECT` 回表才算事实。

## 三级，一套规则

| 级 | 语句 | 判断文本 |
|---|---|---|
| 库 | `SHOW DATABASES`（**带 authorization**） | 每个库的 `name` + `purpose` + `scope` |
| 表 | `SHOW CATALOG ATLAS`（**按单库**） | 每张表的 `table` + `purpose` |
| 树 | `SHOW ROUTES FROM TABLE … AT ROOT` / `SHOW ROUTES UNDER :parent` | 每个 child 的 `name` + `purpose` |

每级都用同一套：把候选的 name + purpose 交给 `scripts/jev_select.py`（`set` 模式：每候选一个
`Noul` + floor probe + 最大比例落差切分），拿回名字集合 + `separated|undecided|empty`。
**只有一个孩子的层不是决定，不问。**

**`undecided` 一律枚举该层**：模型答了但没分开，那就不信它没做出的筛选——丢掉整层才是真的丢信息。
枚举会让答案变宽，所以输出里必须带 `incomplete` / `incomplete_at`。

**多库时拍平**：库级返回多个（跨库要求是合法答案）时，把这几个库的表拍成**一次**请求，option 文本
带上它属于哪个库；收窄后 >3 个库或 >25 个 option 就退回逐库问。

## 顺序与预算

**BFS，不是 DFS**：预算耗尽时应当"所有支都在第 k 层"，而不是"一支到底、一支没碰"——后者是偏斜的
答案。预算是**常数、不是旋钮**：总 jev 调用 12、深度 5、frontier 宽度 4、墙钟 30 s。撞上任何一条都
在输出里写明（`termination: budget: …`）。

代价要说实话：**它比召回慢**。每层一次 jev（约 1 s）+ 本地语句，深树就是几秒；召回只有一条语句。
它买到的是"落点按构造正确"和"答案能说出自己怎么来的"。

## 两条边界

1. **只读**：每条语句都带 authorization，且**只带当前这一级的库**——Atlas 按单库授权，用多个库去问
   会把别库的表混进来（实现时真踩过）。
2. **未授权的库永不进 option**，连"当负例"都不行；发现模式的 `SHOW DATABASES` 永不作为输入来源。

写入侧本轮不接：写必须塌缩成唯一落点，而且**写错库是越界**（个人事实漏进仓库知识库），不是笔误。
要走这条路，得单独设计（`|set| == 1`，否则停下问人）。

## 可审计

`--record FILE` 把这次跑到的**每个引擎答案和每个 jev 答案**记下来，`--replay FILE` 不连库、不连
provider 重放同一趟。这不是调试便利：它让这条路能被回归测试覆盖（`internal/devgate/jev_tree_test.go`
的五份 fixture），也让"当时为什么这么走"可以被复盘。

## 关联

- [检索四条路与 jev 逐层选择](./retrieval-routes-jev.md)
- [jev 作为逐层分支选择器](./jev-branch-selection.md)
- [召回只给路径](../product/query-model.md) §6
- [Skill：jev 走树](../../skills/memora/references/jev-tree.md)
