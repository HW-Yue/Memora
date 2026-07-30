# AI-native 产品宪章

状态：已确认；2026-07-30 起作为产品方向的最高层约束。旧规格与本文冲突时，以本文为准，并进入架构对账而不是继续堆叠 Feature。

## 最终产品是什么

Memora 是一套由 AI 自主建模、读写、整理和持续优化的个人语义数据库。

它的目标不是给人提供另一套数据库管理界面，而是让 AI 用最快、最规范、最不容易出错的方式整理用户授权交给它的全部知识。人主要提供自然语言、资料、目标、授权和例外裁决；AI 是逻辑数据库的首要用户，也是日常 DBA。

“纯 AI 控制”指 AI 决定知识如何成为 Database、Table、Column、Row、关系和语义索引，并通过标准语言执行这些决定。它不表示让模型直接修改 Page 或绕过规则：

- AI 负责语义判断、逻辑建模、查询计划和维护意图；
- Memora SQL（MSQL，对外可简称 SQL）负责表达所有正式操作；
- 确定性引擎负责类型、约束、权限、事务、并发、历史、恢复和物理存储正确性；
- Page、B+ Tree、Buffer Pool、MVCC、Undo/Redo 等物理机制不能暴露为 Agent 的日常操作负担。

## 数据与索引的产品形态

- 持久化单位是完整、可独立修改的语义 Row，不是文档 chunk、Embedding 或聊天转录。
- 每个 Table 有自己的多层语义索引树；内部节点是短描述，叶子只保存 RowID/locator。
- AI 用 SQL 一层一层查看有限分支，选中下一层，直到得到 RowID，再用 `SELECT` 回表读取事实。
- Router 只负责发现和导航，不能返回正文或直接充当答案。
- 普通对话陈述、文档/仓库锚点和已复核来源必须分级；没有 Source Receipt 的
  写入不能冒充 reviewed fact。
- AI 上下文只保留当前有界 `Route Frame`；它是语义工作集，不是物理 Buffer Pool。
- 物理存储后端必须可替换，不能反过来定义 AI 的查询协议或产品体验。

标准读取形态（F70 起当前协议）：

```sql
SHOW DATABASES COMPACT;
SHOW TABLES FROM project_memora COMPACT;
DESCRIBE TABLE project_memora.decisions COMPACT;
SHOW ROUTES FROM TABLE project_memora.decisions AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 20;
SELECT * FROM project_memora.decisions WHERE row_id = :row_id LIMIT 1;
```

每一步都有预算、cursor、稳定 ID 和结构化错误；AI 不需要把全库目录塞进 prompt。

## AI 的标准工作流

1. **发现与读取**：发现库 → 选择表 → 读取 Schema 与顶层 Route → 逐层导航 → 获得 RowID → SQL 回表 → 引用 revision 回答。
2. **新增**：先发现和查重 → 决定复用/新增 Schema → 写完整 Row → 建立关系 → 放入一个或多个语义叶子 → 验证可重新找到。
3. **修改**：精确读取当前 revision → 判断 revise/merge/split → 带 expected revision 写入 → 原子更新关系和索引 → 回读验证。
4. **删除**：确认范围和影响 → 逻辑删除 → 失效所有 Route membership 和其他索引 → 保留可审计历史与补偿能力。
5. **Schema 演化**：先读 Data Dictionary → 预览影响 → 用受限 DDL 修改 → 迁移/补齐 → 重建受影响索引 → 验证旧查询。
6. **语义索引优化**：依据访问失败、分支拥挤、歧义和维护成本提出局部调整；优先拆分/合并局部节点，不因少量变化重建整库。
7. **Row 拆分/合并**：保持语义完整性而不是机械按字数切割，并同步改变上层语义索引。

### Row 拆分示例

当一个知识项已包含两个可独立修改的主题，1200 字不足而完整表达需约 3000 字时，AI 不截断，也不把 Column 上限盲目调大：

1. 读取原 Row 的 revision、关系、来源和全部 Route membership；
2. 生成两个或多个各自完整的新 Row，并明确它们之间的关系；
3. 将原 Row 标为被替代，保留历史和可追溯映射；
4. 重分配关系、来源锚点和叶子 membership；
5. 必要时拆分或改写 Table 的上层 Route 节点；
6. 在一个 Mutation Plan 中提交，失败则不留下半套结构；
7. 从顶层重新导航，确认每个新 Row 都能被正确找到。

合并执行相反流程，同样不能只改正文而留下陈旧索引。

## 用户故事与验收

- **US-HUMAN**：作为普通用户，我只需表达目标、交付资料和纠正错误，不需学习建表、索引或数据库运维；只有语义冲突、高风险、越权和不可恢复操作打断我。
- **US-COLD**：作为第一次接管的 AI，我能在有界输出内发现库、表、Schema 和顶层 Route，无需旧聊天或长期索引 prompt。
- **US-READ**：作为查询 AI，我能只靠逐层 SQL 导航获得 RowID，再回表读取有 revision 的事实；中间结果不泄露正文。
- **US-INSERT**：作为写入 AI，我能先查重，再新增完整 Row、关系与 Route membership，并在提交后从顶层验证可发现。
- **US-UPDATE**：作为维护 AI，我能精确读取目标 revision，只修改目标 Row，并同步修订关系、历史和语义定位。
- **US-DELETE**：作为删除 AI，我能预览影响、逻辑删除目标、失效全部索引引用，并保留审计与补偿路径。
- **US-CORRECT**：作为收到纠正的 AI，我能定位旧事实、保存历史、修订所有相关结构，并说明实际改变了什么。
- **US-SCHEMA**：作为建模 AI，我能发现同义定义、创建或演化 Schema、迁移受影响 Row，并证明旧数据仍可查询。
- **US-DBA**：作为 AI DBA，我能发现分支拥挤、错误归类、陈旧 membership 和 Schema 债务，生成有界、可回滚的优化计划。
- **US-OPTIMIZE**：作为优化 AI，我能依据真实导航失败和访问成本局部优化语义树，同时证明质量改善且事实未改变。
- **US-SPLIT**：作为维护超大或多主题 Row 的 AI，我能语义拆分内容，并在同一次计划中重构关系和上层 Route。
- **US-CONFLICT**：作为遇到多来源冲突的 AI，我会并列证据并请求用户裁决，不让引擎猜测哪条语义为真。
- **US-ASSIMILATE**：作为吸收资料的 AI，我临时阅读外部材料，只写入完整语义模块、来源锚点和覆盖收据，不保存机械 chunk。
- **US-RECOVER**：作为宿主切换或崩溃后的 AI，我能从数据库状态、历史和收据恢复，不依赖某一家模型、官网或 API 地址。
- **US-ENGINE**：作为确定性引擎，我拒绝越权、超限、陈旧 revision 和半完成 Mutation Plan，但不替 AI 猜测语义。
- **US-DEVELOPER**：作为 Feature 开发者，我能在开工前看到目标故事和标准旅程，在合入前用同一故事证明没有偏离产品。

## 永久边界

- 禁止以 Embedding、向量数据库、余弦/距离相似度或其伪装形式实现、评测或兜底语义匹配。
- 禁止全库扫描后把大量内容塞给模型选择；必须逐层缩小 Route Frame。
- 禁止 Agent 绕过 SQL 直接操作存储、索引文件或隐藏管理 API。
- 禁止要求普通用户日常设计 Schema、调索引或修复内部一致性。
- 禁止为了跑通 Feature 而让 SQLite、某个模型 Provider 或宿主适配器成为不可替换的产品核心。
- 禁止仅证明“命令能运行”就宣称 AI-native 体验成立。

每个 Feature 都必须通过[产品与用户故事门禁](../planning/feature-product-gate.md)；未通过就不属于已完成产品能力。
