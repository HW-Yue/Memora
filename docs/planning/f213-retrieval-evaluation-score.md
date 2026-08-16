# F213：外部检索评分与对照报告

状态：已完成；2026-08-06 冻结并验收。2026-08-11 按
[ADR-0010](../decisions/0010-small-scale-high-quality-evaluation.md)转为 **Deferred**：
设施冻结保留、不删除、不再继续投入，恢复条件见该 ADR。

## 单一结果

给定 evaluator-only query/qrel suite 和一个或多个真实候选 run，Memora 确定性重算 Recall@K、
HitRate@K、MRR、覆盖率与成本，并输出绑定输入摘要的可校验对照报告。

F213 只负责评分契约，不解析上游数据、不调用模型、不写 Memora，也不把内部 Router predictor 命中率
包装成外部 qrel 召回率。

## 输入协议

Suite 版本为 `memora.retrieval-suite/v1`：

- `suite_id`、`dataset_id`、`split` 和非空 query；
- query 只有稳定 `query_id` 与显式 `group`，不含答案正文；
- qrel 绑定 `query_id + document_id + relevance`；每题至少一个正相关文档；
- `hash` 覆盖除自身外的完整规范 JSON。

Run 版本为 `memora.retrieval-run/v1`：

- 绑定 suite hash、稳定 run/arm 身份；
- 每题恰有一个 result，状态只能是 `completed` 或 `failed`；
- completed 按真实顺序提交不重复 document ID；failed 候选必须为空；
- 同时提交 input/output token、tool/model call、context character 与端到端微秒原始计数；
- `hash` 同样覆盖完整原始证据。

Ground truth 只进入 scorer，不进入 Agent 请求、MSQL、候选生成或 Provider 上下文。

## 指标语义

对每个 query：

```text
recall@K = TopK 中不同正相关文档数 / 该题全部正相关文档数
hit@K    = TopK 是否至少含一个正相关文档
rr       = 第一个正相关文档的 1/rank；没有则为 0
```

报告对全部 suite query 做宏平均。`failed` 或未成功的题按零分进入分母，不能通过过滤失败抬高指标；
协议本身拒绝缺题、重复题、额外题和重复候选。MRR 使用 run 提交的完整有界候选列表，Recall/Hit 使用
显式 K，首版 CLI 默认 K=5。

报告还按 query `group` 分桶，成本使用原始总数和每题均值。相对基线的 Token/工具调用降幅为：

```text
(baseline_total - run_total) / baseline_total
```

基线为零时字段是 `null`，不伪造 0%。负数代表成本增加。

## CLI 与发布

```text
go run ./cmd/build-retrieval-evaluation-report \
  --suite /abs/suite.json \
  --run /abs/baseline.json --run /abs/memora.json \
  --baseline-run baseline --k 5 --output /abs/report.json
```

所有路径必须绝对且规范化；输入严格 JSON、未知字段 fail closed；报告同目录临时写、flush、rename，
已存在输出拒绝覆盖。API Key、prompt、回答正文与文档正文均不进入协议或报告。

## RED 与完成门

RED：三题 fixture 中一题多正例只召回一半、一题 rank=2、一题 failed；期望 Recall@5=0.5、
HitRate@5=2/3、MRR=0.5，且 optimized 相比 baseline 的 input token/tool call 分别降低 40%/50%。

边界覆盖：hash 篡改、缺题/重题/额外题、重复候选、无正 qrel、failed 携带候选、K 非法、未知基线、
零基线、稳定分桶顺序、取消与已存在输出。完成时执行 targeted、全量、race、vet 和 diff check。

## 关联

- [F212：外置评测数据准备](./f212-external-evaluation-data.md)
- [AI-native 质量模型](../product/quality-model.md)
- [评测 Agent 与外置 Hook](../development/evaluation-agent-observability.md)
