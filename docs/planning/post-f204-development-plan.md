# F204 之后的开发计划

状态：2026-08-06；F212 已完成；其余顺序和依赖不构成批量实现授权。

用户已决定暂缓大批量真实模型评测。因此后续先补产品链路的确定性缺口，再恢复低并发、可续跑的
质量证据；不因为评测暂缓就伪造 Recall/MRR 或默认 arm。

## P0：先解除长文档写入阻塞

### F205：native 多 statement 原子事务（已完成）

当前 F200 只证明单 statement autocommit。长文档多 claim 需要 daemon/native backend 真正支持
`BEGIN → 多条 MSQL → COMMIT/ROLLBACK`，并在 IPC、reopen、crash/fault injection、并发和
reference model 下证明 all-or-nothing。没有这项之前，不开放多 claim 长文档的正式写入。
冻结规格见 [F205](./f205-native-multistatement-transaction.md)。

## P1：恢复评测，但不扩大调用压力

### 候选 F206：可续跑的低并发质量复跑

沿用 F183–F185b 的冻结 corpus、隐藏答案、三 arm 和报告格式；增加 rate-limit/backoff、单 worker
或小并发、token 上限和断点续跑。先用一次成功链路验证 Provider，再决定是否恢复小批量，仍不把
INCOMPLETE 报告变成发布质量。

### F212：外置评测数据准备（已完成）

在恢复真实模型复跑之前，先按冻结清单准备 MTRAG、CRUD-RAG、RGB、MIRACL zh 和 EnterpriseRAG-Bench。
所有原始语料、HF parquet、Git checkout、缓存和后续索引放用户指定的外置目录；仓库只保存清单、代码、
版本和摘要。见 [F212](./f212-external-evaluation-data.md)。F213 先冻结跨数据集 retrieval 评分契约，后续
F214 将 MIRACL/MTRAG 转为 suite，后续低并发复评 Feature 消费这些数据。

### F215：低并发 Provider 退避与评测断点（已完成，真实质量证据不完整）

在恢复真实模型复跑前补齐 429/5xx 退避、单 worker 和 hash-bound checkpoint；它只降低失败重试成本，
不把失败题过滤出分母，也不改变真实质量门。

代码、恢复测试和门禁已完成；真实 DeepSeek smoke 已保存 receipt，保留有界 Route Frame 并续跑失败题后
12 题有 8 题成功。下一步
不是扩大样本，而是先修正 Query Agent 的多轮导航/SQL 选择行为，再重新取得可评分的成功矩阵。

### 正式外部检索测评仍缺的桥接层

F212–F214 只交付数据、suite 和 scorer，不等于已经能生成真实 Memora run。以下仍是候选，必须
独立 Review 后实现：

- F216 候选：冻结可承担成本的公开 corpus slice，让内置资料 Agent 经 MSQL 语义吸收，并保存
  `external document ID → Source Receipt/Row locator` 的 evaluator-only 映射；禁止机械 chunk 入库；
- F217 候选：内置 Query Agent 只读取 suite query 与公开 MSQL，把真实 SQL evidence 反解为 external
  document ID，逐题输出可续跑的 `memora.retrieval-run/v1`；ground truth 不进入 Agent；
- F218 候选：为 Token/工具调用降幅冻结同题、同 corpus、同 Provider 预算的基线 runner；没有真实
  baseline run 时，F213 的降幅字段只能保持空，不能从 fixture 外推。

EnterpriseRAG、CRUD-RAG、RGB 的任务适配，以及外部答案正确性框架的公开数据绑定，继续独立规划；
它们已下载不代表已经可评分。

## P2：把 Hook 变成自己的分析工具

### F207：本地指标聚合与报告（已完成）

消费 F204 的脱敏事件，按 session/turn/host/model/Skill 分桶输出调用次数、上下文量、分段耗时、
回退和失败原因；`build-agent-metrics-report` 可读取多个 Hook JSON，并原子输出开发用 JSON/HTML
报告。跨 session topic 身份、写入时机和 worthiness 仍需独立协议；Admin 不是默认承载。

完成规格见 [F207](./f207-local-metrics-report.md)。

## P3：扩展 Agent 能力（按需）

### 候选 F208：隔离的内置评测/资料 Agent driver

Agent 只能经 MSQL port 调用 Memora，文档解析、coverage、review 和提交复用现有组件；Provider、
OCR、浏览器均按需注入，不把运行时或权重塞进主安装包。先作为评测工具，不承诺 `memora ask` 产品化。

### 候选 F209：OCR/视觉可选运行时

只有 F203 返回 `eligible` 且收益超过延迟/成本门槛，才单独 Review OCR provider、权重和安装方式；
否则保持文本层 PDF 的明确失败和人工处理路径。

## P4：长期候选

- 候选 F210：写入时机、ignore/write 和多 claim 质量证据；依赖 F205 与 F207；
- 候选 F211：Database 级 Route Branch 自治 fan-out，初始 n、超量拆分和合并由 Agent 判断并留 revision/理由；
- Compaction、Accelerate、HNSW、Replication、PITR、多设备同步等继续遵守既有证据门，不按数据库功能清单提前实现。

## 当前明确不做

- 不重新启动高并发真实模型评测；
- 不把 OCR、本地 embedding 权重或浏览器运行时并入主安装包；
- 不将 Hook 事件写回 Memora Database，也不采集宿主完整上下文；
- 不在 native 多 statement 事务补齐前开放无原子性的多 claim 旁路写入。

## 关联

- [后续路线](./future-roadmap.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
- [评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
- [F200 EPUB 单链路](./f200-epub-single-chain-acceptance.md)
