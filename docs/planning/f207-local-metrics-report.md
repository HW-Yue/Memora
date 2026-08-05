# F207：本地 Hook 指标与报告

状态：2026-08-05 已完成；本地 JSON/HTML 报告链路已通过完成门。

## 唯一主要结果

把 F204 的脱敏 `ExternalAgentHookEnvelope` 聚合为可重复的 session/turn 指标，并输出开发用
JSON/HTML 报告，帮助分析 Memora 自身的调用次数、上下文体量、分段耗时、回退和失败原因。

## 冻结边界

- 输入只能是 F204 Hook 快照；不打开数据库、不读取宿主上下文、不保存 prompt/answer/API key，也不
  把指标写回 Memora Database。
- 聚合按 Hook context + RunID + SessionID 分 session，再按 Turn 输出 turn bucket；相同事件在
  重复快照中按稳定 `(host_session_id, run_id, session_id, sequence)` 去重，digest 漂移直接失败。
- 每个 bucket 输出事件/Provider/MSQL/Tool/Bootstrap 次数、输入/输出字节、Provider token、分段
  duration、status/error code、fallback 和按 currency 的 micros 成本；不计算答案 Recall/MRR。
- JSON 是规范机器报告；HTML 是相同报告的转义只读投影，不引入 Admin、Web Storage 或外部网络。
- 没有事件的 envelope 允许进入输入计数但不产生伪造 bucket；输入顺序不影响输出顺序。

## RED

```text
go test ./internal/agentmetrics -run TestAggregateHookSnapshotsBySessionAndTurn
```

初始实现只提供类型和空聚合，测试应因缺失 bucket、去重和指标求和而失败；这不是 Parser 或网络
fixture 错误。

## 完成门

- 聚合、重复快照去重、冲突拒绝、确定性 JSON 和 HTML escaping 有测试；大值溢出 fail closed；
- 多 session/turn、provider usage/cost、fallback/error/status 与空输入均有证据；`-race` 通过；
- standalone `build-agent-metrics-report` 能读多个 Hook JSON，并原子输出 JSON/HTML；不改 Admin；
- 目标 package、命令、全量测试、全量 `-race`、`go vet` 和格式检查通过后合入 `main`。

## 使用

命令只读取已经落盘的 F204 Hook 快照，不启动 daemon，也不访问数据库：

```text
go run ./cmd/build-agent-metrics-report \
  --input /abs/hook-1.json --input /abs/hook-2.json \
  --json /abs/agent-metrics.json --html /abs/agent-metrics.html
```

`--input` 可重复；`--json` 必填，`--html` 可选。所有路径必须是绝对、规范化路径，输出通过同目录
临时文件写入后 rename。

## 关联

- [F204：外置 Agent Hook](./f204-external-agent-hook.md)
- [内置评测 Agent 与外置 Hook 观测](../development/evaluation-agent-observability.md)
- [F204 之后的开发计划](./post-f204-development-plan.md)
