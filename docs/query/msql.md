# MSQL 标准语言

状态：面向 Agent 的唯一正式操作语言。现在代码实现了发现、语义路由、普通 SQL
写入、SPLIT/MERGE、Route / Schema Plan 与查询预算配置。
关键词 / 向量召回入口与行 `links` 读写待实现。

## 定位

MSQL 参考 SQL 的成熟表达方式，以 SQL 为主体增加数据库发现、语义路由和诊断等
Memora 专有操作。不以兼容 MySQL 为目标。相同概念优先沿用熟悉的 SQL 写法；
只有 Memora 独有能力才增加扩展语句。

事务、autocommit 与批处理落在 SQLite 上：写串行，读看最后一次提交。

Codex/Claude Skill、CLI、MCP 和外部 SDK 必须提交同一种 MSQL Request，并经过
同一套 Lexer、Parser、AST、Binder、Policy、事务和执行器。自然语言由 Agent
转换为 MSQL，不属于 MSQL Grammar。

宿主每个结构化 statement input 必须携带 `memora.authorization/v2`，声明 actor
与本次允许访问的 Database。`SHOW DATABASES` 只返回 scope 内对象。

## 现在代码支持的语句

- 发现：`SHOW INSTANCE` / `SHOW DATABASES` / `SHOW TABLES` / `SHOW CATALOG ATLAS`
- 描述：`DESCRIBE DATABASE` / `DESCRIBE TABLE` / `DESCRIBE ROUTE`
- 路由：`SHOW ROUTES`、`OPEN ROUTE`、`CREATE ROUTE`、`ALTER ROUTE`、
  `PLAN ROUTE MUTATION` / `APPLY ROUTE MUTATION`
- 数据：`SELECT`、`INSERT`、`UPDATE`、`DELETE`、`SPLIT`、`MERGE`、`RESTORE`
- Schema：`CREATE` / `ALTER`、`PLAN SCHEMA CHANGE` / `APPLY SCHEMA CHANGE`
- 事务：`BEGIN`、`COMMIT`、`ROLLBACK`
- 历史：`SHOW HISTORY`、`AS OF REVISION` / `COMMIT_SEQUENCE`
- 归档：`SHOW ARCHIVE`、`OPEN ARCHIVE`（删除后唯一的读面，见[行删除](../product/row-delete-archive.md)）
- 配置：`SHOW CONFIGURATION` / `HISTORY`、`ALTER CONFIGURATION`、
  `RESTORE CONFIGURATION`

专有能力用独立声明式语句，解析为明确的 AST 节点。

## 标准进入流程

```sql
SHOW INSTANCE;
SHOW CONFIGURATION; SHOW DATABASES;
SHOW TABLES FROM project_memora COMPACT;
DESCRIBE TABLE project_memora.design_topics COMPACT;
SHOW ROUTES FROM TABLE project_memora.design_topics AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
DESCRIBE ROUTE :route_id;
OPEN ROUTE :leaf_id LIMIT 1;
SELECT ... WHERE row_id = :row_id LIMIT 1;
```

语义发现不把自然语言交给评分器。AI 先读取 Database/Table 的用途，再逐层读取
所选 Table 的短 Route 节点，直到 Leaf 得到唯一 RowID。一个 Leaf 最多一个活跃 Row，
**一行也只占一个 Leaf**（1:1，2026-09-21 修订）。`SHOW ROUTES` 默认只返回短 purpose；可选的 0–1000
字符 synopsis 只通过 `DESCRIBE ROUTE` 按需读取。

Route alias 使用完整替换：

```sql
ALTER ROUTE :route SET ALIASES :aliases;
```

`:aliases` 是参数绑定的 TEXT 数组，`[]` 清空；最多 8 项、单项 1–64 个 Unicode
字符、合计最多 512 UTF-8 bytes。

AI 已明确语义边界后，使用公开 reshape 语句：

```sql
SPLIT work.notes ROW :source
  INTO (title, body) VALUES (:first_title, :first_body), (:second_title, :second_body);
MERGE work.notes ROWS (:first, :second)
  INTO (title, body) VALUES (:merged_title, :merged_body);
```

mutation options 同时提交来源 revision、每个目标的完整 Route snapshot、需要更新的
上层 Route purpose。引擎不猜拆分边界，只在一个原生事务里发布 superseded 来源、
新目标、History、上层 Route revision 和叶子挂载。

Route 树自身需要局部 split/merge/move 时：

```sql
PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal;
APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes;
```

完整契约见 [Route Mutation Plan v1](./route-mutation-plan-v1.md)
和 [Route Mutation Execution v1](./route-mutation-execution-v1.md)。

现有 Table 的 Column/constraint 演化：

```sql
PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal;
APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes;
```

见 [Schema Change Plan v1](./schema-change-plan-v1.md)
和 [Schema Change Execution v1](./schema-change-execution-v1.md)。

查询预算：

```sql
SHOW CONFIGURATION;
ALTER CONFIGURATION QUERY_BUDGETS SET
  ROUTE_CHILDREN :routes, OPEN_LOCATORS :locators, SELECT_SCAN :scan,
  SELECT_ROWS :rows, ROUTE_FRAME_NODES :frame;
RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision;
```

## 待实现

- **关键词 / 向量召回**：产品四条路里的两条，只返回语义路径，见
  [查询形态](../product/query-model.md) §6。
- **行链接**：数据行上已有 `links` 字段；读写语句见
  [行链接](../product/row-links.md)。

## 强制规则

- 实际数据只能通过 SQL 查询；
- Route 只返回导航元数据；
- Route 叶子只返回数据项定位，主 Agent 必须再用 SELECT 回表；
- 所有 CLI 管理操作必须映射为 MSQL；
- 长文本使用参数绑定；
- 查询必须有结果和输出预算；
- 更新应带 expected revision；
- Row 必须能按稳定 `row_id` 使用 SELECT、UPDATE 和 DELETE 精确操作；
- Row 与叶子挂载的变更必须在同一事务中原子可见；
- Parser/AST 验证完整 SQL；
- 响应使用稳定 JSON envelope 和错误码。

`expected_schema_version`、`expected_revision` 和 `max_affected_rows` 是 MSQL
request 的结构化 mutation options。见 [MSQL Mutation Executor v1](./msql-mutation.md)。

文本值超过目标 Column 当前配置的字符上限时，写入返回稳定的字段超限错误；
文本 Column 启动默认上限为 1200 个字符。引擎不自动截断。

逻辑 DELETE 默认保留 revision 和 History；删除是终态。普通 UPDATE 未提供 Route
snapshot 时保留现有叶子挂载；改变语义边界时必须提供新 snapshot。

## 统一响应与多语句

所有语句使用 [MSQL Result Envelope v1](./result-envelope.md)。一次 request
可携带由分号分隔的多条语句。多语句不自动形成事务；事务用 `BEGIN` / `COMMIT` /
`ROLLBACK`。隔离遵循 SQLite 与 [ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)。

## 关联

- [MSQL Lexer v0](./msql-lexer.md)
- [MSQL Parser Core v1](./msql-parser.md)
- [MSQL Batch 与事务边界 v1](./msql-batch-transactions.md)
- [语义路由](./semantic-routing.md)
