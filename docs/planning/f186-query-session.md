# F186：实验性交互式 QuerySession

状态：已批准，2026-08-05 开工。

## 唯一主要结果

在 `internal/agent` 提供实验性的进程内 QuerySession：一个 Session 可以顺序执行多个只读
Query turn，实时输出脱敏生命周期/Trace 事件，支持取消、会话总预算和失败后的有界恢复。
Session 必须复用 F181 `QueryAgent`，不能复制 loop、MSQL executor 或建立旁路数据库入口。

F186 不开放 `memora ask`、不把 Provider 放入 daemon、不持久化会话、不保存原始对话，也不改变
F185b 的 `INCOMPLETE` 质量结论。

## 固定旅程

```text
QuerySession.Start(question)
→ turn_started
→ F181 QueryAgent.Query（原 Bootstrap / Provider / MSQL loop）
  → trace（正文只有 byte count + SHA-256）...
→ turn_completed | turn_failed | turn_cancelled
→ Wait() 返回 QueryResult / error
→ 若会话总预算仍足够，可开始下一独立 turn
```

每个 turn 使用同一 SessionID 和独立 RunID。Session 不把上一 turn 的模型消息、工具正文或答案
注入下一 turn；近期 Page 热度由 daemon/Engine Cache 自然复用，语义上下文仍从当前 MSQL 快照重建。

## 契约与边界

- 同一 Session 同时最多一个 active turn；并发 `Start` fail closed；
- 每个 turn 保留 F181 的 Provider、Tool、statement、正文和输出 token 上限；Session 另设
  `MaxTurns`、`MaxProviderCalls`、`MaxToolCalls` 总预算；
- 新 turn 按剩余总预算收紧 F181 per-turn budget，不借用未来额度；不足一次合法查询时拒绝启动；
- `Cancel` 通过 `context.Context` 同时传播到 Provider 和 MSQL，不进行隐藏重试；
- Provider/MSQL 失败或取消只结束当前 turn，不 poison Session；恢复始终从新的 Bootstrap 开始；
- 事件 channel 容量由现有硬上限推导，生产 Query 不因消费者读取速度阻塞；事件关闭前必须有且仅有
  一个 terminal event；
- 事件只含版本、Session/Run/turn/sequence、kind、稳定 error code 和 F178 TraceEvent，不含问题、
  MSQL 正文、Row、答案、Key 或厂商 hidden reasoning；
- `Wait` 可重复、并发读取同一不可变结果快照。

## RED 与完成门

- RED 先覆盖事件在 Provider 返回前可见，证明不是结束后的 Trace replay；
- 正常 turn 的事件严格有序且可验证，最终结果仍含真实 SELECT evidence；
- active turn 期间第二次 `Start` 被拒绝；取消能停止阻塞 Provider 并输出 cancelled terminal；
- 同一 Session 在取消/失败后可成功运行下一 turn，累计预算按实际 Trace 而非预留上限扣减；
- 超过 turn/provider/tool 总预算后不发生 Provider 或 MSQL 调用；
- `go test -race` 证明 Start/Cancel/Wait/Usage 无竞态；现有 F181 测试与全量 CI 不回归。

用户执行授权：2026-08-05 用户要求持续执行至 F204；真实模型限速时一次成功链路证据即可。

开工前结论：PASS。
