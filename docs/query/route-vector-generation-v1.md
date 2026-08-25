# Route Vector Generation v1

状态：F124c 已冻结并实现；只包含可重建 generation，不包含候选查询。

> **返回值已收窄（2026-08-25）。** 候选同样只给完整语义树路径。
> 另外：Frame 不再带预测器回执，所以「向量预测器不可用」没地方放进一个成功的
> 回答里了——返回零个候选等于宣称「搜过了，树里没有」，是假话。
> 现在直接报错（`not_found`）。
> 本文描述的 generation **仍然没有生产发布方**（`routevector.Service.Publish`
> 只有测试在调），见[已知风险](../development/known-risks.md) §7d。
> 见[候选预测器只给路径](./predictor-path-only-v1.md)。

## 目的

Route 向量只是导航预测器的派生数据。F124c 接收外部或本地 encoder 已生成的归一化
`float32`，将其绑定当前 Route semantic surface、revision 和同一 embedding space，
旁路校验后原子发布。模型缺失、generation 缺失或失效时普通 Router 不受影响。

## Semantic surface

输入只来自当前 live Table Route：name、排序 aliases、path、purpose 和 synopsis。编码器
使用 `memora.route-vector-surface/v1` 的 canonical JSON 文本，并随向量回传该文本的
SHA-256。引擎重新计算并拒绝 digest 或 Route revision 不一致的输入。

禁止把 Row、Column value、History、membership、文档 chunk、图片或答案放入 surface。

## Embedding space

每个 generation 只绑定一个：

- model ID 与 model revision；
- model artifact SHA-256；
- dimensions；
- `float32 + l2 + dot_product`。

所有向量维度必须完全相同、元素有限且 L2 norm 在容差内等于 1。space identity 是上述
字段的确定性 digest；不同 space 不允许混合或由查询端猜测兼容。

## Generation 内容

每个 Database 的 generation manifest 包含 generation ID、space、当前 Route source
digest、每个 Route ID/Table ID/revision/surface digest/vector offset、向量文件 SHA-256。
entries 按 Route ID 排序，向量为 little-endian float32；manifest 不保存 surface 文本。

发布请求必须恰好覆盖该 Database 当前全部 live Table Route。构建完成后再次读取 Route
source；revision/surface 变化则拒绝发布。读取 active generation 时也重验当前 source，
旧 Route revision 只能得到 `stale`，不能参与 F124d 匹配。

## COW 发布与恢复

Instance 的 `<data-dir>/derived/route-vector-v1/` 派生目录使用
staging → generation rename → active marker rename：

1. staging 写入、fsync 并 strict reopen；
2. 原子 rename 为不可变 generation，fsync generations 目录；
3. source reverify；
4. 写临时 marker、fsync、rename、fsync Database 派生目录。

marker 发布前失败保留旧 active；generation rename 后留下的 orphan 不可达且可严格复用。
marker rename 后 parent fsync 失败返回 outcome-unknown，reopen 只信 marker。重复相同发布
可按 generation/manifest digest 幂等确认。

非 active generation 可显式 reclaim；调用方必须传入仍需保留的 generation ID。active、
staging、未知目录和权威 Database 文件永不由该路径删除。

## 产品与接口边界

- generation 位于 Instance 派生目录，不进入 Database Package；包内无向量也可完整导航；
- F124c 不管理 Provider、API key、模型下载、tokenizer 或 query encoder；
- generation 构建是引擎维护能力，不要求 Agent 用 MSQL 操作物理索引；
- F124d 才冻结 `USING VECTOR` 候选 MSQL，并只使用相同 space 的 query vector；
- 当前不实现 HNSW、GPU/Accelerate、量化或答案生成。

## 关联

- [ADR-0007](../decisions/0007-route-predictor-arsenal.md)
- [语义路由投机预取](./speculative-route-prefetch.md)
- [Route Predictor 历史 Feature 计划](../archive/planning/route-predictor-feature-plan.md)
- [F124c 开工与完成门](../archive/planning/f124c-route-vector-generation-gate.md)
