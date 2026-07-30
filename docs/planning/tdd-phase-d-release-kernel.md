# Phase D：发行、质量门与原生内核

目标：先交付可安全安装的 macOS 产品；AI-native 质量达标后，再用原生内核替换原型 Store。

## F44 Database Pack/Open/Install（已完成）

先测：pack 哈希、只读 open、install、同 ID/同名冲突、不可信包、损坏包和索引删除重建。

开发：实现版本化 package manifest 与 MSQL 管理语句；包不携带代码和密钥。

提交：`feat(F44): package portable databases`

## F45 Wiki 确定性导出（已完成）

先测：相同 snapshot 输出哈希一致、稳定链接、rename、删除、跨库关系和增量导出。

开发：实现单向 Export Profile；第一版不回流人类编辑。

提交：`feat(F45): export deterministic Obsidian wiki`

## F46 安全与隐私门（已完成）

先测：数据库 scope、外部模型禁用、路径穿越、日志脱敏、恶意 package 文本和高风险审批绕过。

开发：完成 Policy enforcement、审计日志和 doctor 安全检查。

提交：`feat(F46): enforce local privacy boundaries`

## F47 macOS Release 制品（已完成）

先测：arm64/amd64 二进制在干净 VM 启动；版本可追踪；checksum、损坏下载和 Gatekeeper 提示可诊断。

开发：构建压缩包、SHA-256 清单、许可、版本元数据和可复现构建说明。

提交：`build(F47): produce macOS release artifacts`

## F48 GitHub Release 自动化（已完成）

先测：tag 规则、重复发布、缺失制品、checksum 不匹配和 release smoke；PR 不得发布。

开发：GitHub Actions 在签名 tag 上测试、构建、生成 Release notes、上传制品与 Skill bundle。

提交：`ci(F48): publish verified GitHub releases`

## F49 升级与回滚（已完成）

先测：旧 binary/新 datadir、新 binary/旧 datadir、中断迁移、备份恢复和不支持降级。

开发：实现 format compatibility check、迁移计划、备份点和 `doctor repair` 的安全边界。

提交：`feat(F49): upgrade instances transactionally`

## F50 干净机器验收（已完成）

先测：全新 macOS 用户只安装 Skill，授权后下载 Release、init、启动 daemon、总结项目、重启再查询。

开发：自动化 VM 流程、安装诊断包和发布阻断报告。

提交：`test(F50): verify zero-to-first-memory journey`

## F51 AI-native 发布门（结论已撤销）

历史实现包含字符向量与 cosine，对 Memora 的路径也不是 AI 逐层 SQL 导航，
违反产品宪章。代码和报告只作待清理的审计材料，不能作为通过证据。

返工必须从 `US-COLD`、`US-READ`、`US-INSERT`、`US-UPDATE`、`US-DELETE`、
`US-SPLIT` 等真实旅程出发，
固定每一步 MSQL、Route Frame 和最终数据库状态；禁止 Vector baseline。

原提交：`test(F51): enforce AI-native release gate`（不再代表 Feature 完成）

## F52–F61 原生内核（暂停并待重排）

在以下条件同时满足前，不继续实施：

- 产品宪章与现有 Router/MATCH/Skill 查询主路径完成架构对账；
- Table 级语义树逐层 SQL 旅程有真实端到端证据；
- F51 按新门禁重做并通过；
- SQLite 原型的保留或退出路径已向用户披露并确认。

下列条目保留为候选技术拆分，不代表已批准的下一步顺序。

## F52 原生格式契约（候选）

先测：Page/Record golden、checksum、endianness、未知版本拒绝、逻辑 snapshot round-trip 和随机 decode 不崩溃。

开发：冻结 format v1、Page/Extent/Tablespace 编码和升级 ADR。

提交：`feat(F52): define native storage format v1`

## F53 Page 与 Buffer Pool（候选）

先测：分配/回收、page split、LRU young/old、pin、脏页、并发访问、磁盘满和 checksum 损坏。

开发：实现 file-per-table Tablespace、Page manager、Buffer Pool 和 Page Cleaner。

提交：`feat(F53): persist pages through buffer pool`

## F54 B+ Tree 与 Record（候选）

先测：随机插删改、split/merge、范围扫描、重复键、overflow、重启和模型校验。

开发：实现 Clustered/Secondary B+ Tree、稳定 row locator 和 Record 编码。

提交：`feat(F54): store records in native B+ trees`

## F55 锁、MVCC 与 Undo（候选）

先测：RR/RC 可见性、并发写、FOR UPDATE、回滚、长快照、死锁和 Purge 安全点。

开发：实现 transaction ID、commit sequence、锁管理和 Undo version chain。

提交：`feat(F55): provide native MVCC and undo`

## F56 Redo 与崩溃恢复（候选）

先测：每个 WAL 边界 kill -9、partial page、torn write、重复恢复和未提交事务回滚。

开发：实现 WAL、LSN、checkpoint、doublewrite/等价保护和恢复状态机。

提交：`feat(F56): recover native storage with redo`

## F57 Binlog 与提交协议（候选）

先测：Redo prepare/Binlog/Redo commit 各故障点、Group Commit 顺序、GTID 幂等和重放。

开发：实现 Row-based Binlog、内部两阶段提交和同步保留位点。

提交：`feat(F57): commit durable row binlog`

## F58 原生语义索引（候选）

先测：Posting Run、tombstone、generation manifest、事务可见、后台重建、崩溃发布和 GC。

开发：把经产品门确认的 Table 级 Router 索引迁到原生索引文件，保持上层
MSQL contract 不变；不迁移被撤销的向量或混合匹配主路径。

提交：`feat(F58): migrate semantic indexes to native runs`

## F59 Compaction 与维护（候选）

先测：History 不被 Purge、Undo 安全回收、旧 generation reader、空间上限、暂停恢复和 I/O 限流。

开发：实现 Purge、compaction、generation GC、后台调度和可观测状态。

提交：`feat(F59): compact native storage safely`

## F60 原型到原生迁移（候选）

先测：真实 v0 fixture 迁移后 Row/history/relation/Router 查询等价；中断可恢复；原文件保留到验证成功。

开发：实现 logical snapshot 导入、双重校验、原子切换和回滚工具。

提交：`feat(F60): migrate prototype instances to native engine`

## F61 原生内核发布门（候选）

先测：长时间并发、故障注入、fuzz、恢复、格式兼容、性能和 AI-native 全套回归。

开发：切换默认 Store，保留只读迁移工具；发布兼容矩阵和恢复手册。

提交：`feat(F61): make native engine production default`

## Phase D 退出测试

用户可从 GitHub Release 安装稳定 v0；升级不会丢数据。原生内核只有在产品质量与存储正确性两套门禁同时通过后才成为默认。
