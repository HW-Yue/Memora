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

每次写入或修订时，Agent 还必须从完整 Row 的任意字段中挑选具有发现价值的词，输出结构化词项集合。引擎不判断词义，只把词项与 `row_id + revision` 形成倒排 posting，并保证它与 Row 事务原子可见。

Agent 词项格式固定为去重后的 `index_terms: string[]`，不含逐词权重和来源字段。启动预算为 24 个、启动 Policy 上限为 64 个，两者是 Database 级可演化配置。每个新 revision 必须输出完整列表，由引擎原子替换旧 posting；排序权重只存在于 Database 级 Agent/机械通道配置中。

引擎同时生成带来源标记的机械分词/N-gram posting 作为低权重召回兜底。两路索引分别计分并各自归一化，融合权重以 Database 为单位持久化；新建 Database 启动配置使用 Agent `0.8`、机械 `0.2`。建库后是否允许 AI 调整及其条件留到配置生命周期设计。机械索引可以关闭、删除和重建，不能取代 Agent 词项的语义作用。

## 查询流水线

```text
自然语言意图
→ 索引发现 Sub-agent 逐层搜索多叉 Router，取得叶子候选 ID
→ Query Agent 生成 scope、关键词组、别名和结构化 filter
→ Agent 词项与机械 N-gram posting 并行召回
→ 关系索引扩展一至两跳
→ 硬过滤 status、时间、权限和 snapshot
→ 确定性融合、去重和多样性控制
→ 返回候选数据项定位
→ 主 Agent 按定位执行 SQL 读取最终 Row
```

Router 是多层多叉的语义目录索引，叶子直接产生候选数据项 ID，但不是唯一入口。Route 选错时，全局倒排仍能救回结果；词法查询表达不完整时，Router 和关系提供补充候选。

## 排序信号

候选信号至少包括：

- title、alias、identity 精确命中；
- Agent 词项命中、查询词覆盖和词项稀有度；
- 低权重机械词项命中及其来源；
- Database、Table、Route 匹配；
- 关系距离与关系类型；
- 当前 revision 和现实有效时间；
- Query Agent 明确提供的字段 filter；
- 同一实体多个版本的折叠。

首版使用可解释的加权或 Reciprocal Rank Fusion。Buffer Pool 的 Page 热度只影响物理 I/O，不参与候选评分、Route 选择或查询结果复用。

## SQL 边界

Query Agent 可以调用 `SHOW`、`DESCRIBE`、Route 和全文候选接口，但正文最终必须由带字段投影、`LIMIT`、snapshot 和输出预算的 `SELECT` 取得。

已知 Table 上的全文候选接口使用 `MATCH(...) AGAINST(...)`。该语法由 MSQL Planner 编译为数据项级语义词项倒排查询计划，再结合 scope、结构化 filter 和关系信号；它不依赖 MySQL 内核，也不改变第一版无 Embedding API 的方向。

索引发现结果只返回数据项定位和必要评分信息，不返回正文。主 Agent 必须使用带字段投影、`LIMIT`、snapshot 和输出预算的 `SELECT` 回表读取，不能根据索引结果直接回答。

## 失败与降级

- 结果为空：扩大 Route、尝试别名和全局检索；
- 候选过多：先缩小 Database/Table/时间，不直接提高 Context Pack 上限；
- 同义召回失败：记录词汇差距，生成待审核 alias/Router 修订候选；
- Skill 发现当前内容互相矛盾：按候选 ID 回表，并列向用户展示相关 Row、revision 和来源，不擅自选边；
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

每项比较 Router、Agent 词项、机械 N-gram、混合权重、关系和 Vector baseline，记录 Recall@k、MRR、无关率、工具调用数、延迟和 Context Pack 大小。

## 未决问题

- Agent 写入词项与查询词项的结构化格式、数量和规范化规则；
- 机械分词/N-gram 的语言策略及两路默认权重；
- Query Agent 同义词扩展是否需要固定词典作为可重复兜底；
- 融合采用固定权重还是按查询类别选择已验证模板；
- 关系扩展的默认深度、扇出和循环限制；

## 关联

- [语义路由](./semantic-routing.md)
- [物理与检索索引](../storage/indexing.md)
- [数据库查询 Sub-agent](../agent/database-query-subagent.md)
- [质量模型与验收](../product/quality-model.md)
