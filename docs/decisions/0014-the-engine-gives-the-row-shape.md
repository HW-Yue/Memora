# ADR-0014：行的形状由引擎给定

状态：Accepted，2026-09-21。修订[宪章](../product/ai-native-product-charter.md)里
"AI 决定知识如何成为 Database、Table、Column、Row……"的表述：**Column 移出 AI 的决定范围**。

## 背景

Skill 层从 2026-09-21 起就要求每张表固定 `title` + `summary`、不许 agent 设计列，但那只是约定：
引擎仍然接受任意列。实测的损害来自 agent 自由造列——`me.experiences` 曾被加上
`company`/`role`/`period`/`highlights`，与正文和叶子名重复；业务列 `role` 与 Catalog 的 `ROLE`
概念撞名；四张表的 `row_semantics` 全写成「一行是…」。而数据获取只有四条路（语义索引、关键词、
向量、jev），**没有「按字段过滤」这一条**，私有列从产品形态上就没有读者。

## 决定

1. **每张表固定 `title`（ROLE title）+ `summary`（ROLE summary）**，引擎拥有行形状。
2. **声明的列必须带 `title` 或 `summary` role**，各至多一个；其余一律拒绝。这条在**两条声明路径**
   上都强制：`CREATE TABLE` / `ALTER TABLE ADD COLUMN`（catalog 层）与
   `PLAN SCHEMA CHANGE` 的 `ADD_COLUMN`（计划构建与校验都查），以及 Skill 的
   `memora.schema-plan/v1` ensure（计划阶段就拒）。拒绝时**返回形状规范原文**，让调用方一次改对。
3. **只管新声明，不管已存的**：读取、加载、迁移、备份恢复路径不做这项校验——升级上来的旧实例
   带着历史列照样打开、照样可读可写，只是不能再加。`ALTER_COLUMN`（加宽上限）与 `DROP_COLUMN`
   （退役旧列）继续可用。
4. **role 封闭集收窄**：`identity`/`status`/`fact`/`rationale` 不再可声明（它们只可能存在于旧实例
   里）；分类、状态、时间、人名、关系一律进语义树、正文或 `links`。

## 后果

- agent 的建模面收敛为：库名、库与表的描述、语义索引树、行的标题与正文、`links`。
- 显示与检索不再依赖 agent 写对元数据：每张表的形状一致，Admin 天然只渲染文档。
- 旧实例的自造列成为只读遗留物（可 `DROP_COLUMN` 归档，归档不是删除，值留在 history）。
- 代价：将来要按字段精确检索，只能等引擎扩自己的形状——扩集节奏由人，不由 agent。

## 不做

- 不按**列名**强制（`body ... ROLE summary` 这类命名在文档与测试里合法；要管的是"凭空造列"，
  不是命名风格）。
- 不在读取路径上校验（会把"你不许加列"变成"这个库打不开"）。
- 不把 role 从 `catalog.Column` 的模型里删掉——旧实例的数据仍要能解码。

## 关联

- [宪章](../product/ai-native-product-charter.md) · [MSQL Catalog DDL](../query/catalog-ddl.md)
- [引擎拥有形状](../planning/engine-owned-shape.md)（落地进度）
- [ADR-0011 一切建表](./0011-pure-storage-engine-tables-everything.md)
