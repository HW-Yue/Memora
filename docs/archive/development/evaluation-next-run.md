# 下一次评测启动问题

下次回来时只需要在运行命令的同一个 shell 中加载 `DEEPSEEK_API_KEY`，然后回复：

> DeepSeek key 已加载，开始 F215 低并发 smoke 测评。

不要把密钥粘贴到聊天、仓库、Memora Database、外置盘或报告里。启动命令会延迟从进程环境读取
`DEEPSEEK_API_KEY`，只把 provider/model 名称写入公开 scorecard。

最近一次真实回执已写入外置盘 `runs/deepseek-f215-smoke-20260806-r10-resume-3/`，12 题中 9 题完成了
OPEN ROUTE → SELECT → Answer 链路；剩余 3 题仍未通过质量门。再次运行必须使用新的 run/checkpoint
路径，避免复用旧代码版本的 checkpoint identity。

## 下一轮命令

```text
go run ./cmd/run-answer-benchmark \
  --manifest "$PWD/benchmarks/answer-retrieval-v1/manifest.json" \
  --output-dir /Volumes/yhw/MemoraEvaluation/runs/deepseek-f215-smoke-20260806-r11 \
  --checkpoint /Volumes/yhw/MemoraEvaluation/checkpoints/deepseek-f215-smoke-20260806-r11.json \
  --provider deepseek \
  --api-base-url https://api.deepseek.com/v1 \
  --model deepseek-v4-flash \
  --dialect deepseek-v4-non-thinking \
  --secret-env DEEPSEEK_API_KEY \
  --max-attempts 4 --backoff 250ms --max-backoff 8s \
  --max-provider-calls 6 --max-tool-calls 5 \
  --run-id deepseek-f215-smoke-20260806-r11 \
  --arm atlas-lexical-prefetch-v1 \
  --prompt-id query-agent-v4 \
  --code-revision "$(git rev-parse HEAD)"
```

这轮先验证 Provider wire、MSQL-only Query Agent、Trace 和 checkpoint；若中途遇到 429/5xx，继续
同一命令即可从 checkpoint 恢复。它是 smoke/链路证据，不是外部数据集 Recall/MRR 或发布质量结论。
