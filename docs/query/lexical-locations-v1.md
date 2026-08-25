# Lexical Locations v1

状态：F174 已批准实现；这是全内容倒排索引的第一个用户可见查询协议。

> **返回列已收窄（2026-08-25）。** `revision`／`matched_term_count`／
> `matched_field_count`／`frequency`／`matched_field_ids` 全部去掉——
> 它们是换了名字的分数，给出去调用方就会照着排序和过滤。
> 现在返回 `kind`／`database_id`／`table_id`／`object_id`，
> 外加 route 与 table 命中的 `path`。
> **row 与 column 的 path 还没有**：它要「行 → 叶子」反查，
> 见[叶子直挂 RowID](../storage/leaf-rowid-v1.md)。在那之前它们只给身份，
> 不给猜出来的路径。本文其余部分（游标、快照、边界）仍然有效。

## 语法与预算

```sql
SHOW LEXICAL LOCATIONS FROM ALL TABLES
USING :query [CURSOR :cursor]
LIMIT :location_limit BYTES :utf8_byte_limit;
```

- `query` 是 1–256 个字符的 TEXT literal 或 parameter，最多形成 32 个去重词项；
- `LIMIT` 必填，范围 1–64；`BYTES` 必填，范围 256–65536；
- `CURSOR` 只能继续完全相同的 query、授权 scope 和 lexical snapshot；
- 返回普通 `rows[]` 与标准 `page`，不复用 Route-only Discovery Frame。

该语句读取当前已提交的派生 generation，因此只允许 autocommit；显式 Row transaction 中不暴露
lexical 端口，避免把未提交 Row 与已提交 posting 混成一个虚假快照。

## 位置行

每个当前对象最多返回一行：

```text
kind, database_id, table_id?, object_id, revision,
matched_term_count, matched_field_count, frequency, matched_field_ids
```

`kind` 是 `database | table | column | route | row`。`matched_field_ids` 去重并稳定排序；
不返回正文、snippet、query term 或模型答案。Row 命中后必须按 `database_id + table_id +
object_id + revision` 用 `SELECT` 回表；Route/Catalog 命中也必须通过对应 MSQL 读取当前事实。

## 聚合与排序

query 使用全内容索引唯一 tokenizer。posting 按对象 identity 聚合，排序依次为：

1. 命中的唯一 query term 数降序；
2. 命中的唯一 field 数降序；
3. query term 的 posting frequency 总和降序；
4. Row、Route、Column、Table、Database 的可操作具体度降序；
5. database_id、table_id、kind、object_id 升序。

这些计数是可解释的 lexical 信号，不是相关性概率。零命中是成功空页，不能据此排除未命中对象。

## 权限、快照与故障

- Executor 必须先从当前 Catalog 解析授权 Database，再把稳定 database_id scope 交给查询端口；
- Page Store 只允许按 `(term, database_id)` 物理前缀读取，禁止全索引读取后再过滤；
- scope 进入 cursor 摘要，因此 cursor 不能跨授权范围复用；
- snapshot 由当前 query、授权 database_id 和完整聚合候选规范编码得到；候选变化后 continuation
  返回 `REVISION_CONFLICT`；
- cursor 使用规范 JSON、URL-safe Base64 和校验摘要，未知字段、非规范编码、篡改或错误 offset 均拒绝；
- lexical generation 不可用或损坏时明确失败，不退回 Row scan、Route-only transient index 或旧 generation。

## 边界

这是候选位置武器，不是答案接口；不改变 Router 的语义导航职责，不建立 Row Embedding，也不把
动态索引写入长期 system prompt。F124b 的 `SHOW ROUTE CANDIDATES ... USING LEXICAL` 保持原协议。

## 关联

- [ADR-0008](../decisions/0008-full-content-inverted-index.md)
- [F174 实现门](../planning/f174-bounded-lexical-locations.md)
- [Lexical Route Locations v1](./lexical-route-locations-v1.md)
