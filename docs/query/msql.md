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
- 召回：`RECALL FROM <database> [IN <table>] (MATCH :q | NEAREST :v) LIMIT :n`
  —— 两条路说同一件事（"在哪"），所以同一条语句、同一个信封、同一套权限与稳定排序，
  只回答位置，不返回分数／距离／排名／正文。范围里有单元没有可用向量时，结果带
  `vectors_not_ready` **通知**（聚合计数，不改 `rows`）。详见 [召回的形状](#召回的形状)
- 归档：`SHOW ARCHIVE`、`OPEN ARCHIVE`（删除后唯一的读面，见[行删除](../product/row-delete-archive.md)）
- 向量：`SHOW PENDING VECTORS IN DATABASE :db LIMIT :n`（**待办清单**：哪些单元还缺向量、
  以及每个单元该拿哪段文本去嵌入）与 `ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE :db
  MODEL :model HASH :hash`（宿主把算好的嵌入交回来；一条语句一个单元，批量＝一批语句）
- 修复：`REPAIR LINKS IN DATABASE :database LIMIT :limit`（出队一批懒修复，见[行链接](../product/row-links.md)）
  与 `REPAIR VECTOR INDEX IN DATABASE :database LIMIT :limit`（把派生向量索引修到与真相一致，
  重复执行直到 `remaining` 为 0；它**不重算向量**）
- 配置：`SHOW CONFIGURATION` / `HISTORY`、`ALTER CONFIGURATION`、
  `RESTORE CONFIGURATION`

专有能力用独立声明式语句，解析为明确的 AST 节点。

## 召回的形状

```sql
RECALL FROM project_memora IN notes MATCH :q LIMIT 10;   -- 关键词
RECALL FROM project_memora NEAREST :v LIMIT 10;          -- 向量
```

**查询向量的线上形式（契约，不是实现选择）**：`base64.RawURLEncoding`（无填充）编码的
**小端 IEEE-754 float32 紧凑排列**，长度须是 4 的倍数，**不得含 NaN／Inf**，维度必须等于该
Database 已锁定的维度。写法固定的理由：**解错端序得到的是一个合法向量**——它会返回一批
合法但错误的路径，而召回不返回分数，答案里没有任何东西能显示这件事。MSQL 也没有数组类型，
数字数组会让 `[]any` 变成一等参数值，牵连整个求值器。

**`LIMIT` 是输出截断，不是召回强度。** 两路各自的内部候选数（向量路的 `k+m`）是实现细节；
并集去重后按已有的"表名 + 路径"字典序截断到 `n`。这样 `LIMIT` 的含义在单臂与并集里一致，
也不需要引入分数或权重——那会与「向量只用于定位，从不产出事实」冲突。

配了 provider 的宿主跑 `memora exec` 时会**自动排空**这份清单；没有 provider 时它什么也不做，
单元一直未就绪。**写入时可以顺手带上向量**（快路径）：`mutation.vector = {model, content_hash, values}`，
与 `route_path` 同级。它在**同一个事务**里落到这次写入所造的单元上；哈希不匹配或模型不符则
**拒收该向量、写入照常成功**，并在这条语句的结果上带一条 `vectors_not_ready` 通知说明有单元
仍未就绪——写入是事实、向量是索引，索引失败不能把事实回滚掉，但也不能不吭声。

待办清单交给宿主的是**工作**，不是答案：它带着每个单元的载荷文本（宿主无法嵌入它读不到的
东西），但这不违反召回契约——`RECALL` 依然只回答位置、不给正文。清单一律有界，且**派生自
单元本身**，所以别的客户端写的行、或配 provider 之前写的行，都会照常出现。

**提交嵌入**用同一份编码：`ACCEPT VECTOR` 带的 `HASH` 是宿主**嵌入的那段文本**的哈希，
引擎按自己的载荷规则重算、不匹配即拒——否则一条针对旧修订的向量会贴到当前修订上，
而召回不返回分数，谁都看不出来。引擎从不自己算向量、不发网络请求。

`MATCH :q NEAREST :v` 同时给出＝两路并集：各自取回候选、按位置去重（两路找到同一位置
＝一个位置）、再按"表名 + 路径"字典序输出，`LIMIT` 截断合并后的列表。语法定死为
`MATCH` 在前、不接受乱序。**一路答不了就整条语句失败**——只用一路作答会产出"看起来完整
的半份答案"，那是召回唯一绝不能有的结果。

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
