# F227：统一归档模型

状态：**Stage 0–2 与 Admin UI 已实现，Stage 3 部分完成（2026-08-20）**。
剩余：`ARCHIVE ROW`／`ARCHIVE RELATION`（Stage 3 后半）、`ARCHIVE COLUMN`（Stage 4）、
change log 与健康项（Stage 5）、CLI 与 SKILL.md（Stage 6 后半）。逐阶段进度见下。

**每一类对象都可归档，归档即唯一的删除语义，Memora 不提供物理擦除。**
前端默认看不到任何归档对象，只有点开归档才可见——见
[Admin UI 归档规则](./f227-archive-admin-ui.md)。

## 一条不变量，五个对象

1. 归档**不销毁任何数据**，也**不重写任何后代**；
2. 归档**总是可逆**，逆操作是同一对象的 `UNARCHIVE`；
3. 对象可见 **iff** 它自身与它的每一级祖先都未归档；
4. 每个读面**有且只有一个**加入归档对象的开关：`INCLUDING ARCHIVED`；
5. 对归档对象（或归档祖先之下的对象）的**任何写入硬失败**，
   错误点明"已归档，先 UNARCHIVE"。

祖先链：`Database → Table → Column`，`Database → Table → Row`，
`Database → Table → Route → Row 归属`，`Relation → 两端 Row`。

## 各对象的实测差距

| 对象 | 归档 | 取消归档 | 列出已归档 | 差距 |
|---|---|---|---|---|
| Row | `DELETE FROM`（`State=deleted`） | 仅 `RESTORE … TO REVISION :n`，须先知道 revision | **无**（`includeDeleted` 只在 `row/service.go:140` 内部存在，MSQL 无入口） | 缺读面、缺直接逆操作 |
| Route | `DELETE ROUTE`（`Deleted=true`） | **Stage 1 已加 `UNARCHIVE ROUTE`** | 无 | 三处可见性泄漏，Stage 1 已修，见下 |
| Relation | `UNRELATE`（`StateDeleted`） | 无（只能重新 `RELATE`，换新身份） | 无 | 缺保持身份的逆操作 |
| Column | `DROP_COLUMN` | 无，且明确 `Reversible=false` | 无 | 真的从 schema 移除，不是归档 |
| Table | **Stage 2 已加 `ARCHIVE TABLE`** | **Stage 2 已加** | Stage 3 | 已交付 |
| Database | **Stage 2 已加 `ARCHIVE DATABASE`** | **Stage 2 已加** | Stage 3 | 已交付 |

### 关于 Route：一次记录订正

本文档初版称 `router.deleteSubtree`（`internal/router/service.go`）会清空 locator
并摘除 children，因此"现有 `DELETE ROUTE` 是有损的"。**该结论只对了一半**：
`internal/router` 与 `internal/row` 是一套平行实现，daemon 并不接线它
（`daemon/lifecycle.go:198` 用的是 `nativerow` + `nativerouter`），
所以那条有损路径不在真实运行路径上。

线上路径 `nativerow.DeleteRouterNode` 本来就只翻 `Deleted` 标志，是无损的。
它真实存在的问题是另外三条**可见性泄漏**，都已在 Stage 1 修掉：

- `Roots(tableID)` 不过滤 `Deleted`，归档的 root 仍会被列出；
- `openPage` 只检查 `Kind`，**`OPEN ROUTE` 能打开一个已归档的叶子**；
- `membershipsForRow` 只看 membership 自己的 `Deleted`，
  指向已归档叶子的归属照样返回。

另外 `nodes()` 与 `StageNode` 都把"删除后还有修订"判为损坏，
等于把删除写死成不可逆——`UNARCHIVE` 必须放开这一条（只允许纯还原）。
平行实现里的有损 `deleteSubtree` 一并改成了无损 tombstone，
不留一条"删除即销毁"的代码路径。

## 语法

```text
ARCHIVE   DATABASE work                      REASON :reason     (L2)
ARCHIVE   TABLE    work.notes                REASON :reason     (L2)
ARCHIVE   COLUMN   work.notes.draft_score    REASON :reason     (L2)
ARCHIVE   ROUTE    :route_id                 REASON :reason     (L2)
ARCHIVE   ROW      work.notes :row_id        REASON :reason     (L1)
ARCHIVE   RELATION :relation_id              REASON :reason     (L1)
UNARCHIVE <同上，无 REASON>                                      (同级)
```

`REASON` 必填并写入 change log。

读面统一加一个修饰词，而不是新增 N 条 `SHOW ARCHIVED X` 语句：

```text
SHOW DATABASES INCLUDING ARCHIVED
SHOW TABLES FROM work INCLUDING ARCHIVED
SHOW ROUTES FROM TABLE work.notes AT ROOT INCLUDING ARCHIVED
SELECT … FROM work.notes INCLUDING ARCHIVED WHERE …
DESCRIBE TABLE work.notes INCLUDING ARCHIVED
```

`INCLUDING ARCHIVED` 只放宽**该语句**的过滤器，不改变任何祖先规则，
结果行必须带 `archived`、`archived_at`、`archived_reason` 三个字段，
让调用方无法把归档对象误当活跃对象。

### 兼容与命名

- `DELETE FROM`、`DELETE ROUTE`、`UNRELATE`、`DROP_COLUMN` **全部保留**，
  语义重新定义为对应对象的归档，不再声称是删除；
- 磁盘状态字符串**保持 `deleted` 不变**（`row/model.go:13`、`relation/model.go:13`、
  `router.Node.Deleted`）。改成 `archived` 是零收益的存储格式迁移，
  `repository.go:434` 一类的解码校验都得跟着动。统一的是**对外词汇**，不是磁盘编码；
- `DROP TABLE` / `DROP DATABASE` 继续不被 parser 接受，
  但错误信息从 `unsupported_statement` 改为点名 `ARCHIVE`。

## 三条实现约束

### 1. 可见性由对象自身决定，绝不下沉到后代

归档 Table **不**逐行改 Row，归档 Database **不**逐个标记 Table。
可见性一律现算"自身或任一祖先已归档"。

理由是硬的：5,000 行的 Table 不该为一次归档产生 5,000 次 Row 修订；
F226 之后 poison 按库收敛，这种批量写正是最容易毒化该库的写入形态。
直接后果——归档只产生 **1 条** change log（`object_archived`），
`UNARCHIVE` 之后每行的 `revision` 一个都没变。

同一条规则决定了嵌套行为：单独归档过的 Table，在库被 `UNARCHIVE` 之后
**仍然是归档状态**。`UNARCHIVE` 不替用户撤销另一个决定。

### 2. "不可见"是一份穷举清单，漏一处就是 bug

`SHOW DATABASES`／`SHOW TABLES`／`DESCRIBE TABLE` 的列清单／Catalog Atlas／
Bootstrap Frame／`visibleLexicalDatabaseIDs`（`executor/lexical_locations.go:52`）／
`SHOW LEXICAL LOCATIONS FROM ALL TABLES`／Route 解析与 `OPEN ROUTE`／
Relation 端点解析／`SELECT`／Semantic Health 扫描，
一律按"自身或祖先已归档"过滤。

**别名必须一并退出解析集**（`Database.Aliases`、`Table.Aliases`、`Column.Aliases`），
否则一个 synonym 会解析到不可见对象。

归档 Column 的额外要求：列定义与所有 Row 里的值**原样保留**，
只从 `DESCRIBE`、`SELECT *` 的展开、Catalog Atlas 与 schema 校验中消失。
这比 `DROP_COLUMN` 严格更好——它让"不可逆"这个属性直接消失。

### 3. 名字继续占用命名空间

归档对象保留原名。再建同名 Database/Table/Column **硬失败**，错误里点名那个归档对象，
给出 `UNARCHIVE` 与改名两条出路。不做自动改名（`work@archived-<ts>` 之类）：
悄悄改用户的数据名比报错更糟，而对 Agent 来说一次带解法的报错只是一个 round trip。

只读库（`ReadOnly=true`）与包安装库（`PackageSHA256 != ""`）拒绝归档，
那属于包卸载，本 Feature 不做。

## 语义健康的联动

- `unrouted_row`、`orphan_membership`、F224 强制 Route、F225 强制 summary
  **一律跳过归档对象及归档祖先之下的对象**，否则一次归档淹掉整份报告；
- 不需要 `dangling_relation`：没有物理删除，端点永远还在，只是被隐藏；
- 新增 `archived_container`（low_risk，纯信息）：报出归档对象数量与占用。

## 分阶段

- **Stage 0（已完成 2026-08-20）**：SKILL.md 写明容器当前不可删除。
- **Stage 1（已完成 2026-08-20）**：Route 归档的可见性泄漏与不可逆性。
  `Roots`／`openPage`／`membershipsForRow` 按 `Deleted` 过滤；
  `nodes()`／`StageNode` 放开"归档后只允许纯还原"；新增
  `ARCHIVE ROUTE :id REASON :r` 与 `UNARCHIVE ROUTE :id`（L2，走 `ExpectedRevision`）；
  归档的兄弟节点不再占用 fan-out 名额；平行实现的有损 `deleteSubtree`
  一并改为无损 tombstone。
- **Stage 2（已完成 2026-08-20）**：`catalog.Database`／`catalog.Table` 增
  `ArchivedAt`／`ArchivedReason`（原生 codec 可选字段 18/19 与 13/14，旧文件读为未归档）；
  `ARCHIVE|UNARCHIVE DATABASE|TABLE`（L2，`ARCHIVE` 必填 `REASON`）；
  可见性收在 `nativecatalog` 的 `DescribeDatabase`／`DescribeTable` 一个咽喉点上——
  它们直接拒绝归档对象，于是**所有**读写路径（`SELECT`／`INSERT`／`DESCRIBE`／
  `SHOW`／Row 服务）自动生效，不必逐处记住规则；`ShowDatabases`／`ShowTables` 过滤，
  `Describe/ShowArchived*` 是唯一的穿透读。归档**不改 Row**，实测 `revision` 不变。
  **归档不自增 SchemaVersion**——那会被误当成 schema 变更（详见下方"一个前置缺陷"）。
- **Stage 3（部分完成 2026-08-20）**：`INCLUDING ARCHIVED` 已铺到
  `SHOW DATABASES`／`SHOW TABLES`／`DESCRIBE DATABASE`／`DESCRIBE TABLE`，
  只放宽出现它的那一条语句，后端能力用可选接口断言、不支持就明确报错。
  **未做**：`ARCHIVE ROW`／`ARCHIVE RELATION` 这两个保持身份的直接逆操作——
  Row 的 `DELETE` 目前会把 Route 归属清空为 `[]`（`nativerow/service.go` 的
  `emptyRoutes`），Relation 的 `StageRelation` 也把「删除后还有修订」判为冲突，
  两者都要先改成无损，与 Stage 1 对 Route 做的是同一类改动。
- **Stage 4**：`ARCHIVE COLUMN`，`DROP_COLUMN` 重定义为它的别名，
  移除 `Reversible=false`。
- **Stage 5**：change log `object_archived`、`archived_container` 健康项、
  F224/F225 归档豁免。
- **Stage 6（Admin UI 已完成 2026-08-20）**：[Admin UI](./f227-archive-admin-ui.md)
  的全局归档模式、站点标识、不持久化与深链接说明已落地；Gateway 只读，
  前端不代为执行归档。**未做**：CLI 子命令、SKILL.md 与两个 adapter。

## 一个前置缺陷（已单独修复）

Stage 2 的实现暴露出一个与归档无关的已发布缺陷：**对有数据的表改名之后，
它的全部 Row 都读不出来**（`native row record is corrupt: visible Row locator
has wrong Table`）。根因是两处要求 Row 的 schema 版本与 Table 当前版本严格相等
（`nativerow/indexed_reader.go` 的 `locatorMatchesTable`、`nativerow/repository.go`
的 `normalize`），而 Row 保留的是写入时的版本，引擎本就不会因 Catalog 变更重写 Row。
已改为「非零且不超过 Table 当前版本」并单独提交。

## 明确不做

- **不提供物理擦除。** Memora 今天在任何层级都没有真正的擦除——Row `DELETE`
  同样保留 History 与旧修订。隐私场景（误写进去的密码、他人的敏感信息）需要一个
  统一的"抹除并重写 History"能力，单独立项一次做对，不挂在归档上做半套；
- 不引入一行式 `DROP` DDL；不为归档级联重写后代；不级联归档 Relation；
- 不改磁盘状态字符串；不做跨对象的原子归档；不做 Database Package 卸载。

## RED 与完成门

- RED 先证明（Stage 1，已完成）：归档一个叶子后 `OPEN ROUTE` 仍打得开、
  `Roots` 仍列出归档的 root、指向归档叶子的 membership 仍返回、
  且 `StageNode` 拒绝任何还原；
- RED 先证明：`DROP TABLE` / `DROP DATABASE` 是 `unsupported_statement`；
- 归档后：`SHOW TABLES`／Catalog Atlas／Bootstrap Frame／
  `SHOW LEXICAL LOCATIONS FROM ALL TABLES`／别名解析／`OPEN ROUTE`
  均不再出现该对象，且后代的 `revision` **一个都没变**；
- 归档是**一条** change log 记录，不随后代数量增长；
- 归档对象上的写入硬失败，错误点明 `UNARCHIVE`；
- `UNARCHIVE` 后对象与全部后代原样可见，`revision` 仍未变，
  Route 的 locator 与 children 完整复原；
- 归档 Database 后再 `UNARCHIVE`，此前单独归档的 Table 仍保持归档；
- 同名对象创建失败并点名归档对象；只读库与包安装库拒绝归档；
- `INCLUDING ARCHIVED` 的结果行必带 `archived`/`archived_at`/`archived_reason`，
  且走与不带该修饰词时同一套 cursor/limit/bytes 预算语义；
- 归档容器内的 Row 不产生 `unrouted_row`；
- 归档 Column 后 `SELECT` 该列名失败，但 `INCLUDING ARCHIVED` 能取回原值。

## 关联

- [Admin UI 归档规则](./f227-archive-admin-ui.md)
- [执行计划](./execution-plan.md)
- [F224 Row 必须可导航](./f224-mandatory-row-route.md)
- [F225 Row 必须可展示](./f225-mandatory-row-summary.md)
- [F226 Database 级故障隔离](./f226-per-database-fault-isolation.md)
