# F81 之后的小 Feature 规划

状态：F81–F150、F152 已完成；F151/F153–F160 已评估并延后，下一项为 F161 Multi-device Sync evidence gate。用户已于
2026-08-01 明确授权持续执行至 F163，不在中间 Feature 等待重复授权。一个 Feature
只交付一个可独立测试、验收、合入和回滚的主要结果。Milestone 只表达依赖，不允许
合并实施。

Table Router 仍是权威语义结构；ADR-0007 允许 Catalog、字面位置和 Route-only
Vector 作为可回退候选预测器，禁止 Row/chunk Vector、隐藏答案融合和全文 prompt 扫描。

## K：存储内核

| Feature | 唯一主要结果 |
| --- | --- |
| F81 Page Codec | 16 KiB Page 字节格式可确定编解码 |
| F82 Page File Manager | Page 可按 identity 定位读写并安全 reopen |
| F83 WAL Record Stream | WAL segment 可追加、扫描并拒绝损坏尾部 |
| F84 WAL Durable Transaction | 事务只有 durable COMMIT 后才报告成功 |
| F85 Crash Recovery | 重启只重放完整已提交事务且幂等 |
| F86a WAL Segment Set | 多个 Segment 可显式 roll、重开并保持全局顺序 |
| F86b Checkpoint Publish | Page durability barrier 后固定恢复起点 |
| F86c Segment Reclaim | 只删除 checkpoint 完全覆盖的旧 Segment |
| F87 Buffer Pool Page Loading | 同一 Page single-flight 装载并可 pin/unpin |
| F88 Buffer Pool Eviction | 有界 young/old LRU 不淘汰 pinned Frame |
| F89 Dirty Page Flush | dirty Page 严守 WAL-before-data |
| F90 B+ Tree Node Codec | internal/leaf Page 编码及不变量可验证 |
| F91 B+ Tree Point Search | 明确 key 可从 root 精确定位到 leaf |
| F92 B+ Tree Range Cursor | 叶链可有界、有序、可续读 |
| F93 B+ Tree Insert | 未满节点内插入/替换保持有序 |
| F94 B+ Tree Split | leaf/internal split 与 root grow 正确 |
| F95 B+ Tree Delete | 删除 key 后查询结果正确 |
| F96 B+ Tree Rebalance | borrow/merge/root shrink 恢复树不变量 |
| F97a B+ Tree Mutation Plan | 多层 mutation 生成零共享写入的私有 Page 计划（已完成） |
| F97b1 Durable WAL Frontier | 独立 control 保存可信 durable byte boundary（已完成） |
| F97b2 Repairing Open | 严格保留 frontier 前缀并清理 speculative tail（已完成） |
| F97c1 Tree Control Codec | slot 1 可保存 versioned root/allocator control（已完成） |
| F97c2 Root/Allocator Redo Codec | metadata payload 可确定编解码（已完成） |
| F97c3 Tree Metadata Recovery | root/allocator metadata 可随 committed WAL 幂等恢复（已完成） |
| F97c4 Tree Revision Separation | physical generation 与逐提交 revision 分离（已完成） |
| F97d1–F97d3 Durable Tree Commit | 已完成：prepare、atomic publish 与 WAL/reopen runtime |

## R：真实 RowID 数据路径

| Feature | 唯一主要结果 |
| --- | --- |
| F98 Catalog Lookup Index | 已完成：Database/Table/Schema identity 由持久化 B+ Tree 定位 |
| F99 Current Row Index | 已完成：Table ID + RowID 精确定位当前 revision |
| F100 Row Version Index | 已完成：RowID + revision/sequence 精确定位历史版本 |
| F101 Table Cursor Index | 已完成：Table 内 live/tombstone Row 可稳定分页 |
| F102 MSQL Point-Get Switch | 已完成：exact RowID `SELECT` 只走已配置的新索引路径 |
| F103 Snapshot Visibility | 已完成：reader 固定 committed snapshot 并读取自身写入 |
| F104 Exact-Object Write Lock | 已完成：同一 Row/Schema/Route 的写入互斥 |
| F105 Legacy Store Migration Reader | 已完成：只读枚举并生成 source-bound 迁移计划 |
| F106 Page Store Migration | 已完成：Plan staging、重验并原子发布三树 generation |
| F107 Page Store Default Switch | 已完成：新 Instance 与已迁移 Instance 只以 Page Store 为查询 authority |
| F108 COW Generation Replacement | 已完成：rebuild 失败保留旧 root，成功原子切换 generation |
| F109 Committed Change Log | 已完成：每个 commit 形成一个完整逻辑变化 envelope |

详细 RED 与故障门见[存储内核小 Feature 计划](./row-read-foundation-feature-plan.md)。

## V：Admin 与可观察性

| Feature | 唯一主要结果 |
| --- | --- |
| F110 Metadata Read Protocol | Admin 可用有界 MSQL 读取 Database/Table/Schema |
| F111 Route Read Protocol | Admin 可按层读取 Route 节点与 locator |
| F112 Row Detail Read Protocol | 已完成：Admin 可按 RowID 读取动态字段与 History |
| F113 Change Read Protocol | 已完成：Admin 可按固定 snapshot commit cursor 读取变化事务 |
| F114 Trace Read Protocol | 已完成：Admin 可有界读取可清理且脱敏的 Route Trace |
| F115 Local Read API | 已完成：固定 scope 的临时 loopback API 安全执行只读 MSQL |
| F116 Embedded Admin Shell | 已完成：Go binary 离线内嵌、校验并启动安全前端壳 |
| F117 Catalog Navigation | 已完成：用户能浏览 Instance→Database→Table→Schema |
| F118 Route Tree Browser | 已完成：用户能逐层展开语义索引并看到 locator |
| F119 Row Document View | 已完成：用户能打开 RowID 查看完整动态文档结构 |
| F120 Change Timeline | 已完成：用户能按事务顺序浏览数据与索引变化 |
| F121 Revision Diff | 已完成：用户能比较 Row revision pair；Route 待历史读取协议 |
| F122 Route Trace View | 已完成：用户能复现 AI 每层选择、回退与预算 |

## Q：真实模型质量

| Feature | 唯一主要结果 |
| --- | --- |
| F123 Real Host Contract | 已完成：Codex/Claude/Kimi 以同一 Skill/任务契约运行 |
| F124 Route Benchmark Corpus | 已完成：固定 Route 树、问题和期望路径可重复生成 |
| F124a Discovery Frame Contract | 已完成：候选位置共享 snapshot、预算和 predictor provenance |
| F124b Lexical Route Locations | 已完成：倒排词项只聚合到 Database/Table/Route 位置 |
| F124c Route Vector Generation | 已完成：Route-only 向量作为可重建派生 generation |
| F124d CPU Exact Route Match | 已完成：CPU 精确点积返回稳定 Top K Route ID |
| F124e Speculative Discovery Skill | 已完成：Skill 组合候选与根 Route，miss 后确定性回退 |
| F125 Route Benchmark Runner | 已完成：真实 host/model 五 arm 盲测矩阵与可重验原始报告 |
| F126 Route Capability Report | 已完成：分桶能力、成本曲线与共同默认门；无真实 source 时保持 Router |
| F127 Story Gate v2 | 已完成证据契约：每个 `US-*` 要求双宿主旅程；真实 evidence 当前 INCOMPLETE |

F124a–F124e 的 RED、边界和顺序见
[Route Predictor 小 Feature 计划](./route-predictor-feature-plan.md)。

## A：语义自治候选

| Feature | 唯一主要结果 |
| --- | --- |
| F128 Semantic Health Scan | 已完成：有界发现 Route、membership 与 Schema 结构债务 |
| F129 Route Mutation Plan | 已完成：只读生成绑定 snapshot/hash 的局部 split/merge/move 计划 |
| F130 Route Plan Execution | 已完成：原子执行已批准 Route 计划并验证覆盖率 |
| F131 Schema Change Plan | 已完成：为 Column/约束演化生成 snapshot-bound 兼容性计划 |
| F132 Schema Migration Execution | 已完成：审批后重验并原子执行 Schema 计划，返回验证/补偿收据 |
| F133 Host Input Capture | 已完成：宿主以稳定 receipt 提交有界 pending 候选且不写数据库事实 |
| F134 Worthiness Decision | 已完成：AI 以绑定 capture 和 verified Mutation Receipt 的稳定收据终结候选 |
| F135 Scalable Database Discovery | 已完成：Catalog Atlas 双预算分页并以 complete coverage 保证冷库可达 |
| F136 Policy Enforcement v2 | 已完成：L0–L3、逐库上限与动态对象由引擎确定性强制 |

## P：产品化候选

| Feature | 唯一主要结果 |
| --- | --- |
| F137 Package Signature | 已完成：Database Package 可携带确定性 Ed25519 签名并离线验证 |
| F138 Package Install | 已完成：已验证包原子安装为引擎强制的持久化只读 Database |
| F139 Package Upgrade | 已完成：已安装只读包可按 current/package-bound 计划审批并原子升级 |
| F140 Package Revocation | 已完成：durable package/signer 撤销阻止安装与升级且保留只读取证 |
| F141 Package Fork | 已完成：只读安装库可按确定性身份 remap 派生独立可写分支 |
| F142 Package Merge | 已完成：fork 以显式三方计划合并非重叠变化并确定性报告冲突 |
| F143 Instance Backup | 已完成：生成严格布局、逐文件哈希并发布后重验的可搬迁备份 |
| F144 Instance Restore | 已完成：备份可经 staging 重验原子恢复到明确且不存在的目标 |
| F145 Instance Move | 已完成：离线 backup/restore 编排保持 daemon 状态且永不自动删除源 |
| F146 MCP Adapter | 已完成：现代/legacy MCP stdio 以唯一工具复用 daemon MSQL 与授权边界 |
| F147 Go SDK | 已完成：公开版本化 Request/Envelope Client 以长连接复用 daemon IPC |
| F148 launchd Integration | 已完成：每 Instance 用户 LaunchAgent 可原子安装、状态查询与安全卸载 |
| F149 Release Artifacts | 已完成：双架构可复现集合以显式 trust root 验证 Ed25519 detached signature |
| F150 Public Release Publish | 已完成：验证后的 tag 仅在显式 publication trust root 通过后原子发布七项资产 |

## E：必须由证据触发

| Feature | 进入条件 |
| --- | --- |
| F151 Compaction | 已评估并延后：256 locator × 16 轮等宽更新为 1.00x，未越过 1.25x 空间放大门 |
| F152 Free Page Reuse | 已完成：10% Catalog churn 的 22.22% 浪费越门，recycled/durable free reuse 将结果降至 0% |
| F153 Secondary Indexes | 已评估并延后：65 个 canonical turn 无 10k+ 非 RowID predicate 明确需求 |
| F154 Buffer Pool Scaling | 已评估并延后：M4 parallel hot-hit 最慢 236.8 ns/op，未越 5 µs 门 |
| F155 Advanced I/O Scheduler | 已评估并延后：M4 串行 1 MiB dirty batch 最慢 359.502 µs，未越 5 ms 门 |
| F156 Physical Undo | 已评估并延后：生产路径保持 durable commit 后 publish、no-steal 与 immutable revision |
| F157 Advanced MVCC | 已评估并延后：65 个 canonical turn 无 multi-writer/强隔离需求，现有 snapshot 对拍通过 |
| F158 Lock Waits/Deadlock | 已评估并延后：65 turn 无等待需求，opposite-order 保持 one-winner/no-deadlock |
| F159 Replication | 已评估并延后：65 turn 无 primary/replica/failover 故事，Change Log 仅保留为未来输入 |
| F160 PITR | 已评估并延后：65 turn 无时间点恢复/RPO/RTO 故事，latest backup 与 History 不冒充 PITR |
| F161 Multi-device Sync | 明确出现多设备双向同步故事 |
| F162 Apple Accelerate Route Scan | 纯 Go CPU exact 的 p95/能耗越过冻结门槛且结果等价 |
| F163 HNSW Route Backend | CPU exact 在真实 Route 规模下越过资源门且 ANN Recall 通过 |

## 批准与执行规则

1. 当前下一项是 F161；持续执行授权覆盖 F110–F163，不在中间 Feature 停工等待重复授权；
2. 每项先提交产品门、精确 RED 清单与失败证据；
3. 最小 GREEN 后补齐边界/故障测试，再独立合入 `main`；
4. 出现第二个主要结果、独立协议、故障域或用户旅程时立即拆 Feature；
5. 候选 Feature 临近开工时仍需补完整规格，不能按本表直接实施。
6. F151–F163 到达时必须执行可复现的进入条件基准；条件成立则实现，条件不成立则
   固化基准、环境和延后结论并继续下一项，不得把证据未成立当作停止整个路线的理由。
