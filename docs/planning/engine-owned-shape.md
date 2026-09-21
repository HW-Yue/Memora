# 引擎拥有行结构，agent 拥有命名、描述、位置与正文

状态：**讨论稿**（2026-09-21 收窄修订：只收回**列**的定义权，不含库/表命名与描述）。未授权开工。

## 提议

| 谁 | 写什么 |
|---|---|
| **agent** | 库名、表名、**表的描述语句**、语义索引树（节点与叶子）、row 的 **title**、row 的**正文**、`links` 的值 |
| **引擎** | **每张表的列集合**：`title` + `summary`（TEXT 上限由引擎一次设定），加系统列、每行 `links`、只读 `route_paths` |

agent **不得定义、不得新增列**；也**不设**引擎级全局附加字段（见下）。表和列的 stable ID、类型、
上限、角色、显示选择全部由引擎自己产生。

## 为什么（用户的原始诉求）

不是「agent 权限太大」，是**表结构太乱、AI 随意定义**：`me.experiences` 被加了
`company`/`role`/`period`/`highlights`，与正文和叶子名重复；业务列 `role` 与 Catalog 的 `ROLE`
撞名；四张表的 `row_semantics` 全写成「一行是…」。而数据获取只有三条路——向量召回、关键词召回、
语义索引逐层导航，拿到 row_id 后点查那一行——**没有「按字段过滤」这条路**，所以每表私有列从产品
形态上就没有读者。

## 不设全局封闭字段集

顾问结论：全局字段只有在**能被过滤或排序**时才有价值；三条召回路径里没有按字段过滤，`status`/
`when` 填了也没人查，只会退化成「写进正文更省事」的重复信息。`links` 是例外——它支撑导航本身
（双写、不变量断言、`doctor` 的 `broken_links`、修复队列 `mem_repairs`），是**引擎的结构**，
不是描述性属性。

**弃选**：引擎定义 `status`/`when` 由 agent 填值——等于把「AI 随意定义列」换成「AI 随意填枚举」，
脏值照样进库，还多一份没人读的 schema 债。要表达状态和时间，写进正文，向量和关键词召回都吃得到。

## agent 保留什么（别收多了）

- 库名、表名；
- **表的描述语句**（`purpose`）——用户明确要求保留；
- 语义索引树——位置是产品核心判断，收回它等于取消语义导航；
- row 的 `title` 与正文（写入时由 agent 给）；
- `links` 的值（链到哪一行）。

## 引擎拥有什么

- 每张表的列集合：`title`、`summary`，`summary` 的 TEXT 上限按「~1000 CJK 文档 + Markdown 语法」
  一次定死，不再每表论证；
- 系统列 `row_id` / `revision` / `commit_sequence` / `row_state` / `schema_version`；
- 每行的 `links` 与只读 `route_paths`；
- `row_semantics` 变成**常数**（每张表同一句），显示列按 role 的选择也不再依赖 agent 写对元数据。

## 实现时要动的地方（现在不动）

- `internal/skillschema/runner.go` 的 `ensure` 不再接受列清单，由引擎合成 `CREATE TABLE`；
- `internal/sqlstore/catalog.go` 的列校验 → 形状校验（拒绝 agent 提交的列定义）；
- MSQL `CREATE TABLE` 的列清单从 **agent 面**退场（人 / L2 是否保留例外，待定）；
- Skill 的 `Evolve schemas` 与 `Decide where knowledge lives` 两节重写；
- Admin：形状统一后，[显示槽位](./admin-display-slots.md) 那条改造自然成立，卡片上不会再出现
  「一行是…」。

## 现存实例怎么收

`me.experiences` 的四个业务列先折进正文与树，再走 `PLAN/APPLY SCHEMA CHANGE` 用 `DROP_COLUMN`
归档——**归档不是删除**（`internal/schemachangeplan/validate.go` 只标归档，值留在 history），
不需要删库重建。

## 代价（要认）

1. 放弃 per-table 字段检索——本来就没有这条路，代价主要是**将来想要也没有**，要等引擎改。
2. US-SCHEMA 里「演化 Column」那一半作废。宪章现写「AI 决定知识如何成为 Database、Table、
   **Column**、Row、关系和语义索引」，改成「引擎给行列，AI 决定库、表、描述、位置与正文」。
3. 建表退化成一次命名动作，L2 审批是否还挂在建表上要重定。

## 待定

- `row_semantics` 还留不留（形状统一后它是常数，信息量为零）。
- 库的 `purpose`/`scope`/`anti_scope` 是否同样保留给 agent（本稿按「保留」写，未确认）。
- 人（L2）能否例外扩展形状，还是连人也不能。
- 「库与表怎么划分」要不要给约束——形状统一后，表之间只剩命名、描述和自己的那棵树。
- 现存实例先迁移，还是新旧形状并行一段时间。
