# MSQL 标准语言

状态：协议定位已确认；Table 级 Router 目标语法待实现。F21/F22 的 MATCH 与
Database 级 Router 语法只作历史兼容说明，不是最终查询主路径。

## 定位

MSQL 是 Memora 面向 Agent 的唯一正式操作语言。它参考 SQL 标准和 MySQL 的成熟表达方式，以 SQL 为主体增加数据库发现、语义路由、包管理、导出和诊断等 Memora 专有操作。目标是让所有正式操作都经过统一、容易解析的标准化语言，而不是发明多套工具协议。

MSQL 不以兼容 MySQL 为目标，不承诺 MySQL 的完整 Grammar、行为、网络协议或客户端兼容性。相同概念优先沿用熟悉的 SQL 写法；只有 Memora 独有能力才增加扩展语句。

对事务、autocommit、批处理等已有成熟 SQL 语义的行为，默认参考 MySQL；只有 Memora 独有能力或有明确产品理由时才偏离，并在 MSQL 规格中显式记录差异。

Codex/Claude Skill、CLI 命令、外部 SDK 和未来可选的内置 Agent Loop 必须提交同一种 MSQL Request，并经过同一套 Lexer、Parser、AST、Binder、Policy、事务和执行器。`pack`、`install`、`open`、`export`、`doctor` 等 CLI 命令只是对应 MSQL 的参数化便捷入口，不能拥有绕过 MSQL 的实现路径。自然语言由 Agent 转换为 MSQL，不属于 MSQL Grammar。

宿主 Agent 的每个结构化 statement input 必须携带 `memora.authorization/v1`，声明 actor 与本次允许访问的 Database 名称或稳定 ID。Policy 同时检查静态限定名、参数化 Route、关系端点和管理操作；`SHOW DATABASES` 只返回 scope 内对象。直接使用内部 Go API 或本地用户运行的普通 SQL 可走可信本地操作员路径，但 `PACK DATABASE`、`EXPORT WIKI` 和 `INSTALL PACKAGE` 没有无 scope/approval 降级。完整边界见[安全与隐私门 v1](../development/security-privacy-gate-v1.md)。

## 标准进入流程

```sql
SHOW INSTANCE;
SHOW DATABASES;
SHOW TABLES FROM project_memora COMPACT;
DESCRIBE TABLE project_memora.design_topics COMPACT;
SHOW ROUTES FROM TABLE project_memora.design_topics AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
OPEN ROUTE :leaf_id LIMIT 20;
SELECT ... WHERE row_id = :row_id LIMIT 1;
```

MSQL v0 使用 `SHOW` / `DESCRIBE` 作为 Database、Table、Route 和 Data Dictionary 的正式发现接口。第一版不要求实现 `information_schema` 查询视图；底层仍由同一套自描述 Data Dictionary 提供结果。

## 候选语句

- 发现：SHOW INSTANCE/DATABASES/TABLES；
- 描述：DESCRIBE DATABASE/TABLE；
- 路由：SHOW ROUTES、OPEN ROUTE；
- 数据：SELECT、INSERT、UPDATE、DELETE；
- Schema：CREATE/ALTER/DROP；
- 事务：BEGIN、COMMIT、ROLLBACK、SET TRANSACTION ISOLATION LEVEL；
- 历史：SHOW HISTORY、AS OF REVISION/COMMIT_SEQUENCE、RESTORE 补偿；
- 关系：RELATE、SHOW RELATIONS、UNRELATE；
- 管理：PACK、INSTALL、OPEN、EXPORT、DOCTOR、REINDEX；
- 显式字面检索：是否保留历史 `MATCH` 语法待架构对账，不能作为语义主路径；

Memora 专有管理能力采用独立的声明式语句，并解析为明确的 AST 节点；不使用 `CALL memora.*(...)` 形式的通用过程调用。F44 已冻结的写法：

```sql
PACK DATABASE work_x BY :author;
OPEN PACKAGE :package READ ONLY;
INSTALL PACKAGE :package TRUSTED;
```

包内容通过参数绑定进入执行器。`READ ONLY` 和 `TRUSTED` 是强制安全子句；这些语句只能
autocommit，显式事务中不直接执行。返回格式见 [Database Package v1](../product/database-package-v1.md)。

F45 已冻结单向 Wiki 导出：

```sql
EXPORT WIKI TO :path PROFILE :profile;
```

CLI 通过参数绑定传入路径和 Profile JSON，Profile 等长文本不得插值进 MSQL；目标必须是绝对规范化路径。语句只允许 autocommit，不读取或回流 Vault 中的人类编辑。投影、稳定路径、manifest 与增量规则见 [Obsidian Wiki 导出](../export/obsidian-wiki.md)。

语义发现不把自然语言交给评分器。AI 先读取 Database/Table 的用途，再逐层读取
所选 Table 的短 Route 节点，直到叶子得到 RowID。aliases、旧名称和关系是可读
数据库内容，由 AI 在判断或明确 SQL filter 中使用，不进入隐藏相似度融合。

## 强制规则

- 实际数据只能通过 SQL 查询；
- Route 只返回导航元数据；
- Route 叶子只返回数据项定位，主 Agent 必须再用 SELECT 回表；
- 所有 CLI 管理操作必须映射为 MSQL，不能直接调用旁路引擎接口；
- 长文本使用参数绑定；
- 查询必须有结果和输出预算；
- 更新应带 expected revision；
- Row 必须能按稳定 `row_id` 使用 SELECT、UPDATE 和 DELETE 精确操作；
- Row、物理索引和 Router membership 的变更必须在同一事务中原子可见或显式标记待重建；
- Parser/AST 验证完整 SQL，正则不负责语法正确性；
- 响应使用稳定 JSON envelope 和错误码。

F15 已把 `expected_schema_version`、`expected_revision` 和 `max_affected_rows` 冻结为 MSQL request 的结构化 mutation options，而不是拼进 SQL 文本。语法、预算和精确 mutation 边界见 [MSQL Mutation Executor v1](./msql-mutation.md)。

F18 已冻结参数化 `RELATE`、有界 `SHOW RELATIONS` 和 revision-guarded `UNRELATE`。关系结果只返回结构化边与稳定 Row 定位；业务内容仍必须使用 SELECT 回表。语法和事务边界见 [MSQL Relationships v1](./msql-relationships.md)。

F21 的 `MATCH database.table QUERY ... TERMS ...` 是已撤销主路径的历史实现，
不得继续扩展语义评分。F22 已实现参数化 Router 管理与遍历，但 root 仍是
Database；迁移差距见 [Router Tree v1](./router-tree-v1.md)。

文本值超过目标 Column 当前配置的字符上限时，INSERT、UPDATE、MERGE 等写入返回稳定的字段超限错误；文本 Column 启动默认上限为 1200 个字符。引擎不自动截断，调用方可以切分后重试，也可以通过声明式 DDL 调整该 Column 的类型或上限；所有变更都经过 Policy 和 revision 校验。

普通 SQL 负责业务 Row 修改；Agent 生成的完整 Route membership 也必须由
声明式 MSQL 语句或 UPDATE 扩展正式提交，不能通过私有 API 旁路写索引。具体
Grammar 待冻结。逻辑 DELETE 默认保留 revision 和 History Store；不可恢复的
PURGE 是独立高风险语句。

普通 UPDATE 未提供新语义 membership 时，引擎使旧 Router membership 立即
失效，将新 revision 标记为 `pending_reindex`，由宿主 AI 后续维护。期间可以
通过稳定 RowID 和精确 SQL 读取，但不能用旧 Route 冒充当前语义定位。

Router 的 Row、子树或 Table generation 重建必须映射为 MSQL `REINDEX` 类
声明式语句。重建在新 generation 中进行，验证后原子切换；少量修改只能走
增量路径，不能无条件启动整表重建。

## 统一响应

所有语句使用 [MSQL Result Envelope v1](./result-envelope.md)。`SELECT`、`SHOW`、`DESCRIBE`、写入和管理语句只改变 statement result 的字段取值，不各自定义顶层结构。单语句也进入 `results[]`；错误、warning、截断、batch 顺序和未知字段兼容规则已经冻结。

## 多语句请求

MSQL v0 必须允许一次 request 携带由分号分隔的多条语句，使 Agent 能在一次往返中完成一组发现或查询。Parser 解析完整 statement list，不能用字符串切分代替语法解析。

批次返回一个统一的 batch envelope，并按输入顺序包含每条语句各自的标准结果 envelope。多语句 request 本身不自动形成事务；事务采用显式边界：`BEGIN` 或 `START TRANSACTION` 开启，`COMMIT` 提交，`ROLLBACK` 回滚。事务边界外的语句按 autocommit 执行。

一个 request 可以包含完整事务，也可以在长驻会话中跨 request 保持事务状态。短生命周期 CLI 不得在进程退出后保留未完成事务。

隔离级别参考 MySQL/InnoDB：默认 `REPEATABLE READ`，首版同时支持 `READ COMMITTED`；一致性读、`FOR SHARE` / `FOR UPDATE` 锁定读、范围锁与防幻读边界按 InnoDB 语义实现。

错误处理按操作类型区分：纯读批次中的一条查询失败不阻止其他独立查询继续执行。每条语句都必须产生结构化结果；失败项至少标明 statement index、对应语句、稳定错误码和清晰原因，不能只返回模糊的 batch 级错误。

显式事务中的任一写操作失败时，整个事务立即自动回滚，事务块内剩余语句和对应 `COMMIT` 标记为 `skipped` / `rolled_back`。同一批次中位于该事务块之后的独立语句继续执行。事务外的写操作各自 autocommit，彼此独立；一条写语句失败只返回自己的结构化错误，不阻止后续事务外语句执行。整体输出预算仍需冻结。

## Skill 内容

Skill 应包含：

- 协议版本与 EBNF；
- 状态机；
- 参数绑定；
- 输出 Schema；
- 错误恢复表；
- 逐层 Database/Table/Route 发现、选择和 Route Frame 预算规则；
- 上下文缓存规则；
- 禁止直接读取物理文件、猜 Schema 或强制覆盖冲突。

Skill 不是安全边界，Parser、Policy 和 MVCC 才是。

## 未决问题

- MSQL v0 首批冻结哪些 SQL 语句和 Memora 扩展？
- 多语句批次的整体输出预算是什么？
- 历史 MATCH 是否删除，或只保留为显式字面检索？
- Table 级 Route DDL、迁移和 generation 语法怎样冻结？
- 自研 Parser 还是基于现有 Go SQL Parser？

## 关联

- [MSQL Lexer v0](./msql-lexer.md)
- [MSQL Parser Core v1](./msql-parser.md)
- [MSQL Batch 与事务边界 v1](./msql-batch-transactions.md)
- [语义路由](./semantic-routing.md)
- [上下文生命周期](./context-lifecycle.md)
