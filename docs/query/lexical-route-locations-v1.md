# Lexical Route Locations v1

状态：F124b 已实现并冻结；只提供可回退导航候选，不恢复旧 Row 倒排查询。

## 语法

```sql
SHOW ROUTE CANDIDATES FROM ALL TABLES
USING LEXICAL :query
LIMIT :candidate_limit BYTES :utf8_byte_limit;
```

`query` 必须是 1–256 个字符的 TEXT literal 或 parameter，最多形成 32 个去重词项。
`LIMIT` 为 1–64，`BYTES` 为 256–65536；两者共同成为 Discovery Frame 的单份硬预算。

结果的普通 `rows[]` 为空，候选只存在于 `discovery`，predictor 固定为
`lexical-route/v1`、score kind 固定为 `match_count`。零命中是成功的空候选 Frame，
不代表任何 Database/Table 不相关。

## 可检索表面

F124b 只读取当前授权 scope 的语义元数据：

- Database：name、aliases、purpose、scope、anti-scope；
- Table：name、aliases、purpose、scope、anti-scope、row semantics；
- Route：name、aliases、path、purpose、synopsis 与当前 revision。

不读取 Column value、Row、History、Route membership、文档 chunk 或正文。Table/Route
删除或 revision 更新后只使用当前可见版本，旧版本和旧 membership 不生成位置。

## 词法与排序

- Unicode letter/digit 按连续 run、小写折叠；
- 连续汉字按相邻 bigram，单个汉字保留 singleton；
- 标点、符号和空白只作为边界；query 词项按首次出现去重；
- score 是命中的唯一 `(词项, 语义字段)` 数量，不是相关性概率；
- 候选按 score 降序、Route/Table/Database 具体度降序、稳定 ID 升序排列；
- `matched_fields` 只返回字段名，不回显 query 词项或原始文本。

同一授权视图的 Catalog 元数据形成 `catalog_revision`；Catalog 加当前 Route surface
形成 `snapshot`。两者均为确定性 SHA-256，query 本身不进入快照。

## 实现边界

首版在读取时用当前语义表面构建短生命周期 posting map，复杂度可接受地让位于减少
LLM 调用；不新增持久化 generation、后台 reindex 或存储格式。真实基准证明需要时再
独立优化，不能改变 MSQL 与 Discovery Frame。

F124b 不自动选表、不预取根 Route、不融合其他 predictor，也不能充当答案来源。

## 关联

- [Discovery Frame v1](./discovery-frame-v1.md)
- [MSQL Route Read v1](./route-read-v1.md)
- [语义路由投机预取](./speculative-route-prefetch.md)
- [F124b 开工与完成门](../planning/f124b-lexical-route-locations-gate.md)
