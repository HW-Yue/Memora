# F169 之后的开发序列

状态：2026-08-02 候选总序列；只确定依赖和拆分，不构成整批实现授权。每项仍须独立 Review、
用户授权、RED → GREEN → REFACTOR、验收和合入。

## 当前出发点

- `main` 当前到 F168；
- `feature/F169-single-row-route-leaf` 已有实现与 Admin 资源复核，尚未合入；
- `feature/F170-inverted-index-surface` 只有待批准规格，尚未开始产品代码；
- 内置吸收 Agent 与外部标准测评结论分别位于独立文档分支，尚未合入；
- `.cc-connect/` 是用户未跟踪内容，不属于任何后续 Feature。

先完成分支 Review 和合入，不从多个未合入分支继续堆代码。总体依赖为：

```text
F169 单 Row leaf
→ F170–F174 全内容倒排位置
→ F175–F179 最小 Provider / Runtime / Query Agent
→ F180–F183 外部答案测评与交互查询
→ F184–F194 写入、网页和整本书吸收
→ F195–F199 格式扩展与真实使用观测
```

## Agent 永久依赖边界

`internal/agent/*` 访问 Memora 的唯一端口是版本化 MSQL Request/Result；同进程也必须经过 Parser、
Policy 和事务执行器。Agent 不得 import Catalog、Row、Router、Assimilation、Store、Page、WAL、
MVCC 或索引实现。Provider、SourceStore、Document IR、Session 和 Checkpoint 可以由 Agent 持有，
但不能读取或修改用户 Database。缺失能力先增加 MSQL Feature，再开发对应 Agent 节点。

当前 `assimilation.record/submit/receipt` 私有 IPC 是已识别迁移项，不进入内置 Agent 工具面。
CI 将以 import allowlist 和 fake `MSQLExecutor` 锁定该边界。

## M0：收口当前分支

1. Review F169 的 Leaf → Row 0..1 不变量、迁移、Admin 和全仓证据，合入后更新状态账本；
2. Review 并合入内置吸收 Agent、单网页快速路径和外部测评边界文档；
3. Review F170 的词项、对象范围、revision replacement 和永久边界；只有单项获准后才进入 RED。

## M1：完成倒排索引武器（F170–F174）

| Feature | 唯一主要结果 | 关键完成证据 |
| --- | --- | --- |
| F170 | 无 I/O lexical reference index | 随机 revision 序列与简单 map 对拍 |
| F171 | Page/WAL 持久化 posting store | reopen、corruption、split、fault injection、race |
| F172 | live Row revision 原子替换 posting | insert/revise/delete/supersede 故障矩阵 |
| F173a | Catalog 与 Route revision 接入 posting | rename、alias、delete、move 后无陈旧位置 |
| F173b | 全量 rebuild 与 snapshot 校验 | 在线状态与重建结果字节级对拍 |
| F174 | 有界 MSQL lexical location 查询 | 只返回位置、预算/cursor、最终 SQL 回表 |

F174 以前不让内置 Agent 依赖未冻结的全文查询。倒排结果是候选武器，不成为答案或新真相源。

## M2：最小内置 Query Agent（F175–F179）

| Feature | 唯一主要结果 | 关键边界 |
| --- | --- | --- |
| F175a | Agent MSQL-only port 与依赖守卫 | 禁止引擎包 import；fake executor 可独立运行全部 Agent 测试 |
| F175 | Memora-owned Provider 接口 | 框架/厂商类型不进入 MSQL、Store 或持久协议 |
| F176 | OpenAI-compatible HTTP Provider | 首个 DeepSeek V4 Flash、懒初始化、无厂商 SDK、密钥不落盘 |
| F177 | Runtime spike 与 ADR | Eino 对照薄自研 loop；体积、RSS、取消、checkpoint、许可证实测 |
| F178 | Agent Event / Trace 信封 | run/session/turn、模型、工具、token、费用和分段耗时可重放 |
| F179 | 只读 benchmark Query Agent | 只用公开 MSQL，输出 final answer + 实际 SELECT evidence |

F177 不预设一定采用 Eino。候选必须保持单一 `memora` 发布体验；只引入实际使用的编排能力，
不引入 Retriever、Vector Store、DevOps、全套 Provider 或本地模型。F179 先是隔离 benchmark host，
不能写库，也不立即宣称 `memora ask` 已成为产品能力。

## M3：外部标准测评与查询产品化（F180–F183）

| Feature | 唯一主要结果 | 公开结果 |
| --- | --- | --- |
| F180 | 冻结 answer benchmark corpus/manifest | source、snapshot、问题、隐藏答案和版本身份完整 |
| F181 | 端到端 answer runner | public scorecard 与 private diagnostics 分离 |
| F182 | Ragas Evaluation Adapter | correctness、事实正确性、p50/p95、token、调用数、费用 |
| F183 | 交互式 QuerySession | 测评过门后才提供流式对话、取消和有界会话恢复 |

Ragas/Python 只属于开发和 CI 工具链，不进入安装包。外部成绩以最终答案正确率为首要结果；
Route、RowID、SQL 重试和回退只供内部定位。实际 `SELECT` Row 才能映射为 retrieved context。

## M4：DeepSeek 写入与长资料垂直链（F184–F194）

| Feature | 唯一主要结果 |
| --- | --- |
| F184 | 受 Policy 强制的 write capability profile 与用户审批 |
| F185 | 当前单网页直接阅读、MSQL 写入和回读验证，不创建长任务 |
| F186 | 可持久恢复的 AssimilationJob 状态、Command、Event 和 checkpoint |
| F187 | 内容寻址临时 SourceStore，完成/取消后按策略清理 |
| F188 | 与格式无关的 Document IR v1 和稳定 source anchor |
| F189 | EPUB 确定性适配器，保留 spine、目录、章节、脚注和资源清单 |
| F190 | `ReadExtent` 与 coverage 调度，证明所有必读范围被处理 |
| F190a | 以正式 MSQL 替代内置 Agent 所需的 `assimilation.*` 私有 IPC |
| F191 | DeepSeek 写入 draft/claim ledger 和有来源约束的 MSQL 候选 |
| F192 | 问题即时输出、用户回答、暂停和恢复受影响分支 |
| F193 | 隔离复核、短事务提交、全局 reconciliation 和 Source Receipt |
| F194 | 从干净 snapshot 吸收整本 EPUB，再由 Ragas 评分隐藏问题 |

写入实验固定 `deepseek-v4-flash` 的精确模式和价格快照；比较写入策略时 Query Agent 保持固定。
隐藏答案不提供给写入模型，确定性 ground truth 或独立 evaluator 负责评分，不能让模型自证。

## M5：按证据扩展（F195–F199）

- F195：DOCX 适配器；
- F196：带文本层 PDF 适配器；
- F197：扫描页 OCR/视觉路径证据门，只有真实不可读比例与质量收益支持时才实现或打包可选资源；
- F198：外置 Agent Hook，只采集 Memora 调用和有界结果；
- F199：Admin 私有诊断视图，按 run/session/model 查看写入、Route、MSQL、耗时和成本。

DOCX、PDF、OCR 不在 EPUB 垂直链跑通前并行堆积。OCR 模型、浏览器运行时和本地 Embedding 权重
默认不进入主安装包。

## 每项共同完成门

- 一个 Feature 一个主要结果，从最新 `main` 创建独立分支；
- 先观察目标测试因能力缺失而失败，再写最小实现；
- Provider/Agent 使用 fake server 与确定性 transcript 测协议，真实 API 证据单独保存且不泄露密钥；
- Page/WAL/索引 Feature 必须有 reopen、corruption、fault、reference model 和 race 中适用的证据；
- 报告固定代码、数据、snapshot、模型、prompt/Skill、预算、评测器和价格版本；
- Review、测试、文档和完成结论全部通过后才合入，后一个 Feature 不构成前一个的默认授权。

## 关联

- [Feature 状态](./feature-status.md)
- [后续路线](./future-roadmap.md)
- [Feature 产品门](./feature-product-gate.md)
- [TDD 协议](./feature-tdd-protocol.md)
- [Agent 的 MSQL 边界与依赖注入](../agent/agent-msql-dependency-injection.md)
- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
