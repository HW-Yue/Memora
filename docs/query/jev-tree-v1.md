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

**没有描述的候选以空 purpose 进去，并且说出来**：`purpose` 为空、或规范化后等于 `name`，
都是"没写描述"——**不拿名字顶上**（那个兜底正是 44/56 复读能烂到没人发现的原因）。该层在
`evidence[].undescribed` 里列出是哪几个，结果里 `undescribed_at` 列出是哪几层。它**不是**
`incomplete`：层照走、也照样落点，只是这层是看着光名字判的。规则见
[Route 的 purpose 契约](./route-purpose-contract-v1.md)。

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

**写入侧不接这条路（用户 2026-09-22 决定）**：写必须塌缩成唯一落点，而且**写错库是越界**
（个人事实漏进仓库知识库），不是笔误。将来若要接，形状是"`|set| == 1`，否则停下问人"，且库层要人
确认一次——在那之前，写入的位置判断仍由 Skill 的写入流程负责。

## 每一步都看得见

脚本边走边说：`--log FILE` 把每一步写成 JSON lines（每条含 `seq`/`at_ms`/`kind`/`layer`/选项数/
选中项/`decision`/`duration_ms`，决策那条还带 provider 自己的 `elapsed_ms`），同样的行默认打到
stderr（`--quiet` 关掉；stdout 只有答案）。答案里也带同样口径：`timings.engine_ms`、`timings.jev_ms`、
`statements`、`decisions`，以及每层的 `evidence[].options_count`/`elapsed_ms`。

**连接是复用的**：整趟走树共用一条 provider 连接（不是常驻进程——跨 run 的连接早被代理掐了，守住它
只多一个要管的进程）。实测：新连接 **696–804 ms/次**，复用后 **244–523 ms/次**。同一句"两段实习都要"
因此从 **3.5 s 降到 1.7 s**（三次决策 1.6 s，第一次 0.8 s 付 TLS、后两次约 0.35 s），跨库那次从
**8.1 s 降到 4.5 s**（九次决策）。远端把空闲连接掐掉时，脚本丢连接重试一次——复用的代价是一次重试，
不是一次失败。离线重放不需要连接。`--replay` 复跑记录时
标 `recorded: true` 且耗时近 0——"决定了什么"与"花了多少"因此可以分开看。

## 可审计

`--record FILE` 把这次跑到的**每个引擎答案和每个 jev 答案**记下来，`--replay FILE` 不连库、不连
provider 重放同一趟。这不是调试便利：它让这条路能被回归测试覆盖（`internal/devgate/jev_tree_test.go`
的五份 fixture），也让"当时为什么这么走"可以被复盘。

## 关联

- [检索四条路与 jev 逐层选择](./retrieval-routes-jev.md)
- [jev 作为逐层分支选择器](./jev-branch-selection.md)
- [召回只给路径](../product/query-model.md) §6
- [Route 的 purpose 契约](./route-purpose-contract-v1.md) — 候选只带 name 与 purpose，所以描述缺失是检索损伤
- [Skill：jev 走树](../../skills/memora/references/jev-tree.md)
