# 当前实现缺口审计

状态：讨论稿，待用户 Review；只记录现状，不代表后续 Feature 已批准或开工。

审计基线：`main` 提交 `7d1863a`。对照产品宪章、当前规划、公开 CLI/Skill、E2E、
Story Gate 与具体实现；不把历史归档或已撤销的 Vector/MATCH 路径算作待实现目标。

## 已经闭环的底座

- macOS Go CLI、daemon、IPC、统一 MSQL Parser/Executor 与稳定 JSON envelope；
- 原生 `.memora` typed Catalog/Row/History/Relation/Table Router；
- 原子 Mutation、尾部崩溃恢复、revision、commit sequence 与逻辑恢复；
- Table Route 逐层导航、RowID 回表、split/merge、来源收据和查询预算 revision；
- Codex/Claude Skill 适配产物、Database package、Wiki 导出、升级门和发行构建；
- SQLite 已移出主程序，语义主路不存在 Vector/cosine/MATCH fallback。

这些能力证明确定性逻辑闭环可以运行，不等于真实模型的长期自治质量已经成立。

## 尚未实现或只完成一部分

| 领域 | 当前已经有 | 尚未实现 |
| --- | --- | --- |
| 真实 AI 用户旅程 | Codex/Claude adapter 与脚本化 F80 Story Gate | 真实 Codex、Claude、Kimi 等模型从自然语言自主选择 Database/Table/Route、决定 worthiness、Schema、split/merge，并验证回答质量 |
| AI-native 质量门 | 八维计分结构、固定场景和脚本 Adapter | 不含 Vector 的真实 Table Router Adapter；fanout/depth/歧义度能力曲线、逐层准确率、共同安全 fanout、token/调用数/延迟和跨模型对照 |
| 持续输入入口 | 显式 `memora reflect` delta/checkpoint/session_end | 宿主可稳定触发的“值得写”判断与会话交接；当前 adapter 只安装 Skill，不保证看见所有宿主活动 |
| 语义 DBA | 手工 Route CRUD、原子 reshape、重复 Row/同义字段/陈旧描述报告 | Router 容量、错挂、漏挂、歧义、导航失败和结构熵检查；基于真实证据生成局部优化计划 |
| 自动维护 | `maintain --report/request` 协议和幂等收据 | 当前实现没有会产生 `auto_fix=true` 的 Issue，`Maintain` 不生成实际 Action；文档所述 Router capacity 与 pending reindex retry 未接入当前代码 |
| Schema 生命周期 | 自描述建库建表、精确同义词复用、Table/Column rename 补偿 | Column 长度调整、类型/NULL/约束变化、删除字段、Row 数据迁移、影响验证和完整 Schema merge |
| 多库发现 | `SHOW DATABASES → SHOW TABLES` | Database 数量增长后的有界分页/目录树/根路由；当前 `SHOW DATABASES` 返回整个授权范围，冷库漏检与上下文成本未解决 |
| Route Frame | 节点数、locator、SELECT 行数配置与宿主状态机 | 字符/token/时间/费用统一执行预算、跨会话 Query Workspace 恢复、真实模型分支回退和不确定性证据 |
| Policy | Database scope、影响行数、revision、Package 安装 hash approval、审计 | L0–L3 风险 Policy、每库自动写权限、Schema/跨库/破坏性操作的统一引擎级审批规则 |
| Database package | pack/install、完整性校验、显式 trusted 安装、只读 manifest 审阅 | `open` 后直接问答、包签名/撤销、版本升级、冲突 merge/fork 策略、History/Source Receipt 携带策略 |
| 备份与恢复产品 | crash-tail 恢复、逻辑 snapshot、升级专用备份/repair | 普通用户可调用的 Instance 备份、搬迁、恢复、定期验证与时间点恢复 |
| 提交变化可视化 | Row History、commit sequence、原子 Mutation | 跨 Row/Schema/Route/membership 的事务级 Committed Change Log、稳定分页和 Admin before/after 时间线 |
| 宿主接入面 | CLI、Skill、Unix socket IPC、Codex/Claude adapter | 可选 MCP adapter、稳定 SDK、launchd 用户服务安装与系统级生命周期集成 |
| 公开发行 | 双架构构建、checksum、发布 workflow 和 clean-machine 测试 | 当前仓库还没有正式 tag/GitHub Release；签名后的真实发布流程尚未实际执行 |
| 原生文件长期运行 | append-only Record、事务 Frame、fsync、重开扫描和内存 ID→offset | 文件 compaction/GC、长期 History 保留下的空间回收、增量打开/checkpoint、热点与大库性能证据 |
| 物理索引与缓存 | 打开时重建通用 `record_id → offset` Map | Catalog 与逻辑 `row_id → latest visible revision` 快速目录、增量打开/checkpoint；当前点查会重组 Catalog，并列举排序全部 Row record ID。Page/B+ Tree/Buffer Pool 由数据后置 |
| 并发数据库内核 | daemon、原子 Mutation、expected revision | 本地单 writer + 多 reader 的最小 MVCC snapshot，以及精确对象排他写锁；范围锁、锁等待、多 writer、物理 Undo/Redo 与死锁检测后置 |
| 同步与灾备 | 稳定逻辑 ID、commit sequence、可携带 snapshot | 在本地 Change Log 之上的 GTID、PITR、多设备增量同步、重放、冲突协议、传输授权与加密 |
| 跨平台 | macOS arm64/amd64 | Linux、Windows、移动端与对应服务/目录/兼容测试；这是明确后置范围 |
| 内置模型 Runtime | 外部宿主统一走 MSQL | `memora ask`、Provider 抽象和模型凭据管理；v0 已明确 defer，不是当前阻塞项 |

## 当前门禁的过度声明

F80 能证明“公开二进制 + 两套 adapter + 同一 MSQL 机械旅程”可运行，但不能独立证明
16 个产品故事全部成立：

- `US-HUMAN` 目前只绑定 `version`；
- `US-CONFLICT` 只绑定一次 History 查询，没有真实矛盾来源、并列展示和用户裁决；
- `US-SCHEMA` 只绑定初始 Schema 创建，没有演化与数据迁移；
- `US-OPTIMIZE` 使用预写好的 split/merge 参数，没有证明 AI 根据导航失败做出正确优化；
- Codex/Claude 报告不调用模型 Provider，Route ID 和每一步 MSQL 都由 Go 测试预先给定。

因此 F80 应理解为“公开运行时机械闭环门”，真实 AI 语义质量仍未验收。

## 文档状态漂移

- `roadmap.md` 仍称 Phase 4 原生底座是当前优先，但 F52–F80 已完成；
- `native-features-transition-review.md` 仍写公开主旅程待修复，已被 F73–F80 覆盖；
- `semantic-health-v1.md` 声称已有 Router capacity/pending reindex 检查，与代码不符；
- `retrieval-quality.md` 仍写 Table Router 与 Database Router 实现待对账，F70/F71 后已过时；
- `installable-database-package.md` 仍出现旧 Database Router、Search Weight 和倒排索引描述；
- `quality-model.md` 正确保留“真实质量门未完成”，但尚未连接 F80 的机械闭环边界。

这些漂移需要先修正文档状态，不能直接从旧“下一 Feature”继续编号实现。

## 建议的讨论顺序

1. 先把 Catalog 解析与 RowID 点查改为可重建内存目录，并冻结本地最小 MVCC 可见性；
2. 再实现事务级 Committed Change Log，并建设本地可视化、只读接口和 Route Trace；
3. 再补真实模型与无向量质量 benchmark，确认 AI 是否找得准、写得对、成本可接受；
4. 再讨论语义 DBA：Router 质量诊断、导航失败反馈和局部优化计划；
5. 再补完整 Schema 演化与 Row 迁移；
6. 再确定持续输入入口、风险 Policy、多库发现与 Query Workspace；
7. 完成 package 问答、备份恢复、正式发行等产品化能力；
8. 最后由规模与故障数据决定 compaction、Page/B+ Tree/Buffer Pool、高级 MVCC/Redo 和远程同步的进入顺序。

任何后续 Feature 都需单独形成待批准计划，用户明确授权后才实现。
