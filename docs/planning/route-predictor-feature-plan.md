# Route Predictor 小 Feature 计划

状态：F124–F124c 已完成；下一项为 F124d。F124d–F124e 仍逐项 Review、RED、实现
和合入，不能整批开工。

## 目标链路

```text
用户问题
→ Catalog Atlas + 可选候选位置
→ AI 选择一个或多个 Table/Route
→ Table Router 正常导航
→ RowID SQL 回表
```

候选命中时减少模型调用；未命中时回退，最终事实链路不变。

## Feature 拆分

| Feature | 唯一主要结果 | 明确不做 |
| --- | --- | --- |
| F124a Discovery Frame Contract | 冻结带 snapshot、预算和 predictor provenance 的候选 envelope | 具体预测算法 |
| F124b Lexical Route Locations | 倒排词项只聚合到 Database/Table/Route 位置 | 正文 snippet、自动选表 |
| F124c Route Vector Generation | 保存绑定 Route revision/model/digest 的可重建向量 generation | 查询、内置模型 |
| F124d CPU Exact Route Match | 对同一 embedding space 精确点积并稳定返回 Top K Route ID | HNSW、GPU、回答生成 |
| F124e Speculative Discovery Skill | Skill 组合 Catalog、候选和少量根 Route，并确定性回退 | 引擎隐藏 Planner |

## F124a Discovery Frame Contract

RED 先证明：不同 predictor 结果无法区分来源；跨 snapshot 混用；每个子查询分别
LIMIT 后总输出无界；预测失败被误报为查询失败。

GREEN：统一 envelope 至少包含 `snapshot`、`catalog_revision`、候选位置、
`predictor`、`reason`、`score_kind`、`truncated` 和全局预算。候选不是事实 Row。

## F124b Lexical Route Locations

RED 先证明：字面命中泄露正文、零命中 Table 被排除、旧 Route membership 仍参与。

GREEN：参数化词项只返回授权范围内的 Database/Table/Route 聚合、命中字段和数量；
AI 可以选择没有字面命中的 Table。

## F124c Route Vector Generation

RED 先证明：不同模型/维度混用；Route revision 改变后旧向量仍 active；中断重建
破坏当前 generation；Database Package 缺向量后无法正常 Router 查询。

GREEN：向量来源只允许 Route semantic surface；generation 旁路构建、校验后原子
发布，旧 generation 可回收。首版接收已生成向量，不在存储引擎内管理 Provider。
生成端可以是离线本地小模型、宿主 adapter 或云端 Provider；不要求外部在线模型，
但 Route 与 query 必须绑定同一 embedding space，纯 CPU exact 只负责后续匹配。

## F124d CPU Exact Route Match

RED 先证明：Top K 与排序 reference model 不同；NaN/错误维度被接受；权限或 Table
scope 在匹配后才过滤；并列分数不确定；输入向量被修改。

GREEN：先过滤授权 scope，再对归一化 `float32` 执行 `O(N*d)` 精确点积；按分数和
稳定 Route ID 确定排序，返回深复制候选。纯 Go 为 reference，Mac Accelerate 后置。

候选 MSQL 仅表达能力，最终语法由 F124a Review 冻结：

```sql
SHOW ROUTE CANDIDATES FROM ALL TABLES
USING VECTOR :query_vector LIMIT 8;
```

## F124e Speculative Discovery Skill

RED 先证明：Skill 把候选当答案；错误预取后不回退；旧 Route Frame 污染新主题；
多条 MSQL 的总 token、调用数和 snapshot 不可审计。

GREEN：第一次发现可组合紧凑 Catalog、候选位置和少量根 Route；模型一次选择多个
Table。预测失败显式回到普通 Router，最终只引用 SQL 回表 Row。

## Benchmark 与完成门

F125 Runner 必须比较 Router-only、Lexical、CPU Vector、Lexical+Vector 和投机根
Route，报告 `predictor_recall@k`、`prefetch_hit_rate`、`model_calls_saved`、
`mispredict_tokens`、总 token、RowID 成功率和 `p95` 有证据回答延迟。

F124a–F124e 每项独立全绿、可构建、可回滚；HNSW 不得作为测试依赖或隐藏 fallback。

## 开工前仍需冻结

- query/Route embedding 由哪个可替换本地或宿主 adapter 生成；
- embedding model 的分发、许可、隐私、版本和重建成本；
- Discovery Frame 的字符/token 全局预算及预取 Table 数；
- 纯 Go exact backend 进入 Accelerate Review 的性能门槛。

候选打包方向：保留无模型也能完整 Router 回退的基础发行包，另提供带 model、
tokenizer、runtime、license、digest 和兼容版本的可选本地 Route Encoder Pack。
它安装到 Instance 级模型目录，不进入 Database Package；当前无 CGO 单文件发行契约
若需增加 native runtime/helper，必须另开 Feature 并重做 arm64/amd64 干净机验收。

## 关联

- [ADR-0007](../decisions/0007-route-predictor-arsenal.md)
- [Route Retrieval Benchmark v3](../development/route-retrieval-benchmark-v3.md)
- [语义路由投机预取](../query/speculative-route-prefetch.md)
