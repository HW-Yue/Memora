# Route 产品模板：业务表的配套语义表

状态：**已实现**（`sqlstore` 同事务建 `_memora_routes_*`）。规格仍以本文为准。
与[写入形态](./write-model.md) §1 第 3 条「语义索引是第三种特殊结构」冲突时，
**以本文为准**——语义索引是业务表旁边的一张普通表，Route 操作就是对它的读写。
查询链路（发现 → 逐层走到叶子 → RowID 回表）仍以
[查询形态](./query-model.md)为准，变的只是 Route 存在哪、一层怎么取。

## 一句话

每张业务表自动配一张普通的语义表。节点是行，引用是 RowID，
走一层只读这一行。Route 语句是对这张表的读写，不是新树种。

## 配套关系

建 `notes` 时引擎同时给出：

```text
notes            正文（普通表）
notes_history    这张表的变更（普通表）
notes_routes     这张表的语义索引（普通表）
```

三张都进 Catalog，都有自己的 RowID 和表树，引擎对它们一视同仁；
角色是产品层记在 Catalog 上的标记。不新造树种，也不和正文挤在同一张表里。
一张表一种行语义。分层原则见
[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)。

「只有数据表写 history」「只有数据表对 Agent 可见」都是产品层的规则，
不是引擎标志。列表类字段（`child_ids` 等）先存为 TEXT 里的 JSON。
现役 `history:<tableID>` 树与版本树由 history 表取代。

系统表（路由表、history 表自己）不再套语义索引，避免递归。
入口写在业务 Table 的 `router_root_id` 上。

## 一个节点是一行

`notes_routes` 的一行就是语义树上的一个节点：

```text
自己的信息（name、purpose、kind、synopsis）
+ parent_id     本表里父节点的 RowID，根为空
+ child_ids     本表里子节点的 RowID 列表
+ row_id        仅叶子：指向 notes 里那一行正文
+ deprecated    是否已废弃
+ successor_ids 废弃后接替它的节点 RowID（拆分为多个，合并为一个）
+ revision      并发 guard 用
```

约束沿用现役产品门，不另开口子：

- 一个叶子最多挂一个活跃 Row；
- **一行只占一个叶子**（1:1，2026-09-21 修订）；
- 一个节点最多 `route_policy.branch_fanout` 个活孩子（默认 12，天花板 100）；
- 路径不存，要完整路径就从叶子顺 `parent_id` 往上算。

往上靠 `parent_id`，往下靠 `child_ids`，两个方向都不需要额外索引。
二者是同一份父子关系的两面，必须在同一次提交里一起落。
业务行上的 `route_leaf_id` 仍然指向叶子节点的 RowID；1:1 之后它与此处叶子上的
`row_id` 是同一件事实的两面，一致性是一条可判定的等式。

配套表对 Agent **不可见**：不能 `SELECT`，也不能直接写。Agent 的查询方式不变，
渐进式展示语义树是引擎自己沿配套表点查做的；写只经 Route 语句，
由引擎守住父子两面一致、扇出上限与同名检查。

## 重构与废弃节点

分两种，规则不同：

**一、有接替者的身份变化（SPLIT／MERGE）→ 旧节点置 `deprecated` + `successor_ids`，
不物理删除。** 改名不换 ID，不产生废弃节点。

树内倒推本身不会断——孩子的 `parent_id` 与新节点在同一次提交里落。
这两个字段保护的是**树外还拿着旧 ID 的东西**：Agent 上一轮拿到的 `route_id`、
上下文里的 Route Frame、逐层下钻途中被另一个事务拆掉的中间节点。

- 读到废弃节点，按 `successor_ids` 跳到接替者，而不是报找不到；
- 接替者可能再被拆，沿链跟随，设跳数上限防环；
- 废弃节点不进父节点的 `child_ids`，不占扇出名额。

**二、没有接替者的变空 → 物理删除，并递归向上剪枝。**（2026-09-21 定，取代原来那句
「节点不物理删除」）

- 摘掉这个子节点后父节点**没有其它 live 子节点**，连父节点一起删并继续向上；
  父节点还有其它子节点，就只把该 id 从 `child_ids` 摘掉，**到此为止**；
- **触发面是「任何让它变空的操作」**，不只在删行：删行、换挂载（UPDATE 改
  `route_leaf_id`）、MOVE 把子节点移走、`APPLY ROUTE MUTATION` 之后不再有子节点的分支。
  清理与触发它的操作**同一个事务**；
- **根也照同一条规则**：根变空一并删除，Catalog 的 `router_root_id` 置空，回到
  「还没有语义树」的合法空状态（`SHOW ROUTES AT ROOT` 返回带确定性快照的空页）；
- **例外**：已经是 `deprecated` 且带 `successor_ids` 的节点不算空壳——它存在是为了让树外
  的旧 ID 仍能解析，不被清空规则删掉。

配套表**不配 history 表**，只有数据表配 history：并发靠行上的 `revision`，
导航意义上的历史由废弃节点与 `successor_ids` 承担；被物理删除的空壳不留下导航历史
（要恢复语义位置走[行删除](../product/row-delete-archive.md)的归档，或由 Agent 重建）。

## 致空场景清单（2026-09-21）

「变空即物理删除」这条规则要落到哪些操作上，先穷举再实现——**空壳分支**与**空叶子**
是两回事，混了就写错：

| 触发 | 空的是谁 | 处理 |
|---|---|---|
| 删行（M4 归档式删除） | 该行占的**叶子** | 叶子删掉 → 父节点可能变空 → 递归剪枝 |
| 换挂载（UPDATE 换叶子） | 旧叶子不再挂这行 | 只是**空叶子**，不剪；它是「挂已有空叶子」这条出路的资源 |
| MOVE（`APPLY ROUTE MUTATION`） | 源父节点失去一个子节点 | 源父变空则递归剪枝；被移动的叶子仍挂着它的行，行跟着走 |
| SPLIT／MERGE | 旧节点被替代 | 置 `deprecated` + `successor_ids`，**保留**，不算空壳 |
| `DELETE ROUTE` | — | **已退役**（2026-09-21）：删空节点没有自由度，是上面这些操作的确定后果 |
| 根变空 | 根 | 一并删除，Catalog 的 `router_root_id` 置空，回到「还没有语义树」的空状态 |

叶子**永不因为"没挂行"被删**：空叶子是合法状态，`INSERT` 挂载失败时的出路就是挂一个已有空叶子。
只有它指向的行不再是 live，叶子才随那个操作消失。

一个操作**将要**让某 live 行失去全部叶子时——**拒绝**（`constraint_violation`），
除非同事务里先把它挂到别处。拒绝是默认，不做隐式重挂：叶子去哪由 Agent 决定。

## 不存路径的后果

[叶子直挂 RowID §7.3](../archive/storage/leaf-rowid-v1.md) 曾以两条理由保留 `Node.Path`，
本文取代该结论：

- path → 节点反查：`ResolveRouterPath` 已无生产调用方，不再需要；
- 全文检索的 `path` 字段（`routefulltext/project.go`）**先不做**：
  只检索 name／aliases／purpose／synopsis，改名或拆分不必重投影子孙。

## 引用一律是表内 RowID

| 从 | 到 | 用什么 |
|---|---|---|
| 叶子 | `notes` 的正文 | 叶子上的 `row_id` |
| 正文 | 挂着它的叶子 | 业务行上的 `route_leaf_id` |
| 父节点 | 子节点 | `child_ids` |
| 子节点 | 父节点 | `parent_id` |
| 表 | 语义树根 | Catalog 里该 Table 的 `router_root_id` |

没有 `(kind, 对象ID)` 这种平行身份证，也没有跨表的总语义树。
每张业务表的导航只活在自己的配套表里。

## 怎么走一层

```text
SHOW ROUTES AT ROOT
  → DescribeTable 读到 router_root_id
  → 在 notes_routes 点查这一行
  → 按 child_ids 逐个点查

SHOW ROUTES UNDER :parent
  → 点查 parent
  → 按 child_ids 逐个点查

OPEN ROUTE :leaf
  → 点查叶子，读出 row_id
  → 结束（不再去记录日志核对）

SELECT ... WHERE row_id =
  → 在 notes 上点查正文
```

每一步的代价只跟这一层的孩子数有关，跟这张表一共有多少节点无关。

`SHOW ROUTES` 可以留着当语句糖，底下就是对配套表的点查。

## 明确不是

- 不是 objects 树上按 `(kind, routeID)` 平铺的卡片；
- 不是全库共用的一棵总 Route 树；
- 不是正文表里用 `kind` 字段混进导航行；
- 不是再为父子关系造一种独立对象或边索引；
- 不是 history 那种 `history:<tableID>` 树种再抄一份叫 `routes:<tableID>`。

objects 树若还在，只可能暂存 Catalog／Relation 等尚未表化的东西；
它对语义索引没有产品职责。

## 和现役的差距

现役：Route 是 object kind 8，正文在 objects 树，键是 `(8, routeID)`，
节点只有 `ParentID`，走一层按 kind 全扫再过滤；根 ID 是随机 `route_` + UUID。

目标：Route 是配套表里的行；走一层读 `child_ids`；根记在 Table 的
`router_root_id`，不靠 tableID 推导根的身份。

迁移要点：节点 ID 从 `route_<uuid>` 换成配套表 RowID，老库升级时
同批重写每条业务行的 `route_leaf_id`；Catalog 新增 `router_root_id`；
`Node.Path` 与全文 `path` 字段删除。

## 关联

- [写入形态](./write-model.md)、[查询形态](./query-model.md)、
  [架构原则](./architecture-principles.md) 第二条
