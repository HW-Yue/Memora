# 数据库 Mutation Agent

状态：写入职责和协议候选；执行形态调整为内置 Agent Runtime 的 write profile。

## 目标

主 Agent 不应在每次发言后机械保存聊天，也不应在已经很长的主上下文中同时完成 Schema 发现、查重、迁移和写入。内置 Agent Runtime 的 write profile 负责把一次对话或资料产生的“稳定状态变化”转成可验证的 MSQL 事务。语义职责与只读查询隔离，但不要求运行成另一个宿主 Agent 或进程。

```text
Main Agent / Session Hook
  → conversation delta + scope hint + source event ID
  → Mutation Agent（独立上下文）
      → discover current Database/Schema/Rows
      → decide ignore/insert/revise/merge/split
      → build MSQL plan
      → validate policy/revision/impact
      → execute and verify
  ← compact Mutation Receipt
```

原始 conversation delta 只是临时输入，默认不进入数据库。

## 什么值得写

至少满足一项：

- 改变项目未来行为的决定或约束；
- 可在以后复用的事实、偏好、经验或技术结论；
- 已完成工作、当前状态和明确下一步；
- 对已有记录的纠正、否定或范围变化；
- 新增可导航的实体、关系或 Schema；
- 用户明确要求记住。

默认忽略：寒暄、重复表述、瞬时推理草稿、未证实猜测、可从代码或权威源即时重建的信息。

## 写入选择

Mutation Agent 必须先检索相邻 Row，再选择：

```text
IGNORE      没有长期价值或完全重复
INSERT      新的独立语义模块
REVISE      同一对象的新版本
SUPERSEDE   新事实使旧事实失效
MERGE       多条重复/重叠 Row 合并
SPLIT       一条 Row 混入多个完整主题
MOVE        Database/Table/Route 边界改变
RELATE      只新增或修正关系
```

永远追加 INSERT 属于质量失败。

## 提交信封

每次写入至少携带：

```json
{
  "source_event_id": "host/session/event",
  "reason": "方案已由候选变为确定决定",
  "confidence": "high",
  "expected_revision": 7,
  "expected_schema_version": 3,
  "max_affected_rows": 4,
  "policy_scope": ["project_memora"]
}
```

`source_event_id` 用于幂等重试。confidence 不是正确性证明；低置信度写入应进入 disputed/candidate 状态或拒绝自动提交。

## 风险边界

- L0/L1 的可逆局部更新可以自动提交；
- Schema、批量 merge、跨库 move 属于 L2，先返回影响计划；
- PURGE、清历史、放宽隐私属于 L3，必须显式授权；
- 任何 revision/schema mismatch 都重新读取，不能强制覆盖；
- 自动 decay/consolidation 只能生成候选事务，不能静默销毁 Row。

## 返回收据

```json
{
  "status": "committed",
  "database": "project_memora",
  "changes": [
    {"op": "REVISE", "title": "存储术语", "revision": 8}
  ],
  "ignored": 3,
  "warnings": [],
  "commit_sequence": 182
}
```

主 Agent 只需要收据，不接收完整 Schema、候选和 SQL 调试日志。

## 与 Query Agent 的关系

- read profile 永远只读，目标是最小 Context Pack；
- write profile 可写，目标是最小且正确的状态变化；
- write profile 可以调用只读发现能力，但 read profile 不能获得写权限；
- 两者共享内置 Runtime 和统一 MSQL 执行核心，权限由引擎 Policy 强制；
- 模型上下文可以轮换，Query Workspace 和机器缓存负责恢复热状态。

## 待验证

- Codex/Claude 怎样稳定提供 conversation delta 和 session event ID；
- 写入在每轮、稳定结论出现时、compaction 前还是会话结束时触发；
- “值得写”分类器的 precision/recall；
- 低置信度 candidate 是否会形成新的待办垃圾；
- Mutation Receipt 是否需要包含用户可读 diff。

## 关联

- [AI 自主权与约束](./autonomy.md)
- [数据库查询 Sub-agent](./database-query-subagent.md)
- [AI-native 产品契约](../product/ai-native-contract.md)
