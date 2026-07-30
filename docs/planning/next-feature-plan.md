# F81 之后的 Feature 规划

状态：讨论稿，待用户 Review；这里只规划顺序，不授权实现。

主线先让用户“看得见”，再证明真实 AI“做得对”，之后才扩大自治和物理内核。
所有语义检索继续禁止 Embedding、Vector、cosine、隐藏评分和全文 prompt 扫描。

## Milestone V：可视化与可观察性

| Feature | 用户结果 | 完成门 |
| --- | --- | --- |
| F81 Inspection MSQL Read Model | 所有真实内容可以稳定、分页、按权限读取 | 10k Row/深 Route/History 有界分页，无 Store 旁路 |
| F82 Local Read API | 本地 UI 和工具有统一只读接口 | loopback、固定 scope、只读 AST、CLI/API envelope 等价 |
| F83 Memora Studio v1 | 用户看到 Database、Schema、Row、History、Relation、来源和 Route Tree | 发行二进制完成干净 Instance 可视化旅程 |
| F84 Route Trace | 用户看到 AI 每层 Route 选择、RowID 和 SQL | 两个宿主真实 trace 可复现、无 prompt/正文泄漏 |

详细边界见[数据可视化与本地观察接口计划](./visual-inspection-feature-plan.md)。

## Milestone Q：真实 AI 质量

| Feature | 用户结果 | 完成门 |
| --- | --- | --- |
| F85 Real Host Run Protocol | Codex、Claude、Kimi 等由宿主实际运行统一任务 | Memora 不接收 Key；自定义 OpenAI-compatible 地址可由宿主使用 |
| F86 No-vector Quality Benchmark v2 | 得到真实 Recall、误写、Schema 熵、调用数、token、延迟和费用 | Table Route 实际选择产生原始证据，禁止 scripted counts 冒充 |
| F87 Story Gate v2 | 每个 `US-*` 都由相符旅程验收 | 修正 F80 的宽松映射；未覆盖故事明确 `INCOMPLETE` |

F85 不内置 Provider。宿主负责模型与 CC Switch 配置，Memora 只验证 MSQL、Route
Trace、结果和收据。

## Milestone A：语义自治

| Feature | 用户结果 | 完成门 |
| --- | --- | --- |
| F88 Semantic Health v2 | 发现 Route 拥挤、空叶、错挂/漏挂、不可达 Row、陈旧 membership 和 Schema 债务 | 文档与代码 Issue kind 一致；不再出现永远 noop 的维护接口 |
| F89 Route Optimization Plan | AI 根据真实失败与成本提出局部 split/merge/move | Studio 预览影响；expected revision；原子执行；从 root 复验质量 |
| F90 Schema Evolution v2 | AI 调整 Column 长度/类型/NULL/约束并迁移 Row | 影响预览、受限 DDL、数据迁移、失败补偿和旧查询验证 |
| F91 Host Input & Worthiness | 稳定结论在正确时机进入 Memora，瞬时信息被忽略 | 显式宿主事件覆盖 checkpoint/session end；50 轮误写率可量化 |
| F92 Scalable Database Discovery | Database 增长后仍能有界发现冷库 | 分页或分层目录经真实 benchmark 选择，不把全库目录放入 prompt |
| F93 Policy v2 | 用户决定哪些操作可自动、需审批或禁止 | L0–L3、每库 scope、Schema/跨库/破坏操作均由引擎强制 |

F88–F90 优先于 F91–F93，因为可视化 trace 会先提供真实维护证据。

## Milestone P：产品化

| Feature | 用户结果 | 完成门 |
| --- | --- | --- |
| F94 Package Lifecycle v2 | 包可签名、升级、撤销、fork/merge，并能在 `open` 后问答 | 不可信包仍无代码/权限；冲突不静默覆盖 |
| F95 Backup, Move & Restore | 用户可备份、搬迁、验证和恢复整个 Instance | 独立命令、可验证清单、故障注入、恢复后 root Route 复查 |
| F96 MCP/SDK/launchd | 第三方 Agent 和 macOS 能稳定管理本地服务 | 全部复用 MSQL/IPC；无第二套业务协议 |
| F97 Signed Public Release | 普通用户可下载安装真实版本 | 签名 tag、双架构 clean-machine、许可和 GitHub Release 实际发布 |

Wiki 双向回流、内置 `memora ask` 和跨平台继续单独 Review，不自动进入本批。

## Milestone E：由数据触发的存储演进

以下仅是候选，必须由 Studio/Benchmark 暴露的规模或故障数据触发：

| Feature | 触发条件 | 边界 |
| --- | --- | --- |
| F98 Native Compaction & Open Checkpoint | append-only 空间或重启扫描超门槛 | 永久 History 不丢失，崩溃时仍可回到旧 generation |
| F99 Page/B+ Tree/Buffer Pool | Row 数、范围查询或 I/O 证明内存目录不足 | 不改变 MSQL、RowID 或 Route 语义 |
| F100 MVCC/Undo/Redo/Locks | 真实多 writer 和隔离需求成立 | 先冻结隔离/恢复故事，再选物理算法 |
| F101 Binlog/PITR/Multi-device | 明确跨设备与时间点恢复产品需求 | GTID、幂等、冲突、加密和保留策略先 Review |

## 建议批准批次

1. 第一批只批准 F81–F84，先获得可视化和真实观察能力；
2. 第二批 Review F85–F87，用真实模型和质量数据纠正产品判断；
3. 第三批 Review F88–F90，建设可解释的 Route/Schema 自治；
4. F91 以后根据前三批证据重新排序，不一次性授权到底。

