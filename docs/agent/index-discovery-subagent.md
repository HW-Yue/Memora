# 索引发现 Sub-agent

状态：两阶段查询方向已确认；定位结果 Schema、融合算法和调用预算待冻结。

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

## 待冻结

- 定位结果是否包含 Database、Table、Row ID、revision 和分数解释；
- 全局 ID 与 Table 内 Row ID 的关系；
- Route、Agent 词项、机械词项和关系信号的归一化与融合；
- 每层候选数、最大深度和总调用预算；
- 主 Agent 一次回表读取多少候选以及怎样二次查询。

## 关联

- [MSQL](../query/msql.md)
- [无向量检索质量链路](../query/retrieval-quality.md)
- [可选内置 Agent Runtime](./embedded-agent-runtime.md)
