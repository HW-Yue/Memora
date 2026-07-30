# 已形成的方向

更新时间：2026-07-29。这里记录方向性结论，不代表实现已验证。

1. Memora 是 AI 自主建模和维护的个人数据库；
2. AI 决定业务数据库、表、字段和语义关系；
3. 引擎负责 Page、索引、事务、MVCC、Undo Log、Redo Log 和恢复；
4. 所有正式操作通过版本化标准语言 MSQL；
5. Agent 先发现数据库、Schema 和 Router，再用 SQL 取数；
6. Route 只导航，实际数据只能通过 SQL 查询；
7. 语义记录是短小完整的知识模块，目标约 800 字；文本 Column 各自声明可演化的字符上限，启动默认值为 1200，超限必须报错并由 Agent 切分或调整 Schema，禁止静默截断；
8. 不保存完整大文档、图片和机械 chunk；资料仅临时读取并吸收；
9. 第一版不依赖向量 API；
10. 检索结合语义 Router、倒排索引和关系图；
11. 修改带 revision，保留历史并处理并发冲突；
12. 动态索引不进入长期 system prompt；
13. 数据库查询采用两阶段链路：索引发现步骤逐层导航并融合倒排和关系信号，只返回候选数据项定位；主 Agent 再按定位生成 MSQL `SELECT` 读取真实数据；
14. 可导出为带 `[[Wikilink]]` 的 Obsidian Wiki；
15. Wiki 是单向派生快照，不是第一阶段的真相源；
16. 设计文档按主题拆分，归档不作为日常上下文；
17. 外部宿主不需要支持 sub-agent；v0 由 Codex/Claude Code 按 Canonical Skill 通过 `memora exec` 执行 MSQL；
18. 默认采用一个本地 Memora Instance、多逻辑 Database、每库多 Table 的层级；
19. 数据文件中的 Data、B+ Tree、倒排和系统 Page 由 daemon 的 Buffer Pool 缓存；Agent 上一次查询访问的 Page 因最近使用自然保持热状态，不建立 Agent 专用的语义优先复用通道；
20. 缓存是可丢弃的性能状态，必须通过版本校验失效，不能成为事实来源；
21. 存储术语与 MySQL/InnoDB 对齐；相同概念使用相同名称，AI-native 独有概念保留 Memora 名称；
22. 核心引擎和 CLI 使用 Go，目标是本地单可执行文件、低安装成本和方便 Agent 调用；
23. CLI + Skill 是第一阶段标准接入面，MSQL 是 CLI 内唯一正式数据操作语言；
24. Table 不与物理 Data File 强绑定，查询按 Page 定位读取，不把整个文件载入内存；
25. 一个可导出的语义 Row 对应一个 Markdown 页面，内部关系、索引和 Undo Log 不单独导出；
26. Data Dictionary 必须自描述用途、边界、Row 语义、别名和版本，使陌生 Agent 无需旧聊天即可接管；
27. 写入交给 Canonical Skill 规定的 write 流程，由宿主 Agent 先查重再选择 ignore/insert/revise/merge/split，并返回短收据；
28. `Memora` 确定作为公开品牌，不再仅作为内部代号；
29. 技术标识统一优先使用 `memora`，不可用时统一使用 `memoradb`，不在不同平台临时采用不同后缀。
30. v0 不内置模型 Provider，也不要求用户向 Memora 配置 API Key；自带 Agent Runtime 只在 Skill-first 体验验证后按独立需求评估；
31. 自然语言由 Codex/Claude Code 按 Skill 转换为 MSQL；所有 CLI/SDK 调用统一经过同一 Parser、Policy、事务和执行器；
32. Agent 不能绕过 MSQL 操作物理存储；查询、写入和 Schema 变更由 Skill 流程区分，并由引擎 Policy 强制。
33. Memora 使用本地常驻 daemon 长期承载 Instance、执行引擎和 Buffer Pool；CLI、`--stdio` bridge、SDK 与可选 MCP adapter 都连接同一个 daemon。
34. 一个逻辑 Database 是可独立分发的产品对象：可导出为自描述包，在另一台机器校验后一键安装，也可由 CLI 以默认只读方式直接打开问答；包格式和命令名仍待原型确定。
35. v0 的 Instance 级全局记忆入口由宿主 Agent 按 Skill 调用 MSQL，先跨 Database 路由再执行有界查询；“全局”不取消 Database 的项目、隐私、导出和删除边界。
36. 数据库包只承载声明式数据和元数据，不携带可执行安装逻辑、模型密钥、宿主聊天或运行时缓存；未信任包不能借文本获得 prompt 或工具权限。
37. MSQL 参考 SQL 标准和 MySQL 的成熟语法，但不以兼容 MySQL 的完整 Grammar、行为、协议或客户端为目标；Memora 的正式操作统一通过这套标准化语言进入 Parser 和执行链。
38. `pack`、`install`、`open`、`export`、`doctor` 等 CLI 管理命令也必须映射为 MSQL；CLI 只是参数化便捷入口，不能拥有绕过 Parser、Policy 和执行器的旁路实现。
39. Memora 专有管理操作采用 `PACK DATABASE ...`、`INSTALL PACKAGE ...` 一类独立声明式语句和专用 AST 节点，不采用 `CALL memora.*(...)` 通用过程调用形式。
40. MSQL v0 使用 `SHOW` / `DESCRIBE` 作为 Agent 的正式元数据发现接口，首版不要求提供 `information_schema` 查询视图；结果由统一的自描述 Data Dictionary 生成。
41. 所有 MSQL 语句统一返回一种稳定 JSON envelope；查询、发现、写入和管理语句不各自定义顶层响应结构，错误使用同一 envelope 和机器可读错误码。
42. MSQL v0 必须支持一次 request 携带多条语句；Parser 解析完整 statement list，批次按输入顺序返回每条语句的标准结果，不能把协议限制为一次请求一条语句。
43. 多语句 request 不自动形成事务；事务使用 `BEGIN` / `START TRANSACTION`、`COMMIT`、`ROLLBACK` 明确边界，边界外按 autocommit 执行，语义参考 MySQL 的显式事务模式。
44. 读操作失败只返回错误；显式事务中的任一写操作失败会立即回滚整个事务，事务外写操作失败只影响当前 autocommit 语句。
45. 纯读多语句批次中单条查询失败后，其他独立查询继续执行；每条语句都返回结构化结果，失败项明确包含语句位置、对应语句、错误码和原因。
46. 没有显式事务时，每条写语句都是独立 autocommit 单元；一条失败不阻止批次中的其他独立语句继续执行，每条语句分别返回成功或结构化错误。
47. 常规 SQL、事务、autocommit 和批处理行为默认参考 MySQL；只有 Memora 独有能力或明确产品理由才偏离，并在 MSQL 规格中记录差异。
48. 显式事务因写失败自动回滚后，事务块内剩余语句和 `COMMIT` 标记为未执行或已回滚，但同一批次中事务块之后的独立语句继续执行。
49. 已知 Database 和 Table 后，全文与语义候选检索采用 `MATCH(...) AGAINST(...)`；该语法由 Memora Planner 编译为自有倒排索引查询计划，不依赖或复用 MySQL 内核。
50. 语义倒排索引以数据项为粒度：Agent 从一条 Row 的任意字段挑选有发现价值的词并输出结构化词项集合，引擎将其维护为 `term → row_id + revision` posting；Agent 不直接操作物理索引结构。
51. 第一版采用混合倒排索引：Agent 词项负责语义精度，机械分词/N-gram 作为可关闭、可删除并重建的低权重字面召回兜底；两路来源分别计分，融合权重可配置并通过 benchmark 校准。
52. Agent 词项与机械词项的融合权重以 Database 为单位持久化，配置带 revision、可审计并随数据库包迁移；若生命周期策略允许 AI 调整，也只能通过声明式 MSQL，不提供旁路调权接口。
53. 新建 Database 的混合倒排启动权重为 Agent `0.8`、机械 `0.2`；查询时两路得分先各自归一化再融合，该权重是可演化配置，具体归一化方法和后续调权由 benchmark 验证。
54. Agent 为每条 Row 输出去重后的 `index_terms: string[]`；词项不带逐词权重或来源 Column，`row_id`、`revision` 和 posting 来源由引擎自动关联。
55. Row 每次产生新 revision 时，Agent 重新输出完整 `index_terms` 快照；引擎在同一事务中原子替换上一 revision 的全部 Agent posting，不使用词项增删 diff。
56. Query Agent 为语义检索输出去重后的 `query_terms: string[]`，查询 Agent 词项通道；引擎从原始问题生成机械词项并查询机械通道，两路按目标 Database 的 Search Weight Profile 融合。
57. Query Skill 规定 `query_terms` 的生成规则，允许补充同义词、旧名称、缩写和跨语言别名；Runtime 校验结构与预算，Data Dictionary 提供已知 alias，MSQL 承载正式操作，不能只依靠 Skill 自律。
58. `query_terms` 启动预算为 12 个、启动 Policy 上限为 32 个；两者是 Database 级可演化配置，当前超限查询返回结构化错误。
59. 每条 Row 的 `index_terms` 启动预算为 24 个、启动 Policy 上限为 64 个；两者是 Database 级可演化配置，Agent 词项保持高价值，不允许用机械拆词填满语义通道。
60. 字符长度限制属于 Column 约束，不设置统一的 Row 级 1200 字符上限；文本 Column 启动默认上限为 1200 个字符，可由用户或 AI 通过 MSQL 演化，超限写入返回结构化错误且不截断。
61. 影响语义质量的数值必须成为数据库内可读、可版本化、可审计的配置，而不是不可见的永久代码常量；但“配置入库”不等于“建库后可修改”，冻结、迁移、用户可调和 AI 可优化的分类与条件推迟到最后阶段讨论。
62. 索引发现结果不能包含业务正文或直接作为答案；Route、Agent 词项、机械词项和关系只负责候选与评分，最终数据必须由主 Agent 按返回定位通过 SQL 回表读取。
63. Buffer Pool 整体参考 MySQL/InnoDB：所有 Database 共用 Instance 级池，以 Page/Frame 缓存数据、B+ Tree、倒排 posting 和系统页；使用 young/old midpoint LRU、后台 Page Cleaner、自适应刷脏，并可保存热 Page 标识供重启后台预热。并发分片不作为 Database 隔离，Query Workspace 与物理缓存严格分离。
64. 对 MySQL/InnoDB 已有成熟实现且不与 Memora 的 AI-native 边界冲突的常规数据库机制，不逐项重新发明；讨论时先用一个范围明确的是非题确认是否参考 MySQL，再只讨论 Memora 必须偏离的部分。
65. 第一版持久化协议纳入独立的 Row-based Binlog：Redo 负责本机崩溃恢复，Binlog 按稳定逻辑 ID 保存已提交事务造成的 Row 变化，不在远端重新执行原始 MSQL 或 Agent 决策；它为未来多设备增量同步、订阅和时间点恢复服务，但设备身份、传输、幂等、环路和冲突解决另行设计。
66. 每次事务必须有本地 transaction ID；MVCC 另外使用 commit sequence 判断可见性。Binlog 的 global transaction ID 跨设备保留原始来源与序号，不能与本地 transaction ID、commit sequence、Row revision 或 Redo LSN 共用。
67. 事务隔离参考 MySQL/InnoDB：默认 `REPEATABLE READ`，首版支持 `READ COMMITTED`；一致性读、`FOR SHARE` / `FOR UPDATE` 锁定读以及 gap/next-key lock 的防幻读边界按 InnoDB 语义设计。
68. Redo Log 与 Binlog 的原子一致性参考 MySQL 内部两阶段提交：Redo prepare、Binlog 持久化、Redo commit；并用 Group Commit 合并多个事务的日志刷盘，同时保持 commit sequence 与 Binlog 顺序一致。它不是跨设备分布式事务。
69. 物理 MVCC 参考 InnoDB，采用“最新 Record + Undo version chain”：更新前写 Undo，旧快照沿 roll pointer 重建，安全后由 Purge 回收。长期语义 revision 进入独立 History Store，不能依赖事务 Undo。
70. Router 正式定义为 Agent 语义目录索引：由多层多叉树组成，内部节点提供短语义分支，叶子节点保存有限的数据项 ID/locator；同一 `row_id` 可被多个叶子引用但不复制 Row。索引发现 Sub-agent 逐层查到候选 ID，再与 Agent 词项、机械词项和关系候选融合评分，主 Agent 最终按 ID 用 SQL 回表。
71. 稳定 `row_id` 是 Row 的永久逻辑身份；正文、Schema、Router 归属和索引重建都不能改变它。Row 必须能通过 MSQL/SQL 精确 SELECT、UPDATE 和逻辑 DELETE；修改时当前 Record、物理索引、机械 posting、Agent 词项、Router 引用、历史和 Binlog 原子更新或显式进入待重建状态，禁止留下静默陈旧索引。
72. 普通 SQL UPDATE 不因缺少新 Agent 索引而失败：旧 Agent posting 和所有 Router membership 立即 tombstone，新 revision 标记 `pending_reindex`，机械索引立即可用，daemon 后台按 expected revision 重建。Router/倒排支持新 generation 旁路构建、校验后原子切换和旧 generation compaction；`row_id → memberships` 反向索引保证 DELETE/SPLIT/MERGE 能失效全部叶子引用。
73. Router 维护分为 Row 增量、局部子树重建和 Database generation 重建；少量变更不得触发全量。整库触发由 Database 配置中的最小脏 Row 数与全局脏比例共同判断，并可因索引规则/格式不兼容升级或完整性失败强制触发；阈值不写死，启动值由 benchmark 决定。
74. 第一阶段只开发和支持 macOS，不同时承担 Linux/Windows 的目录、服务管理和兼容测试成本；跨平台支持以后按独立里程碑进入。
75. Instance 磁盘目录层次参考 MySQL：一个 Instance 根数据目录，Redo、Undo、Binlog 等事务日志集中管理，各逻辑 Database 使用独立子目录；具体文件格式仍为 Memora 自有格式，不追求兼容 MySQL 文件。
76. macOS 用户级默认 datadir 为 `~/Library/Application Support/Memora/instances/default/`，通过系统用户目录 API 解析；可重建缓存和日志分别进入 `~/Library/Caches/Memora/`、`~/Library/Logs/Memora/`，数据库文件不得放入 Caches。允许显式绝对路径覆盖，但默认不使用项目目录、同步盘或需要 sudo 的 `/usr/local`。
77. Instance datadir 顶层固定为 `instance.meta`、`system/`、`databases/`、`redo/`、`undo/`、`binlog/` 和 `tmp/`：系统数据、逻辑库和三类日志分离；只有 `tmp/` 可作为未完成后台工作的可丢弃中间状态，其他目录不得按缓存清理。
78. `databases/` 下每个逻辑库的物理子目录使用不可变 `database_id`，可读名称只由 Data Dictionary 映射；rename、同名库、包安装和设备同步都不能依赖或触发目录改名，ID 的具体编码仍待确定。
79. 每个 Database 子目录固定分为 `data/`、`history/` 与 `indexes/`：前两者保存当前权威 Row/系统记录和长期语义 revision，不得被 REINDEX 删除；`indexes/router/` 与 `indexes/inverted/` 保存可丢弃 generation，必须能从权威数据和索引规则重建。
80. User Table 参考 MySQL file-per-table 思路使用独立 Tablespace，物理目录按不可变 `table_id` 命名；Tablespace 保存当前 Row、聚簇索引和普通二级 B+ Tree，并可滚动多个 Data File。Table rename 不移动目录，跨表事务继续使用 Instance 级日志。
81. 长期语义 revision 使用 Database 级共享的追加式 History Store，按 commit sequence 滚动 segment，并以 `table_id + row_id + revision` 定位；Row 跨表移动、拆分、合并或 Table 改名不搬迁旧历史。History 是权威数据，不采用 Undo 的 Purge 生命周期。
82. Router、Agent 倒排和机械倒排各自维护独立 generation；Database 的 `indexes/manifest` 原子记录当前启用组合及覆盖 commit sequence，查询开始时固定一次。单类索引重建不复制其他索引，发布前旁路写入和校验，旧 generation 等读者释放后回收。
83. 数据库不内置 `candidate/disputed` 等语义冲突状态，也不理解、裁决或自动合并互相矛盾的内容。引擎只检测 revision、锁、唯一键、外键、类型等机械冲突并结构化报错；Skill 负责查询并向用户并列展示语义冲突，得到用户指示后重新生成 SQL 写入。
84. AI-native 的产品验收以“AI 持续维护、用户只处理例外”为准，而不是以是否内置 LLM、Vector 或 SQL 扩展为准。用户提供自然对话或资料后，AI 自主发现已有数据并完成忽略、写入、修订、拆分、合并、Schema/Router 维护和验证；语义冲突、高风险、越权与不可恢复操作才请求用户介入。自动维护只能覆盖已授权且实际交给 Memora 的输入，因此必须建设稳定输入入口，不能假设 Skill 能看见所有宿主活动。
85. Wiki v1 以 `database_id/table_id/row_id.md` 作为稳定路径；rename 不移动页面，跨库关系使用 Vault 根目录下的完整稳定相对 Wikilink。增量 manifest 只拥有自己登记的页面，不删除用户新增文件；v1 不生成 slug、redirect、Router/MOC，也不回流 Markdown 编辑。

## 尚需验证

上述结论需要最小端到端原型验证：

```text
自然对话
→ AI 建库/建表
→ Router 导航
→ SQL 查询
→ revision 更新
→ 新 Agent 接管
→ Wiki 导出
```
