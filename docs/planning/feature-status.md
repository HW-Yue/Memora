# Feature 状态

状态：2026-08-05 当前权威实现账本；旧完成门和执行过程位于
[`archive/planning/`](../archive/planning/README.md)。

> **这份账本按时间顺序记录，不是理解系统的入口。** 要知道系统现在能做什么，读
> [当前系统能力](../product/system-capabilities.md)；要知道哪里有问题，读
> [已知风险](../development/known-risks.md)；要知道接下来做什么，读
> [路线 v2](./roadmap-v2.md)；要派发实现工单，读[执行计划](./execution-plan.md)。
> 本文档用于按 F 编号回溯某项能力的历史证据。

## 状态定义

- **已实现**：代码、规格、测试和完成门均已交付；
- **证据不完整**：机械能力已交付，但仍缺真实 AI/外部环境证据；
- **已撤销/取代**：历史实现不再属于当前产品路径；
- **已评估并延后**：进入条件已执行，但证据不支持现在增加该能力；
- **候选**：稳定方向已形成，尚未拆成获准 Feature。

## 已实现主线

| 范围 | 当前结果 |
| --- | --- |
| F00–F18 | 工程骨架、CLI/daemon/IPC、MSQL、Catalog、Row、事务、History、Relation |
| F19–F27 | 历史索引实验、Router、generation、snapshot、CLI 垂直链路；现行路径由后续 Feature 修正 |
| F28–F50 | Canonical Skill、AI 写入/Schema/冲突/资料/反馈、双宿主 adapter、包、Wiki、安装与发布基础 |
| F52–F80 | 原生 Store 迁移、Table 级 Router、AI-native 链路修正、来源与配置 |
| F81–F109 | Page/WAL/Recovery/Buffer Pool/B+ Tree、真实 RowID、snapshot、锁、迁移、COW、Change Log |
| F110–F126 | Admin 读取与 UI、真实宿主契约、Route benchmark、Lexical/Vector/预取和能力报告 |
| F128–F150 | 语义健康与变更计划、Host Input、Policy、Package 生命周期、备份恢复、MCP/SDK、发行 |
| F152 | free Page reuse |
| F164 | 删除失效的 F42 benchmark 与测试型 F43 runtime gate |
| F165 | 删除未接入产品链路的硬编码 `internal/skillquery` Runner |
| F166 | 清除已撤销 `MATCH` 的 Lexer、Policy、Host 与当前文档残留 |
| F167 | 删除旧 Row Agent-index generation，并归档 `pending_reindex` 规格 |
| F168 | Admin 默认固定监听 `127.0.0.1:3888`，占用时拒绝随机降级 |
| F169 | Route Leaf 冻结为最多一个活跃 Row；旧项目数据已迁为 15 个单 Row Leaf |
| F170 | 全内容 lexical reference index；Row 与语义表面共用确定性 tokenizer 和 revision replacement |
| F171 | 可重开的 Page/WAL/B+ Tree posting store；object/owner/posting 单计划原子替换 |
| F172a | 当前 Row 确定性投影；四树 generation v2；合法三树 v1 启动时自动 COW 升级 |
| F172b | 在线 Row posting batch publication；故障 poison/reopen；revision gap 自动 COW 重建 |
| F173a | Catalog Database/Table/Column posting seed 与在线原子发布；drop tombstone、故障恢复和 Row-only v2 COW 升级 |
| F173b1 | Route 确定性投影、Plan/generation v3、v2 增量/COW 升级与删除恢复 |
| F173b2 | direct CRUD、Route Plan 与 Row+Route reshape 统一原子发布 Route posting；故障 poison/reopen 收敛 |
| F173c | `REBUILD LEXICAL INDEX` 全量 COW replacement、规范 snapshot SHA-256、parity receipt 与 daemon 重开 |
| F174 | 权限先行的全内容 lexical locations；有界 rows、稳定 cursor、RowID/revision SQL 回表 |
| F175a | 仅标准库的 `protocol/msql`；SDK 兼容 aliases 与 request/envelope wire golden |
| F175b | 单实例共享 MSQL Service；独立 Session、IPC/同进程共核、取消/回滚与并发隔离 |
| F175c | Agent consumer-owned MSQL port、单次协议验证 gateway、scripted fake 与全树 import allowlist |
| F176 | 无模型 Query Bootstrap Frame；Atlas 续页、lexical、root 投机预取、独立 snapshot 与 12 KB 默认总预算 |
| F177 | 厂商中立非流式 Provider port；严格 message/tool/usage 验证、单次 gateway 与 scripted fake |
| F178 | 正文脱敏 Agent Event/Trace/Usage 信封、可重放 Summary 与并发安全 recorder |
| F179 | Eino/薄 loop 可复现对照；选择 Memora-owned 有界状态机并冻结重评触发器 |
| F180 | 标准库 OpenAI-compatible HTTP Provider；延迟密钥解析、脱敏失败与真实 Kimi tool-call smoke |
| F181 | 只读 benchmark Query Agent；压缩上下文、L0 MSQL batch、真实 SELECT evidence 与完整 Trace |
| F182 | 确定性 answer corpus；合成 fixture、blind task、严格 manifest/ground truth 与逐字 golden |
| F182a | Route alias 的有界 MSQL 完整替换/读取；revision 冲突、transaction rollback、live posting 与 fault/reopen 收敛 |
| F182b | Catalog/MSQL 支持受控 `fact/rationale` Column role；规范化、原生 round-trip 与未知值 fail closed |
| F183 | clean Instance MSQL-only 物化、12 题 Blind Query Agent、严格公私报告与目录级原子发布 |
| F184 | evaluator-only ground truth、真实 SQL Row context、隔离 Ragas adapter 与公开质量报告 |
| F185a | 三个可执行 Query Bootstrap arm；公开标签与实际 Profile、Frame、MSQL transcript 一致 |
| F185b | 三 arm 同身份 release gate、固定质量/覆盖阈值与 context/token 优先选择；真实报告为 INCOMPLETE |
| F186 | 进程内实验 QuerySession；实时脱敏 Trace、取消、会话总预算、失败/取消后有界恢复，复用 F181 loop |
| F187 | Agent L1 Write Gateway；固定 scope/actor/guards、proposal SHA-256、一次性用户审批与真实 Policy 拒绝越权 |
| F188 | 单网页/短文本内置 Agent；一次有界 draft、用户审批、单 Row Route 写入、真实 RowID/revision/commit SELECT 回读 |
| F189 | Agent-owned Source Intake；范围确认、即时问题/等待/回答摘要、单调进度、取消与并发安全事件协议 |
| F190 | 可持久恢复的 AssimilationJob；hash-chain journal、幂等 Command、Intake/checkpoint 恢复与 torn-tail 处理 |
| F191 | 内容寻址临时 SourceStore；流式摘要、四类配额、跨 Job 复用、reopen 校验与引用回收 |
| F192 | 格式无关 Document IR v1；稳定 ID/anchor、结构层级、reading order、表格/脚注关系与规范摘要 |
| F193 | EPUB 确定性适配器；container/OPF/spine/nav/NCX、结构 XHTML、脚注/表格与资源摘要 |
| F194 | ReadExtent coverage 调度；完整语义节点窗口、digest 确认、无原文 checkpoint 与确定性恢复 |
| F195 | 正式 assimilation MSQL；结构审阅、同库 L1/精确 approval、同核事务与无正文 receipt |
| F196 | 可替换 Provider 的单 extent 多 claim 草拟；可恢复 hash-chain ledger、可信 anchor/provenance 注入与候选 MSQL |
| F197 | AssimilationJob branch-local issue/answer；即时事件、独立 revision、未受影响分支继续与无正文恢复 |
| F198 | author/reviewer 隔离的独立语义复核；新鲜 challenge 请求、数字/anchor/冲突/非原文检查与无正文 artifact 恢复 |
| F199 | accepted review 到正式 MSQL 短事务的对账；review evidence、实际 object ID/revision/commit sequence 收据与 in-doubt 只读恢复 |
| F200 | clean daemon 的冻结 EPUB 单链；解析、coverage、draft、独立复核、单 statement 原子提交、Source Receipt 与固定 Query Agent 回答闭环 |
| F201 | 标准库 DOCX adapter；package relationship/content type、正文/heading/list/table/footnote/image 与稳定 Document IR anchor |
| F202 | 标准库文本层 PDF adapter；classic xref/Page tree、Flate、WinAnsi/ToUnicode、逐页文本 IR、稳定 object-span anchor 与 fail-closed 预算 |
| F203 | OCR/视觉路径证据门；逐页成对 baseline/OCR 摘要、Recall gain/延迟计算、稳定 digest，证据不足自动 deferred |
| F204 | 显式 opt-in 外置 Agent Hook；只采集脱敏 TraceEvent 与 host/session/model 元数据，有界并发安全快照 |
| F205 | 已完成：native daemon 多 statement L1 原子事务；统一事务工厂、native store 批量提交、IPC/重开/并发/故障证据 |
| F207 | 已完成：F204 Hook 快照的 session/turn 指标聚合、重复/冲突校验、确定性 JSON/HTML 报告与独立命令；不计算 Recall/MRR |
| F212 | 已完成（2026-08-11 转 Deferred）：冻结 5 套公开评测语料清单与外置盘准备器；支持 Git commit、HTTP Range 续传、SHA-256、离线 verify-only；本机 3.1 GB 全量复核通过 |
| F213 | 已完成（2026-08-11 转 Deferred）：标准 retrieval suite/run 与 evaluator-only Recall@K、HitRate@K、MRR、分桶和 Token/工具调用对照 |
| F214 | 已完成（2026-08-11 转 Deferred）：MIRACL zh 与 MTRAG 四 domain 的 query/qrel 确定性适配器；normalized suite 已在外置盘生成 |
| F215 | 已完成（证据不完整，2026-08-11 转 Deferred）：低并发退避、ExFAT 报告发布、Route Frame 续上下文与 checkpoint；DeepSeek smoke 续跑后为 9/12 成功，质量仍未通过 |

F97 被拆为 F97a、F97b1–b2、F97c1–c4、F97d1–d3，所有拆分项均已实现。

## 当前执行

- F185b 实现已完成，但真实 Kimi 三 arm 共 36 题均因 33 次 HTTP 429 与 3 次 wire failure 不可评分；
  release report 保持 `INCOMPLETE`、无默认 arm；
- 2026-08-05 用户决定延期大批量真实质量复跑。该门继续阻止“质量已通过”和“默认 arm 已选定”
  的发布声明，但不再阻止后续实验性 Feature 开发；F200 单条 EPUB 全链路、F201 DOCX adapter 和 F202
  文本层 PDF adapter、F203 OCR/视觉证据门和 F204 外置 Hook 已通过；真实批量质量仍按用户决定延期；
- F200 同时确认当前 native daemon 只能完成单 statement assimilation autocommit。F205 已补齐多
  statement plan 的原子 native 执行；正式多 claim 仍需沿用 F195 review/approval，不能旁路写入。
- F207 已补齐 F204 Hook 的本地 session/turn 指标聚合与 JSON/HTML 报告命令；报告只用于平台自身
  可观测性，不替代 Recall/MRR 或外部答案质量评测。
- F215 已补齐有限退避、可恢复 checkpoint、DeepSeek V4 MSQL 导航提示、空 statements 兼容、
  有界 Route Frame 续上下文和 ExFAT 外置盘报告发布；2026-08-06 smoke 续跑后为 9/12 成功，继续阻止
  质量通过和默认 arm 选择。
- 2026-08-11 用户决定评测继续做，但不再扩大样本量，改为小规模、高质量、可复现的对照实验；
  主指标改为不依赖模型的确定性检索命中判定，LLM judge 降级为次要参考。决策与理由见
  [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)。前置项为候选
  [F219](./f219-deterministic-answer-scoring.md)：现行 `validScores` 要求四个 judge 指标全有或全无，
  单指标失败即丢弃整题，这是 9 题中 6 题 `evaluator_failed` 的直接原因，小样本下不可接受。
  F185b release gate 维持有效，继续阻止“质量已通过”和“默认 arm 已选定”的对外声明。

## 例外与非当前路径

| Feature | 状态 | 当前解释 |
| --- | --- | --- |
| F21 MATCH Fusion | 已撤销 | Row/字符向量融合不再是语义主路径 |
| F23 Index Discovery | 已撤销 | 不再让融合评分直接返回事实候选 |
| F43 内置产品 Runtime | 延后 | v0 由外部宿主使用统一 MSQL；评测 Agent 是新的独立候选 |
| F51 AI-native Release Gate v1 | 已撤销 | 旧 Vector/cosine 证据无效 |
| F127 Story Gate v2 | 证据不完整 | evidence contract 已实现，真实双宿主报告仍为 `INCOMPLETE` |

F19/F20、F22、F30 等历史实现留下的有效底层能力已被现行 Table Router、Route-only
predictor 和 Canonical Skill 复用或替代；其旧产品结论不再有效。

## 已执行证据门并延后

| Feature | 延后原因 |
| --- | --- |
| F151 Compaction | 等宽更新空间放大 1.00x，未越 1.25x 门 |
| F153 Secondary Index | canonical workload 没有 10k+ 非 RowID predicate 需求 |
| F154 Buffer Pool Scaling | M4 hot-hit 远低于 5 µs 门 |
| F155 Advanced I/O Scheduler | 1 MiB dirty batch 远低于 5 ms 门 |
| F156 Physical Undo | 当前 durable-then-publish、no-steal、immutable revision 足够 |
| F157 Advanced MVCC | 没有 multi-writer/强隔离产品故事 |
| F158 Lock Wait/Deadlock | 当前 fail-fast one-winner 满足 workload |
| F159 Replication | 没有 primary/replica/failover 故事 |
| F160 PITR | 没有已冻结 RPO/RTO 故事 |
| F161 Multi-device Sync | 没有双设备离线写与冲突语义 |
| F162 Apple Accelerate | 4,368×384 CPU exact p95 最高 2.434 ms |
| F163 HNSW | 17,472×384 CPU exact p95 最高 9.957 ms、33.171 MB |
| F226 Stage 2 按库拆分文件 | 最热读路径（Catalog Atlas、`SHOW LEXICAL LOCATIONS FROM ALL TABLES`）本来就跨库，拆分即常态 fan-out；主导损坏模式是共享引擎代码缺陷，拆文件零作用；570 行仅 556 KB，规模不支持。Stage 1 已在逻辑层解决实际故障隔离 |
| F212–F215 外部大语料评测设施 | 设施已交付并验收，但三次真实运行（Kimi 12 题、三 arm 36 题、DeepSeek 9/12）均未产出可用质量结论；改走小规模确定性对照，设施冻结保留。见 [ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md) |
| 候选 F216–F218 公开语料桥接层 | 依赖 F212–F215，随之 Deferred；恢复条件同 ADR-0010 |

“延后”不是失败或漏做；这些 Feature 的交付物就是可复现门槛、实测证据和当前不实现的结论。

## 新候选

- F187–F199：单网页写入、交互式整本 EPUB 吸收、独立复核与提交对账；
- F200（已完成）：只验收一条干净 snapshot 的 EPUB 完整链路和结果结构，不做批量模型质量评分；
- F201–F204（已完成）：DOCX、文本层 PDF、OCR/视觉证据门与外置 Hook 已完成；当前不规划 Admin 迭代。
- Database 级 Route Branch 自治 fan-out：初始目标、超目标例外和后续调整均由 Agent
  判断并留下 revision/理由，引擎不提供统一语义常量；

- F219 候选：确定性主评分与部分指标评分表示；ADR-0010 之后任何评测运行的前置项。
- F220 候选：Query Working Set——带完整 Route 链路的有界语义工作集；同时是多轮记忆缺陷
  （[已知风险](../development/known-risks.md)第 1 条）的修复。
- F221 候选：Evidence 充分性与导航终止条件；零行 SELECT 不再终止导航，无证据时拒绝作答。
- F222 候选：Release Gate Policy v2；确定性主判定与 report/gate 双模式，解除 F185b 死锁。
- F224 候选：Row 必须可导航；写入时强制至少一个 Route 归属，杜绝语义上不可达的孤儿 Row。
- F225 候选：Row 必须可展示；写入时强制 summary role 列非空。SKILL.md 强约束已落地，引擎侧待实现。
- F226：Stage 1（poison 按库收敛）已实现；Stage 2（按库拆分文件）已评估并延后，见上表。
- [F227](./f227-object-archive.md) **已实现**（2026-08-20）：统一归档模型。**六类对象全部可归档，归档即唯一的
  删除语义，不提供物理擦除**。动词 `ARCHIVE`/`UNARCHIVE`，读面统一加 `INCLUDING ARCHIVED`
  修饰词；可见性 iff 自身与每级祖先均未归档，绝不下沉到后代（归档只产生 1 条 change log，
  后代 `revision` 不变）；磁盘状态字符串保持 `deleted` 不变，统一的是对外词汇。
  实测差距：Table/Database 整体缺失；`DROP_COLUMN` 是真移除不是归档；
  **`router.deleteSubtree` 会清空 locator 并摘除 children，现有 `DELETE ROUTE` 是有损的**，
  Stage 1 先修它。前端规则见 [Admin UI 归档](./f227-archive-admin-ui.md)：
  默认全站不可见，唯一全局开关，深链接返回 200 + 归因横幅。
  六类对象（Database/Table/Column/Route/Row/Relation）全部可归档，`INCLUDING ARCHIVED`
  读面、Admin UI 全局归档模式、健康项联动、SKILL.md 与两个 adapter 均已交付，
  并有一张覆盖全部读面的
  [可见性矩阵测试](../../internal/daemon/f227_visibility_matrix_test.go)。
  实现过程中另修两个同源的已发布缺陷：**改名**与 **`DROP_COLUMN`** 都会让相关 Row
  全部读不出来——Catalog 变更从不重写 Row，但有代码假设二者必须一致。
- F223 已实现：Route Branch Fan-out 硬上限；`route_policy.branch_fanout` 启动默认 12，
  `CREATE ROUTE`、Route Mutation Plan 与 Semantic Health 统一读取本库值，越界一律失败并在
  信封里给出重构子树与提高上限两条可执行出路。取代下面「Database 级 Route Branch 自治
  fan-out」候选中「引擎不提供统一语义常量」的表述。

派发顺序与完成判据以[执行计划](./execution-plan.md)为准。

具体拆分以[后续开发序列](./post-f169-development-plan.md)为准。写入时机与 worthiness
质量评测仍安排在查询、写入和 Hook 观测稳定之后；worthiness 决策不可逆性的相关事实见
[语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)（讨论稿）。

代码清理的保留/删除理由见[旧代码清理边界](../development/legacy-code-boundary.md)。

候选进入实现前仍须按 [Feature 产品门](./feature-product-gate.md)和
[TDD 协议](./feature-tdd-protocol.md)拆成一个结果一个 Feature。
