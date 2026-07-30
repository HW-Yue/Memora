# 索引发现 Sub-agent

状态：历史设计。产品目标改为主 AI 直接按 MSQL 逐层导航 Table 级 Router；
倒排融合与 query_terms 路径已撤销。

## 职责

索引发现 Sub-agent 只负责从自然语言意图定位候选数据项，不读取或返回业务正文。

```text
查询意图
→ 逐层发现 Database
→ 在多叉 Router 中逐层打开相关分支
→ 到达叶子并取得候选数据项 ID
→ 生成 query_terms
→ 查询 Agent 词项与机械 N-gram 倒排
→ 融合 Route、倒排和关系信号
→ 排序、去重
→ 返回候选数据项定位
```

主 Agent 接收定位后，必须自己生成 MSQL `SELECT`，按 ID、revision、字段投影和预算读取真实 Row。索引发现结果不能代替 SQL 数据读取。

## 边界

- Router 内部节点只提供导航元数据，叶子只提供数据项 ID/locator；
- 倒排索引只提供候选 ID 和评分信号；
- Sub-agent 不把 Row 正文、完整索引或调试过程带回主上下文；
- 所有发现调用仍通过 MSQL，不允许直接读取索引文件；
- 主 Agent 不根据索引摘要直接回答，必须 SQL 回表验证；
- 宿主不支持 sub-agent 时，由同一 Agent 按 Skill 的有界状态机完成发现；不能因此绕过 SQL 回表边界。

## 失败处理

- Route 无结果时扩大到 Database 或 Instance 级倒排；
- 倒排无结果时回退到更上层 Route 并重新生成 query terms；
- 多层发现达到预算后返回结构化 `truncated`，不能无限导航；
- 候选之间分数接近时保留多个定位，让主 Agent 用 SQL 读取后判断。

具体权重、预算、截断和一致性规则见 [Index Discovery v1](../query/index-discovery-v1.md)。主 Agent 的回表字段与二次查询策略仍由具体问题和输出预算决定。

## 关联

- [MSQL](../query/msql.md)
- [无向量检索质量链路](../query/retrieval-quality.md)
- [可选内置 Agent Runtime](./embedded-agent-runtime.md)
