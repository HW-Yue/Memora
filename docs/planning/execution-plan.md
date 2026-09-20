# 执行计划

状态：**2026-09-20 重写。这是当前唯一的工作队列。**

上一份队列（[2026-08 引擎侧 E 阶段](../archive/planning/execution-plan-2026-08.md)）
依据的是自研 Page/WAL/B+ Tree，代码已删除，**不再派发**。战略理由见
[路线 v3](./roadmap-v3.md)（引擎轨道已结束，见该文头部注记）。

每项仍须按 [TDD 协议](./feature-tdd-protocol.md) 独立 Review、授权、实现、验收。
持久化相关项测 reopen / 中断 / 错误注入即可；SQLite 自己的页格式与崩溃恢复不测。

## 已裁定、不再争论

| 决定 | 结论 |
| --- | --- |
| 存储基座 | SQLite + sqlite-vec，不自研引擎（[ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)） |
| 检索主路径 | 融合发现；逐层导航是兜底与区域探索（[ADR-0012](../decisions/0012-row-vector-leaf-path.md)） |
| Row 向量 | 允许，命中直接返回叶子路径，逐段带 `route_id` |
| 写入侧 | 本轮不动；`branch_fanout` 硬上限保留 |
| jev | Skill 侧选择器，不进内核（[jev 选择器](../query/jev-branch-selection.md)） |

## 队列

按编号顺序。Q0 阻塞 Q2。

### Q0. vec0 按 kind 预过滤 ← **当前阻塞**

**问题**：`mem_vectors` 混装 route 与 row，kind 过滤在 KNN 之后
（`internal/sqlstore/search.go`）。Row 远多于 Route 时，route 候选接近零且不报错。
ADR-0012 之后两种 kind 都是主路径，互相挤。

**改动**：按 kind 分表，或用 vec0 metadata / partition 做**预**过滤。

**RED**：一个 Row 远多于 Route 的库，`USING VECTOR` 的 route 候选现在接近空；
修好后 route 与 row 各自按自身空间取 top-K。

**完成**：两种 kind 互不挤掉；reopen 后索引仍按 kind 隔离。

### Q1. F224：Row 必须可导航

[规格](./f224-mandatory-row-route.md)。ADR-0012 把它从候选升级为硬前置：
零叶子的 Row 向量能命中但没有路径可返回，等于不可召回。

**完成**：写入无 Route 归属时失败；存量孤儿有可执行出路（补挂或拒绝召回并说明）。

### Q2. 融合发现面 `USING FUSED`

形态见 [检索路线](../query/retrieval-routes-jev.md)。新增
`SHOW ROUTE CANDIDATES … USING FUSED`，引擎内 RRF，只出逐段带 ID 的语义路径。
不新增「选哪条路线」概念。

**开工前必须裁定**（现为讨论稿待决）：词法同步 vs 向量异步的可见性语义。

**完成**：只返回路径 + 末端 kind；无分数；失败即 `not_found`；LIMIT 受回表成本约束。

### Q3. 查询 Skill 编排（内核之后）

路线 A/B/C 的选择与「全部都走」在 Skill 侧。jev 逐层 `Choice` 不改内核。
不与 Q0–Q2 并行开工。

## 刻意不在本队列

- 写入侧 jev / 取消 fan-out 上限
- 自研引擎、三份日志、B+ Tree、MVCC
- 上一份 E 阶段未完成项（E9 盘上索引等）——对象已不存在
