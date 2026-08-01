# 数据库 Mutation Agent

状态：F31/F33/F34 已实现 Skill 写入、显式会话交接和语义冲突交互。

## 目标

主 Agent 不应在每次发言后机械保存聊天。Canonical Skill 规定如何把一次对话或资料产生的“稳定状态变化”转成可验证的 MSQL 事务；宿主可用独立 sub-agent 隔离上下文，也可在当前 Agent 中执行同一有界流程。

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

每个文本字段不得超过其 Column 当前配置的字符上限，Column 启动默认值为 1200。超限写入必须返回结构化错误；Mutation Agent 根据语义选择 SPLIT、改用合适字段类型或提交 Schema 调整后重试，不能截断正文、忽略错误或强制写入。调整上限必须通过 MSQL、Policy 和 revision 校验。

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

`source_event_id` 用于幂等重试。confidence 不是正确性证明，数据库也不根据它创建 `candidate/disputed` 状态。Skill 发现待写内容与现有 Row 语义冲突时，必须读取并向用户并列展示冲突内容；得到用户指示后重新生成 SQL 修改方案，不能让引擎猜测真伪或自动选边。低置信且无法向用户说明的内容不应自动提交。

## 语义索引词项

每个 INSERT、REVISE、MERGE 或 SPLIT 结果必须为对应 Row 输出去重后的字符串列表：

```json
{
  "index_terms": ["Memora", "MSQL", "个人数据库", "倒排索引"]
}
```

词项可以来自 Row 的任意字段，不记录来源字段，也不携带逐词权重。每次产生新 revision 时，Agent 必须重新输出完整词项列表，不能提交增删 diff。引擎自动关联 `row_id` 和 `revision`，并在同一事务中原子替换上一 revision 的全部 Agent posting。Database 级 Agent/机械通道权重不属于单条 Row 的写入结果。

同一提交还要输出完整 Router membership 快照；一个 `row_id` 可以属于多个叶子。Mutation Agent 将业务 UPDATE、词项替换和 Route membership 替换组合成一个 MSQL 事务，任一步失败都回滚，不能让 Row 与发现索引处于不同 revision。

直接 SQL 修改若没有预先生成语义索引快照，写入仍可成功，但旧词项和 Router 引用立即失效，新 revision 进入 `pending_reindex`。后台 Mutation/Reindex Agent 必须携带 expected revision；过期结果不得覆盖更新后的 Row。

`index_terms` 的启动预算为 24 个、启动 Policy 上限为 64 个。两者存于 Database 配置，建库后是否允许 AI 调整留到配置生命周期设计；超出当前预算时 Agent 应先删除低价值词项，不能把正文机械拆词塞入语义通道。

## 风险边界

- L0/L1 的可逆局部更新可以自动提交；
- Schema、批量 merge、跨库 move 属于 L2，先返回影响计划；
- PURGE、清历史、放宽隐私属于 L3，必须显式授权；
- 任何 revision/schema mismatch 都重新读取，不能强制覆盖；
- 自动 decay/consolidation 只能生成待用户确认的事务计划，不能静默销毁 Row。

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

- discovery/read profile 永远只读，目标是最小候选定位列表；主 Agent SQL 回表后才形成回答或 Context Pack；
- write profile 可写，目标是最小且正确的状态变化；
- write profile 可以调用只读发现能力，但 read profile 不能获得写权限；
- 两者共享 Canonical Skill 规则和统一 MSQL 执行核心，权限由引擎 Policy 强制；
- 模型上下文可以轮换，Query Workspace 和机器缓存负责恢复热状态。

## 待验证

- “值得写”分类器的 precision/recall；
- Mutation Receipt 是否需要包含用户可读 diff。

## 关联

- [Skill 写入流程 v1](./skill-write-v1.md)
- [Conversation Delta 交接 v1](./conversation-delta-v1.md)
- [Skill 语义冲突交互 v1](./skill-conflict-v1.md)
- [AI 自主权与约束](./autonomy.md)
- [数据库查询 Sub-agent（历史）](../archive/design/database-query-subagent.md)
- [AI-native 产品契约](../product/ai-native-contract.md)
