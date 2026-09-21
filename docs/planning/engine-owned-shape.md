# 引擎拥有形状，agent 只给位置与正文

状态：**讨论稿**。方向待你拍板；与[宪章](../product/ai-native-product-charter.md)现有表述
（「AI 决定知识如何成为 Database、Table、Column、Row、关系和语义索引」）冲突，**拍板后按
ADR 修订宪章**。未授权开工。

## 提议

agent 写入只提供四样：**数据库名、表名、语义位置（路径）、row 的正文**。Schema 形状由引擎决定。

| 现在由 agent 写 | 改造后归谁 |
|---|---|
| Database `purpose` / `scope` / `anti_scope` | 引擎（表名/库名已承载语义） |
| Table `purpose` / `row_semantics` / `scope` / `anti_scope` | 引擎：形状固定，不再按表造句 |
| 每个 Column 的 `name` / `type` / `nullable` / `purpose` / `role` | 引擎：`title` + `summary` + 系统列，类型与长度上限固定 |
| `memora.schema-plan/v1` ensure 的列清单 | 只给表名；`runner.ensure` 由引擎合成 `CREATE TABLE` |
| `PLAN/APPLY SCHEMA CHANGE` 的列演化 | agent 不再有列可演化（人保留例外） |
| 语义树的节点与叶子 | **仍由 agent 判断**——收回它等于取消语义导航 |
| row 正文（`ROLE summary` 那一份文档） | **仍由 agent 写** |

## 引擎级封闭字段集

除 `title` + `summary` + 系统列外，引擎保留**一套全局可选字段**：所有 Table 同一语义、同一
渲染，agent 只能给值，**不能定义字段**。候选：`status`（当前状态）、`when`（时间点或区间）、
`link`（指向别的 row）。理由：时间排序、失效过滤、回链是引擎自己要用的能力，从正文里抽比留
字段更脆；同时避免「每张表一套私有结构」。

## 为什么

实测的三处损害都来自 agent 自由造形状：`me.experiences` 的 `company`/`role`/`period`/
`highlights` 与正文、叶子名重复；四张表的 `row_semantics` 全写成「一行是…」；业务列 `role`
与 Catalog 的 `ROLE` 撞名。形状一旦统一：Admin 天然只渲染文档（[显示槽位](./admin-display-slots.md)
那条改造自然成立）；`purpose`/`row_semantics` 不再是每表一句废话；`TEXT` 上限这类坑由引擎
一次设定；召回单元、向量、显示列的角色选择都不再依赖 agent 写对元数据。

## 代价（要认）

1. **放弃 US-SCHEMA**：agent 不再自己演化 Schema，宪章那条要改。
2. **精确字段检索变弱**：「按 company 过滤经历」只能走语义/向量近似，或等引擎扩封闭集——
   扩集节奏由人，不由 agent。
3. 现存实例形状不统一，需要一次迁移。
4. 权限面变化：建表退化成低风险的命名动作，L2 审批是否还挂在建表上要重定。

## 现存实例怎么收

`DROP_COLUMN` 是**归档不是删除**（`internal/schemachangeplan/validate.go` 把列标记归档、值留在
history），所以 `me.experiences` 的四个业务列可以先折进正文与树，再走 `PLAN/APPLY SCHEMA CHANGE`
归档掉，不需要删库重建。

## 实现时要动的地方（现在不动）

- MSQL / Catalog：`CREATE TABLE` 的列清单从 agent 语法退场；Table 的 `purpose`/`row_semantics`
  由引擎写常数或从 agent 面移除。
- `internal/skillschema/runner.go` 的 `ensure` 改由引擎合成定义；`internal/sqlstore/catalog.go`
  的列校验改成形状校验。
- Skill：`Evolve schemas` 与 `Decide where knowledge lives` 两节重写，去掉列的类型与角色。
- 版本：形状是文件级概念，需要与 schema 守卫、实例迁移配合。

## 待定

- 封闭字段集具体几个、叫什么、**谁填值**（agent 给值，还是引擎从正文抽）。
- `title` 从哪来：正文的首个标题，还是另设一个 agent 字段。
- 位置（路径）是不是也算 agent 的输入——本稿按「是」写，它是产品核心判断。
- 人（L2）能否例外扩展形状，还是连人也不能。
- 表名还要不要承担语义：形状统一后，表之间只剩命名与自己的树。
- 现存实例先迁移，还是让新形状并行一段时间。
