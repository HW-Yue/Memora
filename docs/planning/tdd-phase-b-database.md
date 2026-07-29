# Phase B：逻辑数据库与语义检索

目标：以原型 Store 跑通可迁移的逻辑数据库闭环。SQLite 只是实现细节，所有验收针对 Memora 契约。

## F13 Data Dictionary

实现按代码规模门槛拆成三个可独立验证的原子切片：

- F13a：持久化 Database/Table Catalog，覆盖稳定 ID、重名、rename、schema version、必填语义元数据和重启读取；
- F13b：增加 Column Catalog，并验证 Column 变化向 Table/Database schema version 传播；
- F13c：补齐 Catalog DDL/发现语法并实现只依赖 Catalog 契约的 Binder。

先测：CREATE/SHOW/DESCRIBE Database、Table、Column，重名、rename、schema version、purpose/scope 缺失和重启读取。

开发：实现稳定 database/table/column ID、Catalog Binder 和自描述 Schema。

提交：`feat(F13): add self-describing catalog`

## F14 类型、约束与字段预算

先测：NULL、整数、布尔、时间、文本、关系 ID、类型错误、默认 1200 字符和可配置上限。

开发：实现逻辑类型系统、约束验证和稳定错误码；禁止截断。

提交：`feat(F14): enforce logical types and limits`

## F15 Row CRUD 与 revision

实现按代码规模门槛拆成五个原子切片：

- F15a：持久 Row、稳定 `row_id`、按 `column_id` 编码、类型校验和重启读取；
- F15b：expected revision、UPDATE、逻辑 DELETE 和并发冲突；
- F15c：命名/位置参数绑定和类型化表达式求值；
- F15d：Column/Table Binder 和有界 SELECT Planner；
- F15e：参数化 INSERT/UPDATE/DELETE Executor、影响行数预算和注入边界。

先测：参数化 CRUD、稳定 row_id、expected revision 成功/冲突、逻辑 DELETE、影响行数上限和 SQL 注入样例。

开发：实现 Binder、Planner、CRUD Executor、row_state 和 revision。

提交：`feat(F15): execute revisioned row CRUD`

## F16a Transaction-scoped Row API

先测：同一事务可读到自己的多次写入；commit 后整体可见；rollback 后全部不可见；Catalog/Row 绑定共享同一 Store snapshot。

开发：把 Row CRUD 抽成可复用的 transaction scope；autocommit Service API 复用相同原语，避免两套 revision/validation 逻辑。

提交：`feat(F16a): expose transaction-scoped row operations`

## F16b Batch 事务执行核心

先测：autocommit 独立失败继续；显式事务内写失败全回滚；读失败结构化返回；事务后独立语句继续。

开发：连接长驻 MSQL session、Store transaction 和 batch envelope，严格实现已确认语义；session close 回滚未提交事务。

提交：`feat(F16b): execute atomic MSQL batches`

## F16c 可恢复解析与 IPC Session

先测：已定位的 Parser 错误进入对应 statement result；安全恢复后续语句；同一 IPC connection 跨 request 复用事务，断连回滚。

开发：Parser 在 token 边界恢复 batch item；daemon 为每个 IPC session 持有 Batch Executor，并暴露版本化 `msql.execute` payload。

提交：`feat(F16c): connect batch execution to IPC sessions`

## F17a 追加式 History Store

先测：每次修改生成 history revision；同一事务共享 commit sequence；回滚不留历史；逻辑删除和跨重启一致。

开发：实现追加式逻辑 History API、actor/source/reason、稳定 Row snapshot 和 transaction-scoped commit sequence。

提交：`feat(F17a): append semantic row history`

## F17b History Row API

先测：AS OF revision/commit sequence、分页 History、补偿撤销和删除后恢复；补偿产生新 revision，不改写旧历史。

开发：实现 transaction-scoped History read/compensate，并按当前稳定 Column ID/Schema 投影 snapshot。

提交：`feat(F17b): query and compensate row history`

## F17c MSQL History

先测：参数化 AS OF、SHOW HISTORY 有界输出、RESTORE guard/provenance、Batch 回滚与注入文本。

开发：冻结 History AST/Grammar，把查询与补偿接入 MSQL Executor 和 Result Envelope。

提交：`feat(F17c): execute MSQL history operations`

## F18a 关系记录与双向索引

先测：关系 revision、正反向索引一致、逻辑删除、重复 relation ID 和跨重启一致。

开发：实现统一的 transaction-scoped relation Store；引擎持久化 relation type，但不解释 `contradicts` 等业务语义。

提交：`feat(F18a): store revisioned relationships`

## F18b Row 引用完整性

先测：引用不存在、Row 删除自动失效引用、跨表/跨库 Policy、循环关系和事务回滚。

开发：把 endpoint 校验、删除级联和 Policy guard 接入 Row transaction。

提交：`feat(F18b): enforce structured relationship integrity`

## F18c MSQL 关系操作

先测：关系创建、正反向读取、逻辑删除、Batch 回滚和参数注入。

开发：把声明式关系语句接入 Parser、Executor、Result Envelope 和 IPC session。

提交：`feat(F18c): execute MSQL relationship operations`

## F19a Agent 词项快照与 Posting

先测：完整词项快照、规范化去重、旧 revision 失效、24/64 预算和事务回滚。

开发：实现 transaction-scoped `term → row_id + revision` posting、反向快照和 Agent 来源标记。

提交：`feat(F19a): store agent-selected term snapshots`

## F19b Row 与 MSQL 词项接入

先测：任意字段产生的完整词项随 INSERT/UPDATE 原子替换、DELETE 失效、Batch 回滚、空快照和参数边界。

开发：把 `index_terms` mutation option 接入 Row transaction、MSQL Executor 和 IPC request。

提交：`feat(F19b): index row terms atomically`

## F20a 机械 Tokenizer 与 Posting

先测：中英混合、标点、N-gram、长文本、关闭/重建、空间预算和与 Agent posting 隔离。

开发：实现 transaction-scoped tokenizer/N-gram、机械 posting、预算标记和同 revision rebuild。

提交：`feat(F20a): store rebuildable mechanical postings`

## F20b Row 自动机械索引

先测：INSERT/UPDATE/RESTORE 自动替换、DELETE 失效、非文本字段忽略、Batch 回滚和 read-own-write。

开发：从当前 Catalog 文本 Column 按稳定顺序生成机械快照，并接入 Row transaction。

提交：`feat(F20b): maintain mechanical row index`

## F21a MATCH 融合 Planner

先测：两路独立归一化、0.8/0.2 默认权重、配置覆盖、稳定 tie-break、LIMIT 和只返回定位。

开发：实现纯 MATCH Planner、融合评分和通道解释分；不得从索引返回正文。

提交：`feat(F21a): fuse semantic and lexical candidates`

## F21b Row Source 与 MSQL MATCH

先测：真实双索引候选、原始 query 机械分词、参数化 query_terms、表范围、Result Envelope 和 Batch read failure。

开发：把一致快照 source、MATCH AST/Grammar、Executor 和 locator-only 输出接入。

提交：`feat(F21b): execute fused MSQL matches`

## F22a Router 树与 Membership

先测：多叉节点、多个叶子 membership、row_id 反向索引、rename、split/merge、删除全部引用和容量边界。

开发：实现 transaction-scoped Database Router、稳定 node ID、路径 redirect、membership 快照和遍历 cursor。

提交：`feat(F22a): build multi-level semantic router`

## F22b Row Membership 接入

先测：Row revision membership、DELETE 自动失效、结构化完整快照和 Batch 回滚。

开发：把 `route_leaf_ids` mutation option 接入 Row transaction、History 与 Batch。

提交：`feat(F22b): maintain row route memberships`

## F22c MSQL Router 管理与遍历

先测：参数化 CREATE/RENAME/DELETE ROUTE、SHOW/OPEN 有界输出、cursor、Result Envelope 和 Batch 回滚。

开发：把 Router AST/Grammar、Executor 和 locator-only 输出接入统一 MSQL/IPC。

提交：`feat(F22c): execute MSQL router operations`

## F23 索引发现流程

先测：逐层 Route、错误分支由倒排救回、关系补充候选、预算耗尽、冷库发现和最终只返回 ID。

开发：实现发现 Planner 与确定性候选融合接口，供 Agent profile 调用。

提交：`feat(F23): discover row locators across indexes`

## F24 pending_reindex

先测：普通 SQL UPDATE 立即使旧语义索引失效；机械检索可用；后台结果 revision 过期不得覆盖；重试幂等。

开发：实现 durable reindex queue、worker lease、expected revision 和失败可观测性。

提交：`feat(F24): rebuild semantic metadata asynchronously`

## F25 Generation Manifest

先测：Router/Agent/机械 generation 独立重建、manifest 原子切换、查询 pin、崩溃前后组合一致和旧读者释放后回收。

开发：实现 index manifest、旁路验证、发布和 generation GC。

提交：`feat(F25): publish independent index generations`

## F26a 逻辑快照格式与迁移

先测：未知字段保留；旧 format fixture 可迁移；非法 identity、revision 和 commit sequence 被拒绝；语义等价快照哈希一致。

开发：冻结版本化 logical snapshot 信封、v0→v1 migrator 和 canonical hash。

提交：`feat(F26a): define portable logical snapshots`

## F26b 逻辑快照 Store 往返

先测：导出再导入得到等价 Catalog/Row/history/relation；commit sequence 连续；未知字段跨 Store 保留；索引可丢弃重建。

开发：实现 Store 扫描、权威记录导出、空目标原子导入及 Row/History/Relation 定位索引重建。

提交：`feat(F26b): import portable logical snapshots`

## F27 数据库垂直链路

先测：init → DDL → 写入 → Route/MATCH → SELECT 回表 → UPDATE → history → restart 的黑盒 E2E。

开发：补齐 CLI `exec/query/doctor`、完整性检查和用户可读诊断，不新增旁路 API。

提交：`feat(F27): complete local database vertical slice`

## Phase B 退出测试

固定 seed 的一万 Row 数据集通过 CRUD、事务、检索、重建和重启测试；所有索引都能删除后重建；逻辑 snapshot 往返哈希一致。
