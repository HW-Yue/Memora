# 已形成的方向

更新时间：2026-07-31。这里记录方向性结论，不代表实现已验证。

1. Memora 是 AI 自主建模和维护的个人数据库；
2. AI 决定业务数据库、表、字段和语义关系；
3. 引擎负责 Page、索引、事务、MVCC、Undo Log、Redo Log 和恢复；
4. 所有正式操作通过版本化标准语言 MSQL；
5. Agent 先发现数据库、Schema 和 Router，再用 SQL 取数；
6. Route 只导航，实际数据只能通过 SQL 查询；
7. 语义记录是短小完整的知识模块，目标约 800 字；文本 Column 各自声明可演化的字符上限，启动默认值为 1200，超限必须报错并由 Agent 切分或调整 Schema，禁止静默截断；
8. 不保存完整大文档、图片和机械 chunk；资料仅临时读取并吸收；
9. [已被第 112–113 项与 ADR-0007 取代] 曾禁止所有 Embedding、向量与相似度路径；
10. 检索主路径是 AI 对 Table 级语义 Router 的逐层 SQL 导航；关系只在回表后按明确需要扩展，不参与隐藏的相似度融合；
11. 修改带 revision，保留历史并处理并发冲突；
12. 动态索引不进入长期 system prompt；
13. 数据库查询采用两阶段链路：AI 从 Database、Table、Schema 和顶层 Route
逐层导航，叶子只返回候选 RowID；主 Agent 再按 RowID 生成 MSQL `SELECT`
读取真实数据；
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
49. F19–F23 的 `MATCH`、Agent 词项、机械 N-gram 和固定权重融合属于历史原型，
不再是语义查询主路径，也不能作为产品完成证据。
50. 语义发现的权威派生索引改为 Table 级 Router Tree；内部节点保存可读范围，
叶子保存 `row_id + revision` locator，Agent 不操作物理索引结构。
51. 禁止设置或调优“Agent/机械/Vector”语义相似度融合权重。
52. 允许保留传统数据库所需的精确键、唯一键、范围和显式字面查找索引，但它们
不能伪装成语义匹配，也不能替代 AI 逐层 Route 导航。
53. Router fan-out、叶子容量、输出长度和 Route Frame 属于可读、可版本化配置；
它们控制预算，不对自然语言计算相似度。
54. Row 写入必须提交完整 Route membership 快照或进入明确的待语义维护状态。
55. Row 每次产生新 revision 时，旧 Route membership 立即失效；新 membership
通过 expected revision 原子启用。
56. Query Agent 不生成 `query_terms` 交给评分器；它读取短节点描述并显式选择
下一层 Route。
57. aliases 和旧名称属于 Data Dictionary、Schema、Row 或 Route 的可读数据，
由 AI 在导航和 SQL filter 中使用。
58. 查询预算按每层节点数、最大深度、叶子 locator 数和回表 Row 数表达。
59. 既有倒排代码、配置和文档必须在架构对账中决定删除、降级为显式字面能力或
迁移；未完成对账前不得继续扩展。
60. 字符长度限制属于 Column 约束，不设置统一的 Row 级 1200 字符上限；文本 Column 启动默认上限为 1200 个字符，可由用户或 AI 通过 MSQL 演化，超限写入返回结构化错误且不截断。
61. 影响语义质量的数值必须成为数据库内可读、可版本化、可审计的配置，而不是不可见的永久代码常量；但“配置入库”不等于“建库后可修改”，冻结、迁移、用户可调和 AI 可优化的分类与条件推迟到最后阶段讨论。
62. 索引发现结果不能包含业务正文或直接作为答案；Route 只负责逐层导航并在
叶子返回 RowID，最终数据必须由主 Agent 按定位通过 SQL 回表读取。
63. F52–F80 阶段只建立 stable ID/offset、Route child 和 membership 的有界
内存目录，不实现物理 Page Buffer Pool。该阶段已结束；B+ Tree 后续边界由 109
取代。AI Route Frame 与任何物理缓存始终分离。
64. 对 MySQL/InnoDB 已有成熟实现且不与 Memora 的 AI-native 边界冲突的常规数据库机制，不逐项重新发明；讨论时先用一个范围明确的是非题确认是否参考 MySQL，再只讨论 Memora 必须偏离的部分。
65. F52 原生 bootstrap 只实现单 Record append、close/reopen 和按稳定 ID Get；
不建立事务帧、COMMIT、fsync durability 或崩溃恢复承诺。
66. transaction ID、commit sequence、GTID 和 LSN 都在真实 Record 与 MSQL
闭环通过后再逐项加入，不能预埋未验证语义。
67. F52 只支持单进程、单 writer；并发 reader、snapshot、SQL isolation、锁和
死锁检测以后按真实并发需求设计。
68. 半条 Record、CRC 损坏和未知版本在 F52 只返回稳定错误，不截断、不修复、
不声称恢复；F56 才定义原子提交与恢复。
69. History 在 Catalog/Row 与 MSQL 闭环之后作为独立 typed record 接入；物理
MVCC/Undo/Purge 继续后置。
70. Router 正式定义为 Agent 语义目录索引：每个 Table 拥有一棵多层多叉树，
内部节点提供短语义分支，叶子节点保存有限的数据项 ID/locator；同一 `row_id`
可被多个叶子引用但不复制 Row。AI 逐层查到候选 ID，最终按 ID 用 SQL 回表，
不经过隐藏候选融合评分。
71. 稳定 `row_id` 是 Row 的永久逻辑身份；正文、Schema、Router 归属和物理格式
变化都不能改变它。Row 必须能通过 MSQL 精确 SELECT、UPDATE 和逻辑 DELETE；
当前 revision、History、关系和 Router membership 在同一逻辑事务中更新或显式
进入待维护状态，禁止静默陈旧引用。
72. 普通 SQL UPDATE 不因缺少新语义 membership 而留下错误可见索引：所有旧
membership 立即 tombstone，新 revision 标记 `pending_reindex`；宿主 AI 后续按
expected revision 重建。Router 支持新 generation 旁路构建、校验后原子切换和
旧 generation compaction；`row_id → memberships` 反向索引保证
DELETE/SPLIT/MERGE 能失效全部叶子引用。
73. Router 维护分为 Row membership 增量、局部子树调整和 Table 级重建；少量
变化不得触发整表。独立 generation 与 compaction 只有数据证明需要时再实现。
74. 第一阶段只开发和支持 macOS，不同时承担 Linux/Windows 的目录、服务管理和兼容测试成本；跨平台支持以后按独立里程碑进入。
75. 下一阶段优先实现 Memora 原生极简 Store，不再以“先完整复刻 MySQL/InnoDB
物理栈”为顺序；先冻结文件、事务帧、typed logical record、校验和恢复。
76. macOS 用户级默认 datadir 为 `~/Library/Application Support/Memora/instances/default/`，通过系统用户目录 API 解析；可重建缓存和日志分别进入 `~/Library/Caches/Memora/`、`~/Library/Logs/Memora/`，数据库文件不得放入 Caches。允许显式绝对路径覆盖，但默认不使用项目目录、同步盘或需要 sudo 的 `/usr/local`。
77. 极简 datadir 目标只包含 `instance.meta`、`system/system.memora`、每库
`databases/db_<id>/database.memora` 与 `tmp/`；首版不创建 redo/undo/binlog、
Tablespace 或独立索引目录。
78. `databases/` 下每个逻辑库的物理子目录使用不可变 `database_id`，可读名称只由 Data Dictionary 映射；rename、同名库、包安装和设备同步都不能依赖或触发目录改名，ID 的具体编码仍待确定。
79. `database.memora` 从单 Record Frame 起步：F52 只存测试 Record，F53 才存
真实 Catalog/Row，F55 再逐项加入 History、Relation 和 Table Route。逻辑类型
不能退化为 SQLite schema、Go struct dump 或无语义 bucket 文件格式。
80. F52–F80 阶段不做 per-table Tablespace、B+ Tree、固定 Page 或物理 Buffer
Pool，打开文件时重建有界内存定位目录。该历史阶段边界已由 109 取代。
81. 长期语义 History 接入后是权威 record，未来 compaction 不得丢弃；F52–F54
不以尚未实现的 History、事务或 Undo 作为通过条件。
82. F52–F80 阶段将 Page、B+ Tree、checkpoint、Buffer Pool、MVCC、Undo/Redo、
Binlog 和独立 generation 后置。B+ Tree 与最小 MVCC 部分现由 102–109 取代。
83. 数据库不内置 `candidate/disputed` 等语义冲突状态，也不理解、裁决或自动合并互相矛盾的内容。引擎只检测 revision、锁、唯一键、外键、类型等机械冲突并结构化报错；Skill 负责查询并向用户并列展示语义冲突，得到用户指示后重新生成 SQL 写入。
84. AI-native 的产品验收以“AI 持续维护、用户只处理例外”为准。用户提供自然
对话或资料后，AI 自主发现已有数据并完成忽略、写入、修订、拆分、合并、
Schema/Router 维护和验证；语义冲突、高风险、越权与不可恢复操作才请求用户
介入。自动维护只能覆盖已授权且实际交给 Memora 的输入，因此必须建设稳定输入
入口，不能假设 Skill 能看见所有宿主活动。
85. Wiki v1 以 `database_id/table_id/row_id.md` 作为稳定路径；rename 不移动页面，跨库关系使用 Vault 根目录下的完整稳定相对 Wikilink。增量 manifest 只拥有自己登记的页面，不删除用户新增文件；v1 不生成 slug、redirect、Router/MOC，也不回流 Markdown 编辑。
86. v0 的最终信任边界是当前 macOS 用户；宿主 Agent 的结构化 MSQL input 必须用 `memora.authorization/v1` 声明 actor 和 Database scope。Policy 在静态 SQL、动态 Route、关系、Pack 与 Wiki 上强制该 scope；Install 另需绑定 package SHA-256 的显式 approval。daemon 审计只保存有界元数据与 payload hash，并与逻辑 Database snapshot/package 分离。
87. Memora 采用 PolyForm Noncommercial 1.0.0 与独立付费商业许可的双许可证模式：个人及其他非商业用途可免费使用、修改与依许可分发；任何商业用途必须事先取得版权所有者另行签署的书面付费许可。项目属于 source-available，不宣称为 OSI Open Source。
88. GitHub Release 只由已验证签名的 annotated 稳定 SemVer tag 触发；tag target 必须位于 `main`，重复 Release 必须失败。测试、构建和双架构 smoke 只读，只有其后最终 publish job 获得 `contents: write`；Release 固定同时分发双架构制品与带完整许可的确定性 Skill bundle。
89. Instance format 必须区分 current、upgrade-required、newer-format、corrupt 与 migration-incomplete；普通 init/daemon/数据命令不得静默迁移或降级。升级先输出只读计划，再经独立显式批准创建完整性备份和 journal；中断后只能从 journal 绑定或用户明确选择的已验证同 Instance 备份恢复。安装授权不等于升级或回滚授权，宿主 Adapter 不隐式放行 `upgrade --apply` 与 `doctor repair`。
90. 正式 GitHub Release 在 publish 前必须分别于原生 macOS arm64/amd64 隔离
HOME 中，从 publication 内 Canonical Skill 经显式批准和 HTTPS/checksum 安装
开始，完成 init、daemon、doctor、固定项目摘要写入、重启查询与最终健康检查。
每个架构的版本化报告绑定 version、commit、完整步骤和脱敏诊断包 hash；任一
缺失、失败、损坏或绑定不符都阻断发布。该旅程验证安装链路，不替代按产品用户
故事重做的 AI 质量门。
91. [AI-native 产品宪章](../product/ai-native-product-charter.md)是产品方向最高层
约束；旧 Feature 或规格与其冲突时，先标记撤销/待迁移，不以“已经实现”为由保留。
92. AI 是逻辑数据库的首要用户和日常 DBA；确定性引擎负责物理正确性。人主要
提供目标、资料、授权和例外裁决，不负责日常 Schema 与索引维护。
93. 标准发现顺序为 `SHOW DATABASES → SHOW TABLES → DESCRIBE TABLE →
SHOW ROUTES/UNDER → OPEN ROUTE → SELECT by RowID`；MSQL 是 Memora 的 SQL
方言名称，不代表另一个相似度系统。
94. AI 的 `Route Frame` 是有界语义工作集，与缓存 Page 的物理 Buffer Pool
严格分离；两者可以共同提升速度，但不得共享语义职责。
95. AI 负责 Row split/merge 和语义树优化；正文、关系、历史、反向 membership
和受影响上层 Route 必须在一个可回滚 Mutation Plan 中保持一致。
96. 所有 Feature 实施前和合入前都必须通过
[产品与用户故事门禁](./feature-product-gate.md)。持久化后端、查询主路径、
索引算法、Provider 或许可等架构选择必须事前向用户明确披露并获得确认。
97. F51 的发布门结论因使用字符向量/cosine 且未验证逐层 AI 导航而撤销。
98. 原生底座顺序改为 F52 Record Put/Get → F53 Catalog/Row Put/Get → F54
MSQL INSERT/SELECT → F55 接宽对象 → F56 事务/恢复 → F57 迁移/切默认 →
F58 删除 SQLite → F59 Table Router → F60 产品门；详见
[ADR-0003](../decisions/0003-native-minimal-store-first.md)。
99. SQLite 只作为迁移来源临时保留；原生 snapshot 等价、回读和回滚证据完成后，
删除 driver、`internal/store/sqlite`、`.sqlite` 文件名和测试耦合。Unix socket/IPC
是否删除属于另一决策，在用户明确确认前不能与 SQLite 清理混为一件事。
100. F52 的最小自有文件闭环是后续 F53–F80 的起点；“下一项只能是 F53”的
历史顺序现已执行完毕，不再用于指示当前下一 Feature。
101. Route 得到 RowID 后，取数必须是纯 Go 确定性数据库路径，不再调用 AI 或
语义匹配。SQL/主键/事务可见性参考 MySQL，物理实现不要求复制 InnoDB。
102. 精确 RowID 必须使用持久化 B+ Tree 主索引，覆盖 current/version/Table
顺序与 Catalog 定位；点查目标为 `O(log_B N)`。内存 Map 只能作为 cache，当前
Repository 遍历全部 Row record ID 的实现只是待替换过渡层。
103. 本地个人数据库按单 writer、少量 reader 设计；需要 B+ Tree 和精确对象排他
写锁，但复杂 Page latch、gap/next-key lock、范围锁、锁等待、死锁检测、
doublewrite 和复杂后台线程只有实测需要才增加。
104. MVCC 作为正确性能力保留，但首版用 immutable revision、commit marker 和
snapshot commit sequence 实现最小可见性，不预先绑定物理 Undo/Redo 方案。
105. RowID 点查前的 Database/Table/Schema 解析也不能每次重读完整 Catalog。
“daemon 重开时同步全量重建 Catalog Directory”的旧方案已由 F98 取代：Catalog
locator 使用持久化 B+ Tree，reopen 直接打开 committed root。
106. 第一阶段写锁按稳定对象 ID 加排他锁；autocommit 持有到语句终态，显式事务
持有到 commit/rollback，普通 MVCC reader 不取该锁。冲突 fail-fast 还是有界
等待仍需在 F104 Review 时确认。
107. Binlog 第一用途是 Admin 中按 commit sequence 可视化数据、Schema、Route
节点和 membership 的已提交变化；它是事务级逻辑变化流。复制、PITR、GTID 和
多设备同步可以以后复用，但不能主导 F109 的首版事件格式。
108. F124–F126 Benchmark 必须用真实模型测试 Route 每层 fanout、树深和语义歧义度，
报告逐层准确率、最终 RowID 成功率及不同 host/model 的安全 fanout。共享数据库
采用目标模型集合的共同可靠范围，不建立按模型分叉的权威语义树。
109. B+ Tree 是必做的持久化主索引，不再由规模 benchmark 决定是否实现；该结论
取代 67、80、82 中仅针对早期极简闭环的 B+ Tree 后置边界。
110. B+ Tree/Buffer Pool 细节由实现方参考 MySQL 决定：F81–F108 逐项实现 16 KiB
Page、单实例 Buffer Pool、Redo WAL、持久化 B+ Tree 与迁移；普通 Page 更新走
WAL，COW 只用于 rebuild、compaction、snapshot 和 generation/root swap。
111. F81 以后一个 Feature 只允许一个可独立 RED、验收、合入和回滚的主要结果；
Milestone 不构成整批授权。严格执行 RED → GREEN → REFACTOR，恢复、并发和索引
Feature 必须分别提供故障注入、race 或 reference-model 证据。
112. Table Router 继续作为 AI 维护和读取的权威语义结构；Catalog、倒排位置聚合、
Route-only Vector 与有效 Route Frame 可以通过 MSQL 成为带来源、可丢弃的候选预测器。
预测 miss 只影响性能，最终仍显式选择 Route 并按 RowID SQL 回表。
113. Vector 只编码 Route semantic surface，不保存 Row/chunk/正文事实；首版冻结算法
无关接口并实现 CPU 精确扫描。Apple Accelerate 与 HNSW 只有在真实规模证据超过
预定门槛后才进入独立 Feature。详见
[ADR-0007](../decisions/0007-route-predictor-arsenal.md)。
114. F97d3–F109 存储顺序不因 Route Predictor 改变。F124 先冻结 corpus，随后
F124a–F124e 逐项实现候选契约、字面位置、向量 generation、CPU exact 和 Skill
投机预取，再由 F125/F126 比较 Router-only 与优化 arm。
115. F97d3 已将 Tree Mutation Plan 串联为单 writer durable runtime：Open 先执行
WAL recovery，Commit 只在 durable WAL 后原子发布 Buffer batch；outcome unknown 或
durable 后发布失败必须 poison，并只通过 reopen recovery 收敛。
116. F98 已建立持久化 Catalog Lookup Index：Database/Table/Column 的稳定 ID、
名称、别名和当前 Schema revision 通过 B+ Tree 精确定位；冲突在 WAL 前失败，
crash-before-flush 由 WAL 恢复，not-found/corruption 不允许回退全量 Catalog 扫描。
117. F99 已建立持久化 Current Row Index：Table ID + RowID 精确返回当前 Schema
revision、Row revision、commit sequence 与 live/deleted/superseded 状态；整批
expected revision 在 WAL 前校验，当前 revision 只允许单调推进并支持幂等重试。
118. F100 已建立持久化 Row Version Index：exact revision 走点查，AS OF commit
通过倒序 sequence/revision 键用一次 forward cursor 求 floor；legacy sequence 0
只进入 revision key，immutable locator 与稳定 Row identity 均在 WAL 前校验。
119. F101 复用 F99 Current Row Tree 提供 Table prefix Page：after RowID 为
exclusive，每次最多读取 limit + 1 个 locator；live/deleted/superseded 均显式返回，
不因过滤 tombstone 造成无界 Page I/O，跨页 snapshot pin 留给 F103。

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
