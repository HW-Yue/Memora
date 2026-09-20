# MSQL 标准语言

状态：协议定位已确认；F70 已实现 Table 级逐层 Router 语法，F71/F166 已完整删除旧语义
检索语法与 Database 级 Route path，F76 已实现公开原子 SPLIT/MERGE，F79 已实现
版本化查询预算配置，F129/F130 已实现 Route Mutation Plan 与审批执行，F131/F132 已实现
Schema Change Plan 与审批执行。
F173c / F174 的 `REBUILD LEXICAL INDEX` 与 `SHOW LEXICAL LOCATIONS` 已从 Grammar 删除，
词法/向量召回内核一并删除，架构待规划。F182a 增加 Route alias 的有界、revision-guarded
完整替换，并让 Route read 返回非 null alias 列表。
F195 已增加资料吸收 proposal 的结构审阅、hash-bound 提交与最小收据读取语句。

## 定位

MSQL 是 Memora 面向 Agent 的唯一正式操作语言。它参考 SQL 的成熟表达方式，以 SQL 为主体增加数据库发现、语义路由和诊断等 Memora 专有操作。目标是让所有正式操作都经过统一、容易解析的标准化语言，而不是发明多套工具协议。

MSQL 不以兼容 MySQL 为目标，不承诺 MySQL 的完整 Grammar、行为、网络协议或客户端兼容性。相同概念优先沿用熟悉的 SQL 写法；只有 Memora 独有能力才增加扩展语句。

事务、autocommit 与批处理落在 SQLite 上：写串行，读看最后一次提交。只有 Memora 独有能力或有明确产品理由时才偏离标准 SQL 写法，并在 MSQL 规格中显式记录差异。

Codex/Claude Skill、CLI 命令、外部 SDK 和未来可选的内置 Agent Loop 必须提交同一种 MSQL Request，并经过同一套 Lexer、Parser、AST、Binder、Policy、事务和执行器。CLI 只是对应 MSQL 的参数化便捷入口，不能拥有绕过 MSQL 的实现路径。自然语言由 Agent 转换为 MSQL，不属于 MSQL Grammar。

未来内置 Agent 对 Memora 的依赖只有版本化 `ExecuteMSQL` 端口。即使 Agent 与 daemon 在同一 Go
进程中，也必须提交完整 MSQL Request 并经过上述全部阶段；不能把“同进程调用”解释为直接调用
Catalog、Row、Router、Assimilation Controller、Store 或索引包。Agent 需要而 Grammar 尚未表达的
数据库能力，必须先作为独立 MSQL Feature 实现，不能为 Agent 增加私有 RPC 或 Go 后门。

F195 之后，新 Agent 使用正式 assimilation MSQL surface；Job、SourceStore、Document IR 与 coverage
是 Agent-owned 状态。早期 `assimilation.record/submit/receipt` IPC 仅保留外部兼容，新 Agent 禁止
依赖，也不得把它们包装成新的内部工具。

宿主 Agent 的每个结构化 statement input 必须携带 `memora.authorization/v2`，声明 actor 与本次允许访问的 Database 名称或稳定 ID。Policy 同时检查静态限定名、参数化 Route、关系端点和管理操作；`SHOW DATABASES` 只返回 scope 内对象。完整边界见 [Policy Enforcement v2](../archive/development/policy-enforcement-v2.md)。

## 标准进入流程

```sql
SHOW INSTANCE;
SHOW CONFIGURATION; SHOW DATABASES; -- 同一 request，不增加宿主往返
SHOW TABLES FROM project_memora COMPACT;
DESCRIBE TABLE project_memora.design_topics COMPACT;
SHOW ROUTES FROM TABLE project_memora.design_topics AT ROOT LIMIT 12;
SHOW ROUTES UNDER :route_id LIMIT 12;
DESCRIBE ROUTE :route_id; -- 仅当短 purpose 无法稳定选择时
OPEN ROUTE :leaf_id LIMIT 1;
SELECT ... WHERE row_id = :row_id LIMIT 1;
```

MSQL v0 使用 `SHOW` / `DESCRIBE` 作为 Database、Table、Route 和 Data Dictionary 的正式发现接口。第一版不要求实现 `information_schema` 查询视图；底层仍由同一套自描述 Data Dictionary 提供结果。

## 候选语句

- 发现：SHOW INSTANCE/DATABASES/TABLES；
- 描述：DESCRIBE DATABASE/TABLE；
- 路由：SHOW ROUTES、OPEN ROUTE、PLAN/APPLY ROUTE MUTATION；
- 召回：关键词 / 向量 MSQL 门与内核均已删，产品仍是四条路，架构待规划；
- 数据：SELECT、INSERT、UPDATE、DELETE、SPLIT、MERGE；
- Schema：CREATE/ALTER、PLAN SCHEMA CHANGE 与 APPLY SCHEMA CHANGE；
- 事务：BEGIN、COMMIT、ROLLBACK、SET TRANSACTION ISOLATION LEVEL；
- 历史：SHOW HISTORY、AS OF REVISION/COMMIT_SEQUENCE、RESTORE 补偿；
- 行链接：只留行上 `links` 字段；`RELATE` / `UNRELATE` / `SHOW RELATIONS` 已删，读写后面重写；
- 配置：SHOW CONFIGURATION/HISTORY、ALTER CONFIGURATION、RESTORE CONFIGURATION；

Memora 专有管理能力采用独立的声明式语句，并解析为明确的 AST 节点；不使用 `CALL memora.*(...)` 形式的通用过程调用。`PACK DATABASE`、`OPEN PACKAGE`、`INSTALL PACKAGE` 与 `EXPORT WIKI` 已从 Grammar 删除，Parser 直接拒绝。`REBUILD LEXICAL INDEX` 同样已删。

F174 的 `SHOW LEXICAL LOCATIONS` 与 F173c 的 posting 重建**已从 Grammar 和内核删除**。新召回语法见 [查询形态](../product/query-model.md)。

F195 冻结资料吸收提交面：

```sql
REVIEW ASSIMILATION FOR DATABASE work USING :proposal;
SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work;
SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work;
```

REVIEW 逐条解析 proposal 中的 MSQL，只接受同库 L1 数据 mutation，并检查完整 coverage、参数、
Schema/revision/affected-row guard 和 document source provenance；结果是规范 hash-bound plan。
SUBMIT 要求同库 L1 scope 和 `SUBMIT_ASSIMILATION` 精确 approval，在独立 Session 中执行
`BEGIN → statements → COMMIT`。Receipt 不保存 MSQL、参数或正文。结构审阅不等于事实正确性；F196 已增加有锚点的
claim ledger 与候选语句，F197–F199 继续增加问题交互、独立语义复核与回读对账。完整契约见
[F195 规格](../archive/planning/f195-msql-assimilation-surface.md)和 [F196 规格](../archive/planning/f196-draft-claim-ledger.md)。

语义发现不把自然语言交给评分器。AI 先读取 Database/Table 的用途，再逐层读取
所选 Table 的短 Route 节点，直到 Leaf 得到唯一 RowID。一个 Leaf 最多一个活跃 Row，
同一 Row 可以属于多个 Leaf。aliases、旧名称和关系是可读
数据库内容，由 AI 在判断或明确 SQL filter 中使用，不进入隐藏相似度融合。
`SHOW ROUTES` 默认只返回短 purpose；可选的 0–1000 字符 synopsis 只通过
`DESCRIBE ROUTE` 按需读取，并用 revision-guarded
`ALTER ROUTE :route SET SYNOPSIS :synopsis` 更新。Route alias 使用完整替换，避免增量命令在重试时
产生不清楚的继承状态：

```sql
ALTER ROUTE :route SET ALIASES :aliases;
```

`:aliases` 是参数绑定的 TEXT 数组，`[]` 清空；最多 8 项、单项 1–64 个 Unicode 字符、合计最多
512 UTF-8 bytes，去除首尾空白后不得与 name 或其他 alias 大小写不敏感地重复。成功写入一个新
Route revision，并和 alias lexical posting、Change 原子发布。

AI 已明确语义边界后，使用公开 reshape 语句，而不是把普通 UPDATE/INSERT/DELETE
拼成伪原子操作：

```sql
SPLIT work.notes ROW :source
  INTO (title, body) VALUES (:first_title, :first_body), (:second_title, :second_body);
MERGE work.notes ROWS (:first, :second)
  INTO (title, body) VALUES (:merged_title, :merged_body);
```

mutation options 同时提交来源 revision、每个目标的完整 Route snapshot、需要更新的
上层 Route purpose；SPLIT 若来源存在关系，还必须用一基
`relation_target_ordinals` 明确每条关系归属哪个目标。引擎不猜拆分边界，只在一个
原生事务里发布 superseded 来源、新目标、History、关系、上层 Route revision 和
memberships。

Route 树自身需要局部 split/merge/move 时，AI 先显式给出语义命名和完整分组，
再由只读 MSQL 生成可审阅计划：

```sql
PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal;
```

`:proposal` 通过参数绑定传入 `memora.route-mutation-proposal/v1`；结果返回
`memora.route-mutation-plan/v1`、base snapshot hash 和 plan hash。F129 不提供执行入口，
也不从 Row 正文、向量或字面索引猜分组。用户批准原计划 hash 后，F130 使用：

```sql
APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes;
```

引擎在单个 authority write window 重验全部 guard，并在一个 native transaction 发布
Route、membership 与 Change Log。完整契约见 [Route Mutation Plan v1](./route-mutation-plan-v1.md)
和 [Route Mutation Execution v1](./route-mutation-execution-v1.md)。

现有 Table 的 Column/constraint 演化先生成只读计划：

```sql
PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal;
APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes;
```

F131 对完整 Column Schema 建 guard，并仅在约束可能影响既有值时做有界 Row 兼容性
扫描；`blocked` 或截断不能执行。完整契约见
[Schema Change Plan v1](./schema-change-plan-v1.md)。
APPLY 只接受 unchanged `review_required` plan 与 `APPLY_SCHEMA_CHANGE` hash-bound approval，
在 native authority 中重验 Catalog/必要 Row guard 后原子发布；详见
[Schema Change Execution v1](./schema-change-execution-v1.md)。

查询预算通过同一 MSQL 发现和修改，不允许宿主直接改进程变量：

```sql
SHOW CONFIGURATION;
SHOW CONFIGURATION HISTORY LIMIT :limit;
ALTER CONFIGURATION QUERY_BUDGETS SET
  ROUTE_CHILDREN :routes, OPEN_LOCATORS :locators, SELECT_SCAN :scan,
  SELECT_ROWS :rows, ROUTE_FRAME_NODES :frame;
RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION :revision;
```

ALTER/RESTORE 的结构化 mutation options 必须包含 expected revision、actor 和
reason。ALTER 是完整替换，避免遗漏字段继承了哪个旧值不清楚；RESTORE 追加补偿
revision。首批配置只控制查询/上下文预算，不覆盖权限、事务或格式安全上限。

## 强制规则

- 实际数据只能通过 SQL 查询；
- Route 只返回导航元数据；
- Route 叶子只返回数据项定位，主 Agent 必须再用 SELECT 回表；
- 所有 CLI 管理操作必须映射为 MSQL，不能直接调用旁路引擎接口；
- 内置 Agent 只能依赖版本化 MSQL 请求/结果信封，不得 import 或调用引擎领域包；
- 长文本使用参数绑定；
- 查询必须有结果和输出预算；
- 更新应带 expected revision；
- Row 必须能按稳定 `row_id` 使用 SELECT、UPDATE 和 DELETE 精确操作；
- Row、物理索引和 Router membership 的变更必须在同一事务中原子可见或显式标记待重建；
- Parser/AST 验证完整 SQL，正则不负责语法正确性；
- 响应使用稳定 JSON envelope 和错误码。

F15 已把 `expected_schema_version`、`expected_revision` 和 `max_affected_rows` 冻结为 MSQL request 的结构化 mutation options，而不是拼进 SQL 文本。语法、预算和精确 mutation 边界见 [MSQL Mutation Executor v1](./msql-mutation.md)。

F18 的 `RELATE` / `SHOW RELATIONS` / `UNRELATE` **已从 Grammar 删除**。行上仍有 `links`
字段，读写后面重写。旧协议见 [MSQL Relationships v1](../archive/query/msql-relationships.md)。

F21 的 `MATCH database.table QUERY ... TERMS ...` 是已撤销并删除的历史语法，
Parser、Policy 和只读 Host 均拒绝它。F22 已实现参数化 Router 管理与遍历，但 root 仍是
Database；历史迁移背景见 [Router Tree v1](../archive/design/router-tree-v1.md)。

文本值超过目标 Column 当前配置的字符上限时，INSERT、UPDATE、MERGE 等写入返回稳定的字段超限错误；文本 Column 启动默认上限为 1200 个字符。引擎不自动截断，调用方可以切分后重试，也可以通过声明式 DDL 调整该 Column 的类型或上限；所有变更都经过 Policy 和 revision 校验。

普通 SQL 负责业务 Row 修改；Agent 生成的完整 Route membership 也必须由
声明式 MSQL 语句或 UPDATE 扩展正式提交，不能通过私有 API 旁路写索引。具体
Grammar 待冻结。逻辑 DELETE 默认保留 revision 和 History Store；不可恢复的
PURGE 是独立高风险语句。

普通 UPDATE 未提供 Route snapshot 时保留现有 membership，并将 locator revision
与 Row revision 原子推进；提供 snapshot 时则以显式完整集合为准。需要改变语义
边界时必须提供新 snapshot，不能让引擎根据正文自动猜测。

Router 的局部语义重构继续使用 Route Mutation Plan。召回内核已删，没有
lexical generation 重建语句。

## 统一响应

所有语句使用 [MSQL Result Envelope v1](./result-envelope.md)。`SELECT`、`SHOW`、`DESCRIBE`、写入和管理语句只改变 statement result 的字段取值，不各自定义顶层结构。单语句也进入 `results[]`；错误、warning、截断、batch 顺序和未知字段兼容规则已经冻结。

F124b / F124d 的 `SHOW ROUTE CANDIDATES … USING LEXICAL|VECTOR` **已从 Grammar 删除**。
词法/向量召回内核一并删除，召回面按四条路重写前须先规划架构。

## 多语句请求

MSQL v0 必须允许一次 request 携带由分号分隔的多条语句，使 Agent 能在一次往返中完成一组发现或查询。Parser 解析完整 statement list，不能用字符串切分代替语法解析。

批次返回一个统一的 batch envelope，并按输入顺序包含每条语句各自的标准结果 envelope。多语句 request 本身不自动形成事务；事务采用显式边界：`BEGIN` 或 `START TRANSACTION` 开启，`COMMIT` 提交，`ROLLBACK` 回滚。事务边界外的语句按 autocommit 执行。

一个 request 可以包含完整事务，也可以在长驻会话中跨 request 保持事务状态。短生命周期 CLI 不得在进程退出后保留未完成事务。

隔离遵循 SQLite 与 [ADR-0011](../decisions/0011-pure-storage-engine-tables-everything.md)：写事务串行，读只看最后一次提交。没有 Memora 自研 MVCC、快照或 `FOR SHARE` / `FOR UPDATE`。分页读可能看到页与页之间发生的提交。

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

Skill 不是安全边界，Parser、Policy 和 SQLite 事务才是。

## 后续问题

- Table 级 Route DDL 与迁移语法怎样冻结？

## 关联

- [MSQL Lexer v0](./msql-lexer.md)
- [MSQL Parser Core v1](./msql-parser.md)
- [MSQL Batch 与事务边界 v1](./msql-batch-transactions.md)
- [语义路由](./semantic-routing.md)
- [上下文生命周期](../archive/query/context-lifecycle.md)
