# Route Benchmark Runner v1

状态：F125 讨论后冻结的实现规格。

## 执行单元

版本为 `memora.route-benchmark-runner/v1`。每个 run cell 由以下身份唯一确定：

- Corpus hash、scenario ID 与 fixture snapshot；
- `router`、`lexical`、`vector`、`hybrid`、`hybrid_prefetch` 五个固定 arm；
- host surface、Provider、协议、model 与 hostname-only endpoint；
- Task、Canonical Skill、MSQL protocol digest。

Runner 必须执行完整的 `scenario × arm × profile` 笛卡尔积。profile 矩阵沿用 F123：至少
覆盖 Codex、Claude Code 和一个 Kimi Provider；低质量或模型失败保留为 cell 证据，不能
静默跳过。

## 盲测请求与宿主边界

driver 请求只含 host-independent Task、arm、Database/Table/Route fixture、snapshot 与运行
身份，不含 `Expected`、正确 path 或正确 RowID。driver 是由宿主管理的可执行程序，通过
JSON stdin/stdout 与 Runner 通信；Runner 使用 argv 直接启动，不经 shell。

配置和报告只能记录 endpoint hostname。API key、Authorization、完整 URL、prompt、回答
正文和 hidden reasoning 均不进入制品；driver 从自己的宿主环境取得凭据。stdout 有硬上限，
超时与取消由 Task budget 和 context 共同控制。

## Observation 与评分

完成 observation 绑定 run/task/skill/protocol/corpus/snapshot/host receipt，并只公开：

- 每层首选 Route、最终 RowID、实际 SQL fact-read RowID；
- predictor Route IDs、已预取 root IDs、fallback/model/tool call 数；
- input/output/mispredict token 与 embedding/CPU/MSQL/model/end-to-end 延迟；
- truncation、permission denial 和 generation size/rebuild/peak-memory 计数。

Runner 从冻结 ground truth 重算 level top-1、exact path、RowID success、predictor recall、
prefetch hit、无关 locator 与错误 fact read；host 不提交“是否正确”的自评分。候选永远不是
fact evidence，正例 RowID success 还要求实际 fact-read 包含该 RowID；负例要求无 path、
无 RowID、无 fact read。

## 报告与失败

报告按固定顺序保存全部 cell、原始计数、结构化失败原因和内容 hash。receipt 身份或预算
无效、输出协议损坏、矩阵不完整时拒绝生成；合法但错误、truncated、permission denied、
failed 或 budget-exhausted 的 cell 必须保留，供 F126 分桶和置信区间计算。

F125 不选择默认方案。F126 才按 arm、fanout、depth、难度、语言和 host/model 汇总，形成
共同安全预算与后续硬件索引进入证据。

## 运行入口

Runner 只接受绝对规范化路径并以 `0600` 原子发布报告：

```text
go run ./cmd/run-route-benchmark \
  --corpus /abs/repo/benchmarks/route-retrieval-v1.json \
  --canonical-skill /abs/repo/skills/memora \
  --drivers /abs/private/route-drivers.json \
  --output /abs/private/reports/route-run.json
```

driver config 版本为 `memora.route-benchmark-driver-config/v1`。每个 profile 只声明 F123
HostProfile、绝对 executable 与 argv 数组；至少三项组成 Codex、Claude Code、Kimi 矩阵。
argv 禁止 key/token/secret 参数与完整 URL。executable 每个 cell 接收一行 blind Request
JSON，并输出一个 strict Observation JSON；凭据只能由该进程自己的环境或宿主凭据仓取得。

## 关联

- [Route Retrieval Benchmark v3](./route-retrieval-benchmark-v3.md)
- [Route Benchmark Corpus v1](./route-benchmark-corpus-v1.md)
- [Real Host Contract v1](../agent/real-host-contract-v1.md)
