# Memora 文档入口

这里只保留当前有效设计的入口。完成门、旧计划、被取代方案和讨论过程统一放在
[`archive/`](./archive/README.md)，不能作为当前实现依据。

## 从这里开始

**最高产品参考规范——任何设计与实现与这三份冲突时，以这三份为准：**

1. **[写入形态](./product/write-model.md)** — 数据怎么落库：每张表一棵独立 B+ 树、
   history 独立成表、语义索引叶子直接挂 RowID、三份日志各司其职、binlog 唯一恢复依据；
2. **[查询形态](./product/query-model.md)** — 数据怎么被找到：发现 → 语义导航 →
   叶子拿 RowID → 回表点查 → 查历史，一条有界导航链路；
   候选预测器只给完整语义树路径，不做任何其他操作；
3. **[架构原则](./product/architecture-principles.md)** — 代码与结构怎么组织：
   高内聚低耦合、能用一张表解决就别造复杂逻辑、预测器只给路径。
   每条附**判据**（怎么算违反）与已知实例。

**再读这五份理解系统现状并派发工作，不需要读 F 编号。**

4. [当前系统能力](./product/system-capabilities.md) — 系统现在是什么、各能力域的成熟度和实测性能；
5. [已知风险](./development/known-risks.md) — 已确认存在但未被 Feature 文档记录的问题，按严重度排序；
6. [架构审计 2026-08](./development/architecture-audit-2026-08.md) — 某一时点的**实测清单**：
   缺陷、耦合、重复与半迁移逐条列出，每条带 `文件:行` 与调用方计数。
   **不是规格**，过期请按文末命令重扫；
7. [路线 v2](./planning/roadmap-v2.md) — AI-native 的五个差距与 A/B/C/D 分阶段计划；
8. [执行计划](./planning/execution-plan.md) — **当前唯一的工作队列**，编号工单，
   每项带前置、改动范围、RED 和完成判据。派发实现从这里取。

配套的最高层原则与规则：

- [AI-native 产品宪章](./product/ai-native-product-charter.md) — 最高层产品原则和永久边界。
  方向与边界看宪章，写入与查询的具体形态、代码结构的组织方式看上面三份规范；
- [Feature 产品门](./planning/feature-product-gate.md)与
  [TDD 协议](./planning/feature-tdd-protocol.md) — 新开发的拆分、授权和验收规则。

> **注意**：写入与查询形态规范于 2026-08-22 确立，仓库里相当一部分存储与查询文档
> 早于它。凡头部带「**目标形态已改**」注记的，只如实描述**当前代码**，
> 可以照它读代码，但**不能作为新开发的设计依据**。

## Feature 账本（按编号回溯用，不是导航入口）

F 编号按时间顺序记录开发过程，累计两百多项。它用于单项开发的 TDD 与授权，
以及回溯某项能力的历史证据；**理解系统与派发工作请走上面那几份文档**。

- [Feature 状态](./planning/feature-status.md) — 权威的完成、撤销、证据不完整和延期账本；
- [当前产品基线](./product/current-product.md) — 早于本次重组的产品快照，仍然有效；
- [F169 之后的开发序列](./planning/post-f169-development-plan.md)与
  [F204 之后的开发计划](./planning/post-f204-development-plan.md) — 历史顺序，
  已被[路线 v2](./planning/roadmap-v2.md)取代；
- [查询 Agent Feature 序列](./planning/query-agent-feature-sequence.md)与
  [资料吸收 Agent Feature 序列](./planning/assimilation-agent-feature-sequence.md) — 两条独立垂直链；
- [后续路线](./planning/future-roadmap.md) — 早期路线，已被路线 v2 取代。

### 单项 Feature 规格

- [F169：Route Leaf 单 Row 不变量](./planning/f169-single-row-route-leaf.md) — 修复 Leaf
  候选桶缺陷，冻结一个 Leaf 最多一个活跃 Row。
- [ADR-0008：全内容倒排索引](./decisions/0008-full-content-inverted-index.md) — 当前 Row 与
  语义索引进入可重建 lexical postings，最终事实仍由 SQL 回表；
- [F170：全内容倒排语义模型](./planning/f170-inverted-index-surface.md) — 已完成的无 I/O reference index；
- [F171：持久化 Posting Store](./planning/f171-persistent-posting-store.md) — 已完成的 Page/WAL/B+ Tree 物理层。
- [F172a：Row Posting Generation](./planning/f172a-row-posting-generation.md) — 已完成的 Row 投影、generation v2 与 v1 COW 升级。
- [F172b：Live Row Posting Publication](./planning/f172b-live-row-posting-publication.md) — 已完成的在线 Row posting 原子替换与恢复。
- [F173a：Catalog Posting Publication](./planning/f173a-catalog-posting-publication.md) — 已完成的 Catalog seed、在线替换、删除 tombstone 与恢复。
- [F173b1：Route Posting Generation](./planning/f173b1-route-posting-generation.md) — 已完成的 Route 投影、generation v3 与 reopen reconciliation。
- [F173b2：Live Route Posting Publication](./planning/f173b2-live-route-posting-publication.md) — 已完成的 direct/plan/reshape 统一 Route publication。
- [F173c：Lexical Rebuild 与 Snapshot Parity](./planning/f173c-lexical-rebuild-parity.md) — 已完成的全量 COW rebuild、规范摘要与 MSQL 维护契约。
- [F174：有界 Lexical Location 查询](./planning/f174-bounded-lexical-locations.md) — 已完成的权限先行、预算与 cursor 查询链路；
- [Lexical Locations v1](./query/lexical-locations-v1.md) — 全内容倒排位置的 MSQL 与回表协议。
- [F175a：中立 MSQL Wire Protocol](./planning/f175a-neutral-msql-protocol.md) — 已完成的公共 Request/Envelope 与 SDK 零 wire 变更抽取。
- [F175b：单实例共享 MSQL Service](./planning/f175b-shared-msql-service.md) — 已批准的 IPC/同进程共核、Session 隔离与取消实现门。
- [F175c：Agent MSQL-only Port](./planning/f175c-agent-msql-port.md) — 已完成的消费者接口、scripted fake 与 import allowlist 边界。
- [F176：确定性 Query Bootstrap Frame](./planning/f176-query-bootstrap-frame.md) — 已完成的 Atlas、lexical、root 预取与全局上下文预算链路。
- [F177：Memora-owned Provider Port](./planning/f177-provider-port.md) — 已完成的厂商中立 completion/tool-call 协议与 scripted fake。
- [F178：Agent Trace Envelope](./planning/f178-agent-trace-envelope.md) — 已完成的正文脱敏 Event/Usage/Cost 与可重放 recorder。
- [F179：Agent Runtime Spike](./planning/f179-runtime-spike.md) — Eino/薄状态机的可复现实测与完成门。
- [ADR-0009：Memora-owned 薄 Agent Loop](./decisions/0009-memora-owned-agent-loop.md) — F180/F181 不引入通用 Agent 框架的当前决策。
- [F180：OpenAI-compatible Provider](./planning/f180-openai-compatible-provider.md) — 已完成的标准库 HTTP adapter 与真实 Kimi tool-call smoke。
- [F180a：DeepSeek V4 Provider 方言](./planning/f180a-deepseek-v4-dialect.md) — 显式非思考 wire、`max_tokens` 与 Tool Call 兼容边界；
- [F181：只读 benchmark Query Agent](./planning/f181-read-only-query-agent.md) — 已完成的有界 MSQL-only loop、SELECT evidence 与完整 Trace。
- [F182：Answer Corpus / Manifest](./planning/f182-answer-corpus-manifest.md) — 已完成的合成 fixture、blind manifest 与 evaluator-only ground truth。
- [F182a：Route Alias MSQL Round-trip](./planning/f182a-route-alias-msql.md) — F183 前置的有界 alias 原子替换与读取契约。
- [F182b：Fact / Rationale Semantic Roles](./planning/f182b-fact-rationale-semantic-roles.md) — F183 fixture 无损物化所需的受控 Column role 扩展。
- [F183：端到端 Answer Runner](./planning/f183-end-to-end-answer-runner.md) — clean Instance 的 MSQL-only 物化、Blind Query Agent 与公私报告分离。
- [F184：外部答案质量评测](./planning/f184-external-answer-evaluation.md) — 已完成的 evaluator-only ground truth、真实 SQL Row context、隔离 Ragas v0.4.3 adapter 与公开质量报告。
- [F185a：可执行 Query Bootstrap Arms](./planning/f185a-executable-query-arms.md) — 已完成的 Atlas、Lexical 与 Prefetch 真实执行对照，公开 arm 与 Frame/MSQL transcript 一致。
- [F185b：Query Agent Release Gate](./planning/f185b-query-release-gate.md) — 已完成的三 arm 同身份质量门；当前真实 Kimi release report 为 INCOMPLETE，未选默认 arm。
- [F186：实验性交互式 QuerySession](./planning/f186-query-session.md) — 顺序 turn、实时脱敏事件、取消、会话总预算与有界恢复。
- [F187：Agent Write Profile 与审批信封](./planning/f187-agent-write-profile.md) — L1 scope、guard、hash-bound 一次性用户审批与 MSQL-only 执行边界。
- [F188：单网页/短文本写入垂直链](./planning/f188-short-text-write.md) — 一次模型 draft、用户审批、单 Row commit 与真实 RowID SELECT 回读。
- [F189：Source Intake 交互与即时事件](./planning/f189-source-intake-events.md) — inventory、范围确认、问题/等待/回答摘要与同步事件 batch。
- [F190：可持久恢复的 AssimilationJob](./planning/f190-durable-assimilation-job.md) — 幂等 Command、append-only Event/checkpoint、torn-tail 恢复与 checksum fail-closed。
- [F191：内容寻址临时 SourceStore](./planning/f191-content-addressed-source-store.md) — 流式摘要、配额、跨 Job 复用、reopen 校验与引用清理。
- [F192：格式无关 Document IR v1](./planning/f192-document-ir-v1.md) — 层级、阅读顺序、稳定 anchor、表格/脚注关系与规范摘要。
- [F193：EPUB 确定性适配器](./planning/f193-epub-adapter.md) — container/OPF/spine/nav、结构 XHTML、脚注与资源清单。
- [F194：ReadExtent 与 coverage 调度](./planning/f194-read-extent-coverage.md) — 完整语义节点窗口、摘要确认、无原文 checkpoint 与断点续读。
- [F195：正式 MSQL 吸收提交面](./planning/f195-msql-assimilation-surface.md) — 提案结构审阅、hash-bound 提交、同核事务与无正文收据。
- [F196：Draft / Claim Ledger](./planning/f196-draft-claim-ledger.md) — 可替换 Provider 的单窗口草拟、来源锚点绑定与可恢复候选 MSQL 账本。
- [F197：分支问题暂停与恢复](./planning/f197-branch-pause-resume.md) — 问题即时事件、branch-local revision 与不阻塞其他阅读分支的持久恢复。
- [F198：独立语义复核门](./planning/f198-independent-review-gate.md) — 新鲜 reviewer 请求、challenge 绑定和数字/anchor/冲突/非原文复制四项证据。
- [F199：短事务对账与 Source Receipt](./planning/f199-assimilation-reconciliation.md) — accepted review 输入门、MSQL-only 提交、实际对象 ID 收据与 in-doubt 只读恢复。
- [F200：EPUB 单条干净全链路验收](./planning/f200-epub-single-chain-acceptance.md) — 冻结小 EPUB 跑通吸收、正式收据与固定查询，不执行批量质量评分。
- [F201：DOCX 确定性适配器](./planning/f201-docx-adapter.md) — 标准库解析 OOXML 包、正文结构、表格、脚注和内部关系为 Document IR。
- [F202：文本层 PDF 确定性适配器](./planning/f202-text-pdf-adapter.md) — 解析经典 xref/Page tree、受支持字体映射与文本显示操作为逐页 Document IR。
- [F203：OCR/视觉路径证据门](./planning/f203-ocr-evidence-gate.md) — 只校验外部逐页成对证据，不把 OCR 引擎或权重带入主程序。
- [F204：外置 Agent Hook](./planning/f204-external-agent-hook.md) — 只采集脱敏 Memora Trace，按显式 session/host/model 有界观测。
- [F205：native 多 statement 原子事务](./planning/f205-native-multistatement-transaction.md) — 已完成；production daemon 中为多条 L1 数据语句提供真正的 all-or-nothing 提交。
- [F207：本地 Hook 指标与报告](./planning/f207-local-metrics-report.md) — 已完成；将脱敏 Hook 快照聚合为 session/turn JSON/HTML 报告，不计算 Recall/MRR。
- [F204 之后的开发计划](./planning/post-f204-development-plan.md) — 多 statement 原子事务、低并发复评、本地指标和按证据扩展的顺序。
- [公开评测语料候选](./development/public-evaluation-corpus-candidates.md) — CRUD-RAG、RGB、HotpotQA 与 MIRACL 的后续适配边界；
- [F212：外置评测数据准备](./planning/f212-external-evaluation-data.md) — 冻结公开语料清单、ExFAT 外置盘布局、断点续传与离线校验；
- [F213：外部检索评分与对照报告](./planning/f213-retrieval-evaluation-score.md) — evaluator-only Recall@K、HitRate@K、MRR、分桶和成本降幅；
- [F214：外部语料到 Retrieval Suite 适配器](./planning/f214-external-suite-adapters.md) — MIRACL zh 与 MTRAG BEIR query/qrel 的确定性归一化；
- [F215：低并发 Provider 退避与评测断点](./planning/f215-low-concurrency-resume.md) — 单 worker、有限重试、hash-bound checkpoint 与失败题续跑；
- [ADR-0010：小规模高质量评测优先](./decisions/0010-small-scale-high-quality-evaluation.md) — 评测目标从绝对质量分
  改为架构对照证据；F212–F215 与候选 F216–F218 转 Deferred；
- [F219：确定性答案评分](./planning/f219-deterministic-answer-scoring.md) — 候选；不依赖模型的检索命中主判定与
  judge 指标部分缺失表示，是 ADR-0010 之后任何评测运行的前置项。
- [F220：Query Working Set](./planning/f220-query-working-set.md) — 候选；带完整 Route 链路的有界语义工作集，
  用一点上下文换导航时间；同时是多轮记忆缺陷的修复。
- [F221：Evidence 充分性与导航终止](./planning/f221-evidence-sufficiency.md) — 候选；零行 SELECT 不再终止导航，
  无证据时拒绝作答；执行计划第 1 项。
- [F222：Release Gate Policy v2](./planning/f222-release-gate-policy-v2.md) — 候选；确定性主判定 +
  report/gate 双模式，解除 F185b 与 ADR-0010 的死锁。
- [F224：Row 必须可导航](./planning/f224-mandatory-row-route.md) — 候选；写入时强制语义索引，
  杜绝零 Route 归属的孤儿 Row。
- [F225：Row 必须可展示](./planning/f225-mandatory-row-summary.md) — 候选；写入时强制 summary 非空，
  引擎判定「非空」、Skill 承载「完整自足文档」的语义要求。
- [F226：Database 级故障隔离](./planning/f226-per-database-fault-isolation.md) — 候选；修复「一个库出错
  导致整实例读写全停」，并拆分共用的物理文件。
- [F223：Route Branch Fan-out 硬上限](./planning/f223-route-branch-fanout-limit.md) — 已实现；
  一个节点最多 12 个 live child（本库可改），越界一律失败并给出重构或提高上限两条出路。

## 当前产品规格

### 数据与产品

- [当前系统能力](./product/system-capabilities.md) — 按能力域的成熟度与实测性能；
- [AI-native 产品边界](./product/ai-native-boundary.md)
- [AI-native 产品契约](./product/ai-native-contract.md)
- [语义记录模型](./data/semantic-records.md)
- [自描述 Data Dictionary](./data/self-describing-data-dictionary.md)
- [资料吸收](./data/assimilation.md)
- [语义重建的不对称性](./data/semantic-rebuild-asymmetry.md) — 讨论稿；可重建的层最不依赖模型能力，
  最依赖模型能力的内容分解层因原文回收而不可重建，处置方案待决策；
- [Database Package](./product/database-package-v1.md)
- [质量模型](./product/quality-model.md)

### Agent 与查询

- [Canonical Skill](./agent/canonical-skill-v1.md)
- [MSQL](./query/msql.md)
- [语义 Router](./query/semantic-routing.md)
- [Route Branch Fan-out 策略](./query/route-branch-fanout-policy.md) — Database 自治目标与
  Agent 语义重构规则；「无默认值、可带理由超限」两条已被 F223 取代；
- [检索质量链路](./query/retrieval-quality.md)
- [投机 Route 预取](./query/speculative-route-prefetch.md)
- [CPU 精确 Route Match](./query/cpu-exact-route-match-v1.md)
- [Skill 写入](./agent/skill-write-v1.md)
- [Schema 生命周期](./agent/skill-schema-lifecycle-v1.md)
- [Policy Enforcement v2](./development/policy-enforcement-v2.md)
- [MCP Adapter](./agent/mcp-adapter-v1.md)
- [可选内置 Agent Runtime](./agent/embedded-agent-runtime.md)
- [Agent 的 MSQL 边界与依赖注入](./agent/agent-msql-dependency-injection.md)

### 存储与可靠性

**先读这两份**：

- **[存储层：当前实现总览](./storage/README.md)** — 现在实际是什么样，
  逐层给出事实并指向对应的冻结规格。读懂这一层的唯一入口；
- **[写入形态](./product/write-model.md)** — 存储层的**设计终点**，
  最高产品参考规范。总览末尾列出现役实现与它之间的已知偏差。

落实写入形态的迁移设计：

- [叶子直挂 RowID](./storage/leaf-rowid-v1.md) — membership 的废弃与迁移：
  职责拆解、反向索引树、对外可见面变更与分阶段顺序。

其余按 Feature 切分的规格在各自验收门通过时冻结，是证据链而非现状描述，
从总览的对应小节进入。被取代的已移入 `archive/storage/`——包括
[聚簇行存储 v1](./archive/storage/clustered-row-storage-v1.md)，
它曾是设计终点，2026-08-22 被写入形态取代。

### 使用与观察

- [CLI Workflow](./development/cli-database-workflow.md)
- [Go SDK](./development/go-sdk-v1.md)
- [macOS LaunchAgent](./development/macos-launch-agent-v1.md)
- [Admin Shell](./development/embedded-admin-shell-v1.md)
- [Admin Semantic Local Motion](./development/admin-semantic-local-motion-v1.md) — F216 局部、可中断、性能受限的 Route Tree 微动画；不启用全局布局动画。
- [Admin 第三方前端资源](./development/admin-third-party-notices.md)
- [Route Trace](./query/route-trace-read-v1.md)
- [评测 Agent 与外置 Hook](./development/evaluation-agent-observability.md)
- [下一次 DeepSeek 评测启动问题](./development/evaluation-next-run.md) — 只从环境变量读取 key 的 F215 smoke 命令；
- [已知风险](./development/known-risks.md) — 已确认但未被 Feature 文档记录的问题；
- [旧代码清理边界](./development/legacy-code-boundary.md)
- [签名发布制品](./development/macos-signed-release-artifacts-v2.md)
- [干净机器验收](./development/clean-machine-acceptance-v1.md)

## 决策与历史

- 当前 ADR 保存在 [`decisions/`](./decisions/)；新决策必须注明 Accepted、Superseded 或 Deferred；
- 市场快照保存在 [`research/`](./research/)，不是实现规格；
- 历史方案和完成证据只从 [归档入口](./archive/README.md)追溯；
- 日常任务不要批量读取归档文档。
