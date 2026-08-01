# MSQL Row Detail Read v1

状态：F112 已实现并冻结。

## 目的

Admin 与 Agent 仍通过普通 `SELECT` 按 RowID 回表，但 point Row 结果同时携带由
Data Dictionary 决定的展示契约。客户端不得猜测 `title`、`body` 等列名，也不得因
Schema 动态变化丢弃字段。

## Row detail

```sql
SELECT * FROM work.notes WHERE row_id = :row_id LIMIT 1;
```

只有包含精确 `row_id = literal|parameter` 的 SELECT 才返回 `row_detail`。它包含协议
version、Database/Table 稳定身份、当前 Schema version、row semantics，以及显式
`title`/`summary` 展示字段；查询未命中时仍返回相同契约，便于客户端稳定渲染 empty。

每个 SELECT column metadata 可额外包含 `column_id`、`purpose` 和 `semantic_role`。
系统字段没有业务 Column ID；业务字段始终按 SELECT projection 顺序返回，
`SELECT *` 的业务部分按 Catalog Column 顺序返回。

Column DDL 可显式声明：

```sql
title TEXT NOT NULL PURPOSE 'Display title' ROLE title
summary TEXT PURPOSE 'Compact summary' ROLE summary
```

`title` 与 `summary` 在单个 Table 内各最多一个。没有显式角色时 `row_detail.display`
使用 `row_id_revision` fallback；引擎和客户端都不能按列名、位置或文本长度猜角色。

## History page

```sql
SHOW HISTORY FROM work.notes FOR ROW :row_id [CURSOR :cursor] LIMIT :limit;
```

History 只返回 revision 元数据，不返回 values。完整正文仍使用 `SELECT ... AS OF
REVISION ... WHERE row_id = ... LIMIT 1`。History 复用 `memora.list-page/v1`，cursor 绑定
Database/Table/Row scope、完整 revision snapshot 和下一 offset；损坏或跨 scope cursor
返回 `validation_error`，续读期间 Row history 变化返回 `revision_conflict`。

## 边界

- 不新增 Admin Store API，不绕过 MSQL、授权或 point index；
- 不把 Route locator 扩充为 Row 正文；
- 不读取全局 committed changes，它属于 F113；
- 不实现 Admin 页面，它属于 F119。

## 关联

- [自描述 Data Dictionary](../data/self-describing-data-dictionary.md)
- [MSQL SELECT Planner v1](./msql-select.md)
- [MSQL History v1](./msql-history.md)
- [F112 开工与完成门](../archive/planning/f112-row-detail-read-protocol-gate.md)
