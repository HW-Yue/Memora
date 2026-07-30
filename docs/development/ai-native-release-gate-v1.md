# AI-native 发布门 v1

状态：已撤销（2026-07-30）。F51 使用字符向量与 cosine，并让 Memora Adapter
走混合候选评分，不符合 [AI-native 产品宪章](../product/ai-native-product-charter.md)。
本文仅保留历史审计，不能证明发布门通过，也不能授权进入 F52。

## 固定输入

F51 不修改 F42 的五类旅程和统一评分器。它增加
`benchmarks/ai-native-release-v0.json`，固定 28 条候选记录和 18 个同义查询：

- 20 条应持久化，8 条应拒绝，包含瞬时价格、天气、构建耗时、问候、原始摘录、
  陈旧 mutation 和一次性验证码；
- 查询覆盖多项目隔离、revision 历史、资料结论、冷启动和跨宿主接管；
- 4 个查询明确计入 takeover，所有相关 Row ID 都在语料中冻结。

## 真实执行层

受控 benchmark 从进程环境接收 OpenAI-compatible HTTPS 地址、key 和模型，不读取
daemon Config，也不保存 key。模型只提交值得写入的候选并为记录/查询生成发现
词项；未提交的记录视为拒绝。所有 completion 必须正常 `stop`，ID、JSON 和输出
规模严格校验。

五个 Adapter 使用相同语料和评分器：

- `no-memory`：空状态；
- `markdown-search`：原始记录加字面 bigram；
- `sqlite-fts`：真实 SQLite FTS5，使用固定中文预分词；
- `vector`：本地字符 trigram 稀疏向量与 cosine；它不是 dense embedding 产品；
- `memora`：项目 Route、模型词项、机械 n-gram 和 revision 可见性融合。

该 Vector 对照只证明固定本地向量算法下的差异。由于本次 CC Switch/Kimi 模型列表
没有 embedding 模型，报告不得外推为对商业 dense embedding 服务的全面胜出。

## 当时使用的阈值（现已失效）

Memora 必须同时满足：

- write precision ≥ 0.95；
- Recall@5 ≥ 0.90；
- takeover success = 1；
- 平均 Context ≤ 2400 字符；
- write precision 与 Recall@5 均严格高于 Markdown；
- Recall@5 落后 Vector 不超过 0.05；
- 所有宿主结果等价，五份报告、语料、模型输出和执行输出 hash 完整。

模型网络调用只在显式受控运行时发生。普通 CI 不重放付费请求，只校验已提交报告的
suite/corpus/evidence/report/bundle hash、固定阈值和无凭据内容；任一篡改都阻断。

## 历史运行记录（无产品通过效力）

`benchmarks/reports/ai-native-release-v0.json` 由 CC Switch 中的 Kimi 凭据通过
OpenAI-compatible 协议、`moonshot-v1-8k` 生成：

- 模型接受 20/28 条记录，完整生成 18 个查询词项；
- Memora write precision/recall、Recall@5 和 takeover 均为 1；
- 平均 Context 为 156.94 字符；
- Markdown write precision 为 0.714、Recall@5 为 0.778；
- SQLite FTS Recall@5 为 0.944，但无关 Row 率为 0.811；
- 本地稀疏 Vector Recall@5 为 0.167。

这份结果不再允许进入 F52。报告、执行器和验证器必须在架构对账中移出当前发布门；
后续评测禁止加入 dense/sparse embedding、字符向量、cosine 或等价距离匹配。

## 复现

```sh
MEMORA_BENCHMARK_OPENAI_BASE_URL=https://provider.example \
MEMORA_BENCHMARK_OPENAI_API_KEY=... \
MEMORA_BENCHMARK_OPENAI_MODEL=... \
go run ./cmd/run-ai-native-benchmark

go run ./cmd/verify-ai-native-benchmark
```

## 关联

- [AI-native Benchmark v1](./ai-native-benchmark-v1.md)
- [质量模型与验收](../product/quality-model.md)
- [进程配置与宿主边界](./process-configuration.md)
