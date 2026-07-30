# Agent 语义目录索引（Router）

状态：目标架构已确认；F61 已实现 Table 级原生物理闭环，F65 已实现 reshape
原子维护，MSQL/AI 主路切换待 F70。

## 定义

Router 是给 AI 阅读的多层多叉语义目录树。它不是 B+ Tree、文件目录、Vector
Index 或候选评分器。每个 Table 有自己的 root，因为只有 Table 定义了叶子 Row
的共同语义和 Schema。

```text
Database: project_memora
└── Table: decisions
    ├── 产品边界
    │   ├── AI 与引擎职责 → [row_id...]
    │   └── 永久非目标     → [row_id...]
    ├── 查询流程
    └── 存储与恢复
```

Database 只负责将 AI 导向 Table；Table 的 Data Dictionary 说明一条 Row 代表
什么；Router 再把 AI 从 Table 导向有限 RowID。

## 节点与 membership

内部节点只包含：

- stable route ID、parent ID、name、aliases 和 revision；
- 一句话 purpose、范围边界和可选导航提示；
- 启动预算约 8～12 个子分支；
- snapshot、cursor 和 `truncated`。

叶子把子分支替换为有限 locator：

```text
table_id + row_id + row_revision + membership_revision
```

同一 Row 可属于多个叶子，但正文只存一份。引擎维护
`row_id → memberships` 反向索引，因此 revise、delete、split 和 merge 不需要
扫描整棵树即可失效旧引用。

## AI 导航

AI 必须显式执行：

```sql
SHOW DATABASES COMPACT;
SHOW TABLES FROM project_memora COMPACT;
DESCRIBE TABLE project_memora.decisions COMPACT;
SHOW ROUTES FROM TABLE project_memora.decisions AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 20;
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
```

每次返回一层，AI 根据用户意图和节点可读描述选择下一层。当前路径、候选子节点、
预算与 snapshot 构成 `Route Frame`；它随查询结束丢弃，不写入长期 system
prompt，也不等同于物理 Buffer Pool。

Router/OPEN 只返回节点或 locator，不能返回正文、生成答案或自动退化为
Embedding、cosine、全库扫描和混合相似度排名。

## 语义维护

物理 Page 满时由引擎自动 split；语义节点拥挤或含混时，引擎只报告结构事实，
由 AI 决定怎样命名、拆分、合并和移动 membership。

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

- revise：旧 revision 的全部 membership 立即不可见，新快照原子启用；
- delete：保留历史，清除全部活跃 membership；
- split：创建多个语义完整 Row，重分配关系和 membership，必要时修改上层节点，
  原 Row 标记 superseded；
- merge：创建或修订合并目标，保留来源映射，清除被合并 Row 的活跃引用；
- 缺少 AI 维护结果：写入可进入明确 `pending_reindex`，不能继续暴露旧语义定位。

split/merge 只改正文而不改上层 Route，属于完整性失败。

F65 已将 Row、History、Relation、上层 Route revision 和 membership revision
纳入同一原生事务。AI 必须显式给出目标 Row 和结构调整；引擎不使用相似度或
隐藏规则代替语义决策。

## 实现差距

F22 当前使用统一 Database root 和绝对 path；F23/F30 又叠加 MATCH 与候选融合。
这些代码是历史原型，不符合当前主路径。迁移 Feature 必须：

- 建立每 Table 独立 root 和发现语法；
- 将旧 Database 树显式转换或拒绝，不能静默猜测归属；
- 删除 Query Skill 对完整 path、`query_terms` 和 MATCH fallback 的依赖；
- 保留稳定 RowID、revision、cursor、权限和事务语义；
- 用 `US-COLD`、`US-READ`、`US-SPLIT` 端到端验收。

## 关联

- [AI-native 产品宪章](../product/ai-native-product-charter.md)
- [语义树检索质量链路](./retrieval-quality.md)
- [Router Tree v1 历史实现](./router-tree-v1.md)
