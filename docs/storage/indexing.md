# 物理与检索索引

状态：倒排索引方向已形成；聚簇布局尚未确认。

## 两类索引

机器索引由引擎使用：

- B+ Tree；
- 倒排索引、BM25、N-gram；
- Posting、Posting Run 和 tombstone；
- 字段、时间和关系索引。

AI 索引由 Agent 阅读：

- Database purpose；
- Router Page；
- 语义路径和短句柄；
- 表及字段说明。

两者不能混为一套结构。

## 写入

提交成功后，基础倒排和字段索引必须立即可见。高成本语义整理可以异步，但不能影响 read-your-writes。

第一版不依赖 Embedding API。向量即使未来加入，也只能是可删除、可重建的派生索引。

## 聚簇方向候选

当前倾向是混合结构，而不是把完整可变记录放进聚簇叶子：

```text
窄 Row Directory B+ Tree
  → 当前版本和物理位置

Append/Version Store
  → 不可变记录版本

Secondary Index
  → 引用逻辑 row/object ID + revision
```

语义记录正文目标约 800 字，但磁盘 Page 按固定字节管理。Page split 完全由引擎执行。

当前候选使用窄 Row Directory 与独立 Version Store，使正文变大时追加 Record，而不是把可变正文塞入 B+ Tree 叶子。Tablespace、Data File、Extent、Page 和字段编码详见 [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)。

## 未决问题

- 主目录是否属于聚簇索引，叶子内联多少小字段？
- 内部主键使用递增整数、UUIDv7 还是双层 ID？
- 二级索引引用当前对象还是具体 revision？
- 倒排索引采用 B+ Tree posting，还是不可变 Posting Run？
- 中文 tokenizer 与 N-gram 的组合和空间成本；
- 索引如何参与同一事务并实现原子可见；
- 关系索引是否统一由内核提供正向和反向结构。

## 关联

- [语义路由](../query/semantic-routing.md)
- [无向量检索质量链路](../query/retrieval-quality.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
