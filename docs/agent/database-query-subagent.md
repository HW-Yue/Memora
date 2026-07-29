# 数据库查询 Sub-agent

状态：宿主侧兼容设计。默认查询的候选发现职责已由独立的索引发现 Sub-agent 规格取代。

## 目标

主 Agent 可以按 Canonical Skill 自己完成查询，也可以把查询意图交给独立的只读数据库 sub-agent，由后者隔离发现、导航、SQL 重试和结果压缩。Memora v0 不要求宿主支持 sub-agent。

```text
Main Agent
  → query intent + scope hint + result budget
  → Memora Query Agent（独立上下文）
      → SHOW / DESCRIBE / ROUTE
      → SELECT / MATCH / JOIN
      → 必要时修正 SQL
  ← bounded Context Pack
```

## 上下文收益

- Router、Schema 和工具输出留在 sub-agent；
- 主 Agent 不加载完整 MSQL 语法；
- 每次查询可以使用全新 sub-agent，查询历史不会持续增长；
- 主 Agent 只看到最终相关记录和必要证据；
- 数据库引擎仍可缓存 Page、Schema 和执行计划。

## 职责

主 Agent：

- 理解用户当前问题；
- 判断是否需要查 Memora；
- 提供可能的 database/route hint；
- 指定最大返回预算；
- 使用 Context Pack 完成回答或决策。

Query Agent：

- 只读；
- 加载完整 Memora Query Skill 和 MSQL 语法；
- 自己发现数据库和 Schema，不猜字段；
- 按 Query Skill 为当前意图输出去重后的 `query_terms: string[]`，允许加入原问题未出现的同义词、旧名称、缩写和跨语言别名；
- `query_terms` 启动预算为 12 个、启动 Policy 上限为 32 个；两者按 Database 配置，建库后可变性待定；
- 通过 Router、倒排和关系完成查询；
- 只通过 SQL 获取数据；
- 不返回导航过程、完整索引或调试日志。

## 输入候选

```json
{
  "intent": "我们之前怎样设计上下文缓存？",
  "database_hint": "project_memora",
  "route_hint": "/query/context",
  "max_records": 5,
  "max_chars": 2400
}
```

## 输出候选

```json
{
  "status": "ok",
  "scope": "project_memora/query/context",
  "context": "与当前问题直接相关的压缩内容",
  "records": [
    {
      "title": "数据库查询 Sub-agent",
      "table": "design_topics",
      "revision": 3,
      "reason": "直接描述查询上下文隔离"
    }
  ],
  "uncertainty": [],
  "truncated": false
}
```

内部物理 ID、SQL 调试过程和无关候选默认不返回。

## 一次性还是持续复用

宿主侧兼容方案默认采用一次性 Query Agent：每次从干净上下文开始，数据库机器缓存负责性能。

持续复用 Query Agent 可以减少重复发现，但会重新产生上下文膨胀和旧 Schema 污染，只应作为经过测量的可选优化。

未来可选的内置方案可由 Runtime 保存可重建的 Query Workspace，但不属于 v0 依赖。详见 [可选内置 Agent Runtime](./embedded-agent-runtime.md)。

当前默认两阶段链路见 [索引发现 Sub-agent](./index-discovery-subagent.md)：发现阶段只返回数据项定位，主 Agent 再用 SQL 读取正文。本文保留的 Context Pack 方式仅供无法采用该链路的宿主兼容。

## 边界

- Sub-agent 不能让主上下文完全为零；委托消息和最终 Context Pack 仍会进入主上下文。
- 返回结果必须有严格预算，否则只是把污染推迟到最后一步。
- 数据写入暂不交给只读 Query Agent；写操作需要独立 Mutation Agent 或主 Agent 的明确事务计划。
- 不支持 sub-agent 的宿主仍需使用紧凑 Route Frame 方案降级。

## 待验证

- Codex 和 Claude Code 返回 sub-agent 结果时的实际 token 开销；
- 是否能强制只返回结构化 Context Pack；
- 连续查询使用一次性 Agent 的延迟和费用；
- 主 Agent 给错 database hint 时的纠错能力；
- 如何在不同宿主中分发 Query Agent 定义和完整 MSQL Skill。

## 官方能力依据

- Codex 文档将 sub-agent 推荐用于隔离搜索、日志和工具输出，只把摘要带回主线程：https://learn.chatgpt.com/docs/agent-configuration/subagents.md
- Claude Code sub-agent 拥有独立上下文并向主会话返回摘要：https://code.claude.com/docs/en/sub-agents
