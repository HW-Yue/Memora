# Agent 语义目录索引（Router）

状态：已实现；F70 将主路切换到 Table 级逐层导航，F76 暴露原子 reshape，
F77 增加按需中间 Route synopsis，F111 冻结 snapshot cursor 读取协议，F169
将 Leaf 冻结为单 Row locator，F182a 增加 alias MSQL round-trip；ADR-0007 允许可回退的
Route 候选预测器。

> **目标形态已改。** 本文描述的 Leaf → Row 是靠独立的 **Membership 关系**建立的
> （`router.Membership`，自带 revision 与正反双向索引）。
> [写入形态](../product/write-model.md)取代了这一层：**叶子直接挂 RowID**，
> 找到叶子 = 找到它下面的 RowID，不再有独立的 membership 对应关系。
> **不变的是**："一个 Leaf 最多一个活跃 Row"这条不变量保留，同一 Row 仍可属于多个
> Leaf；变的只是它靠什么实现。
> 本文仍如实描述**当前代码**，在实现改完之前可以照它读代码，
> 但**不能作为新开发的设计依据**。
> membership 的职责拆解、新归宿与分阶段迁移见
> [叶子直挂 RowID](../storage/leaf-rowid-v1.md)。

## 定义

Router 是给 AI 阅读的多层多叉语义目录树。它不是 B+ Tree、文件目录、Vector
Index 或候选评分器。每个 Table 有自己的 root，因为只有 Table 定义了叶子 Row
的共同语义和 Schema。

```text
Database: project_memora
└── Table: decisions
    ├── 产品边界
    │   ├── AI 与引擎职责 → row_id
    │   └── 永久非目标     → row_id
    ├── 查询流程
    └── 存储与恢复
```

Database 只负责将 AI 导向 Table；Table 的 Data Dictionary 说明一条 Row 代表
什么；Router 再把 AI 从 Table 确定性导向 RowID。

## 节点与 membership

内部节点包含：

- stable route ID、parent ID、name、aliases 和 revision；
- 默认返回的一句话 purpose；
- 可选、版本化、按需读取的 synopsis；它描述私有子树边界，不作为事实答案；
- 启动预算约 8～12 个子分支；
- snapshot、cursor 和 `truncated`。

aliases 是有界语义面：最多 8 项、每项 1–64 个 Unicode 字符、规范化后合计最多 512 UTF-8
bytes。MSQL 以 `ALTER ROUTE :route SET ALIASES :aliases` 完整替换并校验 expected revision；
rename 继续保留旧 name，但同样受上限约束。`SHOW`/`DESCRIBE` 返回非 null `TEXT_LIST`，alias
进入 lexical postings，但仍只是导航信号，不是事实答案。

叶子把子分支替换为零个或一个活跃 locator：

```text
table_id + row_id + row_revision + membership_revision
```

一个 Leaf 不能同时指向两个 Row；同一 Row 可以属于多个 Leaf，但正文只存一份。
引擎维护
`row_id → memberships` 反向索引，因此 revise、delete、split 和 merge 不需要
扫描整棵树即可失效旧引用。

## AI 导航

AI 必须显式执行：

```sql
SHOW DATABASES LIMIT 16 COMPACT;
SHOW TABLES FROM project_memora LIMIT 16 COMPACT;
DESCRIBE TABLE project_memora.decisions COMPACT;
SHOW ROUTES FROM TABLE project_memora.decisions AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 1;
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
```

每次返回一层，AI 根据用户意图和节点可读描述选择下一层。当前路径、候选子节点、
预算与 snapshot 构成 `Route Frame`；它随查询结束丢弃，不写入长期 system
prompt，也不等同于物理 Buffer Pool。

默认 `SHOW ROUTES` 不携带较长 synopsis。只有相邻 purpose 无法稳定区分时，AI
才执行 `DESCRIBE ROUTE :route_id` 按需读取；详细预算和内容边界见
[中间 Route Synopsis](./route-synopsis.md)。

Router/OPEN 只返回节点或 locator，不能返回正文、生成答案或自动退化为
Row/chunk Embedding、全库正文扫描和混合相似度答案。

`SHOW ROUTES` 与 `OPEN ROUTE` 都返回 `memora.list-page/v1`。`SHOW` 的 cursor 绑定
parent、完整可见序列 snapshot 和下一 offset；合法 Leaf 的 `OPEN` 不产生 next cursor。
精确字段见 [Route Read v1](./route-read-v1.md)。

可选 predictor 可以依据 Catalog、字面位置或 Route-only Vector 返回带来源的候选
Route ID，帮助 AI 预取根节点或缩短冷启动。候选不能跳过显式 Route 选择、扩大权限、
排除零命中 Table 或直接作答；miss 后必须回到本节的正常逐层导航。

## 语义维护

物理 Page 满时由引擎自动 split；语义 Branch 含混时，引擎只报告结构事实，由 AI 决定
怎样命名、拆分或移动。但 Branch 的**数量**是硬约束：一个 root 或 Branch 最多带
本 Database `route_policy.branch_fanout` 个 live child，启动默认 12。第 `N+1` 个子节点
一定失败，失败信封里带两条可执行出路——重构子树，或用
`ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :n` 提高本库上限——由 Agent 自己
判断走哪条。规则见 [F223](../planning/f223-route-branch-fanout-limit.md)与
[Route Branch Fan-out 策略](./route-branch-fanout-policy.md)。
已占用 Leaf 不接收第二个 Row，AI 必须创建新的语义 Leaf，必要时先增加 Branch。

维护粒度：

1. Row 增量：替换单 Row 的完整 membership；
2. 局部子树：处理 overflow、错挂、歧义或语义漂移；
3. Table generation：规则/格式升级或完整性失败时旁路重建。

全量重建不能就地清空当前树：

```text
generation N 继续查询
→ 构建并校验 N+1
→ 原子切换
→ 旧读者释放后回收 N
```

所有维护通过带 expected revision 的 MSQL 和 Mutation Plan 执行；少量变化不得
触发整库或整表重建。

## Row 修改、拆分与删除

- revise：显式新 snapshot 时替换 membership；未提供时保留 membership 并原子
  推进 locator revision；
- delete：保留历史，清除全部活跃 membership；
- split：创建多个语义完整 Row，重分配关系和 membership，必要时修改上层节点，
  原 Row 标记 superseded；
- merge：创建或修订合并目标，保留来源映射，清除被合并 Row 的活跃引用；
- 语义边界实际变化却缺少新 snapshot：Agent 必须补全后再提交，不能让引擎猜测。

split/merge 只改正文而不改上层 Route，属于完整性失败。

F65 已将 Row、History、Relation、上层 Route revision 和 membership revision
纳入同一原生事务。AI 必须显式给出目标 Row 和结构调整；引擎不使用相似度或
隐藏规则代替语义决策。

F71 已删除旧 Database Route、MATCH、query terms、向量和相似度 fallback；
Table Router、稳定 RowID、revision、cursor 与公开 SPLIT/MERGE 是当前唯一主路。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [语义树检索质量链路](./retrieval-quality.md)
- [Router Tree v1 历史实现](../archive/design/router-tree-v1.md)
- [ADR-0007：Router 权威，候选预测器可组合](../decisions/0007-route-predictor-arsenal.md)
