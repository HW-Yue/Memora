# F215：低并发 Provider 退避与评测断点

状态：代码已完成；2026-08-06 冻结。真实 DeepSeek smoke receipt 已补齐，但质量证据仍不完整。

## 单一结果

真实答案评测默认单 worker、模型调用遇到 408/429/5xx 或传输失败时有限退避重试，并在每个题完成后
把脱敏的 public/private case 收据写入 hash-bound checkpoint；进程中断后只跳过已成功题，失败题重新执行。

它不提升并发、不吞掉失败、不改变 Query Agent 的单题预算，也不把 API Key、prompt、答案正文或 hidden
reasoning 写入 checkpoint。成功的评测最终仍由现有 `PublishReports` 生成完整 scorecard/diagnostics；
checkpoint 是可恢复内部状态，不是公开质量报告。

## Retry 规则

- retryable：HTTP 408、429、500、502、503、504 与 `ErrProviderTransport`；
- non-retryable：请求/响应校验、权限/预算、Provider wire、上下文取消；
- 默认最多 4 次尝试，指数退避 250 ms 起、上限 8 s；Retry-After 不直接信任，最终仍受上限约束；
- 每题串行执行，不启动 goroutine 池；模型端 429 只延迟当前题；
- 所有尝试共享父 context，取消立即停止等待和 HTTP 请求。
- 评测 manifest 的默认单题预算保持 4 Provider/3 Tool；若某 Provider 需要不同调用预算，CLI 必须
  显式同时传 `--max-provider-calls` 与 `--max-tool-calls`，两者会进入 checkpoint identity，不得
  静默覆盖 manifest。

## Checkpoint

版本为 `memora.answer-benchmark-checkpoint/v1`，绑定 manifest fixture、RunConfig（provider/model/arm/
prompt/code revision）和绝对 checkpoint identity。每次写入：

```text
<checkpoint>.tmp -> flush -> rename -> checkpoint.json
```

checkpoint 只保存已处理题的脱敏 `PublicCase`、`PrivateCase`、trace/evidence 收据；恢复时严格拒绝
identity/hash/题目 ID 不匹配，成功题按冻结 task 顺序复用，失败题重新调用。损坏或截断 checkpoint
fail closed，不从临时文件猜测进度。

## CLI

```text
go run ./cmd/run-answer-benchmark \
  --manifest /repo/benchmarks/answer-retrieval-v1/manifest.json \
  --output-dir /Volumes/yhw/MemoraEvaluation/runs/deepseek-... \
  --checkpoint /Volumes/yhw/MemoraEvaluation/checkpoints/deepseek-....json \
  --provider deepseek --api-base-url https://api.deepseek.com/v1 \
  --model deepseek-v4-flash --secret-env DEEPSEEK_API_KEY \
  --max-attempts 4 --backoff 250ms --max-backoff 8s \
  --max-provider-calls 6 --max-tool-calls 5 \
  --run-id ... --arm atlas-lexical-prefetch-v1 --prompt-id query-agent-v4 --code-revision ...
```

默认不传 `--checkpoint` 时保持旧的一次性行为；准备正式批量评测时必须显式启用 checkpoint。

## RED 与完成门

RED：本地 Provider 依次返回 429、503、成功，期待只在同一题串行退避三次；checkpoint 写入两题后模拟
进程终止，恢复只调用未成功题；篡改 identity/hash、重复/未知题、截断文件必须拒绝。上述 targeted
测试、全量 `go test`、定向 race、vet 和 diff check 均已通过。2026-08-06 DeepSeek V4 Flash smoke 使用
`query-agent-v4`、`atlas-lexical-prefetch-v1` 和 6 Provider/5 Tool 预算，12 题中 1 题成功、11 题
失败；成功题完整经过 OPEN ROUTE → SELECT → 最终回答，receipt 位于外置盘
`runs/deepseek-f215-smoke-20260806-r8/`。该 receipt 只证明真实 Provider/SQL/Trace/发布链路可执行，
无论有无 key 都不宣称 AI 质量通过。

## 关联

- [F213：外部检索评分与对照报告](./f213-retrieval-evaluation-score.md)
- [外部答案质量评测](./f184-external-answer-evaluation.md)
- [F185b Query Agent Release Gate](./f185b-query-release-gate.md)
