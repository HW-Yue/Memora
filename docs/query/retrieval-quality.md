# 语义树检索质量链路

状态：产品目标已确认；F176 已实现无模型 Bootstrap Frame，真实 Query Agent 与最终答案质量
仍待 F177–F185 Provider、loop 和外部评分链验证。

## 目标

陌生 AI 不读取旧聊天、不扫描全库，以显式 Router 为语义权威，并可组合有界候选
位置，用尽量少的上下文和模型调用稳定找到正确 Row。

主路径是 AI 对自描述语义树的逐层判断，不是引擎对自然语言做相似度排名。

## 写入时准备

每个可检索 Row 由 AI 维护：

- 自解释 title、summary、类型、状态和时间字段；
- aliases、旧名称和结构化正反向关系；
- revision、有效时间、provenance 和 Source Receipt；
- 一个或多个 Table 级 Route leaf membership。

每个 Route 内部节点具有稳定 ID、短语义描述、范围边界和有限子节点；每个 Leaf 只保存
零个或一个 RowID。同一 Row 可以从多个 Leaf 到达。AI 负责节点语义质量，引擎强制
Leaf 单 Row 基数、revision、一致性和原子发布。

Row 的新增、修改、删除、拆分和合并必须同步失效或重建 membership，不能留下指向旧 revision 或已删除 Row 的静默索引。

## 查询主路径

```text
自然语言意图
→ Bootstrap：完整有界 Catalog Atlas + lexical locations + 可选根 Route 预取
→ 第一次模型调用：一次选择多个 Table、预取 Route 或 RowID location
→ DESCRIBE TABLE：只读取所选 Table 的 Schema
→ SHOW ROUTES：每次只展开所选节点的一层
→ OPEN ROUTE：叶子确定性返回零个或一个 RowID
→ SELECT ... WHERE row_id = ?：读取真实数据
→ 必要时沿关系执行新的有界查询
```

AI 每次选择一个或少量分支，并将当前路径、候选节点、预算和 snapshot 组成紧凑 `Route Frame`。旧层级退出上下文后不再携带。

Bootstrap 是 Agent-owned MSQL 编排，不是引擎自动 Planner。Catalog 超预算时必须显式分页，
lexical 零命中或错误预取不能排除未读 Table；它只用廉价数据库工作换取更少模型续推。

## SQL 边界

目标协议以 Table 为 Route 根：

```sql
SHOW ROUTES FROM TABLE project_memora.decisions AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 1;
SELECT title, summary, revision
FROM project_memora.decisions
WHERE row_id = :row_id
LIMIT 1;
```

所有发现结果使用统一 envelope，包含稳定 ID、短描述、cursor、snapshot 和 `truncated`，不包含业务正文。Router 没有回答权，AI 也不能绕过 `SELECT` 直接引用叶子结果作答。

## 失败与修复

- 选错分支：返回父节点或兄弟节点，保持相同 snapshot，不自动退化为全库相似度搜索；
- 结果为空：检查 scope、alias、Schema 和 Route 缺口，形成可审计维护候选；
- 分支过多：由 AI DBA 拆分或重命名局部节点，不提高无限上下文预算；
- Row 难以归类：允许多叶 membership，但不复制 Row；
- Leaf 已占用：新建语义 Leaf，必要时增加 Branch，不能把第二个 Row 塞入原 Leaf；
- membership 陈旧：拒绝冒充最新结果，按 expected revision 局部重建；
- 语义树损坏：从权威 Row、关系和当前规则重建新 generation，校验后原子切换。

## 质量指标

- 从冷启动到正确 RowID 的成功率；
- 首次选对 Database、Table 和 Route 分支的比例；
- 按每层 fanout、树深、兄弟节点歧义度和 host/model 分桶的逐层正确率；
- 满足准确率、调用数和 Route Frame 门槛时，每个模型及共同目标模型集合的安全 fanout；
- 找到目标所需的 SQL 调用数、回退次数和最大 Route Frame；
- Leaf 单 Row 违反数与漏挂、错挂、陈旧 membership 数；
- split/merge 后从顶层重新找到新 Row 的成功率；
- snapshot、权限和 revision 正确率；
- 新宿主/新模型在同一标准流程下的等价性。

禁止以 Vector 分数直接回答，或用 predictor 命中掩盖 Router/SQL 的正确性失败。
对照应同时包含 Router-only、字面位置、Route-only CPU Vector、两者有界并集和
投机预取；所有 arm 的最终事实只来自相同 snapshot 下的 RowID SQL 回表。

F124–F126 使用真实宿主模型在受控 fanout/depth 矩阵上验证逐层选择和候选优化；
详细方法见 [Route Retrieval Benchmark v3](../development/route-retrieval-benchmark-v3.md)。

## 必测故事

- `US-COLD`：只靠发现语句理解陌生库；
- `US-READ`：同义自然语言通过语义节点找到正确 RowID；
- `US-INSERT` / `US-UPDATE` / `US-DELETE`：写后从顶层重新导航且无陈旧定位；
- `US-CORRECT`：revision 更新后旧 membership 不可见；
- `US-SPLIT`：Row 拆分后上层节点和叶子同步变化；
- `US-DBA`：拥挤或含混分支能局部优化和回滚；
- `US-RECOVER`：重启或换宿主后结果不依赖旧上下文。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [Agent 语义目录索引](./semantic-routing.md)
- [产品与用户故事门禁](../planning/feature-product-gate.md)
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
