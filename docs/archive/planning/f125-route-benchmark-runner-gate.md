# F125 Route Benchmark Runner 开工与完成门

状态：已完成（2026-08-01）。

## 单一主要结果

一个受控 Runner 将冻结 Route Corpus 按五个 arm 和真实 host/model profile 矩阵执行，
产出身份、预算、正确性、安全与成本均可重验的原始报告。

## 产品门

- Runner 只评测发现链路，不改变 Router、predictor、Skill 或默认配置；
- ground truth 只留在评分侧，发送给 host 的 Task/fixture 不含 expected path/RowID；
- Provider 凭据属于宿主环境，不进入配置、请求、报告或错误文本；
- 所有 arm 的事实读取仍必须显式记录为 RowID SQL，候选和预取不能成为答案证据；
- 单个质量失败进入失败样本，不因低分中止矩阵；身份、预算或协议伪造则拒绝报告。

结论：PASS。

## RED 清单

入口：`go test ./internal/routebenchmarkrunner ./cmd/run-route-benchmark`

1. 五个固定 arm 或 host/profile cell 被遗漏、重复或乱序；
2. ground truth 泄漏给 driver，或 Task/Corpus/Skill/protocol/snapshot digest 未绑定；
3. receipt 超调用、上下文、耗时预算仍被计入有效运行；
4. 错路径、负例误读、无关 locator、权限拒绝与 truncation 未作为原始失败证据保留；
5. `level_top1_accuracy`、`exact_path_success`、`rowid_success`、predictor/prefetch、token
   与分段延迟计数不可从 observation 确定性重算；
6. driver 经 shell 拼接命令、输出无限制，或配置/报告可携带凭据和完整 endpoint URL；
7. 报告未知字段、篡改 hash、重复 run 或不完整矩阵仍能加载。

首次 RED 预期因 `internal/routebenchmarkrunner` 与 `cmd/run-route-benchmark` 尚不存在而失败。

## 明确不做

- 不在 Runner 内实现模型 API、embedding、Lexical、Vector 或 Router 策略；
- 不决定默认 arm、fanout、预算或 Accelerate/HNSW 进入条件；
- 不保存 prompt、模型答案、hidden reasoning、API key 或 tool 原文；
- 不修改冻结 Corpus v1。

## 完成门

- 完整矩阵、盲测 fixture、预算/身份、安全计数、确定性 hash 与 strict reload 测试全绿；
- command driver 的无 shell、超时、输出上限、取消与脱敏错误测试全绿；
- CLI 原子写出可重验报告，targeted race 与 `./scripts/ci.sh` 全门通过；
- 文档推进到 F126 Capability Report。

## 完成证据

- RED：`go test ./internal/routebenchmarkrunner` 首次因 package 无生产代码失败；随后按矩阵、
  盲测、receipt、评分、strict reload 和 command driver 边界逐项转绿。
- 矩阵：冻结 30 scenario × 5 arm × Codex/Claude Code/Kimi 最小三 profile，共 450 cell；
  固定顺序、唯一 run/task/snapshot/host digest 和完整矩阵重验均通过。
- 正确性：level top-1、exact path、RowID fact-read、predictor/prefetch、错误 locator、负例
  误读与失败样本全部由 Runner 根据 Corpus 重算，不信任 host 自评分。
- 宿主边界：JSON stdin/stdout、绝对 executable + argv、无 shell、每 Task timeout、2 MiB
  stdout 上限、stderr 脱敏、secret/full-URL 配置拒绝测试通过。
- 制品：Canonical Skill/MSQL digest 自动绑定；报告 strict decode、hash、ground-truth
  `ValidateAgainst`、`0600` staging/fsync/rename/目录 fsync 通过。
- 完整门：targeted race 与 `./scripts/ci.sh` 的 format、vet、unit、全仓 race、integration、
  e2e、arm64/amd64 cross-build 全部通过。
