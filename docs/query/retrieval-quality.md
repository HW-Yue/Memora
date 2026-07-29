# 无向量检索质量链路

状态：第一版候选方案；分词、融合权重和门槛需要 benchmark。

## 目标

第一版不调用 Embedding API，也要处理同义表达、跨表检索、关系扩展和过期事实。数据库内核不运行 LLM；Query Agent 负责理解意图，引擎负责可重复的候选检索和排序。

## 写入时准备

每个可检索 Row 由 AI 维护：

- 自解释 title 和短 summary；
- canonical terms、aliases、缩写和旧名称；
- Database/Table/Route scope；
- 类型、状态、时间和重要性字段；
- 结构化正反向关系；
- revision、有效时间和 Source Receipt。

引擎同步生成字段加权倒排索引、中文 N-gram、精确词项和关系索引。Router 与别名变化必须带 revision，使缓存可以准确失效。

## 查询流水线

```text
自然语言意图
→ Query Agent 生成 scope、关键词组、别名和结构化 filter
→ Router 给出少量高概率路径
→ BM25/N-gram/精确词项并行召回
→ 关系索引扩展一至两跳
→ 硬过滤 status、时间、权限和 snapshot
→ 确定性融合、去重和多样性控制
→ SQL 读取最终 Row
→ 受预算约束的 Context Pack
```

Router 是先验和导航，不是唯一入口。Route 选错时，全局倒排仍能救回结果；词法查询表达不完整时，Router 和关系提供补充候选。

## 排序信号

候选信号至少包括：

- title、alias、identity 精确命中；
- summary/body 的 BM25 与 N-gram 分数；
- Database、Table、Route 匹配；
- 关系距离与关系类型；
- 当前 revision、现实有效时间和 disputed 状态；
- Query Agent 明确提供的字段 filter；
- 同一实体多个版本的折叠。

首版使用可解释的加权或 Reciprocal Rank Fusion。LRU 热度只决定先查哪里或复用执行结果，不能把不相关数据抬成正确答案。

## SQL 边界

Query Agent 可以调用 `SHOW`、`DESCRIBE`、Route 和全文候选接口，但正文最终必须由带字段投影、`LIMIT`、snapshot 和输出预算的 `SELECT` 取得。

候选结果默认只返回短句柄、标题、scope、revision 和分数解释，不返回正文。这样即使召回很多，也不会把所有候选灌入模型上下文。

## 失败与降级

- 结果为空：扩大 Route、去掉低置信度 filter、尝试别名和全局检索；
- 候选过多：先缩小 Database/Table/时间，不直接提高 Context Pack 上限；
- 同义召回失败：记录词汇差距，生成待审核 alias/Router 修订候选；
- 当前事实冲突：同时返回 disputed 摘要，不擅自选边；
- 结果被截断：返回 `truncated`、cursor 和下一步建议；
- 索引 revision 落后：拒绝冒充最新结果并重建派生索引。

## 必测集合

- 同义但无共同关键词；
- 中文词边界、英文缩写和中英混合；
- 同名项目或人物的 scope 消歧；
- 已改名的 Database/Table/Column/实体；
- 被 supersede 的旧决策与时间有效事实；
- 需要一跳、两跳关系才能回答的问题；
- Router 故意指错或缺失；
- 大量近似记录下的 Context Pack 精度。

每项比较 Router、BM25/N-gram、关系、融合方案和 Vector baseline，记录 Recall@k、MRR、无关率、工具调用数、延迟和 Context Pack 大小。

## 未决问题

- 中文 tokenizer 采用词典、N-gram，还是两者并行；
- Query Agent 同义词扩展是否需要固定词典作为可重复兜底；
- 融合采用固定权重还是按查询类别选择已验证模板；
- 关系扩展的默认深度、扇出和循环限制；
- 全文能力暴露为 `MATCH`、`SEARCH()` 还是系统候选表。

## 关联

- [语义路由](./semantic-routing.md)
- [物理与检索索引](../storage/indexing.md)
- [数据库查询 Sub-agent](../agent/database-query-subagent.md)
- [质量模型与验收](../product/quality-model.md)
