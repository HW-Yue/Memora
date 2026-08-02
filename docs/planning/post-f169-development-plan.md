# F169 之后的开发序列

状态：2026-08-03 候选总序列；只确定依赖和拆分，不构成整批实现授权。每项仍须独立 Review、
用户授权、RED → GREEN → REFACTOR、验收和合入。

## 当前出发点

- `main` 当前到 F173c，Catalog/Route/Row lexical generation、在线 publication 与显式 rebuild 已通过完整 CI 并合入；
- 内置 Agent、资料吸收、外部标准测评和 MSQL-only 边界文档已合入 `main`；
- F170–F173c 已完成；下一项从最新 `main` 单独 Review F174 有界 MSQL lexical location query；
- `.cc-connect/` 是用户未跟踪内容，不属于任何后续 Feature。

每项继续从最新 `main` 建独立分支，不从多个未合入分支堆代码。
总体依赖为：

```text
F169 单 Row leaf（已完成）
→ F170–F174 全内容倒排位置
→ F175a–F175c 统一 MSQL 执行入口与 Agent 依赖守卫
→ F176–F181 Bootstrap / Provider / Trace / 最小 Query Agent
→ F182–F186 外部答案测评、发布门与交互查询
→ F187–F200 单网页写入、整本书吸收与 EPUB 质量门
→ F201–F204 格式扩展与真实使用观测
```

## Agent 永久依赖边界

`internal/agent/*` 访问 Memora 的唯一端口是版本化 MSQL Request/Result；同进程也必须经过 Parser、
Policy 和事务执行器。Agent 不得 import Catalog、Row、Router、Assimilation、Store、Page、WAL、
MVCC 或索引实现。Provider、SourceStore、Document IR、Session 和 Checkpoint 可以由 Agent 持有，
但不能读取或修改用户 Database。缺失能力先增加 MSQL Feature，再开发对应 Agent 节点。

当前 `assimilation.record/submit/receipt` 私有 IPC 是已识别迁移项，不进入内置 Agent 工具面。
CI 将以 import allowlist 和 fake `MSQLExecutor` 锁定该边界。

## M0：前置收口（已完成）

1. 已校验并合入 Agent、资料吸收、外部评测和后续 Feature 规划文档；
2. 已从最新 `main` 重建 F170 的独立开发分支；
3. F170 的词项、对象范围、snapshot seed、revision replacement 和永久边界已通过 Review。

## M1：完成倒排索引武器（F170–F174）

| Feature | 唯一主要结果 | 关键完成证据 |
| --- | --- | --- |
| F170（已完成） | 无 I/O lexical reference index | 随机 revision 序列与简单 map 对拍 |
| F171（已完成） | Page/WAL 持久化 posting store | reopen、corruption、split、fault injection、race |
| F172a（已完成） | Row 投影与四树 generation v2 | Plan/reference、v1 COW 升级、staging fault |
| F172b（已完成） | live Row revision 原子替换 posting | insert/revise/delete/supersede 故障矩阵 |
| F173a（已完成） | Catalog revision 接入 posting | Database/Table/Column rename、alias、drop 后无陈旧位置 |
| F173b1（已完成） | Route 投影、generation v3 与 reopen | v2 增量/COW 升级、删除恢复后无陈旧位置 |
| F173b2（已完成） | live Route revision 原子发布 posting | direct CRUD、Route Plan、reshape 的 fault/reopen |
| F173c（已完成） | 全量 rebuild 与 snapshot 校验 | 在线状态与重建结果规范摘要对拍 |
| F174 | 有界 MSQL lexical location 查询 | 只返回位置、预算/cursor、最终 SQL 回表 |

F174 以前不让内置 Agent 依赖未冻结的全文查询。倒排结果是候选武器，不成为答案或新真相源。

## M2：统一执行入口与查询上下文（F175a–F178）

| Feature | 唯一主要结果 | 关键完成证据 |
| --- | --- | --- |
| F175a | 中立 `protocol/msql` | SDK wire golden 不变，协议不依赖 runtime/internal |
| F175b | 单实例共享 `MSQLService` | IPC/adapter parity、独立 Session、取消/回滚、并发与 race |
| F175c | Agent MSQL-only port 与 fake harness | import allowlist；Agent 测试不打开 Instance |
| F176 | 确定性 Query Bootstrap Frame | Atlas + lexical + 可选根 Route 预取；snapshot/byte budget/回退 |
| F177 | Memora-owned Provider port | scripted fake；框架/厂商类型不进入数据库协议 |
| F178 | Agent Event / Trace / Usage 信封 | 第一次真实模型调用前已可重放调用、token、费用和分段耗时 |

F175 原范围拆成三个 Feature，因为 wire 兼容、共享服务并发和 import 边界是三个独立故障域。
F176 在没有 API Key 时先冻结首轮上下文；默认一次给模型完整有界 Catalog Atlas、lexical
locations 和少量投机根 Route，减少先选库再逐表询问造成的模型续推。

## M3：最小模型闭环与外部评分（F179–F186）

| Feature | 唯一主要结果 | 公开结果 |
| --- | --- | --- |
| F179 | Runtime spike 与 ADR | Eino 对照薄自研 loop 的体积、RSS、取消、checkpoint、许可证 |
| F180 | OpenAI-compatible HTTP Provider | Kimi 真实 smoke、懒初始化、无厂商 SDK、密钥不落盘 |
| F181 | 只读 benchmark Query Agent | 只用 MSQL，输出 final answer + SELECT evidence + Trace |
| F182 | 冻结 answer corpus/manifest | source、snapshot、问题、隐藏答案、版本与许可完整 |
| F183 | 端到端 answer runner | public scorecard 与 private diagnostics 分离 |
| F184 | Ragas 等外部评分 adapter | correctness、事实正确性、p50/p95、token、调用数、费用 |
| F185 | Query Agent release gate | 固定阈值比较 Router-only、Lexical、Vector 与预取 |
| F186 | 交互式 QuerySession | 门通过后提供流式事件、取消、预算和有界恢复 |

F179 不预设一定采用 Eino。候选必须保持单一 `memora` 发布体验；只引入实际使用的编排能力，
不引入 Retriever、Vector Store、DevOps、全套 Provider 或本地模型。F181 先是隔离 benchmark host，
不能写库，也不立即宣称 `memora ask` 已成为产品能力。F180 的 Key 只由环境或后续 SecretResolver
注入；当前开发优先兼容 Kimi 官方 OpenAI-compatible API，不把模型名写死进数据库协议。

Ragas/Python 只属于开发和 CI 工具链，不进入安装包。外部成绩以最终答案正确率为首要结果；
Route、RowID、SQL 重试和回退只供内部定位。实际 `SELECT` Row 才能映射为 retrieved context。

## M4：单网页写入与整本书垂直链（F187–F200）

| Feature | 唯一主要结果 |
| --- | --- |
| F187 | 受 Policy 强制的 write profile 与用户审批 |
| F188 | 当前单网页/短文本直接写入与 SELECT 回读验证，不创建长任务 |
| F189 | Source intake 交互与即时事件协议 |
| F190 | 可持久恢复的 AssimilationJob 状态、Command、Event 和 checkpoint |
| F191 | 内容寻址临时 SourceStore，完成/取消后按策略清理 |
| F192 | 与格式无关的 Document IR v1 和稳定 source anchor |
| F193 | EPUB 确定性适配器，保留 spine、目录、章节、脚注和资源清单 |
| F194 | `ReadExtent` 与 coverage 调度，证明所有必读范围被处理 |
| F195 | 以正式 MSQL 替代 Agent 所需的 `assimilation.*` 私有 IPC |
| F196 | 可替换 Provider 的 draft/claim ledger 和有来源约束的 MSQL 候选 |
| F197 | 问题即时输出、用户回答、暂停和恢复受影响分支 |
| F198 | author/reviewer 隔离的独立 review gate |
| F199 | 短事务 reconciliation、in-doubt 恢复和 Source Receipt |
| F200 | 从干净 snapshot 吸收整本 EPUB，再由固定 Query Agent 评分隐藏问题 |

写入实验固定实际 Provider/model/mode 和价格快照；Kimi、DeepSeek 可经相同 Provider port 比较，
不把任一厂商写进 Agent 核心。比较写入策略时 Query Agent 保持固定。
隐藏答案不提供给写入模型，确定性 ground truth 或独立 evaluator 负责评分，不能让模型自证。

## M5：按证据扩展（F201–F204）

- F201：DOCX 适配器；
- F202：带文本层 PDF 适配器；
- F203：扫描页 OCR/视觉路径证据门，只有真实不可读比例与质量收益支持时才实现或打包可选资源；
- F204：外置 Agent Hook，只采集 Memora 调用和有界结果；

DOCX、PDF、OCR 不在 EPUB 垂直链跑通前并行堆积。OCR 模型、浏览器运行时和本地 Embedding 权重
默认不进入主安装包。Trace 先由评测 runner 输出开发用 JSON/HTML；当前不规划 Admin 迭代。

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
- [查询 Agent Feature 序列](./query-agent-feature-sequence.md)
- [资料吸收 Agent Feature 序列](./assimilation-agent-feature-sequence.md)
- [Agent 的 MSQL 边界与依赖注入](../agent/agent-msql-dependency-injection.md)
- [内置评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
