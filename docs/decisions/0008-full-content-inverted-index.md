# ADR-0008：当前 Row 与语义索引进入倒排索引

状态：Accepted，2026-08-02；实现按 F170 以后的小 Feature 分阶段交付。

## 背景

F124b 的 Lexical Route Location 只在查询时为 Database、Table 和 Route 语义表面临时构建
posting map。它不索引 Row 内容，也没有可增量维护的持久化倒排结构。

只依赖语义树可以保持路径清晰，但会让精确名称、术语、编号和原文关键词不能直接提供 RowID
位置提示。用户确认新的产品要求：所有当前 Row 内容与 AI 维护的语义索引都应进入倒排索引。

## 决定

Memora 增加可重建的全内容 Lexical Inverted Index：

- 索引所有当前 live Row 的非空标量 Column value；
- 索引 Database、Table、Column 的名称、别名、purpose、scope 和语义说明；
- 索引所有当前 active Route 节点的 name、aliases、path、purpose 和 synopsis；
- 不索引已删除或 superseded Row、旧 Row revision、旧 Route revision 和 History；
- 不把尚未吸收的 PDF、文档 chunk、图片或聊天原文放入索引。

“所有内容”指当前逻辑状态中可序列化的字段值，不表示保存第二份 Row 正文。posting 只保存：

```text
term
→ database_id / table_id
→ object_kind / object_id / revision
→ field_id / term_frequency
```

首版不保存 snippet、原始 value、Embedding 或答案。查询结果只返回有界位置：Row 命中返回
`row_id + revision`，Route 命中返回 `route_id + revision`；调用方必须通过 Router/SQL 读取
当前事实。倒排索引不是事实权威，缺失或损坏时确定性 SQL 与 Router 仍可使用。

## 一致性

- INSERT 必须发布新 Row 的完整 postings；
- UPDATE 必须移除旧 revision 的 postings 并发布完整新集合；
- DELETE、supersede、split 和 merge 必须失效不再 live 的 postings；
- Catalog 和 Route revision 采用相同的完整替换语义；
- postings 与其来源 revision 绑定，查询不得混用不同 snapshot；
- 权限 scope 在读取 posting 前确定，禁止用命中数量泄露未授权 Database；
- 写入成功但索引尚未恢复一致时，相关 lexical 查询返回明确 unavailable，不能返回旧命中。

倒排索引是派生 generation，可以由当前 Catalog、live Row 和 active Route 全量重建。增量发布、
generation 切换和 crash recovery 必须复用 Page/WAL 权威边界，不能在 daemon 中维护只存在于内存的
第二真相源。

## 查询协议

不恢复旧 `MATCH database.table QUERY ...`。旧语法混合了正文候选、评分和答案路径，已经删除。
新的 MSQL 使用独立的有界 location 查询，并使用新 envelope 表达 Row 与 Route 两类位置；
现有 `memora.discovery-frame/v1` 继续只承载 Route 导航，不偷偷加入 RowID。

排序第一版保持可解释的词项/字段命中与稳定 ID tie-break，不宣称 BM25 或语义相似度。
phrase、stemming、停用词、snippet 和历史检索均需独立证据与 Feature。

## 与 ADR-0007 的关系

本 ADR 取代 ADR-0007 中“Lexical 只能返回 Database/Table/Route”的限制，但不改变其 Vector 边界：

- Row、正文和事实 Embedding 仍禁止；
- CPU Vector 仍只匹配 Route semantic surface；
- Lexical RowID 只是确定性位置提示，最终事实仍来自 SQL Row；
- Router 仍是 AI 可读、可维护的语义结构，不由倒排词项取代。

## 分阶段交付

1. F170：冻结并实现全内容 lexical surface、token、posting 与 reference model；
2. F171：实现持久化 posting B+ Tree 与 generation reopen/corruption 证据；
3. F172：把 live Row 写入、修改和删除接入原子索引发布；
4. F173：把 Catalog、Route 和 rebuild 接入同一 snapshot/recovery 边界；
5. F174：增加权限隔离、有界结果和 SQL 回表约束的 MSQL location 查询。

每项必须独立 Review、RED、完成门和合入，不以本 ADR 作为批量实现授权。

## 关联

- [ADR-0007：Router 权威，候选预测器可组合](./0007-route-predictor-arsenal.md)
- [F170：全内容倒排语义模型](../planning/f170-inverted-index-surface.md)
- [物理与语义索引](../storage/indexing.md)
- [Lexical Route Locations v1](../query/lexical-route-locations-v1.md)
