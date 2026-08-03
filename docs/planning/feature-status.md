# Feature 状态

状态：2026-08-04 当前权威实现账本；旧完成门和执行过程位于
[`archive/planning/`](../archive/planning/README.md)。

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

F97 被拆为 F97a、F97b1–b2、F97c1–c4、F97d1–d3，所有拆分项均已实现。

## 当前执行

- F185b：消费同一冻结身份下的 F183/F184 arm 矩阵，冻结有效样本和质量阈值，形成 Query Agent
  release gate；失败或不可评分时继续保持 `memora ask` 延后。

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

“延后”不是失败或漏做；这些 Feature 的交付物就是可复现门槛、实测证据和当前不实现的结论。

## 新候选

- F185b–F186：Query release gate 与后续 QuerySession；F185a 的三个真实执行 arms 已完成；
- F187–F200：单网页写入、交互式整本 EPUB 吸收与隐藏答案评分；
- F201–F204：DOCX/PDF/OCR 证据扩展和外置 Hook；当前不规划 Admin 迭代。
- Database 级 Route Branch 自治 fan-out：初始目标、超目标例外和后续调整均由 Agent
  判断并留下 revision/理由，引擎不提供统一语义常量；

具体拆分以[后续开发序列](./post-f169-development-plan.md)为准。写入时机与 worthiness
质量评测仍安排在查询、写入和 Hook 观测稳定之后。

代码清理的保留/删除理由见[旧代码清理边界](../development/legacy-code-boundary.md)。

候选进入实现前仍须按 [Feature 产品门](./feature-product-gate.md)和
[TDD 协议](./feature-tdd-protocol.md)拆成一个结果一个 Feature。
