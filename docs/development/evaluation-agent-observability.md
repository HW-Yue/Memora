# 内置评测 Agent 与外置 Hook 观测

状态：2026-08-02 方向性结论；实现仍需独立 Feature/ADR Review。

## 目的

Memora 的静态质量依赖 Agent 读取语义目录、逐层选择 Route 并生成 MSQL，不能只用传统
数据库查询脚本代表最终效果。标准评测因此由 Memora 控制的内置评测 Agent 执行；Codex、
Claude Code 等外置 Agent 的能力、Skill 和上下文不同，只作为真实使用观测对象。

这形成两条互补证据链：

```text
冻结题目/快照 -> 内置评测 Agent -> 公开 MSQL -> 收据 -> 隐藏答案评分
外置 Agent 使用 -> Memora 调用 Hook -> session 内真实表现与成本分析
```

## 静态评测权威

内置评测 Agent 是标准化 benchmark host，不要求先成为面向用户的完整 `memora ask` 产品：

- 每个 cell 固定 corpus、Database snapshot、Provider/model、Skill、协议、预算和实验 arm；
- Agent 只能使用与外部宿主相同的公开、版本化 MSQL 能力；
- Agent 不得读取 ground truth，也不得绕过 Parser、Policy、预算、事务或结构化收据；
- Runner 根据隐藏答案重算逐层 Top-1、候选 Recall@K、完整路径、最终 RowID/事实读取成功率；
- 同一题可以比较 Router-only、Lexical、CPU Vector、投机预取和后续候选方案；
- API Key 只来自操作系统密钥存储或进程环境，不进入 Database、日志或报告。

内置 Agent 的模型效果与外置 Agent 在干净、同预算任务上的数据库使用效果应基本可比；外置
宿主额外的工具、历史上下文和自治策略不作为静态发布基准的一部分。

## 外置 Agent Hook

Hook 只观测发往 Memora 的调用及其结构化结果，用来分析真实环境而非生成统一排名：

- 记录显式 `host_session_id`、turn/trace、host、model、Skill/protocol version 和授权 scope；
- 记录 MSQL/Route 操作类型、候选与选择的稳定 ID、回退、调用数、上下文量和分段耗时；
- 不记录 API Key、hidden reasoning、完整宿主上下文或默认保存的用户正文；
- session ID 缺失时标为 unknown，不能把 IPC 连接 ID 猜成长期宿主 session；
- 外置 Agent 的结果按 session、宿主、模型和上下文条件分桶，不能直接替代冻结 benchmark。

跨 session 的同 topic 归因需要另行冻结身份和标注协议；当前不从模型文本自动生成权威
`topic_id`。

## 当前评分范围

近期优先：

- 查询端到端、模型、工具往返、Predictor、Route、MSQL 和存储分段延迟；
- 模型/工具调用数、输入输出上下文、回退与误预测成本；
- Table/Route 候选 Recall@K、逐层分支准确率、完整路径和最终事实读取成功率；
- 同一冻结题目、快照和预算下各实验 arm 的质量—成本曲线。

“何时值得写入、Agent 是否应该主动调用 Memora”暂不进入近期静态门。它更依赖 Canonical
Skill prompt、宿主上下文和模型判断，留到后期通过宿主输入证据、write/ignore ground truth
与真实使用抽样共同评估；当前 Hook 不宣称能计算该项召回率。

## 与内置 Runtime 的关系

本方向不自动重启 F43 的面向用户 Agent Runtime，也不修改 v0 的“外部宿主管理模型密钥”
边界。后续可先实现隔离的 benchmark Agent driver；它通过 Review 后，才评估是否复用为
`memora ask` 的产品 Runtime。

## 关联

- [可选内置 Agent Runtime](../agent/embedded-agent-runtime.md)
- [Route Benchmark Runner v1](./route-benchmark-runner-v1.md)
- [Real Host Contract v1](../agent/real-host-contract-v1.md)
- [AI-native 质量模型与验收](../product/quality-model.md)
