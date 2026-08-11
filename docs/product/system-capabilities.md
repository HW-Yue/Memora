# 当前系统能力

状态：2026-08-11 权威能力快照。**这是了解「系统现在是什么」的入口文档。**

本文按能力域组织，不按 Feature 编号。F 编号是开发过程的账本，不是系统的结构；
查某项能力的历史证据再去 [Feature 状态](../planning/feature-status.md)按编号回溯。

只收录已交付、有测试、当前无已知缺陷的能力。有缺陷或未验证的部分见
[已知风险](../development/known-risks.md)，未实现的部分见[路线 v2](../planning/roadmap-v2.md)。

## 1. 存储引擎（成熟）

自研，零第三方运行时依赖。这是系统中最扎实的一层。

- **Page 层**：16 KiB 固定 Page、Castagnoli CRC32、format version、typed page（Data／
  BTreeInternal／BTreeLeaf／Free／Manifest／Overflow／TreeControl）；损坏明确报错不假装恢复。
- **WAL**：segment、record header、durable frontier、checkpoint、reclaim、torn-tail 恢复、
  tree redo、repair-open。7,404 行，含 corruption 与 fault injection 证据。
- **B+ Tree**：point／range、split、delete、rebalance、多层原子提交（treecommit）。
- **Buffer Pool**：WAL-before-data、young/old 淘汰、dirty batch flush。
- **MVCC**：statement snapshot、精确对象写锁、immutable revision、durable-then-publish、
  no-steal；未实现 physical undo（当前策略下不需要）。
- **索引**：Catalog／当前 Row／Row Version 三类权威索引；Catalog／Route／Row Fulltext
  派生树；Table Row cursor；COW generation replacement（当前 v3）；free Page reuse。

**实测性能**（容器化 Linux／Xeon 2.8 GHz，fsync 未必反映真实盘）：

| 操作 | 结果 |
| --- | --- |
| 单事务批量写入 500 行 | 84 ms → **168 µs/行** |
| 单事务批量写入 50 行 | 20 ms → 401 µs/行（进程启动未摊薄） |
| `SELECT ... LIMIT 20` | < 1 ms 引擎耗时 |
| CLI 单次调用固定开销 | ~10 ms（进程启动 + unix socket 连接，主导单条 CLI 操作） |
| 570 行实例磁盘占用 | 556 KB |

结论：**引擎不是瓶颈**，且余量很大。单条 CLI 操作的 9 ms 里约 95% 是进程启动。

## 2. MSQL 语言与执行（成熟）

- lexer → parser（手写递归下降）→ ast → binder → executor → service → session 分层；
- Catalog、Row、History、Relation、Route、事务与管理语句；
- 单实例共享 MSQL Service，独立 Session、IPC／同进程共核、取消与并发隔离；
- 多 statement 真 all-or-nothing 原子事务（native daemon）；
- 中立 wire protocol（`protocol/msql`，仅标准库），SDK 与 Agent 共用；
- Agent 只能经此入口访问数据，有全树 import allowlist 强制。

## 3. 语义模型与检索（已交付，质量未验证）

- AI 自描述建库／建表／字段建模，自描述 Data Dictionary；
- Table 级多层语义 Route，一个 Leaf 最多一个活跃 Row，一个 Row 可属多个 Leaf；
- 有界 Catalog Atlas 与逐层导航；Route alias；
- 全内容倒排索引：确定性 tokenizer、持久 posting store、在线原子替换、
  全量 COW rebuild 与 parity receipt、权限先行的有界 cursor 查询；
- Route-only lexical／vector 候选；CPU 精确 match；有界投机预取；
- Mutation Plan、关系、历史、逻辑补偿、Route/Schema 变更计划与结构健康扫描。

⚠️ 机械正确性有测试覆盖；**检索质量从未取得可用数字**，见[已知风险](../development/known-risks.md)。

## 4. 文档解析（成熟，有内存上界问题）

不依赖模型的确定性适配器，全部只用标准库，不执行 Office、宏、外部 URL、OCR 或网络请求。

- **Document IR v1**：格式无关，稳定 anchor、层级、reading order、表格／脚注关系；
- **EPUB**：container／OPF／spine／nav／NCX、结构 XHTML、脚注、资源清单；
- **DOCX**：package relationship、正文／heading／list／table／footnote／内部图片关系；
- **PDF（文本层）**：classic xref／Page tree、Flate、WinAnsi／ToUnicode、逐页文本 IR、
  稳定 object-span anchor；无文本层时明确失败，不猜测；
- **OCR**：只有证据门，不带引擎和权重进主程序。

**实测 EPUB 解析**（同上环境）：

| 规模 | 正文 | 节点数 | 峰值堆 | 累计分配 | 耗时 |
| --- | ---: | ---: | ---: | ---: | ---: |
| 小册子 | 0.15 MiB | 441 | 2.5 MiB | 5 MiB | 13 ms |
| 长篇小说 | 2.93 MiB | 8,401 | 25 MiB | 100 MiB | 345 ms |
| 大部头 | 17.62 MiB | 49,601 | 124 MiB | 564 MiB | 2.0 s |

典型个人文档（正文 1–5 MiB）在 10–40 MiB 堆、0.3–0.7 s 内完成，**够用**。
放大倍率收敛于**峰值堆 ≈ 正文 7 倍、累计分配 ≈ 正文 32 倍**（约 2.6 KB 堆／IR 节点）。
配置上界与该倍率未对齐，见[已知风险](../development/known-risks.md)。

## 5. 资料吸收链路（组件完整，端到端只验过一条）

Intake → SourceStore → Document IR → ReadExtent coverage → Draft/Claim ledger →
独立复核 → MSQL 短事务 → Source Receipt。

- 可持久恢复的 AssimilationJob：hash-chain journal、幂等 Command、torn-tail 恢复；
- 内容寻址临时 SourceStore：流式、四类配额、跨 Job 复用、reopen 校验、引用回收；
- coverage 调度：完整语义节点窗口、digest 确认、无原文 checkpoint、断点续读；
- author/reviewer 隔离的独立复核：数字／anchor／冲突／非原文复制四项检查；
- 正式提交：结构审阅、hash-bound 用户批准、同核事务、含实际 Row/Relation ID 的收据；
- 分支问题暂停与恢复，不阻塞其他阅读分支。

⚠️ 只有一本冻结 EPUB 跑通过完整链路（单 claim）。批量与长资料未验证。
原文在 Job 释放后回收，语义分解不可重建，见[语义重建的不对称性](../data/semantic-rebuild-asymmetry.md)。

## 6. 宿主集成与产品外壳（成熟）

- 本地 daemon、Unix socket、CLI、长连接 Go SDK、macOS LaunchAgent；
- Canonical Skill 与 Codex／Claude Code adapter；现代与兼容版 MCP stdio，
  唯一工具 `memora_execute`；
- 本地只读 Admin（`127.0.0.1:3888`），浏览 Catalog／Route／Row／History／Change／
  Route Trace／语义画布；
- Database Package 签名、安装、升级、撤销、fork、三方 merge；
- Instance 备份、恢复、搬迁、格式升级、诊断；Wiki 确定性导出；
- macOS arm64/amd64 签名制品与受门禁约束的 Release 流程；已发布 v0.1.0。

## 7. Agent 与可观测（部分可用）

- 厂商中立 Provider port，标准库 OpenAI-compatible adapter，DeepSeek V4 方言，
  真实 Kimi tool-call smoke 通过；
- 正文脱敏的 Trace／Event／Usage 信封与可重放 recorder；
- 显式 opt-in 外置 Hook 与本地 session/turn 指标 JSON/HTML 报告；
- Agent L1 Write Gateway：固定 scope、guard、hash-bound 一次性审批；
- 只读 Query Agent 与实验性 QuerySession。

⚠️ Query Agent 的循环有结构性缺陷（单步记忆、证据提前终止），是当前最高优先级问题，
见[已知风险](../development/known-risks.md)第 1、2 条。

## 关联

- [已知风险](../development/known-risks.md) — 有缺陷、未验证或有隐患的部分；
- [路线 v2](../planning/roadmap-v2.md) — 未实现部分与 AI-native 差距；
- [Feature 状态](../planning/feature-status.md) — 按编号回溯历史证据。
