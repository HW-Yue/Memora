# 物理与检索索引

状态：倒排索引方向已形成；F25 generation manifest 已冻结；聚簇布局尚未确认。

## 两类索引

机器索引由引擎使用：

- B+ Tree；
- Agent 词项与机械 N-gram 混合倒排索引；
- Posting、Posting Run 和 tombstone；
- 字段、时间和关系索引。

AI 索引由 Agent 阅读：

- Database purpose；
- Router Page；
- 语义路径和短句柄；
- 表及字段说明。

两者不能混为一套结构。

## Agent 生成的语义词项

Memora 的语义倒排索引以数据项为粒度，不要求按固定 Column 建索引。Agent 在写入或修订一条语义 Row 时读取完整业务字段，并用结构化结果给出本次应进入倒排索引的词项。引擎把它们持久化为逻辑上的 `term → row_id + revision` posting。

词项可以来自 Row 的任意字段；Agent 负责判断哪些词具有发现价值，引擎不负责理解词义。Agent 不能直接操作 Posting、Posting Run 或索引文件；它只提交词项集合，引擎负责规范化、去重、事务原子可见、旧 revision 清理和物理重建。

Agent 协议使用简单的 `index_terms: string[]`，不保存逐词权重或来源 Column。启动预算为 24 个、启动 Policy 上限为 64 个，均属于可版本化 Database 配置。每个新 revision 都携带完整词项快照；引擎自动关联 `row_id`、`revision` 和 posting 来源，并原子替换上一 revision 的全部 Agent posting，不接受词项增删 diff。

Row 可以用标准 UPDATE/DELETE 按稳定 `row_id` 修改。当前 Record 变化时，字段索引和机械索引由引擎自动维护；Agent 词项和 Router membership 由 Agent 生成完整新快照并通过 MSQL 原子替换。逻辑 DELETE 为所有活跃 posting 和 Route 引用写入删除状态，索引重建不得改变 `row_id`。

普通 UPDATE 未携带新语义索引快照时采用 `pending_reindex`：旧 Agent posting 和 Router membership 立即 tombstone，新 revision 暂时只依赖机械索引，daemon 完成 Agent 重建后再原子启用。不得为了保持召回而继续暴露与旧内容匹配的 posting。

Router 和倒排索引必须支持 generation 重建：各类新 generation 独立旁路构建并验证，再通过 `indexes/manifest` 原子发布新的启用组合；旧 generation 等读者释放后由 compaction 回收。局部删除使用 tombstone，tombstone 不是永久存储，不能无限增长。

Router、Agent 倒排和机械倒排分别维护 generation，由 Database 的 `indexes/manifest` 原子固定当前组合；一种索引重建不复制其他索引。物理目录见 [Database 物理目录](./database-file-layout.md)。

F25 的 generation record、checksum validation、expected manifest revision、query pin 和旧 generation GC 规则见 [Index Generation Manifest v1](./generation-manifest-v1.md)。

为防止 Agent 漏词，引擎同时生成可关闭、可丢弃并重建的机械分词/N-gram posting。Posting 必须区分 `agent` 与 `mechanical` 来源，查询时分别计分并各自归一化。融合权重以 Database 为单位持久化；新建 Database 启动配置使用 Agent `0.8`、机械 `0.2`。建库后是否允许 AI 调整及其条件留到配置生命周期设计；任何被允许的变更都必须通过 MSQL、revision 和审计。机械词项只作为字面召回兜底。

## 写入

提交成功后，Agent 提交的语义词项和基础字段索引必须立即可见。高成本语义整理可以异步，但不能影响 read-your-writes。

第一版倒排索引混合 Agent 明确提交的语义词项与引擎生成的机械分词/N-gram。机械索引是可关闭、可删除并重建的派生状态。第一版不依赖 Embedding API；向量即使未来加入，也只能是同类可重建派生索引。

## 聚簇方向候选

物理 MVCC 已确定使用“最新 Record + Undo version chain”，不再把不可变 Version Store 当作事务版本链。主数据定位仍有两种候选：

```text
窄 Row Directory B+ Tree
  → 最新 Record 的稳定逻辑定位

Latest Record Store
  → 当前业务字段 + trx metadata + undo pointer

Secondary Index
  → 引用稳定逻辑 row/object ID

Undo Log
  → 短期事务旧版本

History Store
  → 长期语义 revision
```

语义记录启动写作目标约 800 字，文本 Column 启动默认上限为 1200 个字符并可演化；磁盘 Page 仍按固定字节管理。字段超限由逻辑写入层处理；物理 Page split 完全由引擎执行，两者不能混为一件事。

尚待决定最新 Record 是直接放入聚簇索引叶子，还是由窄 Row Directory 指向独立 Record Store。无论选择哪种，事务旧版本都走 Undo，长期历史都走 History Store。Tablespace、Data File、Extent、Page 和字段编码详见 [Tablespace、Page 与 Record 布局](./tablespace-page-record-layout.md)。

## 未决问题

- 主目录是否属于聚簇索引，叶子内联多少小字段？
- 内部主键使用递增整数、UUIDv7 还是双层 ID？
- 二级索引引用当前对象还是具体 revision？
- 倒排索引采用 B+ Tree posting，还是不可变 Posting Run？
- 0.8/0.2 启动权重怎样通过质量 benchmark 校准；
- 关系索引是否统一由内核提供正向和反向结构。

## 关联

- [语义路由](../query/semantic-routing.md)
- [无向量检索质量链路](../query/retrieval-quality.md)
- [MVCC、Undo Log 与 Redo Log](./mvcc-undo-redo.md)
