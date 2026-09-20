# ADR-0012：Row 向量命中直接返回叶子路径

状态：Accepted，2026-09-20。修订 [ADR-0007](./0007-route-predictor-arsenal.md) 的向量
持久化边界，以及 confirmed-directions 第 10 条对「检索主路径」的表述。
形态见 [检索路线与内核面](../query/retrieval-routes-jev.md)。

## 背景

`rewrite/adr0011` 内核把 Route 与 Row 的向量写进同一张 sqlite-vec 表。其中
`indexRow`（`internal/sqlstore/search.go:90-91`）把表名 + row semantics + 全部列值送去
embedding——正是 ADR-0007 与[存储索引边界](../archive/storage/indexing.md)禁止的
「Row 正文向量副本」。而该向量当前**无任何生产读取方**。

禁令源自 F21/F23 的撤销：当时融合评分直接产出事实候选，语义树被绕过。但禁令写的是
「禁止持久化」，是存储级的一刀切，而实际需要的能力是：向量命中一条 Row 之后返回它的
语义路径，让模型据此直接读到那一行。

关键事实：F169 冻结「一个 Leaf 最多一个活跃 Row」，`open_locators` 基数为 `0..1`。
因此**一条 Row 的叶子路径唯一指向那一行**——「只返回路径，不返回事实」这道防火墙
在 Row 命中这条线上不提供任何实际约束。与其维持一个名义上的约束，不如把边界挪到
它真正该在的位置并写明代价。

## 决定

1. **允许持久化 Row 语义向量**，且只服务于一个用途：命中后定位该 Row 的语义位置。
2. **命中 Row 时直接返回其叶子路径**，逐段带 `route_id`，不上卷到祖先 branch。
   模型自行判断该叶子是否满足需求，不满足再走 branch 候选逐层向下。
3. **事实仍然只由 SQL 回表产生。** 融合面与向量面不返回任何字段值、摘要、分数、
   距离或排名。
4. 由此**承认主路径的变化**：融合发现面是检索主路径，在 Row 召回上是首选；
   逐层语义导航退为兜底与区域探索，不再是到达 Row 的唯一通道。
5. **不放开的部分**，ADR-0007 这些边界继续有效：禁止文档 chunk、图片、PDF 的向量；
   禁止把分数或距离外露；禁止向量结果直接充当答案；不同 embedding space 不混比。

## 后果

- 语义树在 Row 召回上从「必经通道」变为「表达位置的坐标系」。它仍是唯一的位置表示，
  但不再是到达 Row 的唯一路径。
- 逐层导航的价值转为三项：融合召回失败时的兜底；需要在一个语义区域内探索或聚合
  而非取单行时的路径；模型判定命中叶子不足时取兄弟与上下文。
- **[F224 Row 必须可导航](../planning/f224-mandatory-row-route.md) 升级为硬前置。**
  零 Route 归属的孤儿 Row 在本设计下**完全不可召回**：向量能命中它，但没有叶子路径
  可返回（[候选预测器只给路径](../query/predictor-path-only-v1.md) 规定零叶子的 Row
  不带该字段）。此前孤儿 Row 只是难找，现在是彻底不可达。
- 检索质量的主判据从「逐层选择准确率」转向「融合召回 + 叶子充分性判断」。
  以 level top-1 为主指标的既有 benchmark 口径需要重做。
- Row 向量随 Row 修改需重算，且 embedding 在提交后异步执行，因此 Row 召回存在
  写后可见延迟；词法通道无此延迟。两者的新鲜度差异必须在融合面显式声明。
- 一张 vec0 表混装两种 kind 且 kind 过滤发生在 KNN 之后，是本决定生效前必须修的缺陷，
  否则 route 与 row 候选互相挤掉。见[检索路线](../query/retrieval-routes-jev.md) §4.1。

## 被本决定修订的表述

| 出处 | 原表述 | 修订后 |
|---|---|---|
| ADR-0007 | 禁止持久化 Row 正文、文档 chunk、图片或事实的向量副本 | Row 语义向量在本 ADR 约束下允许；chunk／图片／PDF 仍禁止 |
| confirmed-directions 第 10 条 | 检索主路径是 AI 对 Table 级语义 Router 的逐层 SQL 导航 | 融合发现是主路径，逐层导航是兜底与区域探索 |
| [存储索引边界](../archive/storage/indexing.md) | 同 ADR-0007 的禁令 | 按上表修订 |

第 51 条「禁止设置或调优融合权重」**不受修订**：RRF 只用 rank，无权重可调。

## 关联

- [检索路线与内核面](../query/retrieval-routes-jev.md)
- [jev 作为逐层分支选择器](../query/jev-branch-selection.md)
- [ADR-0007：Router 权威，候选预测器可组合](./0007-route-predictor-arsenal.md)
- [F224：Row 必须可导航](../planning/f224-mandatory-row-route.md)
- [F221：Evidence 充分性与导航终止](../planning/f221-evidence-sufficiency.md)
