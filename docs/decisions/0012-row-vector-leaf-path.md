# ADR-0012：Row 向量命中直接返回叶子路径

状态：Accepted，2026-09-20。修订 [ADR-0007](./0007-route-predictor-arsenal.md) 的向量
持久化边界。查询形态是四条路：语义索引是 Agent 主路，关键词与向量是双路召回，
jev 只在 Skill。Row 向量允许，命中只给叶子路径。当前没有向量实现。

## 背景

向量命中一条 Row 之后应返回它的语义路径，让模型据此直接读到那一行。
禁令要挡住的是「融合评分直接产出事实候选」——不是挡住 Row 语义向量本身。

关键事实：F169 冻结「一个 Leaf 最多一个活跃 Row」，`open_locators` 基数为 `0..1`。
因此**一条 Row 的叶子路径唯一指向那一行**——「只返回路径，不返回事实」这道防火墙
在 Row 命中这条线上不提供任何实际约束。与其维持一个名义上的约束，不如把边界挪到
它真正该在的位置并写明代价。

## 决定

1. **允许持久化 Row 语义向量**，且只服务于一个用途：命中后定位该 Row 的语义位置。
2. **命中 Row 时直接返回其叶子路径**，逐段带 `route_id`，不上卷到祖先 branch。
   模型自行判断该叶子是否满足需求，不满足再走 branch 候选逐层向下。
3. **事实仍然只由 SQL 回表产生。** 召回面不返回任何字段值、摘要、分数、
   距离或排名。
4. **四条路并列**：语义索引是 Agent 主路；关键词与向量是双路召回；jev 只在 Skill。
   语义树仍是位置的唯一表示，但不是到达 Row 的唯一通道。
5. **不放开的部分**：禁止文档 chunk、图片、PDF 的向量；禁止把分数或距离外露；
   禁止向量结果直接充当答案；不同 embedding space 不混比。

## 后果

- 语义树仍是唯一的位置表示，但不是到达 Row 的唯一路径。
- 逐层导航用于：召回失败时的兜底；一个语义区域内的探索或聚合；
  模型判定命中叶子不足时取兄弟与上下文。
- **[F224 Row 必须可导航](../planning/f224-mandatory-row-route.md) 是硬前置。**
  零 Route 归属的孤儿 Row 没有叶子路径可返回。
- Row 向量随 Row 修改需重算；与关键词召回的可见性差异随实现一起裁定。

## 被本决定修订的表述

| 出处 | 原表述 | 修订后 |
|---|---|---|
| ADR-0007 | 禁止持久化 Row 正文、文档 chunk、图片或事实的向量副本 | Row 语义向量在本 ADR 约束下允许；chunk／图片／PDF 仍禁止 |
| confirmed-directions 第 10 条 | 检索主路径是 AI 对 Table 级语义 Router 的逐层 SQL 导航 | 四条路并列；语义索引是 Agent 主路，召回是另外两条 |
| [存储索引边界](../archive/storage/indexing.md) | 同 ADR-0007 的禁令 | 按上表修订 |

分数与距离不外露；不设置或调优融合权重。

## 关联

- [检索路线与内核面](../query/retrieval-routes-jev.md)
- [jev 作为逐层分支选择器](../query/jev-branch-selection.md)
- [ADR-0007：Router 权威，候选预测器可组合](./0007-route-predictor-arsenal.md)
- [F224：Row 必须可导航](../planning/f224-mandatory-row-route.md)
- [F221：Evidence 充分性与导航终止](../archive/planning/f221-evidence-sufficiency.md)
