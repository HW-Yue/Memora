# CPU Exact Route Match v1

状态：F124d 已冻结并实现。

> **目标形态已改。** [查询形态](../product/query-model.md) §6 与
> [架构原则](../product/architecture-principles.md) §3 冻结了新规则：
> 关键词检索与向量检索**只返回命中项的完整语义树路径，不做任何其他操作**。
> 本文的点积 top-K **内部保留**（超出 limit 时仍靠它决定留哪些），
> 但分数不再进返回值；对外只给完整语义树路径。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。迁移设计见
> [候选预测器只给路径](./predictor-path-only-v1.md)。

## MSQL

```sql
SHOW ROUTE CANDIDATES FROM ALL TABLES
USING VECTOR :query_vector SPACE :space_digest
LIMIT :limit BYTES :bytes;
```

`query_vector` 是 1–4096 维、有限、L2 归一化的 `float32` 数组；JSON/IPC 数字数组
在执行边界确定性转换为 float32。`space_digest` 必须与 F124c active generation 完全
一致，调用方不能只凭维度猜测兼容。

LIMIT 为 1–64，BYTES 为 256–65536；两者继续受 Discovery Frame 全局预算约束。
结果只有 navigation-only Database/Table/Route ID、Route revision、dot-product score 和
predictor receipt，不返回 Row、surface 文本或答案。

## 精确匹配

reference backend 使用纯 Go 对归一化 float32 做 `O(N*d)` 点积。先确定当前授权
Database/Table scope，再打开这些 Database 的 active generation；Table 不在 scope 的
Route 必须在取向量和计算点积之前跳过。按 score 降序、Route ID、Database ID、Table ID
升序稳定排序并截取 Top K。输入向量、generation 与输出均不得共享可修改 slice。

Discovery snapshot 同时绑定当前 Catalog/Route snapshot、请求 space 和实际参与的 active
generation marker；`catalog_revision` 仍只描述 Catalog。这样同一 Route revision 被新模型
generation 替换后不会伪装成同一预测快照。

## 失败与回退

- query 非有限、未归一化、维度不符或 space digest 非法：MSQL validation error；
- generation 缺失、stale、corrupt 或 space 不兼容：该 Database 不参与匹配；
- 没有兼容 generation：返回成功的空 Discovery Frame，predictor 状态为 unavailable；
- 部分 Database 可用：返回可用范围候选并在 receipt 标记覆盖数量，其他范围走 Router；
- Catalog/Route 权威读取失败仍是查询错误，不能用派生索引掩盖。

本 Feature 不生成 query embedding，不实现 HNSW、Accelerate/GPU、量化、自动选表或回答。

## 关联

- [Route Vector Generation v1](./route-vector-generation-v1.md)
- [Discovery Frame v1](./discovery-frame-v1.md)
- [F124d 开工与完成门](../archive/planning/f124d-cpu-exact-route-match-gate.md)
