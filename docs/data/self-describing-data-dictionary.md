# 自描述 Data Dictionary

状态：方向已确认；元数据字段和预算仍需原型验证。

## 目标

一个没有旧聊天、没有缓存的新 Agent，只看有界的 Data Dictionary 就应知道：数据是什么、为什么存在、不包含什么、应该从哪里继续查询。

自描述信息属于数据库本身，随备份、迁移和版本一起走，不能只写在 Skill 或某次聊天里。

## Database 元数据

每个 Database 至少维护：

- `database_id`、稳定名称与别名；
- `purpose`：一句话说明用途；
- `scope` 与 `anti_scope`：收什么、不收什么；
- 创建原因、默认 Policy 与隐私等级；
- Agent 词项与机械词项的 Search Weight Profile 及其 revision；
- 根 Route 列表；
- `schema_version`、`route_revision`；
- 自适应配置版本、最近调优原因和评测依据；
- 创建时间、最近有效变更时间。

## Table 元数据

每个 Table 至少维护：

- `table_id`、名称、别名和废弃名称；
- `purpose` 与 `anti_scope`；
- `row_semantics`：一行代表什么；
- 主身份字段与去重规则；
- 少量正例和反例；
- `schema_version` 与导出配置。

## Column 元数据

每个 Column 至少维护：

- `column_id`、名称、别名；
- 类型、可空性、默认值；
- 文本字段最大字符数等 Column 级约束；
- 业务含义、单位和格式；
- `semantic_role`，v1 支持 `title`、`summary`、`identity`、`status`、`fact`、`rationale`；其中 title 与
  summary 在单 Table 内各最多一个，缺失时展示端只能回退到 RowID/revision；
- 索引提示、隐私标签和废弃状态。

名称可以改变，稳定 ID 和历史别名不能丢失。

## 有界发现协议

```text
SHOW DATABASES COMPACT
DESCRIBE DATABASE <name> COMPACT
SHOW TABLES FROM <database> COMPACT
DESCRIBE TABLE <database.table> COMPACT
SHOW ROUTES FROM DATABASE <name> AT '/' COMPACT
OPEN ROUTE FROM DATABASE <name> AT '<path>'
```

默认输出只包含用途、边界、关键字段、revision 和下一步句柄。详细历史、示例和迁移说明必须显式请求，避免冷启动发现本身占满上下文。

## Schema 卫生规则

- 新建前先按名称、别名、字段签名和用途查重；
- 相似不等于同义，AI 只能提出 merge/rename 计划；
- 合并必须迁移 Row、关系、索引、Router 和旧名称 redirect；
- 表或字段变化后同步更新 purpose、示例和 Router；
- 描述缺失、重复定义、孤儿字段和长期未使用对象进入健康报告；
- 引擎校验结构和 revision，不替 AI 判断业务含义。

## 接管质量检查

冷启动 Agent 应在固定调用与字符预算内回答：

1. 当前有哪些业务范围？
2. 目标问题最可能属于哪个 Database/Table？
3. 一行数据表示什么，当前 Schema revision 是多少？
4. 怎样继续查询而不读取整个库？
5. 哪些范围明确不属于这里？

答不出来意味着自描述失败，即使数据本身仍可被全文搜索到。

## 描述字段怎么写（2026-09-25 咨询结论）

**实测反例（2026-09-25，已修）**：本实例库 `memora` 当时的 `scope` 是「现行原则、规格、能力、风险与工作方向」
——只列知识类别，不说明这个库是什么，冷启动 agent 读不出"一条内容该不该进这里"。原因之一是 Skill
当时没教怎么写这两个字段（`write.md` 只给了 `<what this Database holds>` 这样的占位符）。

**判据**：`purpose` 说明**是什么**（要能让不知道 Memora 是什么的读者读懂），`scope` 画**收哪些的范围**，
`anti_scope` 对**邻库**画界。核心区分：**关于 Memora 的知识，不是存进 Memora 的所有知识。**

**已应用的值（2026-09-25）**：`memora` 的 `purpose`「关于 Memora——一个由 AI 自主建模、通过 MSQL
读写的本地个人记忆与知识库——的产品与仓库知识」，`scope`「Memora 当前有效的产品原则、功能规格、
MSQL 读写能力、已知风险与开发方向」，`anti_scope`「个人身份、求职、实习与个人项目经历归 `me`；
设备与模型配置归 `setup`」。`me` 同步改对：`scope` 里那句「项目」原本与 `memora` 撞车，现在由
`anti_scope` 划开（「Memora 本仓库的产品与仓库知识归 `memora`；设备与模型配置归 `setup`」）。
**`setup` 库本来就是范本**——它的 `anti_scope` 早写明"那在 memora 库""那在 me 库"；这次把另外两边
补成互指，三个库的边界才互为一致。

**曾经的缺口（挡住修改的不是措辞，是语言）**：`ALTER DATABASE` 只有 `RENAME`
（`internal/msql/parser/parser.go` 的 `parseAlter`），所以这些字段在**建库那一刻焊死**。2026-09-21
把它列为候选（[引擎拥有形状](../planning/engine-owned-shape.md)），**2026-09-25 已落地**：
`ALTER DATABASE … SET PURPOSE/SCOPE/ANTI SCOPE` 是一条 L2 有界元数据写，`SET` 只写它点名的字段，
`ANTI SCOPE ''` 撤销一条边界声明。契约见 [MSQL Catalog DDL v1](../query/catalog-ddl.md)；
Skill 侧同步写清三个字段各管什么（`references/write.md`）。

**`ensure` 仍然只复用、不回写描述**——这是有意的，不是遗留：让每次写入携带的 ensure 计划都能改写库
声明，等于把"越具体越容易烂"反过来变成"每次写入都可能漂移"。改描述是**一次显式动作**（先读、比较、
再写），不是 ensure 的副作用。

## 未决问题

- `purpose` 等字段由 AI 自由写，还是使用受限模板？
- Compact 输出的字符、Table 数和 Column 数预算；
- 无向量时如何计算 Schema 同义候选；
- Data Dictionary 是否允许不同语言的别名和说明；
- Schema 健康检查是每次写入、定期任务还是按需触发。

## 关联

- [AI-native 产品契约](../product/ai-native-contract.md)
- [AI 自主权与约束](../archive/agent/autonomy.md)
- [语义路由](../query/semantic-routing.md)
