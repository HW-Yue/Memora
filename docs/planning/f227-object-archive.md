# F227：删除与归档

状态：**已实现（2026-08-20）**。读面、Admin UI、健康项联动、SKILL.md
与两个 adapter 均已交付。逐阶段记录见下。

**一句话规则：能重建的真删，不能重建的归档。**

Route 节点、Relation、Row 删了就是删了，不留回头路；
Column、Table、Database 归档，进入已归档区，可以 `UNARCHIVE` 取回。
前端默认看不到任何归档对象，只有点开归档才可见——见
[Admin UI 归档规则](./f227-archive-admin-ui.md)。

## 为什么按"能否重建"划线

早期版本把六类对象一律做成归档，靠 `UNARCHIVE` 兜底。放弃这个模型的理由：

- **Route 节点身上没有内容。** 它是一条语义索引条目：名字、用途、别名、父子边。
  用户重新 `CREATE ROUTE` 就是完全等价的重建，为它维护一套归档区、
  一套"已归档 Route 列表"读面、一套还原时的路径冲突规则，是纯负债；
- **Relation 同理**，一条连边重新 `RELATE` 即可；
- **删掉的 Row 是孤儿。** `SHOW HISTORY` 只按 `row_id` 寻址
  （`internal/msql/executor/history.go:19` 要求 `show.Row != nil`），
  而删掉的 Row 不出现在任何列表里。给一个谁也拿不到 ID 的对象保留 History，
  保的是一份永远查不到的记录。History 的价值在 **UPDATE**：Row 还活着，
  用户能看它怎么变成今天这样。删除就把 History 一起带走；
- **Table 和 Database 装着别人的东西。** 一次误删覆盖成千上万个 Row，
  且它们没法"重新 CREATE 一遍"。这一层必须可逆——这才是归档存在的理由。

## 各对象的处置

| 对象 | 操作 | 结果 | 找回 |
|---|---|---|---|
| Route 节点 | `DELETE ROUTE :id` (L2) | 终局，不可还原 | 重新 `CREATE ROUTE` |
| Row | `DELETE FROM …` (L1) | 终局，**连同 History** | 无 |
| Relation | `UNRELATE :id` (L1) | 终局 | 重新 `RELATE` |
| Column | `ARCHIVE COLUMN`／`DROP_COLUMN` (L2) | 归档 | `UNARCHIVE COLUMN` |
| Table | `ARCHIVE TABLE` (L2) | 归档 | `UNARCHIVE TABLE` |
| Database | `ARCHIVE DATABASE` (L2) | 归档 | `UNARCHIVE DATABASE` |

`ARCHIVE`／`UNARCHIVE` **只接受 DATABASE / TABLE / COLUMN**。
`ARCHIVE ROUTE`、`ARCHIVE ROW`、`ARCHIVE RELATION` 会被 parser 拒绝并点名
「DATABASE, TABLE or COLUMN」——三者的动词是 `DELETE ROUTE` / `DELETE FROM` /
`UNRELATE`。

## 删除是终局：三条强制规则

### 1. 删 Route 之前，它下面必须是空的

`DELETE ROUTE` 依次检查三件事，任何一条不满足都硬失败并说清原因
（`nativerow.DeleteRouterNode`）：

- `expected_revision` 不匹配 → `ErrRevisionConflict`；
- 还有活跃子节点 → `CodeConstraint`，错误里带上子节点个数，
  「delete them first」；
- 是叶子且还挂着 Row → `CodeConstraint`，错误里带上 Row 个数，
  「move them to another leaf first」。

第三条是这次改动的核心。删一个还挂着 Row 的叶子，等于让这些 Row 失去导航
（F224 要求活跃 Row 至少有一个 Route 归属），用户得到的是一批语义上不可达的数据。
**先把 Row 搬到别的叶子，再删这个叶子**——这个顺序不可协商，
也正因为强制了它，删除才敢做成终局。

删除之后 `StageNode` 拒绝**任何**后续修订（还原和改写都拒），
`nodes()` 把"删除后还有修订"判为文件损坏。一个 Route 节点的墓碑是最终状态。

### 2. 删 Row 带走它的 History

`HistoryPage` 先查当前 Row 状态，若是 `StateDeleted` 就返回 `CodeNotFound`，
和这个 Row 从未存在过一样。`RESTORE … TO REVISION` 同样拒绝：
它的语义是"把活着的 Row 倒回某个修订"，不是删除的后门，
错误明说「deletion is final and RESTORE cannot bring it back」。

### 3. 删 Relation 是终局

`StageRelation` 与 Route 一致：删除后不接受任何后续修订。

## 归档：一条不变量，三个对象

1. 归档**不销毁任何数据**，也**不重写任何后代**；
2. 归档**总是可逆**，逆操作是同一对象的 `UNARCHIVE`；
3. 对象可见 **iff** 它自身与它的每一级祖先都未归档；
4. 每个读面**有且只有一个**加入归档对象的开关：`INCLUDING ARCHIVED`；
5. 对归档对象（或归档祖先之下的对象）的**任何写入硬失败**，
   错误点明"已归档，先 UNARCHIVE"。

祖先链：`Database → Table → Column`，`Database → Table → Row`。

## 语法

```text
ARCHIVE   DATABASE work                      REASON :reason     (L2)
ARCHIVE   TABLE    work.notes                REASON :reason     (L2)
ARCHIVE   COLUMN   work.notes.draft_score    REASON :reason     (L2)
UNARCHIVE <同上，无 REASON>                                      (L2)
```

`REASON` 必填并写入 change log。

读面统一加一个修饰词，而不是新增 N 条 `SHOW ARCHIVED X` 语句：

```text
SHOW DATABASES INCLUDING ARCHIVED
SHOW TABLES FROM work INCLUDING ARCHIVED
SHOW COLUMNS FROM work.notes INCLUDING ARCHIVED
DESCRIBE DATABASE work INCLUDING ARCHIVED
DESCRIBE TABLE work.notes INCLUDING ARCHIVED
```

`INCLUDING ARCHIVED` 只放宽**该语句**的过滤器，不改变任何祖先规则。
`SHOW …` 的归档变体**只返回归档对象**（不是活跃 + 归档的并集），
`DESCRIBE …` 可能返回活跃对象，靠 `archived_at` 是否存在区分——
`catalog` 结构体的这两个字段是 `omitempty`，活跃对象没有这两个键。

**没有 `SHOW ROUTES … INCLUDING ARCHIVED`**：删掉的 Route 不进任何归档区。
**`SELECT … INCLUDING ARCHIVED` 不存在，也不打算做**——删掉的 Row 没有归档区，
归档的容器已经由容器级读面覆盖。`INSERT … INCLUDING ARCHIVED` 同样不被接受：
修饰词永远不放宽写入。

### 兼容与命名

- `DELETE FROM`、`DELETE ROUTE`、`UNRELATE` 保持删除语义，且**现在是真的终局**；
- `DROP_COLUMN` 重定义为归档（见下方缺陷二）；
- 磁盘状态字符串**保持 `deleted` 不变**（`row/model.go:13`、`relation/model.go:13`、
  `router.Node.Deleted`）。容器的归档另用 `ArchivedAt`／`ArchivedReason` 表达，
  与 `deleted` 不共用字段；
- `DROP TABLE` / `DROP DATABASE` 继续不被 parser 接受，
  但错误信息从 `unsupported_statement` 改为点名 `ARCHIVE`。

## 存储层的真相：删除只能是语义上的

`nativestore.Transaction` 只有 `Put`／`Commit`／`Rollback`，**没有 Delete**。
所以"删除"在今天的 Memora 里只能意味着**语义上不可达且不可逆**，
磁盘上的字节还在，直到 F151 Compaction 落地才谈得上回收。

这不是给删除打折：对用户可见的行为——查不到、列不出、还不回来——是完整的。
但**不要对用户承诺"数据已被抹除"**。真正的擦除需要一个统一的
"抹除并重写 History"能力，单独立项一次做对。

## 三条实现约束

### 1. 可见性由对象自身决定，绝不下沉到后代

归档 Table **不**逐行改 Row，归档 Database **不**逐个标记 Table。
可见性一律现算"自身或任一祖先已归档"。

理由是硬的：5,000 行的 Table 不该为一次归档产生 5,000 次 Row 修订；
F226 之后 poison 按库收敛，这种批量写正是最容易毒化该库的写入形态。
直接后果——归档只产生 **1 条** change log（走既有的 Catalog 差异信封，
其中带上新的 `archived_at`／`archived_reason`；没有单独的 `object_archived`
变更种类，也不需要），`UNARCHIVE` 之后每行的 `revision` 一个都没变。

同一条规则决定了嵌套行为：单独归档过的 Table，在库被 `UNARCHIVE` 之后
**仍然是归档状态**。`UNARCHIVE` 不替用户撤销另一个决定。

### 2. "不可见"是一份穷举清单，漏一处就是 bug

这条不是靠人工检查保证的，而是靠
[可见性矩阵测试](../../internal/daemon/f227_visibility_matrix_test.go)：
它把每个读面列成一张表，在「改名后／归档库后／归档表后」三个场景各重放一遍。
矩阵一跑就抓到两处真实泄漏——`SHOW LEXICAL LOCATIONS` 返回已归档 Table 的 Row
（只按 Database 粒度过滤），以及 `SHOW ROUTE CANDIDATES` 因为"库可见则表必可见"
的前提被打破而直接报 internal 错误。

实现上过滤收在少数几个咽喉点，而不是每个读面各写一次：`nativecatalog` 的
`DescribeDatabase`／`DescribeTable`（覆盖几乎所有读写路径）、
`nativerow.resolveColumn` 与 `executor.findColumn`（列名解析）、
`lexicallocation.Request.TableIDs`（倒排索引）、`catalogfulltext` 的
deleted 文档投影。

**别名必须一并退出解析集**（`Database.Aliases`、`Table.Aliases`、`Column.Aliases`），
否则一个 synonym 会解析到不可见对象。

归档 Column 的额外要求：列定义与所有 Row 里的值**原样保留**，
只从 `DESCRIBE`、`SELECT *` 的展开、Catalog Atlas 与 schema 校验中消失。
这比原来的 `DROP_COLUMN` 严格更好——它让"不可逆"这个属性直接消失。

Route 侧同样有一份过滤清单，只不过针对的是删除而非归档：
`Roots(tableID)` 过滤 `Deleted`；`openPage` 拒绝打开已删除叶子
（只有维护读 `InspectLeafPage` 穿透）；`membershipsForRow` 隐藏指向已删除叶子的归属。

### 3. 名字继续占用命名空间

归档对象保留原名。再建同名 Database/Table/Column **硬失败**，错误里点名那个归档对象，
给出 `UNARCHIVE` 与改名两条出路。不做自动改名（`work@archived-<ts>` 之类）：
悄悄改用户的数据名比报错更糟，而对 Agent 来说一次带解法的报错只是一个 round trip。

删除的对象不占名字：一个删掉的 Route 路径可以立刻重建同名节点——
这正是"能重建"的含义。

只读库（`ReadOnly=true`）与包安装库（`PackageSHA256 != ""`）拒绝归档，
那属于包卸载，本 Feature 不做。

## 语义健康的联动

- `unrouted_row`、`orphan_membership`、F224 强制 Route、F225 强制 summary
  **一律跳过归档对象及归档祖先之下的对象**，否则一次归档淹掉整份报告；
- 不需要 `dangling_relation`：Row 删除不级联，但 Relation 端点校验走的是当前状态；
- **不做 `archived_container`**：Admin UI 的已归档区已经回答"仓库里堆了什么"，
  一条恒定触发的信息项只会稀释报告里真正可执行的条目。

## 分阶段

- **Stage 0（已完成 2026-08-20）**：SKILL.md 写明容器当前不可删除。
- **Stage 1（已完成 2026-08-20）**：Route 的可见性泄漏。
  `Roots`／`openPage`／`membershipsForRow` 按 `Deleted` 过滤；
  已删除的兄弟节点不再占用 fan-out 名额；平行实现 `internal/router` 的有损
  `deleteSubtree` 一并改为无损 tombstone（membership 记录原样留在盘上，
  只是不再可达），不留一条"删除即销毁 locator"的代码路径。
- **Stage 2（已完成 2026-08-20）**：`catalog.Database`／`catalog.Table` 增
  `ArchivedAt`／`ArchivedReason`（原生 codec 可选字段 18/19 与 13/14，旧文件读为未归档）；
  `ARCHIVE|UNARCHIVE DATABASE|TABLE`（L2，`ARCHIVE` 必填 `REASON`）；
  可见性收在 `nativecatalog` 的 `DescribeDatabase`／`DescribeTable` 一个咽喉点上——
  它们直接拒绝归档对象，于是**所有**读写路径（`SELECT`／`INSERT`／`DESCRIBE`／
  `SHOW`／Row 服务）自动生效，不必逐处记住规则；`ShowDatabases`／`ShowTables` 过滤，
  `Describe/ShowArchived*` 是唯一的穿透读。归档**不改 Row**，实测 `revision` 不变。
  **归档不自增 SchemaVersion**——那会被误当成 schema 变更（详见下方"缺陷一"）。
- **Stage 3（已完成 2026-08-20）**：`INCLUDING ARCHIVED` 铺到 `SHOW DATABASES`／
  `SHOW TABLES`／`SHOW COLUMNS`／`DESCRIBE DATABASE`／`DESCRIBE TABLE`，
  只放宽出现它的那一条语句，后端能力用可选接口断言、不支持就明确报错。
- **Stage 4（已完成 2026-08-20）**：`ARCHIVE|UNARCHIVE COLUMN`；`DROP_COLUMN`
  重定义为归档，`Reversible=false` 取消，`Destructive` 与 `Reversible` 不再互为反面。
  归档列留在 `table.Columns` 里让存储层继续解码，可见性收在
  `nativerow.resolveColumn` 与 `executor.findColumn` 两个唯一的按名解析点；
  `catalogfulltext` 把归档对象投影为 deleted 文档，terms 离开倒排索引。
  拒绝归档最后一个活跃列。**同时修掉 `DROP_COLUMN` 的一个已发布缺陷**，见下。
- **Stage 5（已完成 2026-08-20）**：语义健康扫描天然跳过归档容器（它走
  `ShowDatabases`，该读面已过滤），`synonymousColumnIssues` 补上 `LiveColumns`。
  **未做 `archived_container` 健康项**：Admin UI 的已归档区已经回答"仓库里堆了什么"，
  再加一条恒定触发的信息项只是噪音。
- **Stage 6（已完成 2026-08-20）**：[Admin UI](./f227-archive-admin-ui.md) 全局归档
  模式已落地；SKILL.md 新增删除／归档一节，两个 adapter 已重生成。
  **未做专用 CLI 子命令**：`memora exec` 本来就能跑 `ARCHIVE`，`memora query` 能跑
  `INCLUDING ARCHIVED`，再包一层只是复制 `exec`，而 `cli.go` 已经 1,820 行。
- **Stage 7（已完成 2026-08-20）**：按"能重建的真删"收束模型。
  Route／Row／Relation 的归档能力整体撤除（`ARCHIVE ROUTE|ROW|RELATION`、
  `UNARCHIVE …`、`SHOW ROUTES … INCLUDING ARCHIVED`、
  `RestoreRouterNode`／`RestoreRelation`／`RestoreRow`／`GetArchivedRouterNode`／
  `ListArchivedRouterChildrenPage` 全部删除），删除恢复为终局；
  新增"删 Route 叶子前必须先搬空 Row"约束；`SHOW HISTORY` 对已删除 Row 返回
  not found；`RESTORE … TO REVISION` 拒绝复活已删除 Row。

## 两个前置缺陷（同源，已修）

两者都不是本 Feature 引入的，是实现过程中撞出来的已发布缺陷，根因相同：
**Catalog 变更从不重写 Row，但有代码假设二者必须一致。**

1. **改名让全部 Row 读不出来**（详见下）；
2. **`DROP_COLUMN` 让全表 Row 读不出来**：`ApplySchemaChangePlan` 只重写 Catalog
   快照，一行 Row 都不动，于是移除列之后每个 Row 都带着一个 Catalog 已不认识的
   列值，`normalize` 判为 `unknown column`。改法有两步：`DROP_COLUMN` 改为归档
   （列仍在 schema 里），且 `normalize` 拆成写入严格／解码携带两条路。

## 缺陷一：改名

Stage 2 的实现暴露出一个已发布缺陷：**对有数据的表改名之后，
它的全部 Row 都读不出来**（`native row record is corrupt: visible Row locator
has wrong Table`）。根因是两处要求 Row 的 schema 版本与 Table 当前版本严格相等
（`nativerow/indexed_reader.go` 的 `locatorMatchesTable`、`nativerow/repository.go`
的 `normalize`），而 Row 保留的是写入时的版本，引擎本就不会因 Catalog 变更重写 Row。
已改为「非零且不超过 Table 当前版本」并单独提交。

## 明确不做

- **不提供物理擦除**，理由见上"存储层的真相"；
- 不给 Route／Row／Relation 做归档区、回收站节点或"已删除列表"读面。
  尤其**不做"父节点下挂一个已归档子节点当回收站"**：它会重写被删对象的语义坐标、
  占用 F223 的 fan-out 预算、把一次写变成子树重写、并让原路径被占住导致重建冲突；
- 不引入一行式 `DROP` DDL；不为归档级联重写后代；不级联删除 Relation；
- 不改磁盘状态字符串；不做跨对象的原子归档；不做 Database Package 卸载。

## RED 与完成门

删除侧：

- 删一个还挂着 Row 的 Route 叶子必须失败，错误点明 Row 个数与"先搬走"；
- 删一个还有活跃子节点的 Route 节点必须失败，错误点明子节点个数
  （而不是被误报成 revision 冲突）；
- 已删除的 Route 节点拒绝任何后续修订（还原与改写都拒），重开文件后仍是删除态；
- 删除的 Row 之后 `SHOW HISTORY` 返回 not found，`RESTORE … TO REVISION` 明确拒绝；
- 删除的 Relation 拒绝任何后续修订；
- `ARCHIVE ROUTE|ROW|RELATION` 被 parser 拒绝并点名 DATABASE/TABLE/COLUMN；
- 删除的 Route 叶子的 membership 记录在盘上完好（维护读可见），但普通读面一律不可达。

归档侧：

- RED 先证明：`DROP TABLE` / `DROP DATABASE` 是 `unsupported_statement`；
- 归档后：`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
  `SHOW LEXICAL LOCATIONS FROM ALL TABLES`／别名解析／`OPEN ROUTE`
  均不再出现该对象，且后代的 `revision` **一个都没变**；
- 归档是**一条** change log 记录，不随后代数量增长；
- 归档对象上的写入硬失败，错误点明 `UNARCHIVE`；
- `UNARCHIVE` 后对象与全部后代原样可见，`revision` 仍未变；
- 归档 Database 后再 `UNARCHIVE`，此前单独归档的 Table 仍保持归档；
- 同名对象创建失败并点名归档对象；只读库与包安装库拒绝归档；
- `INCLUDING ARCHIVED` 的结果行必带 `archived_at`/`archived_reason`，
  且走与不带该修饰词时同一套 cursor/limit/bytes 预算语义；
- 归档容器内的 Row 不产生 `unrouted_row`；
- 归档 Column 后 `SELECT` 该列名失败，但 Row 里的原值原样保留，
  `UNARCHIVE COLUMN` 后可取回；
- 改名后（Table 名与列名）全部 Row 仍可读。

## 关联

- [Admin UI 归档规则](./f227-archive-admin-ui.md)
- [执行计划](./execution-plan.md)
- [F224 Row 必须可导航](./f224-mandatory-row-route.md)
- [F225 Row 必须可展示](./f225-mandatory-row-summary.md)
- [F226 Database 级故障隔离](./f226-per-database-fault-isolation.md)
