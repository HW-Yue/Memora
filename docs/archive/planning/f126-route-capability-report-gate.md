# F126 Route Capability Report 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

将一个或多个已验证 F125 原始报告确定性汇总为分桶能力报告，并只在冻结的正确性、
安全性和收益门全部成立时选择共同默认 discovery arm。

## 产品门

- 先保护 RowID/SQL 事实正确性，再比较 LLM 调用、token 与端到端延迟；
- 报告不能修改 F125 observation，也不能用总平均掩盖 host、fanout 或难题退化；
- 价格只来自显式、版本化 price card；没有价格不猜费用；
- 无合格优化 arm 时默认 `router`，这也是完整而非失败的结论；
- 报告只决定 Skill discovery profile，不改变存储、索引格式或硬件 backend。

结论：PASS。

## RED 清单

入口：`go test ./internal/routecapability ./cmd/build-route-capability-report`

1. source report 未针对同一 Corpus/contract 重验，或重复 run 被重复计数；
2. 缺少 arm、fanout、depth、difficulty、language、host/model 任一分桶；
3. Wilson 95% 区间、p50/p95、model/token delta 与原始计数不可确定性重算；
4. 优化 arm 只凭平均分入选，实际存在 matched cell RowID 回退、错读或越权；
5. model calls 没减少、端到端 p95 变差或失败 cell 存在仍成为默认；
6. 缺 price card 时伪造费用，或 price digest/单位/溢出不受控；
7. 输出未知字段、hash 篡改、source report 漏失或 policy 漂移仍能加载。

首次 RED 预期因 `internal/routecapability` 尚不存在而失败。

## 明确不做

- 不执行模型、不补造 F125 原始 observation；
- 不自动改 Canonical Skill 或运行时配置；
- 不 Review Accelerate/HNSW；它们分别由 F162/F163 的独立资源门处理；
- 不用费用替代正确性、安全或延迟判断。

## 完成门

- 全维度分桶、reference aggregation、置信区间、延迟、成本与 default decision 测试全绿；
- strict load/rehash、重复/漂移 source、整数边界与无 price card 测试全绿；
- CLI 原子发布，targeted race 与 `./scripts/ci.sh` 全门通过；
- 规划推进到 F127 Story Gate v2。

## 完成证据

- RED：`go test ./internal/routecapability` 首次因 package 无生产代码失败。
- 汇总：五 arm 的 aggregate、fanout、depth、difficulty、language、host/model slice，原始
  counts、Wilson 95%、五段 p50/p95、Router call/token delta 与 vector maxima 全绿。
- 决策：matched RowID 零退化、exact path、安全计数、全完成、model calls 至少节省 5%
  和端到端 p95 非回退共同门；任一 cell 回退即拒绝该 arm 的测试通过。
- 成本：可选、排序、bounded micro-USD/million price card 和 digest 绑定；缺价与溢出不
  伪造费用。
- 制品：source report 全量 `ValidateAgainst`、重复 run/身份漂移拒绝、strict JSON、hash、
  source rebuild revalidation 与 `0600` 原子发布通过。
- 当前证据：本机存在 Codex/Claude Code CLI，但无真实 driver config/source report，未把
  synthetic 测试当实测；当前默认保持 Router。
- 完整门：targeted race 与 `./scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、
  e2e、arm64/amd64 cross-build 全部通过。
