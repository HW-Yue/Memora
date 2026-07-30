# MATCH Fusion v1

状态：历史实现，产品方向已撤销。双通道评分不得再作为语义主路径；本文只用于
代码迁移和删除审计，受 [AI-native 产品宪章](../product/ai-native-product-charter.md)约束。

## 输入

Planner 接收：

```text
database_id
agent_terms[]
mechanical_terms[]
limit
```

两组词项分别规范化、去重，不能互相替代。Agent terms 来自 Query Agent 的语义扩展；mechanical terms 来自引擎对原始 query 的确定性 tokenizer。至少一组非空，LIMIT 为 1–1000。

## 评分

每个通道先按“候选命中的去重查询词数量”计算 raw hits，再独立除以该通道本次最大 hits：

```text
agent_score = agent_hits / max_agent_hits
mechanical_score = mechanical_hits / max_mechanical_hits
score = agent_weight × agent_score
      + mechanical_weight × mechanical_score
```

无候选的通道得分为 0，不借用另一通道的分母。启动权重为 Agent 0.8、mechanical 0.2；注入覆盖必须有限、非负且总和为 1。

最终分数降序；同分依次按 database_id、table_id、row_id 升序，保证重试和跨重启排序稳定。不同通道若返回同一 Row 的不同 revision，Planner 视为索引不一致并失败，不能静默合并。

## 输出

结果只包含：

```text
database_id, table_id, row_id, revision,
score, agent_score, mechanical_score
```

超过 LIMIT 设置 `truncated=true`。MATCH 不返回标题、正文或任意业务 Column；Agent 必须按 locator 使用 SELECT 回表。

## MSQL

```sql
MATCH work.notes
QUERY :raw_query
TERMS :query_terms
LIMIT :limit;
```

表必须使用 `database.table` 限定名。`:raw_query` 是非空 TEXT，由引擎的 mechanical tokenizer 生成词项；`:query_terms` 是 Query Agent 提交的 TEXT array，可为空；LIMIT 为 1–1000。`query` 和 `terms` 仍可作为业务标识符，不新增全局保留字。

Executor 在一个 Row transaction snapshot 中读取两套 posting，并按绑定 Table ID 过滤候选。结果进入标准 Result Envelope；MATCH 是纯读，单条失败不会中断批次中的后续独立 MATCH。

## 关联

- [Agent Inverted Index v1](../data/agent-index-v1.md)
- [Mechanical Inverted Index v1](../data/mechanical-index-v1.md)
- [无向量检索质量链路](./retrieval-quality.md)
