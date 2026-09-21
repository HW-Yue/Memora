# 引擎拥有行结构，agent 拥有命名、描述、位置与正文

状态：**部分落地**（2026-09-21）。**Skill 层约定已生效**并已装到三处安装位（`656d63c`：固定
`title` + `summary` 模板、不再教列设计、`Evolve schemas` 只留「加宽上限」与「经批准归档旧列」），
个人库 `me` 也已按此重写（9 行文档折进旧列事实，23 个旧列经审查后归档，回执 `verified=true`，
现在四张表都只剩 `title` + `summary`）。**引擎侧强制已落地**（2026-09-21，见
[ADR-0014](../decisions/0014-the-engine-gives-the-row-shape.md)）：声明的列必须带 title/summary
role，`CREATE TABLE` / `ADD COLUMN` / `PLAN SCHEMA CHANGE` 三条路径都拒绝其余列，读取与旧实例
加载路径不校验。

## 提议

| 谁 | 写什么 |
|---|---|
| **agent** | 库名、**库的描述（`purpose`/`scope`/`anti_scope`）**、表名、**表的描述（`purpose`）**、**语义索引树（节点与叶子）**、row 的 `title`、row 的正文、`links` 的值 |
| **引擎** | **每张表的列集合**：`title` + `summary`（TEXT 上限一次定死），加系统列、每行 `links`、只读 `route_paths` |

agent **不得定义、不得新增列**；也**不设**引擎级全局附加字段。表和列的 stable ID、类型、上限、
角色、显示选择全部由引擎产生。

## 本方案不触及语义索引

**语义索引树完全归 agent**，一直是：节点名、层级、叶子挂在哪一行，都是 agent 的判断，引擎只
负责存、索引（向量/关键词）和导航。本方案一个字都不动它，也不给「库/表怎么划分」加约束。
上一稿把「位置算不算 agent 的输入」列成待定，是错的，已经去掉。

## agent 保留什么（别收多了）

- 库名；库的 `purpose` / `scope` / `anti_scope`；
- 表名；表的 `purpose`；
- **语义索引树**——位置是产品核心判断，收回它等于取消语义导航；
- row 的 `title` 与正文（写入时由 agent 给）；
- `links` 的值（链到哪一行）。

## 三个描述字段是什么

| 字段 | 回答的问题 | `me` 库的真实值 | 必填 |
|---|---|---|---|
| `purpose` | 这个对象**装什么** | 「岳宏伟的个人身份、求职与项目事实」 | 库、表都必填 |
| `scope` | 装进去的**范围**（收哪些） | 「当前有效的个人档案、实习、项目与秋招投递」 | 库必填，表可选 |
| `anti_scope` | **明确不收什么**（排除项，防止错放） | 未写（空） | 可选 |

读数（**已实测，勿再凭印象**）：`DESCRIBE DATABASE` / `DESCRIBE TABLE` 返回三者；`SHOW DATABASES`
与 `SHOW TABLES` 返回的是整个对象结构，`scope` 常在、`anti_scope` 带 `omitempty`——**设了就会返回**，
`me` 只是没写所以看不到；`SHOW CATALOG ATLAS` 把表级三者摊平。写入侧 likewise：`CREATE DATABASE`/
`CREATE TABLE` 支持 `SCOPE`/`ANTI SCOPE`，Skill 的 ensure 计划也把它们合成为 DDL
（`internal/skillschema/runner.go`）。

**已定（2026-09-21，用户）**：库级 `purpose`/`scope`/`anti_scope` **保留给 agent 写**，建库时写一次，
之后每次写入都作为 agent 的放置参考。

**缺口**：`ALTER DATABASE` 只有 `RENAME`（`internal/msql/parser/parser.go` 的 `ADD COLUMN`/`RENAME`），
**没有 amend 路径**。一个「每次写入都要参考」的字段改不动，`scope` 里那句「当前有效」迟早烂掉。
候选：加 `ALTER DATABASE … SET PURPOSE/SCOPE/ANTI SCOPE`（有界的元数据写，走 L2）；或冻结、要改就
新建库。待定。

## `row_semantics` 是什么，以及建议

它是建表时声明的**「一行代表什么」**：`CREATE TABLE ... ROW SEMANTICS '一行是一次可独立更新的投递记录'`，
Binder 强制必填，`row-detail/v1` 契约里也必填。`me` 库四张卡的「一行是…」就是它。

**建议撤掉**：本方案让每张表的行形状统一之后，这句话对每张表都相同，信息量为零。撤掉要动
`docs/query/catalog-ddl.md`、parser/binder、`catalog.Table`、`row-detail/v1` 契约、Admin bundle
里的非空断言——是契约级改动，不是文案改动。**替代**：留一个引擎写的常数（代价是读面上多一句
废话），或彻底移除（读面更干净）。

## 为什么不设全局封闭字段集

全局字段只有在**能被过滤或排序**时才有价值；数据获取只有三条路（向量、关键词、语义索引导航 →
row_id → 点查），没有按字段过滤，`status`/`when` 填了也没人查，只会退化成写进正文更省事的
重复信息。`links` 是例外——它支撑导航本身（双写、不变量断言、`doctor` 的 `broken_links`、
修复队列 `mem_repairs`），是**引擎结构**，不是描述性属性。要表达状态与时间，写进正文，向量和
关键词召回都吃得到。

## 引擎拥有什么

- 每张表的列集合：`title`、`summary`，`summary` 的 TEXT 上限按「~1000 CJK 文档 + Markdown 语法」
  一次定死，不再每表论证；列自己的 `purpose` 也由引擎写；
- 系统列 `row_id` / `revision` / `commit_sequence` / `row_state` / `schema_version`；
- 每行的 `links` 与只读 `route_paths`。

## 实现时要动的地方（现在不动）

- `internal/skillschema/runner.go` 的 `ensure` 不再接受列清单，由引擎合成 `CREATE TABLE`；
- `internal/sqlstore/catalog.go` 的列校验 → 形状校验（拒绝 agent 提交的列定义）；
- MSQL `CREATE TABLE` 的列清单从 **agent 面**退场（人 / L2 是否保留例外，待定）；
- Skill 的 `Evolve schemas` 与 `Decide where knowledge lives` 两节重写；
- Admin：形状统一后，[显示槽位](./admin-display-slots.md) 那条改造自然成立。

## 现存实例怎么收

`me.experiences` 的四个业务列先折进正文与树，再走 `PLAN/APPLY SCHEMA CHANGE` 用 `DROP_COLUMN`
归档——**归档不是删除**（`internal/schemachangeplan/validate.go` 只标归档，值留在 history），
不需要删库重建。

## 代价（要认）

1. 放弃 per-table 字段检索——本来就没有这条路，代价是将来想要也没有，要等引擎改。
2. US-SCHEMA 里「演化 Column」那一半作废。宪章现写「AI 决定知识如何成为 Database、Table、
   **Column**、Row、关系和语义索引」，改成「引擎给行列，AI 决定库、表、描述、位置与正文」。
3. 建表退化成一次命名动作，L2 审批是否还挂在建表上要重定。

## 待定

- `row_semantics`：撤掉、引擎写常数、还是原样留给 agent 写。
- 表级 `scope`/`anti_scope`：跟库级一样保留，还是表只留 `purpose`。
- 库级描述的 **amend 路径**：加 `ALTER DATABASE … SET PURPOSE/SCOPE/ANTI SCOPE`，还是冻结。
- 人（L2）能否例外扩展形状，还是连人也不能。
- 现存实例先迁移，还是新旧形状并行一段时间。
- Skill 的写入流程要不要显式加一句「先读目标库的 purpose/scope/anti_scope 再决定放哪」
  （改了 `skills/memora/` 就要跑 `scripts/sync-skill.sh --check`）。
