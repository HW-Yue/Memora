# 语义路由投机预取

状态：F176 已将 Atlas、全内容 lexical locations 与最多两个 Table root 组装为确定性
Agent Bootstrap Frame；是否默认采用仍由 F185 真实答案门验证。

## 动机

当前标准读取先选 Database、再选 Table、再逐层读取 Table Router。单层约 16 个
十几字的语义节点并不显著占用上下文，主要成本是每次工具返回后都要再次调用模型。

候选方向类似 CPU 分支预测：数据库用廉价、可丢弃的信号预取模型下一步可能需要
的语义 Route；命中时减少一次模型调用，未命中时回到正常导航，不能改变查询结果。

## 候选 Discovery Frame

第一次发现请求可以在一个全局输出预算内同时返回：

1. 全部 Database 和 Table 的紧凑语义描述；
2. 当前问题的倒排索引位置聚合，例如命中的 Database、Table、Route 和数量；
3. 少量高可能性 Table 的根 Route，作为可丢弃的投机预取。

模型随后可以一次选择多个 Table；若预取命中，还能同时选择第一层 Route。未预取
到正确 Table 时，模型显式请求该 Table 的根 Route，链路退化为普通确定性导航。

## 预测器武器库

Discovery 可以把预测器视为由 Canonical Skill 选择、通过 MSQL 显式调用的可组合
原语，而不是引擎内置一个永远正确的自动 Planner。首批候选包括：

- Catalog/Table 紧凑语义描述；
- 倒排词项对 Database、Table、Route 的位置聚合；
- 查询向量与 Route 节点向量的近邻候选；
- 当前会话中仍有效的 Route Frame。

不同预测器只输出带来源的候选 Route ID。Skill 可以单独使用、取有界并集或完全
跳过它们；最终仍由模型读取 AI 维护的语义节点并显式选择路径。

F124d 已冻结向量候选 MSQL：

```sql
SHOW ROUTE CANDIDATES FROM ALL TABLES
USING VECTOR :query_vector SPACE :space_digest
LIMIT 8 BYTES 4096;
```

### 向量只预测语义索引

向量候选第一阶段只覆盖 Route 节点，不覆盖 Row 正文、文档 chunk 或最终事实。
用于生成节点向量的文本可以组合 name、aliases、purpose、ancestor path 和边界说明，
避免只对十几个字的名称编码；结果只返回 Route ID、revision 和可审计分数。

查询向量生成与相似度扫描必须分开看：短查询能降低本地 embedding 推理输入，但
扫描成本取决于 Route 节点数 `N` 和维度 `d`。第一版优先用 CPU 对归一化向量执行
`O(N*d)` 精确点积，不提前实现 HNSW；规模和延迟超门后再开独立 Feature。

已确认的实现顺序：先冻结与物理算法无关的 Route 向量候选 MSQL 接口，再实现 CPU
精确扫描作为 reference backend。当前不实现 HNSW，也不让 SQL 调用方依赖具体索引；
只有真实规模下的 `p95` 延迟、CPU/内存压力和 Recall 证据证明精确扫描越过门槛，
才 Review HNSW Feature。未来替换后端不能改变候选结果 envelope 和正常 Router 回退。

Route 向量是可重建的派生 generation，必须绑定 embedding model/version、维度、
Route revision 和来源文本 digest。模型缺失、版本变化或 generation 失效时回退到
普通 Router，不能影响 Database Package 的事实可读性。

### 产品边界

ADR-0007 已将旧绝对禁令收窄为“禁止向量充当事实、Row/chunk 主索引、答案来源或
不可回退的权威路径”。Route-only predictor 获准进入开发计划；MSQL、generation、
模型分发、隐私和 Benchmark 仍按独立 Feature Review。

## 正确性边界

- Table Router 仍是语义发现和导航主路径；倒排索引不是答案索引。
- 倒排结果只提供位置提示，不能自动排除零命中的 Database 或 Table。
- 默认不在 Discovery Frame 返回 Row 正文或可直接作答的片段。
- 预测命中与否只影响模型调用数和上下文，不能改变可见 Row 集合。
- 所有 Route 选择绑定稳定 ID、snapshot 和 revision；最终事实仍由 SQL 精确回表。
- 预测失败不记为查询失败，也不能把旧预取结果当作隐式相关性缓存。

## 有得有失

可能收益：减少 Database/Table 发现和首层 Route 读取之间的模型续推，尤其适合目录
紧凑、Table 根节点约 16 个的个人数据库。

可能成本：错误预取会浪费少量工具输出、模型输入 token 和数据库读取；若预取范围
过大，反而污染当前 Route Frame。第一版应限制候选 Table 数和统一 token 预算。

## 对当前开发路线的影响

方向性结论：不阻断或改写当前存储内核与 MSQL 原语开发。Database/Table 发现、
倒排位置查询、Table Router 读取、叶子打开和 RowID 回表应继续保持可独立组合的
标准操作；确定性引擎不内置分支预测策略。

第一阶段优先由 Canonical Skill 教导宿主 Agent 在一次工具调用中组合或并行执行
现有 MSQL，并根据上下文预算选择是否投机预取。只有真实宿主 Benchmark 证明需要
新的原子 snapshot、批量结果预算或性能能力时，才拆出独立引擎 Feature。预测策略
变化不能反向改变 SQL 语义、Router 权威结构或存储格式。

## 待验证指标

- `prefetch_hit_rate`：正确 Table 的根 Route 是否已预取；
- `model_calls_saved`：相对严格逐步导航减少的模型调用数；
- `mispredict_tokens`：错误预取进入模型输入的 token；
- `fallback_calls`：预测失败后新增的正常导航调用；
- `predictor_recall@k`：正确 Table/Route 是否进入候选并集；
- 查询 embedding、CPU 精确扫描、索引重建的延迟和资源成本；
- 端到端 RowID 成功率、总输入 token 和 `p95` 有证据回答延迟。

只有在真实宿主模型中证明调用节省大于错误预取成本，才成为默认 Skill profile。

## 关联

- [Agent 语义目录索引（Router）](./semantic-routing.md)
- [上下文生命周期](./context-lifecycle.md)
- [Query Workspace 与缓存边界](./working-set-cache.md)
- [ADR-0007](../decisions/0007-route-predictor-arsenal.md)
- [Route Predictor 历史 Feature 计划](../archive/planning/route-predictor-feature-plan.md)
