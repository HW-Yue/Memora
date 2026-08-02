# F170：全内容倒排语义模型

规划状态：已批准；2026-08-03 单项 Review 通过，获准按 RED → GREEN → REFACTOR 实现。

## 唯一主要结果

提供一个确定性、可对拍的 reference index：它能把当前 live Row、Catalog 语义字段和 active Route
转换为统一 lexical documents/postings，并以完整替换方式处理 revision 更新和删除。

F170 不持久化 Page、不接入 Row 写事务、不增加 MSQL。它冻结后续持久化与查询共同依赖的语义，
避免 F171–F174 各自实现不同 tokenizer、字段范围和陈旧 posting 规则。

## 用户故事

- US-READ：精确术语未来可以产生 RowID 或 Route 位置，再由 SQL/Router 读取事实；
- US-UPDATE：Row 或 Route 修订后，旧词项不能继续命中；
- US-DELETE：逻辑删除或 supersede 后，当前 lexical view 不再返回对象；
- US-COLD：新 Agent 可以把关键词候选与显式语义树组合，减少无效导航调用。

本 Feature 的直接用户结果是形成可验证的索引语义基线；真正 MSQL 用户旅程由 F174 交付。

## 输入表面

### Row document

- identity：database_id、table_id、row_id、revision、schema revision、state；
- fields：当前 Row 中每个非空 Column value；
- field 名使用稳定 column_id，不依赖可改名的显示名称；
- TEXT、INTEGER、BOOLEAN、TIMESTAMP、RELATION_ID 都按稳定文本编码参与；
- deleted/superseded Row 生成空 replacement，从当前 view 移除。

### Semantic document

- Database：name、aliases、purpose、scope、anti-scope；
- Table：name、aliases、purpose、scope、anti-scope、row semantics；
- Column：name、aliases、purpose、semantic role；
- Route：name、aliases、path、purpose、synopsis；
- deleted Catalog/Route revision 生成空 replacement。

不接收文档 chunk、原始 PDF、History、Embedding、snippet 或模型生成 query terms。

## Token 与 posting

首版沿用 F124b 的确定性规则：Unicode letter/digit 连续 run 小写折叠，连续汉字生成相邻
bigram，单个汉字保留 singleton，标点和空白是边界。单个字段内重复 term 记录 frequency；
document replacement 的比较使用去重、稳定排序后的 posting 集合。

每条 posting 至少携带 object kind、Database/Table/Object ID、revision、field ID 和 frequency。
不携带原值。相同输入必须产生字节级稳定的 document digest 与 posting 顺序。

## 变更模型

Reference Index 只接受完整对象 snapshot 或 replacement：

```text
Replace(current document at revision N)
→ remove every posting owned by revision N-1
→ add every posting owned by revision N
```

- `Build` 从当前 snapshot 建立新 Index，允许每个对象从任意正 revision 开始，不要求读取 History；
- 空增量 Index 的首次 `Replace` revision 必须为 1；
- 新 revision 必须严格等于当前 revision + 1；
- Database/Table/Object identity 不能跨 revision 改变；
- 相同 replacement 幂等；
- stale、跳号和部分字段 patch 必须稳定拒绝；
- inactive replacement 只保留最新 tombstone revision，不保留可查询 posting。

## 标准未来旅程

F174 预期提供独立 location 查询，逻辑旅程为：

```sql
SHOW LEXICAL LOCATIONS FROM ALL TABLES
USING :query LIMIT :locations BYTES :bytes;

SELECT * FROM project.notes
WHERE row_id = :matched_row_id AND revision = :matched_revision
LIMIT 1;
```

语法名称在 F174 冻结；F170 不把示例加入 Parser。

## 失败与恢复

F170 是无 I/O reference model，不伪造 reopen/WAL 证据。它必须覆盖 malformed identity、非法 Unicode、
revision conflict、重复字段、inactive replacement、中文/英文 token 边界和随机状态序列。

持久化 corruption、Page split、reopen 和 WAL fault matrix 属于 F171；Row 原子发布故障属于 F172；
Route/rebuild snapshot 故障属于 F173。

## RED 与验收

RED 入口：

```text
go test ./internal/fulltext
```

首批测试：

- `TestReferenceIndexCoversEveryLiveRowAndSemanticField`；
- `TestReferenceIndexReplacementRemovesStaleTerms`；
- `TestReferenceIndexDeleteAndSupersedeRemovePostings`；
- `TestReferenceIndexTokenizesChineseAndLatinDeterministically`；
- `TestReferenceIndexRejectsStaleOrPartialReplacement`；
- `TestReferenceIndexBuildSeedsArbitraryCurrentRevisions`；
- 固定 seed 的随机 revision 序列与简单 map reference 对拍。

完成时执行受影响包、`go test ./...`、`go test -race ./...` 和 `go vet ./...`。

## 永久边界审计

- 只返回位置，不返回正文或答案；
- 最终事实必须 SQL 回表；
- 不建立 Row Embedding；
- 不扩大授权 scope；
- Agent 不访问 posting/Page 私有接口；
- F170 不形成第二套生产查询路径。

用户执行授权：2026-08-03 用户要求开始顺序执行后续 Feature；本 Review 只批准 F170 的上述范围。

开工前结论：PASS。

## 关联

- [ADR-0008](../decisions/0008-full-content-inverted-index.md)
- [Feature 产品门](./feature-product-gate.md)
- [TDD 协议](./feature-tdd-protocol.md)
