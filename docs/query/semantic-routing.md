# Agent 语义目录索引（Router）

状态：多层多叉树和叶子 ID 候选已确认；节点容量、分裂协议和评分待冻结。

## 目标

Router 是给 Agent 阅读的多层语义目录索引。索引发现 Sub-agent 从根节点逐层选择一个或多个相关分支，直到叶子节点取得候选数据项 ID；它不读取业务正文或物理 Page。

```text
Router Root
├── 项目
│   ├── Memora
│   │   ├── 存储引擎 → [row_id...]
│   │   ├── MSQL     → [row_id...]
│   │   └── 检索设计 → [row_id...]
│   └── 其他项目
├── 人物
└── 决策
```

Router 是 Memora 专有的 Agent 索引，不是 B+ Tree、文件目录或 MySQL Router。代码和协议可以简称 Router，对用户解释时优先称“Agent 语义目录索引”。

## Router Page

内部节点只包含：

- 当前逻辑路径和一句话用途；
- 启动预算约 8～12 个子分支；
- 每个子分支一句话 scope；
- 可选的相关 Database/Table scope；
- 可选查询提示；
- Schema/Route revision。

叶子节点使用同样的短说明，但把子分支替换为有限数量的稳定数据项 ID/locator。具体 locator 是否需要同时携带 Database、Table、Row ID 和 revision，留到协议冻结。

同一 `row_id` 可以同时出现在多个语义相关的叶子节点中；叶子只保存引用，不复制 Row。Row 内容或归属变化时，Agent 通过 MSQL 提交完整 Route membership 快照，引擎在同一事务中增加、移动或删除所有叶子引用。逻辑 DELETE 后不得继续作为活跃 Router 候选。

启动候选预算为 300～500 个中文字、Policy 上限约 800 字。分支数和字符预算属于 Database Route Profile；若后续生命周期策略允许调整，只能通过 MSQL、Policy 和 revision 修改。Router 不返回完整业务记录。

## 三路检索

```text
Semantic Router：理解性导航
Inverted Index：Agent 词项为主、机械 N-gram 低权重兜底的全局召回
Relation Graph：扩展结构和语义邻居
```

Router 候选与倒排、关系候选融合后只输出数据项定位，最终仍由 SQL 选择和读取语义记录。

默认由索引发现 Sub-agent 逐层读取 Router，并与两路倒排和关系信号融合，只向主 Agent 返回候选数据项定位。主 Agent 再按定位执行 SQL；Router 或倒排结果本身不能返回正文。

## 语义分裂

物理 Page 满时引擎自动 split。Router 内部节点 fan-out 或叶子 ID 数超过配置预算时，引擎只能报告语义 overflow；怎样命名新分支、移动 ID 和保持语义完整必须由 AI 决定并通过事务修改。引擎负责树结构、引用完整性、版本和原子切换。

## Row 修改、拆分与删除

Router membership 至少绑定：

```text
router_generation + leaf_id + row_id + row_revision + status
```

引擎另外维护 `row_id → memberships` 反向索引。因此 UPDATE、DELETE、SPLIT 或 MERGE 不需要扫描整棵树，就能找到该 Row 在所有叶子中的引用并立即写入 tombstone。

普通 SQL UPDATE 如果没有同时提供新的 Agent 词项和 Route membership，仍然提交业务 Row、物理索引和机械索引，但必须：

1. 立即让旧 revision 的 Agent posting 和全部 Router 引用不可见；
2. 把新 revision 标记为 `pending_reindex`；
3. 由 daemon 异步调用 Agent 生成完整词项和多叶子 membership；
4. 通过带 expected revision 的 MSQL 原子启用；
5. 若 Row 再次变化，则丢弃过期结果并重新排队。

逻辑 DELETE 保留 Row 历史，但从所有活跃发现索引中消失。SPLIT 将原 Row 标记为 superseded/deleted，清除其活跃引用，再为新 Row 分配各自稳定 ID 并重新索引。

## 可重建 generation

Router 是派生索引，不是真相源，但少量变化绝不能触发整库重建。维护分为三级：

1. Row 增量：默认路径，只删除该 `row_id` 的旧 membership 并写入新 membership；
2. 子树重建：某个叶子/分支的 tombstone、overflow 或语义漂移超过本地阈值时，只重建该子树；
3. Database generation 重建：变化已广泛分布、全局脏比例超过阈值、索引规则/格式不兼容升级或完整性校验失败时使用。

整库触发不使用一个写死的绝对条数，而由 Database 的 Router Maintenance Profile 判断：

```text
dirty_rows >= full_rebuild_min_rows
AND dirty_rows / active_rows >= full_rebuild_ratio
```

局部子树使用自己的 `subtree_dirty_ratio`、tombstone ratio 和 overflow 条件。启动值通过 benchmark 确定并写入数据库配置，后续是否允许 AI 优化留到配置生命周期阶段。

全量重建不能就地清空当前树，而是：

```text
active generation N 继续查询
→ 后台构建 generation N+1
→ 校验结构、版本和 Row 覆盖
→ 原子切换 active generation
→ 等旧读者结束后回收 N
```

重建输入来自当前有效 Row、Schema、关系和 Agent 索引规则。索引 generation 必须支持失败回滚；旧 generation 和 tombstone 最终由 compaction 物理清理。Row、子树、Database 三种作用域的触发、观察和取消都必须映射为声明式 MSQL，不能通过私有运维旁路。

## 内部身份与外部路径

- 内部使用稳定 route/object ID；
- Agent 使用 `/project/memora/indexing` 等语义路径；
- 同一导航会话可使用 `@1` 等短句柄；
- 重命名路径不能改变内部身份；
- 旧路径可作为别名或 redirect。

## 未决问题

- Router 采用系统表还是独立系统对象；
- Router 如何自动发现内容变化并提示需要重组？
- 路由与倒排结果如何合并评分？
- 根目录数据库很多时如何避免 `SHOW DATABASES` 自身变长？
- Router 路径错误时，混合词项索引能否稳定救回记录？
- 内部节点 fan-out、叶子 ID 数、搜索深度和 beam width 的启动配置；
- generation 切换前的覆盖率、重复率和质量验收门槛；
- `subtree_dirty_ratio`、`full_rebuild_min_rows` 和 `full_rebuild_ratio` 的启动值；

## 关联

- [MSQL](./msql.md)
- [无向量检索质量链路](./retrieval-quality.md)
- [上下文生命周期](./context-lifecycle.md)
- [物理与检索索引](../storage/indexing.md)
